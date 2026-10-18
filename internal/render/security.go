package render

import (
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Options configures optional remote access and profile services. AuthToken is
// required for a non-loopback listener; shutdown uses its separate private token.
type Options struct {
	Profile       string
	AuthToken     string
	EmbedOrigins  []string
	DownloadDir   string
	ProxyUsername string
	ProxyPassword string
}

func validateOptions(options Options) error {
	for _, origin := range options.EmbedOrigins {
		if origin == "*" {
			continue
		}
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return errors.New("embed origins must be http(s) origins without paths")
		}
	}
	return nil
}
func constantToken(got, want string) bool {
	return want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
func (h *handler) scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if h.options.AuthToken != "" && r.Header.Get("X-Forwarded-Proto") == "https" {
		return "https"
	}
	return "http"
}
func (h *handler) explicitOrigin(origin string) bool {
	for _, allowed := range h.options.EmbedOrigins {
		if allowed == origin || allowed == "*" {
			return true
		}
	}
	return false
}
func (h *handler) authorize(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	ancestors := "'self'"
	for _, origin := range h.options.EmbedOrigins {
		ancestors += " " + origin
	}
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src 'self'; frame-ancestors "+ancestors)
	if h.options.AuthToken == "" {
		host, port, err := net.SplitHostPort(r.Host)
		_, expectedPort, _ := net.SplitHostPort(h.address)
		if err != nil || !loopback(host) || port != expectedPort {
			http.Error(w, "invalid host", 403)
			return false
		}
	} else {
		u, err := url.Parse("//" + r.Host)
		if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			http.Error(w, "invalid host", 403)
			return false
		}
	}
	origin := r.Header.Get("Origin")
	if origin != "" {
		if origin != h.scheme(r)+"://"+r.Host && !h.explicitOrigin(origin) {
			http.Error(w, "cross-origin access denied", 403)
			return false
		}
		if h.options.AuthToken != "" && h.explicitOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, MCP-Protocol-Version")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.WriteHeader(http.StatusNoContent)
				return false
			}
		}
	}
	// The private stop token remains usable during recovery of an authenticated
	// controller, without exposing the public remote-access token to recovery code.
	if r.URL.Path == "/_shutdown" {
		return true
	}
	bearer := ""
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		bearer = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if h.options.AuthToken != "" && !constantToken(bearer, h.options.AuthToken) && !constantToken(r.URL.Query().Get("token"), h.options.AuthToken) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="Wok"`)
		http.Error(w, "unauthorized", 401)
		return false
	}
	return true
}
func localAddress(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsUnspecified() {
		if strings.Contains(host, ":") {
			host = "::1"
		} else {
			host = "127.0.0.1"
		}
		return net.JoinHostPort(host, port)
	}
	return address
}
