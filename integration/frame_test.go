package integration

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestFrames drives same-origin and cross-origin iframes, nested two deep.
// The oopif variant enables site isolation so the cross-origin frames run in
// their own renderer processes, as they do in desktop Chrome.
func TestFrames(t *testing.T) {
	engine := os.Getenv("WOK_TEST_BROWSER")
	if engine == "" {
		t.Skip("set WOK_TEST_BROWSER for browser integration")
	}
	engine, _ = filepath.Abs(engine)
	bin := filepath.Join(t.TempDir(), "wok"+exeSuffix())
	if b, e := exec.Command("go", "build", "-o", bin, "../cmd/wok").CombinedOutput(); e != nil {
		t.Fatalf("%v %s", e, b)
	}
	page := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, body)
		}
	}
	inner := `<!doctype html><title>Inner</title><button id="b" onclick="this.textContent='inner clicked'">Inner button</button><input id="i"><input id="f" type="file"><script>window.who='inner'</script>`
	// The cross-origin server is reached as localhost, the top page as 127.0.0.1.
	crossMux := http.NewServeMux()
	cross := httptest.NewServer(crossMux)
	defer cross.Close()
	crossURL := strings.Replace(cross.URL, "127.0.0.1", "localhost", 1)
	topMux := http.NewServeMux()
	top := httptest.NewServer(topMux)
	defer top.Close()
	topMux.HandleFunc("/inner", page(inner))
	topMux.HandleFunc("/", page(`<!doctype html><title>Top</title><button id="topbtn">Top button</button><div style="height:60px"></div><iframe id="same" src="/inner" style="width:400px;height:150px;border:5px solid red;padding:7px"></iframe><iframe id="cross" src="`+crossURL+`/cross" style="width:420px;height:260px"></iframe><script>window.who='top'</script>`))
	crossMux.HandleFunc("/cross", page(`<!doctype html><title>Cross</title><button id="b" onclick="this.textContent='cross clicked'">Cross button</button><input id="i"><br><iframe id="nested" src="`+top.URL+`/inner" style="width:380px;height:150px"></iframe><script>window.who='cross'</script>`))

	modes := map[string]string{"shared": engine}
	if runtime.GOOS != "windows" {
		wrapper := filepath.Join(t.TempDir(), "isolated-browser")
		os.WriteFile(wrapper, []byte("#!/bin/sh\nexec '"+engine+"' --site-per-process \"$@\"\n"), 0700)
		modes["oopif"] = wrapper
	}
	for mode, browserPath := range modes {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			invoke := func(args ...string) (string, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, bin, append([]string{"--profile", "frames"}, args...)...)
				cmd.Env = append(os.Environ(), "WOK_HOME="+home)
				var out, errOut bytes.Buffer
				cmd.Stdout = &out
				cmd.Stderr = &errOut
				e := cmd.Run()
				return strings.TrimSpace(out.String() + errOut.String()), e
			}
			run := func(args ...string) string {
				t.Helper()
				out, e := invoke(args...)
				if e != nil {
					t.Fatalf("%v: %v %s", args, e, out)
				}
				return out
			}
			expect := func(want string, args ...string) {
				t.Helper()
				if got := run(args...); got != want {
					t.Fatalf("%v = %s, want %s", args, got, want)
				}
			}
			t.Cleanup(func() { _, _ = invoke("stop") })
			run("start", "--browser", browserPath, "--no-sandbox")
			run("open", top.URL)
			run("wait", "#cross >> #nested >> #b", "--state", "visible")

			// Snapshots include every frame; their refs act inside the frame.
			snap := run("snapshot", "-i")
			// Text inputs and buttons in document order: top, same, cross, nested.
			var buttons, boxes []string
			for _, line := range strings.Split(snap, "\n") {
				f := strings.Split(line, "\t")
				if len(f) == 3 && f[1] == "button" && f[2] != `"Choose File"` {
					buttons = append(buttons, f[0])
				}
				if len(f) == 3 && f[1] == "textbox" {
					boxes = append(boxes, f[0])
				}
			}
			if len(buttons) != 4 || len(boxes) != 3 || !strings.Contains(snap, `"Cross button"`) || strings.Count(snap, `"Inner button"`) != 2 {
				t.Fatalf("snapshot missing frame content:\n%s", snap)
			}
			run("click", buttons[1])
			run("click", buttons[2])
			run("click", buttons[3])
			run("fill", boxes[1], "cross-ref")
			expect(`"inner clicked"`, "get", "text", "#same >> #b")
			expect(`"cross clicked"`, "get", "text", "#cross >> #b")
			expect(`"inner clicked"`, "get", "text", "#cross >> #nested >> #b")
			expect(`"cross-ref"`, "get", "value", "#cross >> #i")

			// Inline frame chains and --frame scope CSS selectors and eval.
			run("open", top.URL)
			run("click", "#cross >> #nested >> #b")
			run("click", "#cross >> #b")
			run("fill", "#same >> #i", "same-css")
			expect(`"inner clicked"`, "get", "text", "#cross >> #nested >> #b")
			expect(`"cross clicked"`, "--frame", "#cross", "get", "text", "#b")
			expect(`"same-css"`, "--frame", "#same", "eval", "document.querySelector('#i').value")
			expect(`"top"`, "eval", "window.who")
			expect(`"inner"`, "--frame", "#same", "eval", "window.who")
			expect(`"cross"`, "--frame", "#cross", "eval", "window.who")
			expect(`"inner"`, "--frame", "#cross >> #nested", "eval", "window.who")
			if b := run("--frame", "#cross", "snapshot", "-i"); strings.Contains(b, "Top button") || !strings.Contains(b, "cross clicked") {
				t.Fatal(b)
			}
			if b := run("--frame", "#cross", "find", "role", "button"); !strings.Contains(b, "cross clicked") || strings.Contains(b, "Top button") {
				t.Fatal(b)
			}
			file := filepath.Join(t.TempDir(), "frame.txt")
			os.WriteFile(file, []byte("x"), 0600)
			run("upload", "#cross >> #nested >> #f", file)
			expect(`"frame.txt"`, "--frame", "#cross >> #nested", "eval", "document.querySelector('#f').files[0].name")
			shot := filepath.Join(t.TempDir(), "button.png")
			run("screenshot", shot, "#cross >> #b")
			if info, e := os.Stat(shot); e != nil || info.Size() == 0 {
				t.Fatal("frame element screenshot missing", e)
			}
			if out, e := invoke("--frame", "#topbtn", "eval", "1"); e == nil || !strings.Contains(out, "not an iframe") {
				t.Fatal(out)
			}

			// Flows use the same chained selectors.
			flowFile := filepath.Join(t.TempDir(), "frames.yaml")
			os.WriteFile(flowFile, []byte(`version: 1
steps:
  - open: "`+top.URL+`"
  - click: "#cross >> #b"
  - fill: {selector: "#cross >> #nested >> #i", value: "from-flow"}
  - expect: {selector: "#cross >> #b", text: "cross clicked"}
  - expect: {frame: "#cross >> #nested", js: "window.who === 'inner'"}
  - if: {selector: "#same >> #b", visible: true}
    then:
      - click: "#same >> #b"
  - expect: {selector: "#same >> #b", text: "inner clicked"}
`), 0600)
			if b := run("run", flowFile); !strings.Contains(b, `"ok":true`) {
				t.Fatal(b)
			}
			expect(`"cross clicked"`, "get", "text", "#cross >> #b")
			expect(`"from-flow"`, "get", "value", "#cross >> #nested >> #i")
		})
	}
}
