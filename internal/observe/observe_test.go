package observe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alg0rix/wok/internal/cdp"
	"github.com/gorilla/websocket"
)

type mockRequest struct {
	ID        int             `json:"id"`
	SessionID string          `json:"sessionId"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params"`
}
type mockBrowser struct {
	mu      sync.Mutex
	conn    *websocket.Conn
	calls   []mockRequest
	targets []map[string]string
}

func newMock(t *testing.T) (*mockBrowser, *cdp.Client) {
	t.Helper()
	mock := &mockBrowser{targets: []map[string]string{{"targetId": "page1", "type": "page"}, {"targetId": "worker1", "type": "service_worker"}}}
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		mock.mu.Lock()
		mock.conn = conn
		mock.mu.Unlock()
		for {
			var request mockRequest
			if conn.ReadJSON(&request) != nil {
				return
			}
			mock.mu.Lock()
			mock.calls = append(mock.calls, request)
			var result any = map[string]any{}
			switch request.Method {
			case "Target.getTargets":
				result = map[string]any{"targetInfos": mock.targets}
			case "Target.attachToTarget":
				var p struct {
					TargetID string `json:"targetId"`
				}
				_ = json.Unmarshal(request.Params, &p)
				result = map[string]string{"sessionId": "log-" + p.TargetID}
			case "Page.getFrameTree":
				result = map[string]any{"frameTree": map[string]any{"frame": map[string]string{"id": "frame1"}}}
			}
			err = conn.WriteJSON(map[string]any{"id": request.ID, "result": result})
			mock.mu.Unlock()
			if err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	c, err := cdp.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return mock, c
}
func (mock *mockBrowser) event(t *testing.T, method, session string, params any) {
	t.Helper()
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if err := mock.conn.WriteJSON(map[string]any{"method": method, "sessionId": session, "params": params}); err != nil {
		t.Fatal(err)
	}
}
func startManager(t *testing.T, config Config) (*Manager, *mockBrowser) {
	t.Helper()
	mock, c := newMock(t)
	if config.Dir == "" {
		config.Dir = t.TempDir()
	}
	m, err := New(context.Background(), c, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m, mock
}
func readRoute(t *testing.T, m *Manager, route string, result any) int {
	t.Helper()
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest("GET", route, nil))
	if result != nil {
		if err := json.Unmarshal(w.Body.Bytes(), result); err != nil {
			t.Fatal(w.Body.String(), err)
		}
	}
	return w.Code
}
func await(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not reached")
}
func requestParams(id, kind, loader string, timestamp float64) map[string]any {
	return map[string]any{"requestId": id, "type": kind, "loaderId": loader, "timestamp": timestamp, "request": map[string]string{"url": "https://example.test/" + id, "method": "GET"}}
}
func TestOrderedLogsNetworkAndDocumentReset(t *testing.T) {
	m, mock := startManager(t, Config{})
	mock.event(t, "Runtime.consoleAPICalled", "log-page1", map[string]any{"type": "log", "args": []map[string]any{{"type": "string", "value": "hello"}}, "timestamp": 1})
	mock.event(t, "Runtime.exceptionThrown", "log-page1", map[string]any{"timestamp": 2, "exceptionDetails": map[string]any{"text": "Uncaught", "exception": map[string]string{"description": "Error: broken"}}})
	mock.event(t, "Network.requestWillBeSent", "log-page1", requestParams("r1", "Image", "old", 10))
	mock.event(t, "Network.responseReceived", "log-page1", map[string]any{"requestId": "r1", "type": "Image", "response": map[string]int{"status": 201}})
	mock.event(t, "Network.loadingFinished", "log-page1", map[string]any{"requestId": "r1", "timestamp": 10.25})
	await(t, func() bool {
		var entries []NetworkEntry
		readRoute(t, m, "/runtime/network?tab=page1", &entries)
		return len(entries) == 1 && entries[0].Finished
	})
	var console []ConsoleEntry
	readRoute(t, m, "/runtime/console", &console)
	if len(console) != 1 || console[0].Text != "hello" {
		t.Fatalf("console %+v", console)
	}
	var errors []ErrorEntry
	readRoute(t, m, "/runtime/errors", &errors)
	if len(errors) != 1 || errors[0].Text != "Error: broken" {
		t.Fatalf("errors %+v", errors)
	}
	var network []NetworkEntry
	readRoute(t, m, "/runtime/network", &network)
	if network[0].Status != 201 || network[0].DurationMs != 250 {
		t.Fatalf("network %+v", network)
	}
	// A new Document request precedes frameNavigated on the wire.
	mock.event(t, "Network.requestWillBeSent", "log-page1", requestParams("newdoc", "Document", "new", 11))
	mock.event(t, "Page.frameNavigated", "log-page1", map[string]any{"frame": map[string]string{"id": "frame1", "loaderId": "new"}})
	await(t, func() bool {
		var entries []ConsoleEntry
		readRoute(t, m, "/runtime/console", &entries)
		return len(entries) == 0
	})
	readRoute(t, m, "/runtime/network", &network)
	if len(network) != 1 || network[0].RequestID != "newdoc" {
		t.Fatalf("lost navigation request: %+v", network)
	}
	var state NetworkState
	readRoute(t, m, "/runtime/network-state", &state)
	if state.Inflight != 1 {
		t.Fatalf("navigation inflight %+v", state)
	}
	mock.event(t, "Network.loadingFailed", "log-page1", map[string]any{"requestId": "newdoc", "timestamp": 12, "errorText": "net::ERR_FAILED"})
	mock.event(t, "Network.requestWillBeSent", "log-page1", requestParams("sse", "EventSource", "new", 12))
	await(t, func() bool { readRoute(t, m, "/runtime/network-state", &state); return state.Inflight == 0 })
	readRoute(t, m, "/runtime/network", &network)
	if network[0].Error != "net::ERR_FAILED" {
		t.Fatal(network)
	}
	if readRoute(t, m, "/runtime/network?tab=missing", nil) != 404 {
		t.Fatal("invalid tab silently selected")
	}
}
func TestDownloadsCollisionAndFrameMapping(t *testing.T) {
	m, mock := startManager(t, Config{})
	old := filepath.Join(m.config.DownloadDir, "report.txt")
	if err := os.WriteFile(old, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	mock.event(t, "Browser.downloadWillBegin", "", map[string]string{"guid": "download1", "frameId": "frame1", "url": "https://example.test/file", "suggestedFilename": "report.txt"})
	await(t, func() bool {
		var entries []DownloadEntry
		readRoute(t, m, "/runtime/downloads", &entries)
		return len(entries) == 1
	})
	actual := filepath.Join(m.config.DownloadDir, "report (1).txt")
	if err := os.WriteFile(actual, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	mock.event(t, "Browser.downloadProgress", "", map[string]any{"guid": "download1", "state": "completed", "receivedBytes": 3, "totalBytes": 3})
	var downloads []DownloadEntry
	await(t, func() bool {
		readRoute(t, m, "/runtime/downloads?tab=page1", &downloads)
		return len(downloads) == 1 && downloads[0].State == "completed"
	})
	if downloads[0].Path != actual || downloads[0].Tab != "page1" || downloads[0].ReceivedBytes != 3 {
		t.Fatalf("download %+v", downloads)
	}
}
func TestDialogPolicyAndStrictHTTP(t *testing.T) {
	m, mock := startManager(t, Config{})
	mock.event(t, "Page.javascriptDialogOpening", "log-page1", map[string]string{"type": "confirm", "message": "Proceed?"})
	var dialogs []DialogEntry
	await(t, func() bool { readRoute(t, m, "/runtime/dialogs", &dialogs); return len(dialogs) == 1 })
	if dialogs[0].Accepted {
		t.Fatal("confirm not dismissed")
	}
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/runtime/dialog-policy", strings.NewReader(`{"accept":true,"promptText":"Wok"}`)))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	mock.event(t, "Page.javascriptDialogOpening", "log-page1", map[string]string{"type": "prompt", "message": "Name?", "defaultPrompt": "default"})
	await(t, func() bool { readRoute(t, m, "/runtime/dialogs", &dialogs); return len(dialogs) == 2 })
	if !dialogs[1].Accepted || dialogs[1].PromptText != "Wok" {
		t.Fatal(dialogs)
	}
	data, err := os.ReadFile(filepath.Join(m.config.Dir, "dialog-policy.json"))
	if err != nil || !strings.Contains(string(data), `"accept":true`) {
		t.Fatal(string(data), err)
	}
	for _, body := range []string{`{}`, `{"accept":true,"unknown":1}`, `{"accept":true} {}`, `{"accept":"yes"}`, `{"accept":true,"promptText":"` + strings.Repeat("x", 65536) + `"}`} {
		w := httptest.NewRecorder()
		m.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/runtime/dialog-policy", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatalf("accepted invalid policy (%d)", w.Code)
		}
	}
	w = httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/runtime/console", nil))
	if w.Code != 405 {
		t.Fatal(w.Code)
	}
}
func TestProxyCredentialsOnlyForProxyAndRetryBounded(t *testing.T) {
	m, mock := startManager(t, Config{ProxyUsername: "proxy-user", ProxyPassword: "private-pass"})
	for _, source := range []string{"Proxy", "Proxy", "Server"} {
		mock.event(t, "Fetch.authRequired", "log-page1", map[string]any{"requestId": "auth1", "authChallenge": map[string]string{"source": source}})
	}
	mock.event(t, "Fetch.requestPaused", "log-page1", map[string]string{"requestId": "paused1"})
	var replies []mockRequest
	await(t, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		replies = nil
		for _, call := range mock.calls {
			if call.Method == "Fetch.continueWithAuth" || call.Method == "Fetch.continueRequest" {
				replies = append(replies, call)
			}
		}
		return len(replies) == 4
	})
	for i, want := range []string{"ProvideCredentials", "CancelAuth", "Default"} {
		var p struct {
			AuthChallengeResponse map[string]string `json:"authChallengeResponse"`
		}
		if err := json.Unmarshal(replies[i].Params, &p); err != nil {
			t.Fatal(err)
		}
		if p.AuthChallengeResponse["response"] != want {
			t.Fatal(string(replies[i].Params))
		}
		if i > 0 && (p.AuthChallengeResponse["username"] != "" || p.AuthChallengeResponse["password"] != "") {
			t.Fatal("credentials exposed outside initial proxy challenge")
		}
	}
	if replies[3].Method != "Fetch.continueRequest" {
		t.Fatal("paused request not continued")
	}
	// The observer never changes device emulation or user agent.
	mock.mu.Lock()
	defer mock.mu.Unlock()
	for _, call := range mock.calls {
		if strings.HasPrefix(call.Method, "Emulation.") || call.Method == "Network.setUserAgentOverride" {
			t.Fatal("observer changed emulation")
		}
	}
	_ = m
}
func TestBoundedBuffersAndTargetReconciliation(t *testing.T) {
	m, mock := startManager(t, Config{})
	for i := 0; i < 550; i++ {
		params, _ := json.Marshal(map[string]any{"type": "log", "args": []map[string]int{{"value": i}}})
		m.handle(cdp.Event{Method: "Runtime.consoleAPICalled", SessionID: "log-page1", Params: params})
	}
	var entries []ConsoleEntry
	readRoute(t, m, "/runtime/console", &entries)
	if len(entries) != 500 || entries[0].Text != "50" || entries[499].Text != "549" {
		t.Fatalf("bad buffer %d %+v", len(entries), entries[0])
	}
	mock.mu.Lock()
	mock.targets = []map[string]string{{"targetId": "page2", "type": "page"}}
	mock.mu.Unlock()
	if err := m.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if readRoute(t, m, "/runtime/console?tab=page1", nil) != 404 || readRoute(t, m, "/runtime/console?tab=page2", nil) != 200 {
		t.Fatal("target reconciliation failed")
	}
}
