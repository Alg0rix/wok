package render

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func mcpRequest(t *testing.T, h *handler, body string) map[string]any {
	t.Helper()
	r := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	r.Host = h.address
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}
func TestMCPInitializeAndTools(t *testing.T) {
	h := &handler{address: "127.0.0.1:1234"}
	r := mcpRequest(t, h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`)
	result := r["result"].(map[string]any)
	if result["protocolVersion"] != "2024-11-05" {
		t.Fatal(result)
	}
	r = mcpRequest(t, h, `{"jsonrpc":"2.0","id":"list","method":"tools/list"}`)
	tools := r["result"].(map[string]any)["tools"].([]any)
	if len(tools) < 24 {
		t.Fatalf("only %d tools", len(tools))
	}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		schema := tool["inputSchema"].(map[string]any)
		if _, ok := schema["required"].([]any); !ok {
			t.Fatalf("invalid required schema for %s", tool["name"])
		}
	}
	r = mcpRequest(t, h, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"browser_skills","arguments":{}}}`)
	if r["result"].(map[string]any)["isError"] != false {
		t.Fatal(r)
	}
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	req.Host = h.address
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 204 || w.Body.Len() != 0 {
		t.Fatalf("notification response: %d %s", w.Code, w.Body.String())
	}
}
func TestMCPInvalidRequests(t *testing.T) {
	h := &handler{address: "127.0.0.1:1234"}
	cases := []struct {
		body string
		code float64
	}{{`{`, -32700}, {`{"jsonrpc":"1.0","id":1,"method":"initialize"}`, -32600}, {`{"jsonrpc":"2.0","id":1,"method":"missing"}`, -32601}, {`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser_shell","arguments":{}}}`, -32602}, {`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser_open","arguments":{"url":"x","profile":"other"}}}`, -32602}, {`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser_command","arguments":{"command":"run","args":["flow.yml"]}}}`, -32602}, {`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"browser_command","arguments":{"command":"skills","args":["install"]}}}`, -32602}}
	for _, tt := range cases {
		r := mcpRequest(t, h, tt.body)
		if r["error"].(map[string]any)["code"] != tt.code {
			t.Fatalf("%s: %v", tt.body, r)
		}
	}
}
func TestToolCommandKeepsLiteralArguments(t *testing.T) {
	args, err := toolCommand("browser_fill", map[string]any{"selector": "#x", "text": "--profile other; $(touch /tmp/nope)"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, []string{"fill", "#x", "--profile other; $(touch /tmp/nope)"}) {
		t.Fatal(args)
	}
	args, err = toolCommand("browser_viewport", map[string]any{"width": float64(390), "height": float64(844), "mobile": true, "dpr": float64(3)})
	if err != nil || !reflect.DeepEqual(args, []string{"viewport", "390", "844", "--mobile", "--dpr", "3"}) {
		t.Fatalf("%v %v", args, err)
	}
}
