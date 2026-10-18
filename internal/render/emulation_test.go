package render

import (
	"context"
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

func TestEmulationValidation(t *testing.T) {
	valid := []string{`{"width":390,"height":844,"deviceScaleFactor":3,"mobile":true}`, `{"width":1,"height":8192,"deviceScaleFactor":0.5,"mobile":false}`, `{"reset":true}`}
	for _, body := range valid {
		if _, err := decodeEmulation(strings.NewReader(body)); err != nil {
			t.Errorf("%s: %v", body, err)
		}
	}
	invalid := []string{`null`, `{}`, `{"reset":null}`, `{"reset":true,"width":null}`, `{"reset":true,"width":390}`, `{"reset":true,"extra":1}`, `{"width":8192,"height":8192,"deviceScaleFactor":4,"mobile":true}`, `{"reset":true} {}`, `{"reset":true}` + strings.Repeat(" ", 4097)}
	template := `{"width":%s,"height":844,"deviceScaleFactor":%s,"mobile":%s}`
	for _, values := range [][3]string{{"0", "3", "true"}, {"8193", "3", "true"}, {"390.5", "3", "true"}, {"390", "0.4", "true"}, {"390", "4.1", "true"}, {"390", "1e400", "true"}, {"390", "3", "null"}} {
		body := strings.Replace(template, "%s", values[0], 1)
		body = strings.Replace(body, "%s", values[1], 1)
		body = strings.Replace(body, "%s", values[2], 1)
		invalid = append(invalid, body)
	}
	for _, body := range invalid {
		if _, err := decodeEmulation(strings.NewReader(body)); err == nil {
			t.Errorf("accepted %.150s", body)
		}
	}
}

type emulationCall struct {
	method, session string
	params          map[string]any
}
type emulationFake struct {
	mu      sync.Mutex
	calls   []emulationCall
	targets []string
}

func emulationClient(t *testing.T) (*cdp.Client, *emulationFake) {
	t.Helper()
	fake := &emulationFake{targets: []string{"one"}}
	upgrade := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrade.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var request struct {
				ID      uint64         `json:"id"`
				Session string         `json:"sessionId"`
				Method  string         `json:"method"`
				Params  map[string]any `json:"params"`
			}
			if conn.ReadJSON(&request) != nil {
				return
			}
			fake.mu.Lock()
			fake.calls = append(fake.calls, emulationCall{request.Method, request.Session, request.Params})
			var result any = map[string]any{}
			switch request.Method {
			case "Target.getTargets":
				targets := []map[string]any{}
				for _, id := range fake.targets {
					targets = append(targets, map[string]any{"targetId": id, "type": "page"})
				}
				result = map[string]any{"targetInfos": targets}
			case "Target.createTarget":
				fake.targets = append(fake.targets, "created")
				result = map[string]any{"targetId": "created"}
			case "Target.attachToTarget":
				result = map[string]any{"sessionId": "session-" + request.Params["targetId"].(string)}
			case "Browser.getVersion":
				result = map[string]any{"product": "HeadlessChrome/130.0.1"}
			}
			fake.mu.Unlock()
			if conn.WriteJSON(map[string]any{"id": request.ID, "result": result}) != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	c, err := cdp.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c, fake
}
func TestEmulationHTTPPersistenceAndReset(t *testing.T) {
	c, fake := emulationClient(t)
	ready := filepath.Join(t.TempDir(), "render.json")
	state, err := newEmulation(context.Background(), c, ready)
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	h := &handler{client: c, address: "127.0.0.1:1234", emulationState: state}
	request := func(method, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/render/emulation", strings.NewReader(body))
		r.Host = h.address
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", ""); w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"reset":true}` {
		t.Fatalf("GET: %d %s", w.Code, w.Body.String())
	}
	w := request("POST", `{"width":390,"height":844,"deviceScaleFactor":3,"mobile":true}`)
	if w.Code != 200 {
		t.Fatalf("POST: %d %s", w.Code, w.Body.String())
	}
	saved, err := os.ReadFile(filepath.Join(filepath.Dir(ready), "viewport.json"))
	if err != nil {
		t.Fatal(err)
	}
	config, err := decodeEmulation(strings.NewReader(string(saved)))
	if err != nil || config.Width != 390 || !config.Mobile {
		t.Fatalf("saved: %s %v", saved, err)
	}
	// The owner session remains attached after the HTTP command returns.
	fake.mu.Lock()
	calls := append([]emulationCall(nil), fake.calls...)
	fake.targets = append(fake.targets, "two")
	fake.mu.Unlock()
	metrics, touch, ua := false, false, false
	for _, call := range calls {
		switch call.method {
		case "Target.detachFromTarget":
			t.Fatal("emulation owner detached")
		case "Emulation.setDeviceMetricsOverride":
			metrics = call.params["width"] == float64(390) && call.session == "session-one"
		case "Emulation.setTouchEmulationEnabled":
			touch = call.params["enabled"] == true
		case "Emulation.setUserAgentOverride":
			ua = strings.Contains(call.params["userAgent"].(string), "Android")
		}
	}
	if !metrics || !touch || !ua {
		t.Fatalf("missing overrides: metrics=%v touch=%v ua=%v", metrics, touch, ua)
	}
	// A tab opened by another CLI process receives the profile settings.
	deadline := time.Now().Add(3 * time.Second)
	found := false
	for time.Now().Before(deadline) {
		fake.mu.Lock()
		for _, call := range fake.calls {
			if call.session == "session-two" && call.method == "Emulation.setDeviceMetricsOverride" {
				found = true
			}
		}
		fake.mu.Unlock()
		if found {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !found {
		t.Fatal("new tab did not inherit emulation")
	}
	if w := request("POST", `{"reset":true}`); w.Code != 200 {
		t.Fatalf("reset: %d %s", w.Code, w.Body.String())
	}
	state.mu.Lock()
	retained := len(state.pages)
	state.mu.Unlock()
	if retained != 0 {
		t.Fatalf("retained %d sessions after reset", retained)
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(ready), "viewport.json"))
	if err != nil || string(data) != `{"reset":true}` {
		t.Fatalf("reset file %s %v", data, err)
	}
	if w := request("POST", `{"width":0}`); w.Code != 400 {
		t.Fatalf("invalid config: %d", w.Code)
	}
}
func TestEmulationRejectsCorruptStoredConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "viewport.json"), []byte(`{"width":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newEmulation(context.Background(), nil, filepath.Join(dir, "render.json")); err == nil {
		t.Fatal("corrupt configuration silently ignored")
	}
}
func TestEmulationRestoresBeforeReady(t *testing.T) {
	c, fake := emulationClient(t)
	dir := t.TempDir()
	data := []byte(`{"width":800,"height":600,"deviceScaleFactor":1,"mobile":false}`)
	if err := os.WriteFile(filepath.Join(dir, "viewport.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	state, err := newEmulation(context.Background(), c, filepath.Join(dir, "render.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	found := false
	for _, call := range fake.calls {
		if call.method == "Emulation.setDeviceMetricsOverride" && call.params["width"] == float64(800) {
			found = true
		}
	}
	if !found {
		t.Fatal("settings were not applied before startup returned")
	}
}
