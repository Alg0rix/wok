package render

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

type recordingBarrier struct {
	fake *emulationFake
	err  error
}

func (b recordingBarrier) Sync(context.Context) error {
	b.fake.mu.Lock()
	b.fake.calls = append(b.fake.calls, emulationCall{method: "Recorder.Sync"})
	b.fake.mu.Unlock()
	return b.err
}
func TestRenderNavigationSynchronizesRecordersBeforeLoading(t *testing.T) {
	for _, route := range []struct {
		path, body string
		want       int
	}{{"/render/navigate?url=https://example.com", "", 200}, {"/render/input", `{"type":"navigate","url":"https://example.com"}`, 204}, {"/render/tab/new?url=https://example.com", "", 200}} {
		t.Run(route.path, func(t *testing.T) {
			c, fake := emulationClient(t)
			h := &handler{client: c, address: "127.0.0.1:1234", observer: recordingBarrier{fake: fake}}
			r := httptest.NewRequest("POST", route.path, strings.NewReader(route.body))
			r.Host = h.address
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != route.want {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			barrier, nav, create := -1, -1, -1
			for i, call := range fake.calls {
				switch call.method {
				case "Recorder.Sync":
					barrier = i
				case "Page.navigate":
					nav = i
					if call.params["url"] != "https://example.com" {
						t.Fatal(call.params)
					}
				case "Target.createTarget":
					create = i
					if call.params["url"] != "about:blank" {
						t.Fatal("new tab navigated before recorder synchronization")
					}
				}
			}
			if barrier < 0 || nav < 0 || barrier >= nav {
				t.Fatalf("bad navigation order barrier=%d navigate=%d", barrier, nav)
			}
			if strings.Contains(route.path, "tab/new") && (create < 0 || create >= barrier) {
				t.Fatalf("bad creation order create=%d barrier=%d", create, barrier)
			}
		})
	}
}
func TestRenderNavigationFailsWhenRecordersCannotAttach(t *testing.T) {
	c, fake := emulationClient(t)
	h := &handler{client: c, address: "127.0.0.1:1234", observer: recordingBarrier{fake: fake, err: errors.New("recorder unavailable")}}
	r := httptest.NewRequest("POST", "/render/navigate?url=https://example.com", nil)
	r.Host = h.address
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 502 {
		t.Fatalf("%d: %s", w.Code, w.Body.String())
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, call := range fake.calls {
		if call.method == "Page.navigate" {
			t.Fatal("navigated without a recorder")
		}
	}
}
