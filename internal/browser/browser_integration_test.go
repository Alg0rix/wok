package browser

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alg0rix/wok/internal/cdp"
)

// Opt in with WOK_TEST_BROWSER=/path/to/chrome-headless-shell.
func TestRealBrowserNumberFill(t *testing.T) {
	executable := os.Getenv("WOK_TEST_BROWSER")
	if executable == "" {
		t.Skip("set WOK_TEST_BROWSER for real browser integration")
	}
	profile := t.TempDir()
	cmd := exec.Command(executable, "--headless", "--no-sandbox", "--disable-gpu", "--remote-debugging-port=0", "--user-data-dir="+profile, "about:blank")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var endpoint string
	for endpoint == "" {
		data, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) >= 2 {
				endpoint = "ws://127.0.0.1:" + lines[0] + lines[1]
			}
		}
		if endpoint != "" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	client, err := cdp.Dial(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	page, err := Attach(ctx, client, "")
	if err != nil {
		t.Fatal(err)
	}
	defer page.Close()
	if err = page.Navigate(ctx, "data:text/html,"+url.PathEscape(`<input id="number" type="number" value="123"><fieldset disabled><input id="disabled" value="old"></fieldset>`)); err != nil {
		t.Fatal(err)
	}
	if err = page.Fill(ctx, "#number", "45", nil); err != nil {
		t.Fatal(err)
	}
	value, err := page.Get(ctx, "value", "#number", "", nil)
	if err != nil || value != "45" {
		t.Fatalf("number value = %v, %v; want 45", value, err)
	}
	if err = page.Fill(ctx, "#number", "", nil); err != nil {
		t.Fatal(err)
	}
	value, err = page.Get(ctx, "value", "#number", "", nil)
	if err != nil || value != "" {
		t.Fatalf("cleared number value = %v, %v", value, err)
	}
	if err = page.Fill(ctx, "#disabled", "new", nil); err == nil {
		t.Fatal("Fill accepted disabled fieldset child")
	}
}
