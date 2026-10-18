package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alg0rix/wok/internal/cdp"
	"github.com/gorilla/websocket"
)

func TestNormalizeURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"example.com", "https://example.com"},
		{"localhost:8080/path", "https://localhost:8080/path"},
		{"http://localhost:8080/", "http://localhost:8080/"},
		{"about:blank", "about:blank"},
		{"data:text/html,Hello", "data:text/html,Hello"},
	} {
		got, err := normalizeURL(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("normalizeURL(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{"", "javascript:alert(1)", "JaVaScRiPt:alert(1)", "ftp://example.com"} {
		if got, err := normalizeURL(in); err == nil {
			t.Errorf("normalizeURL(%q) accepted %q", in, got)
		}
	}
}
func TestKeyModifiers(t *testing.T) {
	k, err := parseKey("Control+Shift+a")
	if err != nil {
		t.Fatal(err)
	}
	if k.Key != "A" || k.Code != "KeyA" || k.VK != 65 || k.Modifiers != 10 || k.Text != "" {
		t.Fatalf("unexpected shortcut: %+v", k)
	}
	k, err = parseKey("Enter")
	if err != nil || k.Text != "\r" || k.VK != 13 {
		t.Fatalf("unexpected Enter: %+v %v", k, err)
	}
	if _, err = parseKey("Weird+Enter"); err == nil {
		t.Fatal("accepted invalid modifier")
	}
	if _, err = parseKey("InvalidKey"); err == nil {
		t.Fatal("accepted invalid key")
	}
}

// protocolPage serves CDP responses so API behavior can be verified without
// depending on a browser executable in unit tests.
func protocolPage(t *testing.T, respond func(string, map[string]any) any) *Page {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var req struct {
				ID     int            `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if conn.ReadJSON(&req) != nil {
				return
			}
			result := respond(req.Method, req.Params)
			if result == nil {
				return
			}
			if conn.WriteJSON(map[string]any{"id": req.ID, "result": result}) != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	client, err := cdp.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return &Page{Client: client, SessionID: "session", TargetID: "target"}
}
func TestGetPageTextAndViewportScreenshot(t *testing.T) {
	var expression string
	var beyond any
	page := protocolPage(t, func(method string, params map[string]any) any {
		switch method {
		case "Runtime.evaluate":
			expression, _ = params["expression"].(string)
			return map[string]any{"result": map[string]any{"value": "visible page text"}}
		case "Page.captureScreenshot":
			beyond = params["captureBeyondViewport"]
			return map[string]any{"data": "cG5n"}
		default:
			return map[string]any{}
		}
	})
	text, err := page.Get(context.Background(), "text", "", "", nil)
	if err != nil || text != "visible page text" || expression != "document.body?.innerText ?? ''" {
		t.Fatalf("Get page text = %v, %v; expression %q", text, err, expression)
	}
	data, err := page.Screenshot(context.Background())
	if err != nil || string(data) != "png" || beyond != false {
		t.Fatalf("Screenshot = %q, %v; captureBeyondViewport=%v", data, err, beyond)
	}
}
func TestSnapshotRefPrefixAndMonotonicIDs(t *testing.T) {
	page := protocolPage(t, func(method string, params map[string]any) any {
		if method == "DOM.getDocument" {
			return map[string]any{"root": map[string]any{"backendNodeId": 42}}
		}
		return map[string]any{"nodes": []any{map[string]any{"backendDOMNodeId": 99, "role": map[string]any{"value": "button"}, "name": map[string]any{"value": "Save"}}}}
	})
	refs := &Refs{}
	for i, want := range []string{"@e1", "@e2"} {
		nodes, err := page.Snapshot(context.Background(), true, refs)
		if err != nil || len(nodes) != 1 || nodes[0].Ref != want {
			t.Fatalf("snapshot %d = %+v, %v", i, nodes, err)
		}
		key := strings.TrimPrefix(want, "@")
		if len(refs.Items) != 1 || refs.Items[key].DocumentID != 42 || refs.Items[key].TargetID != "target" {
			t.Fatalf("refs = %+v", refs)
		}
	}
	if _, ok := refs.Items["e1"]; ok {
		t.Fatal("old snapshot ref retained")
	}
}
func TestNavigateReturnsWhenDisconnected(t *testing.T) {
	page := protocolPage(t, func(method string, params map[string]any) any {
		if method == "Page.navigate" {
			return map[string]any{"loaderId": "loader", "frameId": "frame"}
		}
		// The navigation response is followed by a CDP request only if the
		// caller sends one; close the transport independently below.
		return map[string]any{}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- page.Navigate(ctx, "https://example.com") }()
	time.AfterFunc(10*time.Millisecond, func() { page.Client.Close() })
	select {
	case err := <-done:
		if err == nil || err == context.DeadlineExceeded {
			t.Fatalf("Navigate error = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("Navigate did not detect CDP disconnect")
	}
}

func TestRenderKeyboardKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		vk   int
	}{{"Insert", 45}, {"F1", 112}, {"F12", 123}, {"Shift+F8", 119}} {
		k, err := parseKey(tc.name)
		if err != nil || k.VK != tc.vk || k.Key == "" || k.Code != k.Key || k.Text != "" {
			t.Errorf("parseKey(%q) = %+v, %v", tc.name, k, err)
		}
	}
	for _, name := range []string{"F0", "F13", "F1junk"} {
		if _, err := parseKey(name); err == nil {
			t.Errorf("accepted invalid function key %q", name)
		}
	}
}
