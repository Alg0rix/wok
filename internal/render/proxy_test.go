package render

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alg0rix/wok/internal/cdp"
	"github.com/gorilla/websocket"
)

func TestCDPDiscoveryAndWebSocketRelay(t *testing.T) {
	upgrade := websocket.Upgrader{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrade.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var request struct {
				ID     uint64         `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if conn.ReadJSON(&request) != nil {
				return
			}
			var result any = map[string]any{"path": r.URL.Path, "expression": request.Params["expression"]}
			switch request.Method {
			case "Browser.getVersion":
				result = map[string]any{"product": "HeadlessChrome/130.0.1", "protocolVersion": "1.3"}
			case "Target.getTargets":
				result = map[string]any{"targetInfos": []map[string]any{{"targetId": "one", "type": "page", "title": "Wok", "url": "about:blank"}}}
			}
			if conn.WriteJSON(map[string]any{"id": request.ID, "result": result}) != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	endpoint := "ws" + strings.TrimPrefix(upstream.URL, "http") + "/devtools/browser/browser-test"
	c, err := cdp.Dial(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	h := &handler{client: c, endpoint: endpoint, options: Options{AuthToken: "secret"}}
	server := httptest.NewServer(h)
	defer server.Close()
	getJSON := func(path string) map[string]any {
		t.Helper()
		res, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			body, _ := io.ReadAll(res.Body)
			t.Fatalf("HTTP %d: %s", res.StatusCode, body)
		}
		var result map[string]any
		if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	version := getJSON("/json/version/?token=secret")
	ws := version["webSocketDebuggerUrl"].(string)
	if !strings.HasSuffix(ws, "/devtools/browser/browser-test?token=secret") {
		t.Fatal(ws)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	proxied, err := cdp.Dial(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := proxied.Call(ctx, "", "Browser.getVersion", nil, &result); err != nil {
		t.Fatal(err)
	}
	proxied.Close()
	if result["product"] != "HeadlessChrome/130.0.1" {
		t.Fatal(result)
	}
	pageWS := "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/page/one?token=secret"
	page, err := cdp.Dial(ctx, pageWS)
	if err != nil {
		t.Fatal(err)
	}
	if err := page.Call(ctx, "", "Runtime.evaluate", map[string]any{"expression": "1+1"}, &result); err != nil {
		t.Fatal(err)
	}
	page.Close()
	if result["path"] != "/devtools/page/one" || result["expression"] != "1+1" {
		t.Fatal(result)
	}
	res, err := http.Get(server.URL + "/devtools/page/missing?token=secret")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("missing page: %d", res.StatusCode)
	}
	res, err = http.Get(server.URL + "/json/list")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("unauthorized discovery: %d", res.StatusCode)
	}
	r := httptest.NewRequest("GET", "/json/list?token=secret", nil)
	r.Host = "wok.example"
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "wss://wok.example/devtools/page/one?token=secret") {
		t.Fatal(w.Body.String())
	}
}
