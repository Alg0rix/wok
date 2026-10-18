package render

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvironmentValidation(t *testing.T) {
	for _, body := range []string{`{"media":"dark"}`, `{"media":"light","offline":true}`, `{"media":"no-preference"}`, `{"offline":false}`} {
		if _, err := decodeEnvironment(strings.NewReader(body)); err != nil {
			t.Errorf("%s: %v", body, err)
		}
	}
	for _, body := range []string{`{}`, `null`, `{"media":null}`, `{"offline":"true"}`, `{"media":"contrast"}`, `{"other":true}`, `{"offline":true} {}`, `{"offline":true}` + strings.Repeat(" ", 4097)} {
		if _, err := decodeEnvironment(strings.NewReader(body)); err == nil {
			t.Errorf("accepted %s", body[:min(len(body), 100)])
		}
	}
}
func TestEnvironmentSurvivesViewportReset(t *testing.T) {
	c, fake := emulationClient(t)
	dir := t.TempDir()
	state, err := newEmulation(context.Background(), c, filepath.Join(dir, "render.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	dark := "dark"
	offline := true
	if err := state.updateEnvironment(context.Background(), environmentPatch{Media: &dark, Offline: &offline}); err != nil {
		t.Fatal(err)
	}
	if err := state.update(context.Background(), emulationConfig{Width: 390, Height: 844, Mobile: true, DeviceScaleFactor: 3}); err != nil {
		t.Fatal(err)
	}
	if err := state.update(context.Background(), emulationConfig{Reset: true}); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	active, retained := state.active(), len(state.pages)
	state.mu.Unlock()
	if !active || retained != 1 {
		t.Fatalf("environment lost on viewport reset: active=%v retained=%d", active, retained)
	}
	saved, err := os.ReadFile(filepath.Join(dir, "environment.json"))
	if err != nil || !strings.Contains(string(saved), `"media":"dark"`) {
		t.Fatalf("saved environment: %s %v", saved, err)
	}
	fake.mu.Lock()
	lastMedia, lastOffline := false, false
	for _, call := range fake.calls {
		if call.method == "Emulation.setEmulatedMedia" {
			features := call.params["features"].([]any)
			lastMedia = len(features) == 1 && features[0].(map[string]any)["value"] == "dark"
		}
		if call.method == "Network.emulateNetworkConditions" {
			lastOffline = call.params["offline"] == true
		}
	}
	fake.mu.Unlock()
	if !lastMedia || !lastOffline {
		t.Fatalf("media=%v offline=%v", lastMedia, lastOffline)
	}
	normal := "no-preference"
	offline = false
	if err := state.updateEnvironment(context.Background(), environmentPatch{Media: &normal, Offline: &offline}); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	retained = len(state.pages)
	state.mu.Unlock()
	if retained != 0 {
		t.Fatalf("inactive environment retained %d sessions", retained)
	}
}
func TestEnvironmentRestoredBeforeReadiness(t *testing.T) {
	c, fake := emulationClient(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "environment.json"), []byte(`{"media":"dark","offline":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	state, err := newEmulation(context.Background(), c, filepath.Join(dir, "render.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	found := false
	for _, call := range fake.calls {
		if call.method == "Network.emulateNetworkConditions" && call.params["offline"] == true {
			found = true
		}
	}
	if !found {
		t.Fatal("offline settings not restored before startup returned")
	}
}
