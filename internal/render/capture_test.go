package render

import (
	"context"
	"testing"

	"github.com/alg0rix/wok/internal/browser"
)

func TestCaptureUsesEmulationOwnerAndSwitchesAfterReset(t *testing.T) {
	c, fake := emulationClient(t)
	owner := &browser.Page{Client: c, SessionID: "emulation-owner", TargetID: "one"}
	temporary := &browser.Page{Client: c, SessionID: "temporary", TargetID: "one"}
	state := &emulationState{client: c, config: emulationConfig{Width: 390, Height: 844, DeviceScaleFactor: 3, Mobile: true}, pages: map[string]*browser.Page{"one": owner}}
	h := &handler{client: c, emulationState: state}
	if _, err := h.capture(context.Background(), temporary, "png"); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.config = emulationConfig{Reset: true}
	state.mu.Unlock()
	if _, err := h.capture(context.Background(), temporary, "jpeg"); err != nil {
		t.Fatal(err)
	}
	// An already-open stream switches back to the emulation owner on its next frame.
	state.mu.Lock()
	state.config.Reset = false
	state.mu.Unlock()
	if _, err := h.capture(context.Background(), temporary, "jpeg"); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	calls := []emulationCall{}
	for _, call := range fake.calls {
		if call.method == "Page.captureScreenshot" {
			calls = append(calls, call)
		}
	}
	if len(calls) != 3 {
		t.Fatalf("got %d screenshot calls", len(calls))
	}
	for i, want := range []string{"emulation-owner", "temporary", "emulation-owner"} {
		if calls[i].session != want {
			t.Errorf("capture %d: session %s, want %s", i, calls[i].session, want)
		}
	}
	if calls[0].params["format"] != "png" || calls[2].params["format"] != "jpeg" || calls[2].params["quality"] != float64(65) {
		t.Fatalf("capture formats: %+v", calls)
	}
}
