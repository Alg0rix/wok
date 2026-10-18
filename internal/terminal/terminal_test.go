package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/alg0rix/wok/internal/browser"
	"github.com/alg0rix/wok/internal/cdp"
	"github.com/gorilla/websocket"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFiniteANSIFrameWithoutTTY(t *testing.T) {
	upgrade := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := upgrade.Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer c.Close()
		for {
			var req struct {
				ID int `json:"id"`
			}
			if c.ReadJSON(&req) != nil {
				return
			}
			if c.WriteJSON(map[string]any{"id": req.ID, "result": map[string]any{"result": map[string]any{"value": map[string]int{"w": 32, "h": 16}}}}) != nil {
				return
			}
		}
	}))
	defer server.Close()
	c, e := cdp.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	picture := image.NewRGBA(image.Rect(0, 0, 32, 16))
	picture.Set(0, 0, color.RGBA{R: 255, A: 255})
	var encoded bytes.Buffer
	png.Encode(&encoded, picture)
	var out bytes.Buffer
	e = Run(context.Background(), &browser.Page{Client: c, SessionID: "session"}, Config{FPS: 5, Frames: 1, Width: 32, Out: &out, Capture: func(context.Context) ([]byte, error) { return encoded.Bytes(), nil }})
	if e != nil || !bytes.Contains(out.Bytes(), []byte("▀")) || !bytes.Contains(out.Bytes(), []byte("38;2;255;0;0")) {
		t.Fatalf("invalid ANSI frame: %v", e)
	}
	if json.Valid(out.Bytes()) {
		t.Fatal("terminal output unexpectedly JSON")
	}
}
