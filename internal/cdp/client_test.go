package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func server(t *testing.T, handler func(*websocket.Conn)) string {
	t.Helper()
	upgrade := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrade.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		handler(conn)
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}
func dial(t *testing.T, endpoint string) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, err := Dial(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
func TestConcurrentCallsRouteOutOfOrder(t *testing.T) {
	endpoint := server(t, func(conn *websocket.Conn) {
		var requests [2]struct {
			ID        uint64 `json:"id"`
			SessionID string `json:"sessionId"`
		}
		for i := range requests {
			if conn.ReadJSON(&requests[i]) != nil {
				return
			}
		}
		for i := len(requests) - 1; i >= 0; i-- {
			r := requests[i]
			if conn.WriteJSON(map[string]any{"id": r.ID, "sessionId": r.SessionID, "result": map[string]string{"session": r.SessionID}}) != nil {
				return
			}
		}
		_, _, _ = conn.ReadMessage()
	})
	c := dial(t, endpoint)
	var wg sync.WaitGroup
	for _, session := range []string{"one", "two"} {
		wg.Add(1)
		go func(session string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var result struct {
				Session string `json:"session"`
			}
			if err := c.Call(ctx, session, "Test.echo", nil, &result); err != nil {
				t.Error(err)
			} else if result.Session != session {
				t.Errorf("got %q for %q", result.Session, session)
			}
		}(session)
	}
	wg.Wait()
}
func TestProtocolError(t *testing.T) {
	c := dial(t, server(t, func(conn *websocket.Conn) {
		var req struct {
			ID uint64 `json:"id"`
		}
		if conn.ReadJSON(&req) != nil {
			return
		}
		_ = conn.WriteJSON(map[string]any{"id": req.ID, "error": map[string]any{"code": -32601, "message": "Unknown method"}})
		_, _, _ = conn.ReadMessage()
	}))
	var protocol *ProtocolError
	err := c.Call(context.Background(), "", "bad", nil, nil)
	if !errors.As(err, &protocol) || protocol.Code != -32601 || !strings.Contains(err.Error(), "Unknown method") {
		t.Fatalf("unexpected error: %v", err)
	}
}
func TestDisconnectReleasesPending(t *testing.T) {
	c := dial(t, server(t, func(conn *websocket.Conn) { _, _, _ = conn.ReadMessage() }))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Call(ctx, "", "pending", nil, nil); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected disconnect, got %v", err)
	}
	select {
	case <-c.Done():
	case <-ctx.Done():
		t.Fatal("Done was not closed")
	}
	if c.Err() == nil {
		t.Fatal("missing terminal error")
	}
	if err := c.Call(ctx, "", "later", nil, nil); err == nil {
		t.Fatal("call after disconnect succeeded")
	}
}
func TestPendingCancellationAndTimeout(t *testing.T) {
	received := make(chan struct{}, 2)
	c := dial(t, server(t, func(conn *websocket.Conn) {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
			received <- struct{}{}
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- c.Call(ctx, "", "cancel", nil, nil) }()
	<-received
	// The write has completed before the server has the opportunity to observe cancellation.
	// Wait for the client to release its writer to exercise cancellation of a pending response.
	for len(c.writer) != 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	if err := c.Call(ctx2, "", "timeout", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if c.Err() != nil {
		t.Fatalf("pending cancellation closed client: %v", c.Err())
	}
}
func TestEventsAndUnsubscribe(t *testing.T) {
	send := make(chan struct{})
	c := dial(t, server(t, func(conn *websocket.Conn) {
		<-send
		for i := 0; i < 300; i++ {
			if conn.WriteJSON(Event{Method: "Page.event", SessionID: "tab", Params: json.RawMessage(`{"n":1}`)}) != nil {
				return
			}
		}
		_, _, _ = conn.ReadMessage()
	}))
	events, unsubscribe := c.Subscribe("Page.event")
	close(send)
	select {
	case event := <-events:
		if event.SessionID != "tab" || string(event.Params) != `{"n":1}` {
			t.Fatalf("bad event: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("no event")
	}
	// Delivery and unsubscribe may overlap; both must be safe.
	unsubscribe()
	unsubscribe()
	for range events {
	}
	c.Close()
	closed, cancel := c.Subscribe("Page.event")
	cancel()
	if _, ok := <-closed; ok {
		t.Fatal("subscription after close remained open")
	}
}
func TestCloseReleasesPending(t *testing.T) {
	received := make(chan struct{})
	c := dial(t, server(t, func(conn *websocket.Conn) {
		_, _, _ = conn.ReadMessage()
		close(received)
		_, _, _ = conn.ReadMessage()
	}))
	result := make(chan error, 1)
	go func() { result <- c.Call(context.Background(), "", "pending", nil, nil) }()
	<-received
	c.Close()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("expected error")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not release call")
	}
}
func TestRejectNonWebSocketEndpoint(t *testing.T) {
	if _, err := Dial(context.Background(), "http://localhost"); err == nil {
		t.Fatal("accepted HTTP endpoint")
	}
}

func TestWildcardPreservesCrossMethodOrder(t *testing.T) {
	send := make(chan struct{})
	c := dial(t, server(t, func(conn *websocket.Conn) {
		<-send
		for _, method := range []string{"Network.requestWillBeSent", "Network.responseReceived", "Page.frameNavigated"} {
			if conn.WriteJSON(Event{Method: method, SessionID: "tab", Params: json.RawMessage(`{}`)}) != nil {
				return
			}
		}
		_, _, _ = conn.ReadMessage()
	}))
	all, stop := c.SubscribeBuffer("*", 10)
	defer stop()
	exact, cancel := c.Subscribe("Network.responseReceived")
	defer cancel()
	close(send)
	for _, want := range []string{"Network.requestWillBeSent", "Network.responseReceived", "Page.frameNavigated"} {
		select {
		case event := <-all:
			if event.Method != want {
				t.Fatalf("got %q, want %q", event.Method, want)
			}
		case <-time.After(time.Second):
			t.Fatal("missing wildcard event")
		}
	}
	select {
	case event := <-exact:
		if event.Method != "Network.responseReceived" {
			t.Fatal(event)
		}
	case <-time.After(time.Second):
		t.Fatal("missing exact subscription event")
	}
}
