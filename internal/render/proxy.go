package render

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alg0rix/wok/internal/browser"
	"github.com/gorilla/websocket"
)

func (h *handler) proxyURL(r *http.Request, path string) string {
	scheme := "ws"
	if h.scheme(r) == "https" {
		scheme = "wss"
	}
	u := url.URL{Scheme: scheme, Host: r.Host, Path: path}
	if h.options.AuthToken != "" {
		q := u.Query()
		q.Set("token", h.options.AuthToken)
		u.RawQuery = q.Encode()
	}
	return u.String()
}
func (h *handler) discovery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", 405)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/devtools/") {
		h.proxyWebSocket(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "application/json")
	switch strings.TrimSuffix(r.URL.Path, "/") {
	case "/json", "/json/list":
		targets, err := browser.Targets(ctx, h.client)
		if err != nil {
			fail(w, err)
			return
		}
		pages := []map[string]any{}
		for _, t := range targets {
			if t.Type == "page" {
				pages = append(pages, map[string]any{"id": t.TargetID, "type": t.Type, "title": t.Title, "url": t.URL, "description": "", "webSocketDebuggerUrl": h.proxyURL(r, "/devtools/page/"+t.TargetID)})
			}
		}
		json.NewEncoder(w).Encode(pages)
	case "/json/version":
		var version struct {
			Product  string `json:"product"`
			Protocol string `json:"protocolVersion"`
			UA       string `json:"userAgent"`
			JS       string `json:"jsVersion"`
			Revision string `json:"revision"`
		}
		if err := h.client.Call(ctx, "", "Browser.getVersion", nil, &version); err != nil {
			fail(w, err)
			return
		}
		upstream, err := url.Parse(h.endpoint)
		if err != nil {
			fail(w, err)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"Browser": version.Product, "Protocol-Version": version.Protocol, "User-Agent": version.UA, "V8-Version": version.JS, "WebKit-Version": version.Revision, "webSocketDebuggerUrl": h.proxyURL(r, upstream.Path)})
	default:
		http.NotFound(w, r)
	}
}
func (h *handler) proxyWebSocket(w http.ResponseWriter, r *http.Request) {
	upstream, err := url.Parse(h.endpoint)
	if err != nil || upstream.Host == "" {
		http.Error(w, "browser endpoint unavailable", 502)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/devtools/page/") {
		id := strings.TrimPrefix(r.URL.Path, "/devtools/page/")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		targets, err := browser.Targets(ctx, h.client)
		cancel()
		if err != nil {
			fail(w, err)
			return
		}
		found := false
		for _, t := range targets {
			if t.TargetID == id && t.Type == "page" {
				found = true
				break
			}
		}
		if !found {
			http.Error(w, "unknown tab", 404)
			return
		}
		upstream.Path = r.URL.Path
	} else if r.URL.Path != upstream.Path || !strings.HasPrefix(r.URL.Path, "/devtools/browser/") {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	peer, _, err := websocket.DefaultDialer.DialContext(ctx, upstream.String(), nil)
	cancel()
	if err != nil {
		fail(w, fmt.Errorf("connect upstream CDP: %w", err))
		return
	}
	defer peer.Close()
	upgrade := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	client, err := upgrade.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer client.Close()
	peer.SetReadLimit(64 << 20)
	client.SetReadLimit(64 << 20)
	done := make(chan struct{}, 2)
	relay := func(dst, src *websocket.Conn) {
		defer func() { done <- struct{}{} }()
		for {
			kind, data, err := src.ReadMessage()
			if err != nil {
				return
			}
			if kind != websocket.TextMessage && kind != websocket.BinaryMessage {
				return
			}
			if err = dst.WriteMessage(kind, data); err != nil {
				return
			}
		}
	}
	go relay(peer, client)
	go relay(client, peer)
	select {
	case <-done:
	case <-r.Context().Done():
	case <-h.client.Done():
	}
	client.Close()
	peer.Close()
}
