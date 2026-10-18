// Package cdp provides a concurrent Chrome DevTools Protocol WebSocket client.
package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Event is an unsolicited CDP notification, optionally scoped to a session.
type Event struct {
	Method    string          `json:"method"`
	SessionID string          `json:"sessionId"`
	Params    json.RawMessage `json:"params"`
}

// ProtocolError is an error response from the CDP server.
type ProtocolError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *ProtocolError) Error() string { return fmt.Sprintf("CDP error %d: %s", e.Code, e.Message) }

type response struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *ProtocolError  `json:"error"`
}
type reply struct {
	response response
	err      error
}

// Client routes concurrent requests over one WebSocket connection.
type Client struct {
	conn             *websocket.Conn
	writer           chan struct{}
	done             chan struct{}
	mu               sync.Mutex
	err              error
	nextID           uint64
	nextSubscription uint64
	pending          map[uint64]chan reply
	subscriptions    map[string]map[uint64]chan Event
}

// Dial connects to a ws or wss CDP endpoint.
func Dial(ctx context.Context, endpoint string) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid CDP endpoint: %w", err)
	}
	if (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" {
		return nil, errors.New("CDP endpoint must be an absolute ws or wss URL")
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("connect CDP: %w", err)
	}
	c := &Client{conn: conn, writer: make(chan struct{}, 1), done: make(chan struct{}), pending: make(map[uint64]chan reply), subscriptions: make(map[string]map[uint64]chan Event)}
	conn.SetReadLimit(64 << 20)
	go c.read()
	return c, nil
}

// Call sends a request and decodes its result. An empty sessionID addresses the
// browser; otherwise the request uses CDP's flattened session routing.
func (c *Client) Call(ctx context.Context, sessionID, method string, params any, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	c.nextID++
	id := c.nextID
	replies := make(chan reply, 1)
	c.pending[id] = replies
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	request := struct {
		ID        uint64 `json:"id"`
		SessionID string `json:"sessionId,omitempty"`
		Method    string `json:"method"`
		Params    any    `json:"params,omitempty"`
	}{id, sessionID, method, params}
	// Encode before taking the writer so a bad parameter does not close a healthy connection.
	data, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode CDP request: %w", err)
	}
	select {
	case c.writer <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.Err()
	}
	if err = ctx.Err(); err == nil {
		deadline := time.Now().Add(30 * time.Second)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		_ = c.conn.SetWriteDeadline(deadline)
		interrupted := make(chan struct{})
		stop := context.AfterFunc(ctx, func() { c.fail(ctx.Err()); close(interrupted) })
		err = c.conn.WriteMessage(websocket.TextMessage, data)
		if !stop() {
			<-interrupted
		}
		if err != nil {
			c.fail(fmt.Errorf("write CDP request: %w", err))
		}
	}
	<-c.writer
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	select {
	case r := <-replies:
		if r.err != nil {
			return r.err
		}
		if r.response.Error != nil {
			return r.response.Error
		}
		if result != nil && len(r.response.Result) > 0 {
			return json.Unmarshal(r.response.Result, result)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.Err()
	}
}

// Subscribe receives events for a method from all sessions. The method "*"
// receives all events in their original wire order. Each subscription
// buffers 256 events; new events are dropped when the consumer falls behind.
// The returned cancellation function is safe to call more than once. The channel
// closes when unsubscribed or when the client disconnects.
func (c *Client) Subscribe(method string) (<-chan Event, func()) {
	return c.SubscribeBuffer(method, 256)
}

// SubscribeBuffer is Subscribe with a bounded custom queue. Capacity is clamped
// to 1..65536; overflowing queues drop new events without blocking CDP responses.
func (c *Client) SubscribeBuffer(method string, capacity int) (<-chan Event, func()) {
	if capacity < 1 {
		capacity = 1
	}
	if capacity > 65536 {
		capacity = 65536
	}
	ch := make(chan Event, capacity)
	c.mu.Lock()
	c.nextSubscription++
	id := c.nextSubscription
	if c.err != nil {
		close(ch)
	} else {
		if c.subscriptions[method] == nil {
			c.subscriptions[method] = make(map[uint64]chan Event)
		}
		c.subscriptions[method][id] = ch
	}
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if channels := c.subscriptions[method]; channels != nil {
			if target, ok := channels[id]; ok {
				delete(channels, id)
				close(target)
			}
			if len(channels) == 0 {
				delete(c.subscriptions, method)
			}
		}
	}
}

// Done closes when the connection closes or fails.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err returns the terminal connection error, or nil while connected.
func (c *Client) Err() error { c.mu.Lock(); defer c.mu.Unlock(); return c.err }

// Close closes the connection and immediately releases pending calls.
func (c *Client) Close() error { c.fail(net.ErrClosed); return nil }

func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	c.err = err
	close(c.done)
	for id, ch := range c.pending {
		ch <- reply{err: err}
		delete(c.pending, id)
	}
	for method, channels := range c.subscriptions {
		for _, ch := range channels {
			close(ch)
		}
		delete(c.subscriptions, method)
	}
	c.mu.Unlock()
	_ = c.conn.Close()
}

func (c *Client) read() {
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			c.fail(fmt.Errorf("read CDP response: %w", err))
			return
		}
		var message struct {
			response
			Event
		}
		if err = json.Unmarshal(data, &message); err != nil {
			c.fail(fmt.Errorf("decode CDP message: %w", err))
			return
		}
		c.mu.Lock()
		if message.ID != 0 {
			if ch, ok := c.pending[message.ID]; ok {
				delete(c.pending, message.ID)
				ch <- reply{response: message.response}
			}
		} else if message.Method != "" {
			for _, ch := range c.subscriptions[message.Method] {
				select {
				case ch <- message.Event:
				default:
				}
			}
			if message.Method != "*" {
				for _, ch := range c.subscriptions["*"] {
					select {
					case ch <- message.Event:
					default:
					}
				}
			}
		}
		c.mu.Unlock()
	}
}
