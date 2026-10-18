package render

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alg0rix/wok/internal/browser"
)

var closeTabs sync.Mutex
var writePaths = map[string]bool{"/render/emulation": true, "/render/input": true, "/_shutdown": true, "/render/navigate": true, "/render/click": true, "/render/scroll": true, "/render/type": true, "/render/key": true, "/render/move": true, "/render/mousedown": true, "/render/mouseup": true, "/render/tab/new": true, "/render/tab/close": true}

type action struct {
	kind, text, key  string
	params           map[string]any
	centerX, centerY bool
}

func queryNumber(q url.Values, name string, required bool) (float64, error) {
	raw := q.Get(name)
	if raw == "" && !required {
		return 0, nil
	}
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > 100000 {
		return 0, fmt.Errorf("invalid %s", name)
	}
	return n, nil
}
func modifierBits(raw string) (int, error) {
	bits := 0
	if raw == "" {
		return bits, nil
	}
	for _, m := range strings.Split(strings.ToLower(raw), ",") {
		switch m {
		case "alt":
			bits |= 1
		case "ctrl":
			bits |= 2
		case "meta":
			bits |= 4
		case "shift":
			bits |= 8
		default:
			return 0, errors.New("invalid modifier")
		}
	}
	return bits, nil
}
func buttonBits(raw string) (int, error) {
	bits := 0
	if raw == "" {
		return bits, nil
	}
	for _, b := range strings.Split(raw, ",") {
		switch b {
		case "left":
			bits |= 1
		case "right":
			bits |= 2
		case "middle":
			bits |= 4
		default:
			return 0, errors.New("invalid buttons")
		}
	}
	return bits, nil
}
func parseAction(path string, q url.Values, body io.Reader) (action, error) {
	a := action{kind: strings.TrimPrefix(path, "/render/")}
	allowed := "tab,token"
	switch a.kind {
	case "navigate":
		allowed += ",url"
	case "type":
		allowed += ",text"
	case "key":
		allowed += ",key,mods"
	case "scroll":
		allowed += ",dy,x,y,mods"
	case "click", "mousedown", "mouseup":
		allowed += ",x,y,button,mods"
	case "move":
		allowed += ",x,y,buttons,mods"
	default:
		return a, errors.New("unsupported action")
	}
	for k, v := range q {
		if len(v) != 1 || !strings.Contains(","+allowed+",", ","+k+",") {
			return a, fmt.Errorf("unexpected query parameter %s", k)
		}
	}
	switch a.kind {
	case "navigate":
		a.text = q.Get("url")
		e := inputEvent{Type: "navigate", URL: &a.text}
		b, _ := json.Marshal(e)
		_, err := decodeInput(strings.NewReader(string(b)))
		return a, err
	case "type":
		if q.Has("text") {
			a.text = q.Get("text")
		} else {
			b, err := io.ReadAll(io.LimitReader(body, 32769))
			if err != nil {
				return a, err
			}
			a.text = string(b)
		}
		if len(a.text) > 32768 {
			return a, errors.New("text too long")
		}
		return a, nil
	case "key":
		a.key = q.Get("key")
		if len(a.key) == 0 || len(a.key) > 64 {
			return a, errors.New("invalid key")
		}
		if _, err := modifierBits(q.Get("mods")); err != nil {
			return a, err
		}
		names := map[string]string{"enter": "Enter", "tab": "Tab", "backspace": "Backspace", "delete": "Delete", "escape": "Escape", "space": "Space", "up": "ArrowUp", "down": "ArrowDown", "left": "ArrowLeft", "right": "ArrowRight", "home": "Home", "end": "End", "pageup": "PageUp", "pagedown": "PageDown", "insert": "Insert"}
		if name, ok := names[strings.ToLower(a.key)]; ok {
			a.key = name
		} else if len([]rune(a.key)) != 1 {
			n, err := strconv.Atoi(strings.TrimPrefix(strings.ToLower(a.key), "f"))
			if !strings.HasPrefix(strings.ToLower(a.key), "f") || err != nil || n < 1 || n > 12 {
				return a, errors.New("unsupported key")
			}
			a.key = "F" + strconv.Itoa(n)
		}
		if q.Get("mods") != "" {
			a.key = strings.ReplaceAll(q.Get("mods"), ",", "+") + "+" + a.key
		}
		return a, nil
	}
	mods, err := modifierBits(q.Get("mods"))
	if err != nil {
		return a, err
	}
	a.centerX = a.kind == "scroll" && !q.Has("x")
	a.centerY = a.kind == "scroll" && !q.Has("y")
	x, err := queryNumber(q, "x", !a.centerX)
	if err != nil || x < 0 {
		return a, errors.New("invalid x")
	}
	y, err := queryNumber(q, "y", !a.centerY)
	if err != nil || y < 0 {
		return a, errors.New("invalid y")
	}
	a.params = map[string]any{"x": x, "y": y, "modifiers": mods, "type": "mouseMoved"}
	switch a.kind {
	case "scroll":
		dy, err := queryNumber(q, "dy", true)
		if err != nil {
			return a, err
		}
		a.params["type"] = "mouseWheel"
		a.params["deltaX"] = 0
		a.params["deltaY"] = -dy / 2
	case "move":
		bits, err := buttonBits(q.Get("buttons"))
		if err != nil {
			return a, err
		}
		a.params["buttons"] = bits
	default:
		b := q.Get("button")
		if b == "" {
			b = "left"
		}
		bits, err := buttonBits(b)
		if err != nil || strings.Contains(b, ",") {
			return a, errors.New("invalid button")
		}
		a.params["button"] = b
		a.params["clickCount"] = 1
		a.params["buttons"] = bits
		a.params["type"] = "mousePressed"
		if a.kind == "mouseup" {
			a.params["type"] = "mouseReleased"
			a.params["buttons"] = 0
		}
	}
	return a, nil
}
func (h *handler) compat(w http.ResponseWriter, r *http.Request) {
	a, err := parseAction(r.URL.Path, r.URL.Query(), http.MaxBytesReader(w, r.Body, 32768))
	if err != nil {
		http.Error(w, err.Error(), 400)
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
	switch a.kind {
	case "navigate":
		err = h.prepareNavigation(ctx)
		if err == nil {
			err = p.Navigate(ctx, a.text)
		}
	case "type":
		err = p.Type(ctx, a.text)
	case "key":
		err = p.Press(ctx, a.key)
	default:
		if a.centerX || a.centerY {
			var metrics any
			metrics, err = p.Eval(ctx, `({x:innerWidth/2,y:innerHeight/2})`)
			if err == nil {
				m, ok := metrics.(map[string]any)
				if !ok {
					err = errors.New("invalid viewport")
				} else {
					if a.centerX {
						a.params["x"] = m["x"]
					}
					if a.centerY {
						a.params["y"] = m["y"]
					}
				}
			}
		}
		if err == nil {
			err = p.Call(ctx, "Input.dispatchMouseEvent", a.params, nil)
		}
		if err == nil && a.kind == "click" {
			a.params["type"] = "mouseReleased"
			a.params["buttons"] = 0
			err = p.Call(ctx, "Input.dispatchMouseEvent", a.params, nil)
		}
	}
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, "ok\n")
}
func (h *handler) tabAction(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/render/tab/new" {
		raw := r.URL.Query().Get("url")
		if raw == "" {
			raw = "about:blank"
		}
		b, _ := json.Marshal(inputEvent{Type: "navigate", URL: &raw})
		if _, err := decodeInput(strings.NewReader(string(b))); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var result struct {
			ID string `json:"targetId"`
		}
		if err := h.client.Call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &result); err != nil {
			fail(w, err)
			return
		}
		if err := h.prepareNavigation(ctx); err != nil {
			fail(w, err)
			return
		}
		if raw != "about:blank" {
			p, err := browser.Attach(ctx, h.client, result.ID)
			if err != nil {
				fail(w, err)
				return
			}
			defer p.Close()
			if err = p.Navigate(ctx, raw); err != nil {
				fail(w, err)
				return
			}
		}
		json.NewEncoder(w).Encode(map[string]string{"id": result.ID})
		return
	}
	closeTabs.Lock()
	defer closeTabs.Unlock()
	targets, err := browser.Targets(ctx, h.client)
	if err != nil {
		fail(w, err)
		return
	}
	pages := []browser.Target{}
	for _, t := range targets {
		if t.Type == "page" {
			pages = append(pages, t)
		}
	}
	if len(pages) <= 1 {
		http.Error(w, "cannot close last tab", 409)
		return
	}
	id := r.URL.Query().Get("tab")
	if id == "" {
		id = pages[0].TargetID
	}
	if err := h.client.Call(ctx, "", "Target.closeTarget", map[string]any{"targetId": id}, nil); err != nil {
		fail(w, err)
		return
	}
	io.WriteString(w, `{"ok":true}`)
}
