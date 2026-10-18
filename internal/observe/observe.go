// Package observe retains browser events across individual Wok CLI commands.
package observe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alg0rix/wok/internal/cdp"
)

type Config struct {
	Dir           string
	DownloadDir   string
	ProxyUsername string
	ProxyPassword string
}
type ConsoleEntry struct {
	Type      string            `json:"type"`
	Text      string            `json:"text"`
	Args      []json.RawMessage `json:"args"`
	Timestamp float64           `json:"timestamp"`
}
type ErrorEntry struct {
	Text      string          `json:"text"`
	Details   json.RawMessage `json:"details"`
	Timestamp float64         `json:"timestamp"`
}
type NetworkEntry struct {
	RequestID  string  `json:"requestId"`
	URL        string  `json:"url"`
	Method     string  `json:"method"`
	Type       string  `json:"type"`
	Status     int     `json:"status"`
	DurationMs float64 `json:"durationMs"`
	Error      string  `json:"error,omitempty"`
	Finished   bool    `json:"finished"`
	loader     string
	started    float64
	// Details for `wok request`; bodies are fetched lazily from the browser.
	requestHeaders  map[string]any
	responseHeaders map[string]any
	postData        string
	hasPostData     bool
	mimeType        string
}
type DownloadEntry struct {
	GUID              string  `json:"guid"`
	Tab               string  `json:"tab,omitempty"`
	URL               string  `json:"url"`
	SuggestedFilename string  `json:"suggestedFilename"`
	Path              string  `json:"path,omitempty"`
	State             string  `json:"state"`
	ReceivedBytes     float64 `json:"receivedBytes"`
	TotalBytes        float64 `json:"totalBytes"`
	name              string
	before            map[string]bool
}
type DialogEntry struct {
	Type          string `json:"type"`
	Message       string `json:"message"`
	DefaultPrompt string `json:"defaultPrompt"`
	Accepted      bool   `json:"accepted"`
	PromptText    string `json:"promptText,omitempty"`
	Error         string `json:"error,omitempty"`
	Timestamp     int64  `json:"timestampUnixMs"`
}
type DialogPolicy struct {
	Accept     bool   `json:"accept"`
	PromptText string `json:"promptText,omitempty"`
}
type NetworkState struct {
	Inflight           int   `json:"inflight"`
	LastActivityUnixMs int64 `json:"lastActivityUnixMs"`
}
type tab struct {
	id, session  string
	console      []ConsoleEntry
	errors       []ErrorEntry
	network      []*NetworkEntry
	requests     map[string]*NetworkEntry
	inflight     map[string]bool
	authAttempts map[string]int
	authNetwork  map[string]string
	dialogs      []DialogEntry
	lastActivity int64
	debug        *debugState
}
type Manager struct {
	c           *cdp.Client
	config      Config
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	closeOnce   sync.Once
	syncMu      sync.Mutex
	mu          sync.Mutex
	tabs        map[string]*tab
	sessions    map[string]*tab
	frames      map[string]string
	downloads   []*DownloadEntry
	downloadIDs map[string]*DownloadEntry
	policy      DialogPolicy
	policyMu    sync.Mutex
	unsubs      []func()
	barriers    chan chan struct{}
	routes      []*Route
	recording   *recording
	video       *video
	traceMu     sync.Mutex
	tracing     bool
}

// New subscribes before enabling browser events and starts discovering pages.
// Logging sessions do not change emulation settings or user agent.
func New(ctx context.Context, c *cdp.Client, config Config) (*Manager, error) {
	if !filepath.IsAbs(config.Dir) {
		return nil, errors.New("Wok runtime directory must be absolute")
	}
	if config.DownloadDir == "" {
		config.DownloadDir = filepath.Join(config.Dir, "downloads")
	}
	if !filepath.IsAbs(config.DownloadDir) {
		return nil, errors.New("Wok downloads directory must be absolute")
	}
	for _, dir := range []string{config.Dir, config.DownloadDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	}
	lifetime, cancel := context.WithCancel(ctx)
	m := &Manager{c: c, config: config, ctx: lifetime, cancel: cancel, tabs: map[string]*tab{}, sessions: map[string]*tab{}, frames: map[string]string{}, downloadIDs: map[string]*DownloadEntry{}, barriers: make(chan chan struct{})}
	data, err := os.ReadFile(filepath.Join(config.Dir, "dialog-policy.json"))
	if err == nil {
		if err = json.Unmarshal(data, &m.policy); err != nil {
			cancel()
			return nil, fmt.Errorf("load Wok dialog policy: %w", err)
		}
	} else if !os.IsNotExist(err) {
		cancel()
		return nil, err
	}
	if err = m.loadRoutes(); err != nil {
		cancel()
		return nil, err
	}
	// One ordered stream preserves request/response and navigation ordering.
	// The larger bounded queue absorbs bursts while a dialog/auth call completes.
	events, unsub := c.SubscribeBuffer("*", 4096)
	m.unsubs = append(m.unsubs, unsub)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		for {
			select {
			case <-lifetime.Done():
				return
			case barrier := <-m.barriers:
				// CDP responses can overtake the recorder consumer. Drain the ordered
				// events already queued before acknowledging a command's read barrier.
			drain:
				for i := 0; i < 4096; i++ {
					select {
					case event, ok := <-events:
						if !ok {
							break drain
						}
						m.handle(event)
					default:
						break drain
					}
				}
				close(barrier)
			case event, ok := <-events:
				if !ok {
					return
				}
				m.handle(event)
			}
		}
	}()
	setup, stop := context.WithTimeout(lifetime, 5*time.Second)
	err = c.Call(setup, "", "Browser.setDownloadBehavior", map[string]any{"behavior": "allow", "downloadPath": config.DownloadDir, "eventsEnabled": true}, nil)
	if err == nil {
		err = c.Call(setup, "", "Target.setAutoAttach", map[string]any{"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true, "filter": []map[string]any{{"type": "page", "exclude": false}, {"exclude": true}}}, nil)
	}
	stop()
	if err != nil {
		m.Close()
		return nil, fmt.Errorf("enable Wok download tracking (requires Chromium Browser.setDownloadBehavior): %w", err)
	}
	if err = m.Sync(lifetime); err != nil {
		m.Close()
		return nil, err
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-lifetime.Done():
				return
			case <-c.Done():
				cancel()
				return
			case <-ticker.C:
				_ = m.Sync(lifetime)
			}
		}
	}()
	return m, nil
}
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		m.cancel()
		for _, unsub := range m.unsubs {
			unsub()
		}
		m.wg.Wait()
		m.syncMu.Lock()
		defer m.syncMu.Unlock()
		m.mu.Lock()
		sessions := make([]string, 0, len(m.sessions))
		for session := range m.sessions {
			sessions = append(sessions, session)
		}
		m.tabs = map[string]*tab{}
		m.sessions = map[string]*tab{}
		m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = m.c.Call(ctx, "", "Target.setAutoAttach", map[string]any{"autoAttach": false, "waitForDebuggerOnStart": false, "flatten": true}, nil)
		for _, session := range sessions {
			_ = m.c.Call(ctx, "", "Target.detachFromTarget", map[string]string{"sessionId": session}, nil)
		}
	})
}

// Sync reconciles page targets. Calls are serialized and bounded to five seconds.
func (m *Manager) Sync(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	if err := m.syncTargets(ctx); err != nil {
		return err
	}
	barrier := make(chan struct{})
	select {
	case m.barriers <- barrier:
	case <-ctx.Done():
		return ctx.Err()
	case <-m.ctx.Done():
		return m.ctx.Err()
	}
	select {
	case <-barrier:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-m.ctx.Done():
		return m.ctx.Err()
	}
}
func (m *Manager) syncTargets(parent context.Context) error {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	if err := m.ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	var targets struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
		} `json:"targetInfos"`
	}
	if err := m.c.Call(ctx, "", "Target.getTargets", nil, &targets); err != nil {
		return err
	}
	alive := map[string]bool{}
	for _, target := range targets.TargetInfos {
		if target.Type != "page" {
			continue
		}
		alive[target.TargetID] = true
		m.mu.Lock()
		existing := m.tabs[target.TargetID]
		m.mu.Unlock()
		if existing != nil {
			continue
		}
		var attached struct {
			SessionID string `json:"sessionId"`
		}
		if err := m.c.Call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": target.TargetID, "flatten": true}, &attached); err != nil {
			return fmt.Errorf("attach Wok event recorder: %w", err)
		}
		if err := m.setupTab(ctx, target.TargetID, attached.SessionID); err != nil {
			return err
		}
	}
	m.mu.Lock()
	var stale []*tab
	for id, t := range m.tabs {
		if !alive[id] {
			stale = append(stale, t)
		}
	}
	m.mu.Unlock()
	for _, t := range stale {
		m.removeTab(t)
		_ = m.c.Call(ctx, "", "Target.detachFromTarget", map[string]string{"sessionId": t.session}, nil)
	}
	return nil
}
func (m *Manager) removeTab(t *tab) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tabs, t.id)
	delete(m.sessions, t.session)
	for frame, id := range m.frames {
		if id == t.id {
			delete(m.frames, frame)
		}
	}
}
func appendBounded[T any](entries []T, value T) []T {
	entries = append(entries, value)
	if len(entries) > 500 {
		copy(entries, entries[len(entries)-500:])
		var zero T
		entries[len(entries)-1] = zero
		entries = entries[:500]
	}
	return entries
}
func (m *Manager) handle(event cdp.Event) {
	if event.Method == "Target.attachedToTarget" {
		m.attached(event)
		return
	}
	if event.Method == "Target.detachedFromTarget" {
		var p struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(event.Params, &p) == nil {
			m.mu.Lock()
			t := m.sessions[p.SessionID]
			m.mu.Unlock()
			if t != nil {
				m.removeTab(t)
			}
		}
		return
	}
	if event.Method == "Fetch.authRequired" {
		m.proxy(event)
		return
	}
	if event.Method == "Fetch.requestPaused" {
		// Pick the route on the ordered event goroutine so timed routes are
		// consumed in request order; only the protocol reply runs async.
		route := m.matchRoute(event)
		go m.fetchPaused(event, route)
		return
	}
	if event.Method == "Page.javascriptDialogOpening" {
		m.dialog(event)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.HasPrefix(event.Method, "Browser.download") {
		m.download(event)
		return
	}
	t := m.sessions[event.SessionID]
	if t == nil {
		return
	}
	switch event.Method {
	case "Runtime.bindingCalled", "Page.screencastFrame", "Debugger.paused", "Debugger.resumed":
		m.devtoolsEventLocked(t, event)
	case "Runtime.consoleAPICalled":
		var p struct {
			Type      string            `json:"type"`
			Args      []json.RawMessage `json:"args"`
			Timestamp float64           `json:"timestamp"`
		}
		if json.Unmarshal(event.Params, &p) != nil {
			return
		}
		texts := make([]string, 0, len(p.Args))
		for _, arg := range p.Args {
			var value struct {
				Value               json.RawMessage `json:"value"`
				Description         string          `json:"description"`
				UnserializableValue string          `json:"unserializableValue"`
			}
			if json.Unmarshal(arg, &value) != nil {
				continue
			}
			text := value.Description
			if value.UnserializableValue != "" {
				text = value.UnserializableValue
			}
			if len(value.Value) > 0 {
				var s string
				if json.Unmarshal(value.Value, &s) == nil {
					text = s
				} else {
					text = string(value.Value)
				}
			}
			texts = append(texts, text)
		}
		t.console = appendBounded(t.console, ConsoleEntry{p.Type, strings.Join(texts, " "), p.Args, p.Timestamp})
	case "Runtime.exceptionThrown":
		var p struct {
			Timestamp        float64 `json:"timestamp"`
			ExceptionDetails struct {
				Text      string `json:"text"`
				Exception struct {
					Description string `json:"description"`
				} `json:"exception"`
			} `json:"exceptionDetails"`
		}
		if json.Unmarshal(event.Params, &p) != nil {
			return
		}
		text := p.ExceptionDetails.Text
		if p.ExceptionDetails.Exception.Description != "" {
			text = p.ExceptionDetails.Exception.Description
		}
		t.errors = appendBounded(t.errors, ErrorEntry{text, append(json.RawMessage(nil), event.Params...), p.Timestamp})
	case "Page.frameNavigated":
		var p struct {
			Frame struct {
				ID       string `json:"id"`
				ParentID string `json:"parentId"`
				LoaderID string `json:"loaderId"`
			} `json:"frame"`
		}
		if json.Unmarshal(event.Params, &p) != nil {
			return
		}
		m.frames[p.Frame.ID] = t.id
		if p.Frame.ParentID == "" {
			t.console = []ConsoleEntry{}
			t.errors = []ErrorEntry{}
			network := []*NetworkEntry{}
			requests := map[string]*NetworkEntry{}
			inflight := map[string]bool{}
			// Navigation's Document request arrives before frameNavigated; retain the
			// new loader's requests while dropping events from the previous document.
			for _, record := range t.network {
				if p.Frame.LoaderID != "" && record.loader == p.Frame.LoaderID {
					network = append(network, record)
					requests[record.RequestID] = record
					if t.inflight[record.RequestID] {
						inflight[record.RequestID] = true
					}
				}
			}
			t.network = network
			t.requests = requests
			t.inflight = inflight
			t.authAttempts = map[string]int{}
			t.authNetwork = map[string]string{}
			for frame, id := range m.frames {
				if id == t.id && frame != p.Frame.ID {
					delete(m.frames, frame)
				}
			}
			t.dialogs = []DialogEntry{}
			t.lastActivity = time.Now().UnixMilli()
		}
	case "Network.requestWillBeSent":
		var p struct {
			RequestID        string  `json:"requestId"`
			LoaderID         string  `json:"loaderId"`
			Type             string  `json:"type"`
			Timestamp        float64 `json:"timestamp"`
			RedirectResponse *struct {
				Status int `json:"status"`
			} `json:"redirectResponse"`
			Request struct {
				URL         string         `json:"url"`
				Method      string         `json:"method"`
				Headers     map[string]any `json:"headers"`
				PostData    string         `json:"postData"`
				HasPostData bool           `json:"hasPostData"`
			} `json:"request"`
		}
		if json.Unmarshal(event.Params, &p) != nil {
			return
		}
		record := &NetworkEntry{RequestID: p.RequestID, URL: p.Request.URL, Method: p.Request.Method, Type: p.Type, started: p.Timestamp, loader: p.LoaderID, requestHeaders: p.Request.Headers, postData: p.Request.PostData, hasPostData: p.Request.HasPostData}
		if old := t.requests[p.RequestID]; old != nil {
			old.Finished = true
			if p.RedirectResponse != nil {
				old.Status = p.RedirectResponse.Status
			}
			old.DurationMs = (p.Timestamp - old.started) * 1000
		}
		if len(t.network) == 500 {
			old := t.network[0]
			if t.requests[old.RequestID] == old {
				delete(t.requests, old.RequestID)
			}
		}
		t.network = appendBounded(t.network, record)
		t.requests[p.RequestID] = record
		if p.Type != "WebSocket" && p.Type != "EventSource" {
			t.inflight[p.RequestID] = true
		}
		t.lastActivity = time.Now().UnixMilli()
	case "Network.responseReceived":
		var p struct {
			RequestID string `json:"requestId"`
			Type      string `json:"type"`
			Response  struct {
				Status   int            `json:"status"`
				Headers  map[string]any `json:"headers"`
				MimeType string         `json:"mimeType"`
			} `json:"response"`
		}
		if json.Unmarshal(event.Params, &p) != nil {
			return
		}
		if record := t.requests[p.RequestID]; record != nil {
			record.Status = p.Response.Status
			record.Type = p.Type
			record.responseHeaders = p.Response.Headers
			record.mimeType = p.Response.MimeType
		}
		if p.Type == "WebSocket" || p.Type == "EventSource" {
			delete(t.inflight, p.RequestID)
		}
		t.lastActivity = time.Now().UnixMilli()
	case "Network.loadingFinished", "Network.loadingFailed":
		var p struct {
			RequestID string  `json:"requestId"`
			Timestamp float64 `json:"timestamp"`
			ErrorText string  `json:"errorText"`
		}
		if json.Unmarshal(event.Params, &p) != nil {
			return
		}
		if record := t.requests[p.RequestID]; record != nil {
			record.Finished = true
			record.Error = p.ErrorText
			record.DurationMs = (p.Timestamp - record.started) * 1000
		}
		delete(t.inflight, p.RequestID)
		for fetchID, networkID := range t.authNetwork {
			if networkID == p.RequestID {
				delete(t.authAttempts, fetchID)
				delete(t.authNetwork, fetchID)
			}
		}
		t.lastActivity = time.Now().UnixMilli()
	}
}
func (m *Manager) download(event cdp.Event) {
	if event.Method == "Browser.downloadWillBegin" {
		var p struct {
			GUID              string `json:"guid"`
			FrameID           string `json:"frameId"`
			URL               string `json:"url"`
			SuggestedFilename string `json:"suggestedFilename"`
		}
		if json.Unmarshal(event.Params, &p) != nil {
			return
		}
		name := filepath.Base(strings.ReplaceAll(p.SuggestedFilename, "\\", "/"))
		if name == "." || name == "/" || name == ".." {
			name = ""
		}
		record := &DownloadEntry{GUID: p.GUID, Tab: m.frames[p.FrameID], URL: p.URL, SuggestedFilename: p.SuggestedFilename, State: "inProgress"}
		record.name = name
		record.before = map[string]bool{}
		if entries, err := os.ReadDir(m.config.DownloadDir); err == nil {
			for _, entry := range entries {
				if downloadNameMatches(entry.Name(), name) {
					record.before[entry.Name()] = true
				}
			}
		}
		if len(m.downloads) == 500 {
			delete(m.downloadIDs, m.downloads[0].GUID)
		}
		m.downloads = appendBounded(m.downloads, record)
		m.downloadIDs[p.GUID] = record
	} else {
		var p struct {
			GUID          string  `json:"guid"`
			State         string  `json:"state"`
			ReceivedBytes float64 `json:"receivedBytes"`
			TotalBytes    float64 `json:"totalBytes"`
			FilePath      string  `json:"filePath"`
		}
		if json.Unmarshal(event.Params, &p) != nil {
			return
		}
		if record := m.downloadIDs[p.GUID]; record != nil {
			record.State = p.State
			record.ReceivedBytes = p.ReceivedBytes
			record.TotalBytes = p.TotalBytes
			if p.State == "completed" {
				record.Path = m.completedDownloadPath(record)
			}
			if p.FilePath != "" && filepath.IsAbs(p.FilePath) {
				relative, err := filepath.Rel(m.config.DownloadDir, p.FilePath)
				if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					record.Path = p.FilePath
				}
			}
		}
	}
}
func (m *Manager) dialog(event cdp.Event) {
	var p struct {
		Type          string `json:"type"`
		Message       string `json:"message"`
		DefaultPrompt string `json:"defaultPrompt"`
	}
	if json.Unmarshal(event.Params, &p) != nil {
		return
	}
	m.mu.Lock()
	t := m.sessions[event.SessionID]
	policy := m.policy
	m.mu.Unlock()
	if t == nil {
		return
	}
	accepted := p.Type == "alert" || policy.Accept
	record := DialogEntry{Type: p.Type, Message: p.Message, DefaultPrompt: p.DefaultPrompt, Accepted: accepted, Timestamp: time.Now().UnixMilli()}
	params := map[string]any{"accept": accepted}
	if p.Type == "prompt" && accepted {
		params["promptText"] = policy.PromptText
		record.PromptText = policy.PromptText
	}
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	err := m.c.Call(ctx, event.SessionID, "Page.handleJavaScriptDialog", params, nil)
	cancel()
	if err != nil {
		record.Error = err.Error()
	}
	m.mu.Lock()
	if m.tabs[t.id] == t {
		t.dialogs = appendBounded(t.dialogs, record)
	}
	m.mu.Unlock()
}
func (m *Manager) tabLocked(id string) (*tab, error) {
	if id != "" {
		if t := m.tabs[id]; t != nil {
			return t, nil
		}
		return nil, errors.New("Wok tab not found")
	}
	ids := make([]string, 0, len(m.tabs))
	for id := range m.tabs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return nil, errors.New("Wok has no page tabs")
	}
	return m.tabs[ids[0]], nil
}

func (m *Manager) proxy(event cdp.Event) {
	if m.config.ProxyUsername == "" {
		return
	}
	var p struct {
		RequestID     string `json:"requestId"`
		NetworkID     string `json:"networkId"`
		AuthChallenge struct {
			Source string `json:"source"`
		} `json:"authChallenge"`
	}
	if json.Unmarshal(event.Params, &p) != nil || p.RequestID == "" {
		return
	}
	m.mu.Lock()
	t := m.sessions[event.SessionID]
	if t == nil {
		m.mu.Unlock()
		return
	}
	if p.NetworkID != "" {
		t.authNetwork[p.RequestID] = p.NetworkID
	}
	method := "Fetch.continueRequest"
	params := map[string]any{"requestId": p.RequestID}
	if event.Method == "Fetch.authRequired" {
		method = "Fetch.continueWithAuth"
		response := map[string]string{"response": "Default"}
		if p.AuthChallenge.Source == "Proxy" {
			if t.authAttempts[p.RequestID] > 0 {
				response["response"] = "CancelAuth"
			} else {
				response["response"] = "ProvideCredentials"
				response["username"] = m.config.ProxyUsername
				response["password"] = m.config.ProxyPassword
				t.authAttempts[p.RequestID]++
			}
		}
		params["authChallengeResponse"] = response
	}
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	defer cancel()
	_ = m.c.Call(ctx, event.SessionID, method, params, nil)
}

// completedDownloadPath avoids reporting an older file when Chromium resolves
// a suggested-name collision. Ambiguous concurrent downloads leave Path empty.
func (m *Manager) completedDownloadPath(record *DownloadEntry) string {
	entries, err := os.ReadDir(m.config.DownloadDir)
	if err != nil {
		return ""
	}
	found := ""
	for _, entry := range entries {
		if record.before[entry.Name()] || !downloadNameMatches(entry.Name(), record.name) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if found != "" {
			return ""
		}
		found = filepath.Join(m.config.DownloadDir, entry.Name())
	}
	return found
}
func downloadNameMatches(candidate, name string) bool {
	if name == "" {
		return false
	}
	if candidate == name {
		return true
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	prefix := stem + " ("
	suffix := ")" + ext
	if !strings.HasPrefix(candidate, prefix) || !strings.HasSuffix(candidate, suffix) {
		return false
	}
	number := strings.TrimSuffix(strings.TrimPrefix(candidate, prefix), suffix)
	n, err := strconv.Atoi(number)
	return err == nil && n > 0
}

// setupTab is serialized with discovery and auto-attachment. No manager state
// mutex is held across protocol calls, allowing CDP responses to be dispatched.
func (m *Manager) setupTab(ctx context.Context, id, session string) error {
	m.mu.Lock()
	existing := m.tabs[id]
	m.mu.Unlock()
	if existing != nil {
		if existing.session != session { // A polling attach raced browser auto-attachment.
			_ = m.c.Call(ctx, session, "Runtime.runIfWaitingForDebugger", nil, nil)
			_ = m.c.Call(ctx, "", "Target.detachFromTarget", map[string]string{"sessionId": session}, nil)
		}
		return nil
	}
	t := &tab{id: id, session: session, console: []ConsoleEntry{}, errors: []ErrorEntry{}, network: []*NetworkEntry{}, dialogs: []DialogEntry{}, authAttempts: map[string]int{}, authNetwork: map[string]string{}, requests: map[string]*NetworkEntry{}, inflight: map[string]bool{}, lastActivity: time.Now().UnixMilli()}
	m.mu.Lock()
	m.tabs[id] = t
	m.sessions[session] = t
	m.mu.Unlock()
	methods := []string{"Runtime.enable", "Network.enable", "Page.enable"}
	if m.config.ProxyUsername != "" {
		methods = append(methods, "Fetch.enable")
	}
	for _, method := range methods {
		var params any
		if method == "Fetch.enable" {
			params = map[string]any{"handleAuthRequests": true}
		}
		if err := m.c.Call(ctx, session, method, params, nil); err != nil {
			m.removeTab(t)
			_ = m.c.Call(ctx, "", "Target.detachFromTarget", map[string]string{"sessionId": session}, nil)
			return fmt.Errorf("enable Wok recorder %s: %w", method, err)
		}
	}
	var tree struct {
		FrameTree struct {
			Frame struct {
				ID string `json:"id"`
			} `json:"frame"`
		} `json:"frameTree"`
	}
	if err := m.c.Call(ctx, session, "Page.getFrameTree", nil, &tree); err == nil && tree.FrameTree.Frame.ID != "" {
		m.mu.Lock()
		m.frames[tree.FrameTree.Frame.ID] = id
		m.mu.Unlock()
	}
	m.setupDevtools(session)
	return nil
}
func (m *Manager) attached(event cdp.Event) {
	var p struct {
		SessionID          string `json:"sessionId"`
		WaitingForDebugger bool   `json:"waitingForDebugger"`
		TargetInfo         struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
		} `json:"targetInfo"`
	}
	if json.Unmarshal(event.Params, &p) != nil || p.SessionID == "" {
		return
	}
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	defer cancel()
	// Resume is unconditional, including unsupported target types and setup errors.
	// Use an independent short cleanup context if setup exhausted its deadline.
	defer func() {
		resumeCtx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = m.c.Call(resumeCtx, p.SessionID, "Runtime.runIfWaitingForDebugger", nil, nil)
	}()
	if p.TargetInfo.Type != "page" {
		return
	}
	_ = m.setupTab(ctx, p.TargetInfo.TargetID, p.SessionID)
}
