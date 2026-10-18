package session

import (
	"context"
	"errors"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrivateStateAndNames(t *testing.T) {
	for _, name := range []string{"../bad", "", "a/b", "..", "a.b"} {
		if _, err := Open(t.TempDir(), name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	s, err := Open(t.TempDir(), "work")
	if err != nil {
		t.Fatal(err)
	}
	st := &State{Profile: "work", Endpoint: "ws://localhost:9222/devtools/browser/id", Token: "private", Refs: []byte(`{"next":42}`)}
	if err = s.Save(st); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil || got.Token != st.Token || got.Endpoint != st.Endpoint {
		t.Fatal(got, err)
	}
	info, err := os.Stat(filepath.Join(s.Dir, "state.json"))
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	info, err = os.Stat(s.Dir)
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0700 {
		t.Fatal(info, err)
	}
}
func TestLockCancellation(t *testing.T) {
	s, _ := Open(t.TempDir(), "work")
	unlock, err := s.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err = s.Lock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	unlock()
	release, err := s.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
}

// A browser which accepts Browser.close but stays live/unresponsive must not
// cause us to erase its identity or report a confirmed stop.
func TestStopTimeoutPreservesLiveState(t *testing.T) {
	var closing atomic.Bool
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var req struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			if conn.ReadJSON(&req) != nil {
				return
			}
			if req.Method == "Browser.close" {
				closing.Store(true)
			} else if closing.Load() {
				time.Sleep(300 * time.Millisecond)
			}
			if conn.WriteJSON(map[string]any{"id": req.ID, "result": map[string]any{}}) != nil {
				return
			}
		}
	}))
	defer server.Close()
	s, _ := Open(t.TempDir(), "work")
	state := &State{Profile: "work", Endpoint: "ws" + strings.TrimPrefix(server.URL, "http"), PID: 123, RenderURL: "http://127.0.0.1:9999"}
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := s.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wanted unconfirmed timeout, got %v", err)
	}
	got, err := s.Load()
	if err != nil || got.Endpoint != state.Endpoint || got.PID != state.PID || got.RenderURL != state.RenderURL {
		t.Fatalf("live state cleared: %+v %v", got, err)
	}
}
