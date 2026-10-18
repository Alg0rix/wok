package render

import (
	"context"
	"reflect"
	"testing"

	"github.com/alg0rix/wok/internal/session"
)

func TestMCPSelectsPersistedTabForScreenshot(t *testing.T) {
	t.Setenv("WOK_HOME", t.TempDir())
	store, err := session.Open("", "selected")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(&session.State{Profile: "selected", TargetID: "two"}); err != nil {
		t.Fatal(err)
	}
	c, fake := emulationClient(t)
	fake.mu.Lock()
	fake.targets = []string{"one", "two"}
	fake.mu.Unlock()
	h := &handler{client: c, options: Options{Profile: "selected"}}
	result, err := h.callTool(context.Background(), "browser_screenshot", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["isError"] != false {
		t.Fatal(result)
	}
	fake.mu.Lock()
	for _, call := range fake.calls {
		if call.method == "Target.attachToTarget" && call.params["targetId"] != "two" {
			t.Fatalf("captured wrong selected tab: %v", call.params)
		}
		if call.method == "Page.captureScreenshot" && call.session != "session-two" {
			t.Fatal(call.session)
		}
	}
	fake.mu.Unlock()
	if _, err = h.mcpScreenshot(context.Background(), "missing"); err == nil {
		t.Fatal("explicit missing tab silently recovered")
	}
	fake.mu.Lock()
	fake.targets = nil
	fake.mu.Unlock()
	if _, err = h.mcpScreenshot(context.Background(), ""); err == nil {
		t.Fatal("missing pages did not fail")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, call := range fake.calls {
		if call.method == "Target.createTarget" {
			t.Fatal("screenshot created a tab")
		}
	}
}
func TestMCPOfflineBooleanUsesCLIOnOff(t *testing.T) {
	for _, b := range []bool{true, false} {
		args, err := toolCommand("browser_set", map[string]any{"setting": "offline", "value": b})
		want := "off"
		if b {
			want = "on"
		}
		if err != nil || !reflect.DeepEqual(args, []string{"set", "offline", want}) {
			t.Fatalf("%t: %v %v", b, args, err)
		}
	}
	if _, err := toolCommand("browser_set", map[string]any{"setting": "offline", "value": "true"}); err == nil {
		t.Fatal("non-boolean offline value accepted")
	}
}
