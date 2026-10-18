package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPlaywrightCLICommandSet(t *testing.T) {
	engine := os.Getenv("WOK_TEST_BROWSER")
	if engine == "" {
		t.Skip("set WOK_TEST_BROWSER for browser integration")
	}
	engine, err := filepath.Abs(engine)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "wok"+exeSuffix())
	if b, e := exec.Command("go", "build", "-o", bin, "../cmd/wok").CombinedOutput(); e != nil {
		t.Fatalf("%v %s", e, b)
	}
	home := t.TempDir()
	workDir := t.TempDir()
	invoke := func(sessionName string, args ...string) (string, string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		fullArgs := args
		if sessionName != "" {
			fullArgs = append([]string{"-s=" + sessionName}, args...)
		}
		cmd := exec.CommandContext(ctx, bin, fullArgs...)
		cmd.Dir = workDir
		cmd.Env = append(os.Environ(), "WOK_HOME="+home, "WOK_BROWSER="+engine)
		var out, errOut bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errOut
		e := cmd.Run()
		return strings.TrimSpace(out.String()), strings.TrimSpace(errOut.String()), e
	}
	run := func(sessionName string, args ...string) string {
		t.Helper()
		out, errOut, e := invoke(sessionName, args...)
		if e != nil {
			t.Fatalf("-s=%s %v: %v\nstdout: %s\nstderr: %s", sessionName, args, e, out, errOut)
		}
		return out
	}
	t.Cleanup(func() {
		_, _, _ = invoke("", "kill-all")
	})

	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page2":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<!doctype html><title>Page Two</title><h1>Second Page</h1>`)
			return
		case "/echo":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Echo", "true")
			fmt.Fprint(w, `{"echo":true}`)
			return
		case "/debug.js":
			w.Header().Set("Content-Type", "application/javascript")
			fmt.Fprint(w, "function runStep() {\n  window.stepCount = (window.stepCount || 0) + 1;\n  window.stepCount = window.stepCount + 10;\n  return window.stepCount;\n}\n")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html>
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>DevTools Fixture</title>
<style>
  body { height: 2000px; margin: 16px; font-family: sans-serif; }
  #hover-box, #dbl-btn, #ctx-btn, #drag-src, #drop-zone {
    display: inline-block; width: 90px; height: 36px; margin: 4px; border: 1px solid #333;
  }
</style>
<div id="hover-box" onmouseenter="window.hovered=true">Hover</div>
<button id="dbl-btn" ondblclick="window.dbl=true">DblClick</button>
<button id="ctx-btn" oncontextmenu="event.preventDefault();window.rightClicked=true">RightClick</button>
<div id="drag-src" draggable="true" ondragstart="event.dataTransfer.setData('text/plain','dragged-item')">DragMe</div>
<div id="drop-zone" ondragover="event.preventDefault()" ondrop="event.preventDefault();window.droppedText=event.dataTransfer.getData('text/plain');window.droppedFiles=[...event.dataTransfer.files].map(f=>f.name)">DropHere</div>
<select id="color"><option value="red">Red</option><option value="green">Green</option></select>
<input id="agree" type="checkbox">
<input id="note">
<section id="scoped"><button id="scoped-btn">Scoped Action</button></section>
<script src="/debug.js"></script>
<script>
  addEventListener('keydown', e => { window.lastKeyDown = e.key; });
  addEventListener('keyup', e => { window.lastKeyUp = e.key; });
  addEventListener('mousedown', e => { window.mouseAt = e.clientX + ':' + e.clientY + ':' + e.button; });
  addEventListener('mouseup', e => { window.mouseUpButton = e.button; });
  addEventListener('wheel', e => { window.wheelDelta = e.deltaY; });
  console.info('info-msg');
  console.warn('warn-msg');
  console.error('error-msg');
  fetch('/echo', {method: 'POST', body: 'hello-post'});
</script>`)
	}))
	defer fixture.Close()

	// Help and raw output
	if out := run("", "--help", "dblclick"); !strings.Contains(out, "wok dblclick") {
		t.Fatal(out)
	}
	// open with no URL starts the session
	run("dev", "open", "--no-sandbox")
	if out := run("", "list"); !strings.Contains(out, `"profile":"dev"`) || !strings.Contains(out, `"running":true`) {
		t.Fatal(out)
	}
	if out := run("dev", "show"); !strings.Contains(out, "/render") {
		t.Fatal(out)
	}
	run("dev", "goto", fixture.URL)
	if out := run("dev", "get", "title", "--raw"); out != "DevTools Fixture" {
		t.Fatalf("unexpected --raw title: %q", out)
	}
	run("dev", "resize", "900", "700")
	if out := run("dev", "eval", "innerWidth"); out != "900" {
		t.Fatal(out)
	}
	if out := run("dev", "eval", "async () => { await null; return {w: innerWidth} }"); out != `{"w":900}` {
		t.Fatalf("eval should call a function expression: %s", out)
	}

	// Element interactions
	run("dev", "hover", "#hover-box")
	if out := run("dev", "eval", "window.hovered === true"); out != "true" {
		t.Fatal(out)
	}
	run("dev", "dblclick", "#dbl-btn")
	if out := run("dev", "eval", "window.dbl === true"); out != "true" {
		t.Fatal(out)
	}
	run("dev", "click", "#ctx-btn", "right")
	if out := run("dev", "eval", "window.rightClicked === true"); out != "true" {
		t.Fatal(out)
	}
	run("dev", "drag", "#drag-src", "#drop-zone")
	if out := run("dev", "eval", "window.droppedText"); out != `"dragged-item"` {
		t.Fatal(out)
	}
	run("dev", "drop", "#drop-zone", "--text", "external-text")
	if out := run("dev", "eval", "window.droppedText"); out != `"external-text"` {
		t.Fatal(out)
	}
	dropFile := filepath.Join(workDir, "sample.txt")
	if err := os.WriteFile(dropFile, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	run("dev", "drop", "#drop-zone", dropFile)
	if out := run("dev", "eval", "window.droppedFiles[0]"); out != `"sample.txt"` {
		t.Fatal(out)
	}
	if out := run("dev", "select", "#color", "Green"); out != `["green"]` {
		t.Fatal(out)
	}
	run("dev", "check", "#agree")
	if out := run("dev", "eval", "document.querySelector('#agree').checked"); out != "true" {
		t.Fatal(out)
	}
	run("dev", "uncheck", "#agree")
	if out := run("dev", "eval", "document.querySelector('#agree').checked"); out != "false" {
		t.Fatal(out)
	}

	// Scoped snapshot, single-arg find, element eval, locator & highlight
	if out := run("dev", "snapshot", "#scoped"); !strings.Contains(out, "Scoped Action") || strings.Contains(out, "DblClick") {
		t.Fatal(out)
	}
	if out := run("dev", "find", "Scoped Action"); !strings.Contains(out, "@e") {
		t.Fatal(out)
	}
	if out := run("dev", "eval", "el => el.id", "#scoped-btn"); out != `"scoped-btn"` {
		t.Fatal(out)
	}
	if out := run("dev", "generate-locator", "#scoped-btn"); !strings.Contains(out, "#scoped-btn") {
		t.Fatal(out)
	}
	run("dev", "highlight", "#scoped-btn")
	if out := run("dev", "eval", "document.querySelectorAll('[data-wok-highlight]').length"); out != "1" {
		t.Fatal(out)
	}
	run("dev", "highlight", "--hide")
	if out := run("dev", "eval", "document.querySelectorAll('[data-wok-highlight]').length"); out != "0" {
		t.Fatal(out)
	}

	// Keyboard and Mouse
	run("dev", "keydown", "Shift")
	run("dev", "keyup", "Shift")
	if out := run("dev", "eval", "window.lastKeyDown + ':' + window.lastKeyUp"); out != `"Shift:Shift"` {
		t.Fatal(out)
	}
	run("dev", "mousemove", "55", "65")
	run("dev", "mousedown", "left")
	run("dev", "mouseup", "left")
	if out := run("dev", "eval", "window.mouseAt"); out != `"55:65:0"` {
		t.Fatal(out)
	}
	run("dev", "mousewheel", "0", "120")
	run("dev", "wait", "--fn", "window.wheelDelta === 120")

	// Screenshot target and PDF
	var shot map[string]any
	if err := json.Unmarshal([]byte(run("dev", "screenshot", "#dbl-btn")), &shot); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(workDir, shot["path"].(string))); err != nil || !bytes.HasPrefix(data, []byte("\x89PNG")) {
		t.Fatal(err)
	}
	var pdfOut map[string]any
	if err := json.Unmarshal([]byte(run("dev", "pdf")), &pdfOut); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(workDir, pdfOut["path"].(string))); err != nil || !bytes.HasPrefix(data, []byte("%PDF-")) {
		t.Fatal(err)
	}

	// Navigation history
	run("dev", "goto", fixture.URL+"/page2")
	run("dev", "go-back")
	if out := run("dev", "get", "title", "--raw"); out != "DevTools Fixture" {
		t.Fatal(out)
	}
	run("dev", "go-forward")
	if out := run("dev", "get", "title", "--raw"); out != "Page Two" {
		t.Fatal(out)
	}
	run("dev", "go-back")
	run("dev", "reload")

	// Tabs by index
	run("dev", "tab-new", fixture.URL+"/page2")
	var tabs []map[string]any
	if err := json.Unmarshal([]byte(run("dev", "tab-list")), &tabs); err != nil || len(tabs) < 2 {
		t.Fatal(err, tabs)
	}
	origIdx, newIdx := "1", "0"
	if strings.Contains(fmt.Sprint(tabs[0]["title"]), "DevTools Fixture") {
		origIdx, newIdx = "0", "1"
	}
	run("dev", "tab-select", origIdx)
	if out := run("dev", "get", "title", "--raw"); out != "DevTools Fixture" {
		t.Fatal(out)
	}
	run("dev", "tab-close", newIdx)

	// Cookies, localStorage, sessionStorage, state-save and state-load
	run("dev", "cookie-set", "token", "xyz")
	if out := run("dev", "cookie-get", "token"); !strings.Contains(out, `"value":"xyz"`) {
		t.Fatal(out)
	}
	if out := run("dev", "cookie-list"); !strings.Contains(out, `"token"`) {
		t.Fatal(out)
	}
	run("dev", "localstorage-set", "k1", "v1")
	if out := run("dev", "localstorage-get", "k1", "--raw"); out != "v1" {
		t.Fatal(out)
	}
	if out := run("dev", "localstorage-list"); !strings.Contains(out, `"k1":"v1"`) {
		t.Fatal(out)
	}
	run("dev", "sessionstorage-set", "sk", "sv")
	if out := run("dev", "sessionstorage-get", "sk", "--raw"); out != "sv" {
		t.Fatal(out)
	}
	if out := run("dev", "sessionstorage-list"); !strings.Contains(out, `"sk":"sv"`) {
		t.Fatal(out)
	}
	run("dev", "sessionstorage-delete", "sk")
	run("dev", "sessionstorage-clear")

	stateFile := filepath.Join(workDir, "state.json")
	run("dev", "state-save", stateFile)
	run("dev", "cookie-delete", "token")
	run("dev", "cookie-clear")
	run("dev", "localstorage-delete", "k1")
	run("dev", "localstorage-clear")
	run("dev", "state-load", stateFile)
	run("dev", "reload")
	if out := run("dev", "cookie-get", "token"); !strings.Contains(out, `"value":"xyz"`) {
		t.Fatal(out)
	}
	if out := run("dev", "localstorage-get", "k1", "--raw"); out != "v1" {
		t.Fatal(out)
	}

	// Dialog aliases
	run("dev", "dialog-accept", "hello")
	run("dev", "dialog-dismiss")

	// Network inspection, route mocking, and network-state-set
	var reqs []map[string]any
	if err := json.Unmarshal([]byte(run("dev", "requests")), &reqs); err != nil || len(reqs) == 0 {
		t.Fatal(err, reqs)
	}
	echoIdx := "1"
	for _, r := range reqs {
		if strings.HasSuffix(fmt.Sprint(r["url"]), "/echo") {
			echoIdx = fmt.Sprint(int(r["index"].(float64)))
		}
	}
	if out := run("dev", "request", echoIdx); !strings.Contains(out, "/echo") {
		t.Fatal(out)
	}
	if out := run("dev", "request-headers", echoIdx); !strings.Contains(out, "requestHeaders") {
		t.Fatal(out)
	}
	if out := run("dev", "request-body", echoIdx); !strings.Contains(out, "hello-post") {
		t.Fatal(out)
	}
	if out := run("dev", "response-headers", echoIdx); !strings.Contains(strings.ToLower(out), "x-echo") {
		t.Fatal(out)
	}
	if out := run("dev", "response-body", echoIdx, "--raw"); out != `{"echo":true}` {
		t.Fatal(out)
	}

	run("dev", "route", "**/mocked", "--status", "201", "--body", `{"mocked":true}`, "--header", "X-Mock:yes")
	if out := run("dev", "route-list"); !strings.Contains(out, "**/mocked") {
		t.Fatal(out)
	}
	if out := run("dev", "eval", `fetch('/mocked').then(async r => ({status: r.status, mock: r.headers.get('x-mock'), body: await r.json()}))`); !strings.Contains(out, `"status":201`) || !strings.Contains(out, `"mocked":true`) {
		t.Fatal(out)
	}
	run("dev", "unroute", "**/mocked")

	run("dev", "network-state-set", "offline")
	if out := run("dev", "eval", "navigator.onLine"); out != "false" {
		t.Fatal(out)
	}
	run("dev", "network-state-set", "online")

	// Console level filtering & run-code
	if out := run("dev", "console", "warning"); !strings.Contains(out, "warn-msg") || !strings.Contains(out, "error-msg") || strings.Contains(out, "info-msg") {
		t.Fatal(out)
	}
	if out := run("dev", "run-code", "eval '20+22'"); !strings.Contains(out, "42") {
		t.Fatal(out)
	}

	// Action recording, tracing, and video
	run("dev", "recording-start")
	run("dev", "fill", "#note", "recorded text")
	run("dev", "click", "#dbl-btn")
	recOut := run("dev", "recording-stop")
	if !strings.Contains(recOut, "recorded text") || !strings.Contains(recOut, "fill ") {
		t.Fatal(recOut)
	}

	traceFile := filepath.Join(workDir, "trace.json")
	run("dev", "tracing-start")
	run("dev", "eval", "1+1")
	run("dev", "tracing-stop", traceFile)
	if info, err := os.Stat(traceFile); err != nil || info.Size() == 0 {
		t.Fatal(err)
	}

	videoFile := filepath.Join(workDir, "session.avi")
	run("dev", "video-show-actions")
	run("dev", "video-start", videoFile)
	run("dev", "click", "#dbl-btn")
	run("dev", "video-chapter", "Intro")
	run("dev", "video-stop")
	run("dev", "video-hide-actions")
	if data, err := os.ReadFile(videoFile); err != nil || !bytes.HasPrefix(data, []byte("RIFF")) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "session.chapters.json")); err != nil {
		t.Fatal(err)
	}

	// Debugger pause-at, step-over, resume
	run("dev", "pause-at", "debug.js:2")
	run("dev", "eval", "setTimeout(runStep, 10); true")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		out := run("dev", "step-over")
		if strings.Contains(out, `"paused":true`) {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	run("dev", "resume")
	run("dev", "pause-at", "--clear")
	run("dev", "wait", "--fn", "window.stepCount === 11")

	// Attach and detach across profiles
	run("guest", "attach", "dev")
	if out := run("guest", "get", "title", "--raw"); out != "DevTools Fixture" {
		t.Fatal(out)
	}
	run("guest", "detach")

	// delete-data and close-all
	run("dev", "delete-data")
	if out := run("", "profile", "list"); strings.Contains(out, `"profile":"dev"`) {
		t.Fatalf("deleted profile still listed: %s", out)
	}
	run("other", "open", "--no-sandbox")
	if out := run("", "close-all"); !strings.Contains(out, `"profile":"other"`) {
		t.Fatal(out)
	}
}
