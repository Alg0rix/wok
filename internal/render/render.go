// Package render serves a local, interactive view of a persistent browser.
package render

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alg0rix/wok/internal/browser"
	"github.com/alg0rix/wok/internal/cdp"
	"github.com/alg0rix/wok/internal/observe"
)

// Serve runs a loopback-only viewer until cancellation, browser exit, or shutdown.
// The ready file is published atomically after listening succeeds.
func Serve(ctx context.Context, endpoint, listen, readyFile, token string) error {
	return ServeWithOptions(ctx, endpoint, listen, readyFile, token, Options{})
}

// ServeWithOptions starts the profile controller and its optional remote APIs.
func ServeWithOptions(ctx context.Context, endpoint, listen, readyFile, token string, options Options) error {
	if err := validateOptions(options); err != nil {
		return err
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil || (!loopback(host) && options.AuthToken == "") {
		return fmt.Errorf("remote render listeners require an auth token: %q", listen)
	}
	c, err := cdp.Dial(ctx, endpoint)
	if err != nil {
		return err
	}
	defer c.Close()
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	h := &handler{client: c, token: token, cancel: cancel, address: ln.Addr().String(), options: options, endpoint: endpoint}
	h.emulationState, err = newEmulation(ctx, c, readyFile)
	if err != nil {
		return err
	}
	defer h.emulationState.close()
	runtimeDir := ""
	if readyFile != "" {
		runtimeDir = filepath.Dir(readyFile)
	} else {
		runtimeDir, err = os.MkdirTemp("", "wok-runtime-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(runtimeDir)
	}
	// The recorder owns a separate connection: Chromium reports every attach on
	// a connection, so a shared one would expose the controller's sessions to
	// the recorder's auto-attach reconciliation, which detaches duplicates.
	recorder, err := cdp.Dial(ctx, endpoint)
	if err != nil {
		return err
	}
	defer recorder.Close()
	manager, err := observe.New(ctx, recorder, observe.Config{Dir: runtimeDir, DownloadDir: options.DownloadDir, ProxyUsername: options.ProxyUsername, ProxyPassword: options.ProxyPassword})
	if err != nil {
		return err
	}
	defer manager.Close()
	h.runtime = manager.Handler()
	h.observer = manager
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	result := make(chan error, 1)
	go func() { result <- srv.Serve(ln) }()
	defer srv.Close()
	if readyFile != "" {
		data, _ := json.Marshal(map[string]any{"url": "http://" + localAddress(ln.Addr().String()), "pid": os.Getpid(), "endpoint": endpoint})
		if err := publish(readyFile, data); err != nil {
			return err
		}
		defer os.Remove(readyFile)
	}
	select {
	case <-ctx.Done():
		return nil
	case <-c.Done():
		return nil
	case <-recorder.Done():
		return nil
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func publish(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".render-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type handler struct {
	client         *cdp.Client
	address, token string
	cancel         context.CancelFunc
	emulationState *emulationState
	options        Options
	endpoint       string
	runtime        http.Handler
	observer       interface{ Sync(context.Context) error }
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	if strings.HasPrefix(r.URL.Path, "/runtime/") && h.runtime != nil {
		h.runtime.ServeHTTP(w, r)
		return
	}
	if r.URL.Path == "/mcp" {
		h.mcp(w, r)
		return
	}
	if r.URL.Path == "/json" || strings.HasPrefix(r.URL.Path, "/json/") || strings.HasPrefix(r.URL.Path, "/devtools/") {
		h.discovery(w, r)
		return
	}
	method := http.MethodGet
	if writePaths[r.URL.Path] {
		method = http.MethodPost
	}
	if r.URL.Path == "/render/emulation" || r.URL.Path == "/render/environment" {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", 405)
			return
		}
		method = r.Method
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		http.Error(w, "method not allowed", 405)
		return
	}
	if id := r.URL.Query().Get("tab"); id != "" && strings.HasPrefix(r.URL.Path, "/render") {
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
	}
	switch r.URL.Path {
	case "/render/emulation":
		h.emulation(w, r)
	case "/render/environment":
		h.environment(w, r)
	case "/health":
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{ "status": "ok" }`)
	case "/":
		target := "/render"
		if token := r.URL.Query().Get("token"); token != "" {
			target += "?token=" + url.QueryEscape(token)
		}
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
	case "/render":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, viewerHTML)
	case "/_shutdown":
		if h.token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+h.token)) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		h.cancel()
	case "/render/tabs":
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		targets, err := browser.Targets(ctx, h.client)
		if err != nil {
			fail(w, err)
			return
		}
		pages := make([]browser.Target, 0)
		for _, t := range targets {
			if t.Type == "page" {
				pages = append(pages, t)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pages)
	case "/render/viewport":
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		p, err := browser.Attach(ctx, h.client, r.URL.Query().Get("tab"))
		if err != nil {
			fail(w, err)
			return
		}
		defer p.Close()
		metrics, err := p.Eval(ctx, `({width:innerWidth,height:innerHeight,dpr:devicePixelRatio})`)
		if err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(metrics)
	case "/render/screenshot", "/render/screenshot.png", "/render/screenshot.ppm":
		h.screenshot(w, r)
	case "/render/stream", "/render/stream.mjpeg":
		h.stream(w, r)
	case "/render/input":
		h.input(w, r)
	case "/render/html":
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		p, err := browser.Attach(ctx, h.client, r.URL.Query().Get("tab"))
		if err != nil {
			fail(w, err)
			return
		}
		defer p.Close()
		html, err := p.Eval(ctx, `document.documentElement.outerHTML`)
		if err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'self'")
		fmt.Fprint(w, html)
	case "/render/navigate", "/render/click", "/render/scroll", "/render/type", "/render/key", "/render/move", "/render/mousedown", "/render/mouseup":
		h.compat(w, r)
	case "/render/tab/new", "/render/tab/close":
		h.tabAction(w, r)
	default:
		http.NotFound(w, r)
	}
}
func fail(w http.ResponseWriter, err error) { http.Error(w, err.Error(), http.StatusBadGateway) }
func (h *handler) screenshot(w http.ResponseWriter, r *http.Request) {
	var maxW, maxH int
	if r.URL.Path == "/render/screenshot.ppm" {
		var err error
		maxW, maxH, err = ppmSize(r.URL.Query())
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	p, err := browser.Attach(ctx, h.client, r.URL.Query().Get("tab"))
	if err != nil {
		fail(w, err)
		return
	}
	defer p.Close()
	metrics, err := p.Eval(ctx, `({width:innerWidth,height:innerHeight})`)
	if err != nil {
		fail(w, err)
		return
	}
	if m, ok := metrics.(map[string]any); ok {
		w.Header().Set("X-Wok-Viewport-Width", fmt.Sprint(m["width"]))
		w.Header().Set("X-Wok-Viewport-Height", fmt.Sprint(m["height"]))
	}
	data, err := h.capture(ctx, p, "png")
	if err != nil {
		fail(w, err)
		return
	}
	if r.URL.Path == "/render/screenshot.ppm" {
		var out bytes.Buffer
		if err := writePPM(&out, data, maxW, maxH); err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "image/x-portable-pixmap")
		w.Write(out.Bytes())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(data)
}
func (h *handler) stream(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	attachCtx, attachCancel := context.WithTimeout(ctx, 10*time.Second)
	p, err := browser.Attach(attachCtx, h.client, r.URL.Query().Get("tab"))
	attachCancel()
	if err != nil {
		fail(w, err)
		return
	}
	defer p.Close()
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	rc := http.NewResponseController(w)
	timer := time.NewTicker(200 * time.Millisecond)
	defer timer.Stop()
	for {
		callCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		data, err := h.capture(callCtx, p, "jpeg")
		stop()
		if err != nil {
			return
		}
		rc.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err = fmt.Fprintf(w, "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(data)); err != nil {
			return
		}
		if _, err = w.Write(data); err != nil {
			return
		}
		if _, err = io.WriteString(w, "\r\n"); err != nil {
			return
		}
		if err = rc.Flush(); err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

type inputEvent struct {
	Type   string   `json:"type"`
	X      *float64 `json:"x,omitempty"`
	Y      *float64 `json:"y,omitempty"`
	DeltaX *float64 `json:"deltaX,omitempty"`
	DeltaY *float64 `json:"deltaY,omitempty"`
	Button *string  `json:"button,omitempty"`
	Text   *string  `json:"text,omitempty"`
	Key    *string  `json:"key,omitempty"`
	URL    *string  `json:"url,omitempty"`
}

func decodeInput(r io.Reader) (inputEvent, error) {
	var e inputEvent
	dec := json.NewDecoder(io.LimitReader(r, 65537))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return e, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return e, errors.New("expected one JSON object")
	}
	allowed := map[string]string{"click": "xyb", "move": "xy", "scroll": "xydd", "text": "t", "key": "k", "navigate": "u"}[e.Type]
	if allowed == "" {
		return e, errors.New("unknown input type")
	}
	coords := e.Type == "click" || e.Type == "move" || e.Type == "scroll"
	finite := func(v *float64, max float64) bool {
		return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) && math.Abs(*v) <= max
	}
	if coords {
		if !finite(e.X, 100000) || !finite(e.Y, 100000) || *e.X < 0 || *e.Y < 0 {
			return e, errors.New("valid x and y are required")
		}
	} else if e.X != nil || e.Y != nil {
		return e, errors.New("unexpected coordinates")
	}
	if e.Type == "scroll" {
		if !finite(e.DeltaX, 100000) || !finite(e.DeltaY, 100000) {
			return e, errors.New("valid deltaX and deltaY required")
		}
	} else if e.DeltaX != nil || e.DeltaY != nil {
		return e, errors.New("unexpected scroll deltas")
	}
	if e.Button != nil && (e.Type != "click" || (*e.Button != "left" && *e.Button != "middle" && *e.Button != "right")) {
		return e, errors.New("invalid button")
	}
	if (e.Text != nil) != (e.Type == "text") || (e.Key != nil) != (e.Type == "key") || (e.URL != nil) != (e.Type == "navigate") {
		return e, errors.New("missing or unexpected input fields")
	}
	if e.Text != nil && len(*e.Text) > 32768 {
		return e, errors.New("text too long")
	}
	if e.Key != nil && (len(*e.Key) == 0 || len(*e.Key) > 64) {
		return e, errors.New("invalid key")
	}
	if e.URL != nil {
		u, err := url.Parse(*e.URL)
		if err != nil || len(*e.URL) > 8192 || (u.Scheme != "http" && u.Scheme != "https" && *e.URL != "about:blank") || ((u.Scheme == "http" || u.Scheme == "https") && u.Host == "") {
			return e, errors.New("URL must use http or https")
		}
	}
	return e, nil
}
func (h *handler) input(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	e, err := decodeInput(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	p, err := browser.Attach(ctx, h.client, r.URL.Query().Get("tab"))
	if err != nil {
		fail(w, err)
		return
	}
	defer p.Close()
	switch e.Type {
	case "text":
		err = p.Type(ctx, *e.Text)
	case "key":
		err = p.Press(ctx, *e.Key)
	case "navigate":
		err = h.prepareNavigation(ctx)
		if err == nil {
			err = p.Navigate(ctx, *e.URL)
		}
	case "click", "move", "scroll":
		params := map[string]any{"x": *e.X, "y": *e.Y, "type": "mouseMoved"}
		if e.Type == "scroll" {
			params["type"] = "mouseWheel"
			params["deltaX"] = *e.DeltaX
			params["deltaY"] = *e.DeltaY
		}
		if e.Type == "click" {
			button := "left"
			if e.Button != nil {
				button = *e.Button
			}
			params["button"] = button
			params["clickCount"] = 1
			params["type"] = "mousePressed"
		}
		err = p.Call(ctx, "Input.dispatchMouseEvent", params, nil)
		if err == nil && e.Type == "click" {
			params["type"] = "mouseReleased"
			err = p.Call(ctx, "Input.dispatchMouseEvent", params, nil)
		}
	}
	if err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
