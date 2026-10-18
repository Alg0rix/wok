package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExtendedCommandsAndFlows(t *testing.T) {
	engine := os.Getenv("WOK_TEST_BROWSER")
	if engine == "" {
		t.Skip("set WOK_TEST_BROWSER for browser integration")
	}
	engine, _ = filepath.Abs(engine)
	bin := filepath.Join(t.TempDir(), "wok"+exeSuffix())
	if b, e := exec.Command("go", "build", "-o", bin, "../cmd/wok").CombinedOutput(); e != nil {
		t.Fatalf("%v %s", e, b)
	}
	home := t.TempDir()
	invoke := func(profile string, args ...string) ([]byte, []byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, append([]string{"--profile", profile}, args...)...)
		cmd.Env = append(os.Environ(), "WOK_HOME="+home)
		var out, errOut bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errOut
		e := cmd.Run()
		return out.Bytes(), errOut.Bytes(), e
	}
	run := func(profile string, args ...string) string {
		t.Helper()
		out, errOut, e := invoke(profile, args...)
		if e != nil {
			t.Fatalf("%s %v: %v %s %s", profile, args, e, out, errOut)
		}
		return strings.TrimSpace(string(out))
	}
	t.Cleanup(func() {
		for _, p := range []string{"full", "remote"} {
			_, _, _ = invoke(p, "stop")
		}
	})
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" {
			time.Sleep(25 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"ready":true}`)
			return
		}
		if r.URL.Path == "/download" {
			w.Header().Set("Content-Disposition", `attachment; filename="wok-test.txt"`)
			fmt.Fprint(w, "download-body")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Extended fixture</title><label>Email<input id="email"></label><input id="file" type="file" multiple><button id="submit" onclick="document.querySelector('#success').textContent=document.querySelector('#email').value">Submit</button><div id="success"></div><button id="alert" onclick="alert('Hello')">Alert</button><button id="confirm" onclick="window.answer=confirm('Continue?')">Confirm</button><button id="prompt" onclick="window.promptAnswer=prompt('Name?','default')">Prompt</button><a id="download" href="/download">Download</a><script>console.log('fixture-ready');window.ready=true;fetch('/api').then(r=>r.json()).then(()=>window.apiReady=true);setTimeout(()=>{throw new Error('fixture-exception')},100)</script>`)
	}))
	defer fixture.Close()
	run("full", "start", "--browser", engine, "--no-sandbox")
	run("full", "open", fixture.URL)
	run("full", "wait", "--load")
	run("full", "wait", "--fn", "window.apiReady === true")
	run("full", "wait", "--text", "Submit")
	run("full", "wait", "--url", ":")
	run("full", "wait", "#email", "--state", "visible")
	run("full", "wait", "#absent", "--state", "hidden")
	run("full", "wait", "--network-idle", "--network-idle-ms", "100")
	if b := run("full", "find", "role", "button", "--nth", "1"); !strings.Contains(b, "@e") {
		t.Fatal(b)
	}
	run("full", "find", "text", "Submit")
	run("full", "find", "css", "button")
	run("full", "cookies", "set", "sid", "cookie-value")
	if b := run("full", "cookies"); !strings.Contains(b, "cookie-value") {
		t.Fatal(b)
	}
	run("full", "cookies", "delete", "sid")
	run("full", "cookies", "clear")
	run("full", "storage", "local", "set", "quote'key", `value"'`)
	if b := run("full", "storage", "local", "get", "quote'key"); b != `"value\"'"` {
		t.Fatal(b)
	}
	run("full", "storage", "local", "delete", "quote'key")
	run("full", "storage", "session", "set", "token", "abc")
	run("full", "storage", "session", "clear")
	file := filepath.Join(t.TempDir(), "upload.txt")
	os.WriteFile(file, []byte("uploaded"), 0600)
	run("full", "upload", "#file", file)
	if b := run("full", "eval", "document.querySelector('#file').files[0].name"); b != `"upload.txt"` {
		t.Fatal(b)
	}
	if b := run("full", "console"); !strings.Contains(b, "fixture-ready") {
		t.Fatal("past console missing", b)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if b := run("full", "errors"); strings.Contains(b, "fixture-exception") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("past exception missing")
		}
		time.Sleep(30 * time.Millisecond)
	}
	if b := run("full", "network"); !strings.Contains(b, "/api") {
		t.Fatal("past request missing", b)
	}
	run("full", "click", "#alert")
	run("full", "click", "#confirm")
	if b := run("full", "eval", "window.answer"); b != "false" {
		t.Fatal("confirm default should dismiss", b)
	}
	run("full", "dialog", "accept", "typed prompt")
	run("full", "click", "#confirm")
	if b := run("full", "eval", "window.answer"); b != "true" {
		t.Fatal(b)
	}
	run("full", "click", "#prompt")
	if b := run("full", "eval", "window.promptAnswer"); b != `"typed prompt"` {
		t.Fatal(b)
	}
	if b := run("full", "dialogs"); !strings.Contains(b, "Continue?") {
		t.Fatal(b)
	}
	run("full", "click", "#download")
	deadline = time.Now().Add(5 * time.Second)
	for {
		b := run("full", "downloads")
		if strings.Contains(b, "completed") && strings.Contains(b, "wok-test.txt") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("download not completed", b)
		}
		time.Sleep(40 * time.Millisecond)
	}
	run("full", "wait", "--download")
	downloaded, e := os.ReadFile(filepath.Join(home, "profiles/full/downloads/wok-test.txt"))
	if e != nil || string(downloaded) != "download-body" {
		t.Fatal(string(downloaded), e)
	}
	run("full", "set", "device", "iphone-14")
	if b := run("full", "eval", "screen.width"); b != "390" {
		t.Fatal(b)
	}
	run("full", "set", "media", "dark")
	if b := run("full", "eval", `matchMedia('(prefers-color-scheme: dark)').matches`); b != "true" {
		t.Fatal(b)
	}
	run("full", "set", "offline", "on")
	if b := run("full", "eval", "navigator.onLine"); b != "false" {
		t.Fatal(b)
	}
	run("full", "set", "offline", "off")
	run("full", "set", "media", "no-preference")
	run("full", "viewport", "reset")
	ansi := run("full", "terminal", "--frames", "1", "--width", "32")
	if !strings.Contains(ansi, "▀") {
		t.Fatal("terminal frame missing")
	}
	// A flow uses stable selectors and captured tab IDs; flags are treated literally.
	flowFile := filepath.Join(t.TempDir(), "test.yaml")
	source := `version: 1
profile: overridden
steps:
  - open: "${APP_URL}"
  - fill: {selector: "#email", value: "${EMAIL}"}
  - click: "#submit"
  - expect: {selector: "#success", text: "${EMAIL}"}
  - tab: {new: "${APP_URL}/details"}
    save: details
  - tab: {select: "${details.targetId}"}
  - expect: {js: "document.title === 'Extended fixture'"}
  - snapshot: {interactive: true}
  - tab: {close: "${details.targetId}"}
  - eval: "2+2"
`
	os.WriteFile(flowFile, []byte(source), 0600)
	var report struct {
		OK      bool   `json:"ok"`
		Profile string `json:"profile"`
		Steps   []struct {
			OK bool `json:"ok"`
		} `json:"steps"`
		Screenshot string `json:"screenshot"`
	}
	output := run("full", "run", flowFile, "--var", "APP_URL="+fixture.URL, "--var", "EMAIL=--profile")
	if e = json.Unmarshal([]byte(output), &report); e != nil || !report.OK || report.Profile != "full" || len(report.Steps) != 10 {
		t.Fatal(output, e)
	}
	failure := filepath.Join(t.TempDir(), "fail.json")
	os.WriteFile(failure, []byte(`{"steps":[{"expect":{"selector":"#missing"},"timeout":"100ms"}]}`), 0600)
	artifacts := t.TempDir()
	out, _, e := invoke("full", "run", failure, "--artifacts", artifacts)
	if e == nil {
		t.Fatal("failed expectation exited zero")
	}
	if e = json.Unmarshal(out, &report); e != nil || report.OK || report.Screenshot == "" {
		t.Fatalf("missing failure report/artifact: %s %v", out, e)
	}
	if _, e = os.Stat(report.Screenshot); e != nil {
		t.Fatal(e)
	}
	script := filepath.Join(t.TempDir(), "steps.txt")
	os.WriteFile(script, []byte("# comment\nget title\neval '1+1'\n"), 0600)
	if b := run("full", "exec", script); !strings.Contains(b, `"ok":true`) {
		t.Fatal(b)
	}
	// Authenticated remote discovery, raw CDP, and MCP use the same live browser.
	var remote map[string]any
	json.Unmarshal([]byte(run("remote", "start", "--browser", engine, "--no-sandbox", "--auth-token", "test-secret", "--embed-origin", fixture.URL)), &remote)
	base := remote["renderUrl"].(string)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, e := client.Get(base + "/render")
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("remote access missing auth", resp.StatusCode)
	}
	viewer := run("remote", "render")
	if !strings.Contains(viewer, "token=test-secret") {
		t.Fatal("authenticated viewer link missing", viewer)
	}
	request := func(path string, body string) *http.Response {
		t.Helper()
		method := "GET"
		var input io.Reader
		if body != "" {
			method = "POST"
			input = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, base+path, input)
		req.Header.Set("Authorization", "Bearer test-secret")
		req.Header.Set("Content-Type", "application/json")
		resp, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		return resp
	}
	resp = request("/json/version", "")
	var discovery map[string]any
	json.NewDecoder(resp.Body).Decode(&discovery)
	resp.Body.Close()
	if !strings.Contains(discovery["webSocketDebuggerUrl"].(string), "test-secret") {
		t.Fatal(discovery)
	}
	external := exec.Command(bin, "--cdp", base, "--auth-token", "test-secret", "eval", "1+2")
	external.Env = append(os.Environ(), "WOK_HOME="+home)
	if b, e := external.CombinedOutput(); e != nil || strings.TrimSpace(string(b)) != "3" {
		t.Fatalf("proxy CDP: %s %v", b, e)
	}
	resp = request("/mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Contains(body, []byte("protocolVersion")) {
		t.Fatal(string(body))
	}
	resp = request("/mcp", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"browser_command","arguments":{"command":"eval","args":["2+3"]}}}`)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Contains(body, []byte(`"isError":false`)) || !bytes.Contains(body, []byte("5")) {
		t.Fatal(string(body))
	}
	// Explicit embed origin is allowed, unrelated origin is rejected.
	req, _ := http.NewRequest("GET", base+"/render?token=test-secret", nil)
	req.Header.Set("Origin", fixture.URL)
	resp, e = client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	req, _ = http.NewRequest("POST", base+"/render/type?token=test-secret&text="+url.QueryEscape("x"), nil)
	req.Header.Set("Origin", "https://evil.example")
	resp, e = client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("untrusted remote origin accepted")
	}
	run("remote", "stop")
	run("full", "stop")
}
