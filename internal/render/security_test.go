package render

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRemoteAuthenticationAndOrigins(t *testing.T) {
	h := &handler{address: "0.0.0.0:1234", token: "private", cancel: func() {}, options: Options{AuthToken: "public", EmbedOrigins: []string{"https://app.example"}}}
	cases := []struct {
		method, path, origin, auth string
		want                       int
	}{
		{"GET", "/health", "", "", 401}, {"GET", "/health", "", "public", 401}, {"GET", "/health", "", "Bearer public", 200}, {"GET", "/health?token=public", "", "", 200},
		{"GET", "/render?token=public", "https://app.example", "", 200}, {"GET", "/render?token=public", "https://evil.example", "", 403},
		{"POST", "/_shutdown", "", "Bearer private", 204}, {"POST", "/_shutdown", "", "Bearer public", 401}, {"OPTIONS", "/render/input", "https://app.example", "", 204},
	}
	for _, tt := range cases {
		r := httptest.NewRequest(tt.method, tt.path, nil)
		r.Host = "wok.example"
		r.Header.Set("Origin", tt.origin)
		r.Header.Set("Authorization", tt.auth)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tt.want {
			t.Errorf("%s %s: %d %s", tt.method, tt.path, w.Code, w.Body.String())
		}
		if tt.origin == "https://app.example" && w.Code < 400 {
			if w.Header().Get("Access-Control-Allow-Origin") != tt.origin {
				t.Error("missing explicit-origin CORS")
			}
		}
	}
	r := httptest.NewRequest("GET", "/render?token=public", nil)
	r.Host = "wok.example"
	r.Header.Set("Origin", "https://wok.example")
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "https://app.example") {
		t.Fatal("missing frame ancestor")
	}
}
func TestOptionsAndLocalAddress(t *testing.T) {
	for _, origin := range []string{"javascript:alert(1)", "https://example.com/path", "https://example.com?q=x", "https://user@example.com"} {
		if validateOptions(Options{EmbedOrigins: []string{origin}}) == nil {
			t.Errorf("accepted %s", origin)
		}
	}
	for input, want := range map[string]string{"0.0.0.0:123": "127.0.0.1:123", "[::]:123": "[::1]:123", "127.0.0.1:123": "127.0.0.1:123"} {
		if got := localAddress(input); got != want {
			t.Errorf("%s: %s", input, got)
		}
	}
}
func TestRuntimeProtectedByGlobalAuth(t *testing.T) {
	called := false
	h := &handler{options: Options{AuthToken: "secret"}, runtime: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(204) })}
	r := httptest.NewRequest("GET", "/runtime/network", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 || called {
		t.Fatal("runtime route bypassed authentication")
	}
	r.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 || !called {
		t.Fatal("authorized runtime route rejected")
	}
}
