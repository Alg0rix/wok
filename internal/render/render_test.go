package render

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestViewerSecurity(t *testing.T) {
	h := &handler{address: "127.0.0.1:1234", token: "secret", cancel: func() {}}
	cases := []struct {
		name, method, path, host, origin, auth string
		want                                   int
	}{
		{"viewer", "GET", "/render", "127.0.0.1:1234", "", "", 200},
		{"health", "GET", "/health", "localhost:1234", "", "", 200},
		{"dns rebinding", "GET", "/render", "evil.example:1234", "", "", 403},
		{"wrong port", "GET", "/render", "localhost:9999", "", "", 403},
		{"cross origin screenshot", "GET", "/render/screenshot", "localhost:1234", "http://evil.example", "", 403},
		{"cross origin write", "POST", "/render/input", "localhost:1234", "http://evil.example", "", 403},
		{"null origin", "POST", "/render/input", "localhost:1234", "null", "", 403},
		{"wrong method", "POST", "/render", "localhost:1234", "", "", 405},
		{"shutdown token required", "POST", "/_shutdown", "localhost:1234", "", "", 401},
		{"shutdown wrong token", "POST", "/_shutdown", "localhost:1234", "", "Bearer nope", 401},
		{"shutdown valid", "POST", "/_shutdown", "localhost:1234", "http://localhost:1234", "Bearer secret", 204},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, tt.path, nil)
			r.Host = tt.host
			r.Header.Set("Origin", tt.origin)
			r.Header.Set("Authorization", tt.auth)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}
func TestDecodeInput(t *testing.T) {
	valid := []string{`{"type":"click","x":0,"y":20}`, `{"type":"move","x":10,"y":10}`, `{"type":"scroll","x":1,"y":2,"deltaX":0,"deltaY":-120}`, `{"type":"text","text":"hello"}`, `{"type":"key","key":"Enter"}`, `{"type":"navigate","url":"https://example.com"}`, `{"type":"navigate","url":"about:blank"}`}
	for _, body := range valid {
		if _, err := decodeInput(strings.NewReader(body)); err != nil {
			t.Errorf("valid %s: %v", body, err)
		}
	}
	invalid := []string{`null`, `{}`, `{"type":"click","x":-1,"y":0}`, `{"type":"click","x":1}`, `{"type":"click","x":1,"y":0,"button":"bogus"}`, `{"type":"click","x":1,"y":0,"key":"Enter"}`, `{"type":"text"}`, `{"type":"key","key":""}`, `{"type":"navigate","url":"javascript:alert(1)"}`, `{"type":"navigate","url":"http:foo"}`, `{"type":"text","text":"x","unknown":1}`, `{"type":"text","text":"x"} {}`, `{"type":"move","x":1e100,"y":1}`, `{"type":"scroll","x":1,"y":2,"deltaY":2}`, `{"type":"move","x":1,"y":2,"deltaY":2}`}
	for _, body := range invalid {
		if _, err := decodeInput(strings.NewReader(body)); err == nil {
			t.Errorf("accepted invalid %s", body)
		}
	}
}
func TestInputRejectsBeforeBrowserAccess(t *testing.T) {
	h := &handler{address: "127.0.0.1:1234"}
	r := httptest.NewRequest("POST", "/render/input", strings.NewReader(`{"type":"text","text":"x","unexpected":true}`))
	r.Host = h.address
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("got %d", w.Code)
	}
}
func TestPublishReadyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ready.json")
	if err := publish(path, []byte(`{"url":"http://127.0.0.1:1234","pid":1}`)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && stat.Mode().Perm() != 0600 {
		t.Fatalf("mode %v", stat.Mode())
	}
	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(files) != 1 {
		t.Fatalf("temporary files left: %v %v", files, err)
	}
}
func TestServeRejectsPublicBinding(t *testing.T) {
	for _, listen := range []string{"0.0.0.0:0", ":0", "[::]:0", "example.com:1234"} {
		if err := Serve(context.Background(), "", listen, "", ""); err == nil {
			t.Errorf("accepted %s", listen)
		}
	}
}
