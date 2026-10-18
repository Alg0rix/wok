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

// TestFlowControlAndRoutes drives a chat-like page with flow loops, branches,
// stacked one-shot routes, and request body inspection.
func TestFlowControlAndRoutes(t *testing.T) {
	engine := os.Getenv("WOK_TEST_BROWSER")
	if engine == "" {
		t.Skip("set WOK_TEST_BROWSER for browser integration")
	}
	engine, _ = filepath.Abs(engine)
	dir := t.TempDir()
	bin := filepath.Join(dir, "wok"+exeSuffix())
	if b, e := exec.Command("go", "build", "-o", bin, "../cmd/wok").CombinedOutput(); e != nil {
		t.Fatalf("%v %s", e, b)
	}
	home := t.TempDir()
	invoke := func(args ...string) ([]byte, []byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, append([]string{"--profile", "flowctl"}, args...)...)
		cmd.Env = append(os.Environ(), "WOK_HOME="+home)
		var out, errOut bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		e := cmd.Run()
		return out.Bytes(), errOut.Bytes(), e
	}
	run := func(args ...string) string {
		t.Helper()
		out, errOut, e := invoke(args...)
		if e != nil {
			t.Fatalf("%v: %v %s %s", args, e, out, errOut)
		}
		return strings.TrimSpace(string(out))
	}
	t.Cleanup(func() { _, _, _ = invoke("stop") })
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.WriteHeader(501) // the real server never answers; routes must
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><title>Chat</title><textarea class="chat-input"></textarea><button class="chat-send">Send</button><div class="log"></div>
<script>window.statuses=[];document.querySelector('.chat-send').onclick=async()=>{const message=document.querySelector('.chat-input').value;const r=await fetch('/api/chat/stream',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({message})});window.statuses.push(r.status);const d=document.createElement('div');d.className='msg';d.textContent=message;document.querySelector('.log').append(d)}</script>`)
	}))
	defer fixture.Close()
	run("start", "--browser", engine, "--no-sandbox", "--no-render")

	if _, errOut, e := invoke("route", "*/api/*", "--status", "200"); e == nil || !strings.Contains(string(errOut), "**/api/**") {
		t.Fatalf("expected a pattern hint, err=%v %s", e, errOut)
	}

	flowFile := filepath.Join(dir, "chat.wok.yaml")
	if err := os.WriteFile(flowFile, []byte(`profile: flowctl
steps:
  - unroute: true
  - route: {pattern: '**/api/**', status: 200, body: '{}'}
  - route: {pattern: '**/api/chat/stream', status: 500, body: '{}', times: 1}
  - open: '${APP_URL}'
  - each:
      items: [first follow-up, second]
      as: msg
      steps:
        - fill: {selector: .chat-input, value: '${msg.item}'}
        - click: .chat-send
        - expect: {js: 'document.querySelectorAll(".msg").length === ${msg.number}'}
  - expect: {js: 'JSON.stringify(window.statuses) === "[500,200]"'}
  - if: {selector: .missing}
    then:
      - click: .missing
    else:
      - eval: 'window.branch = "else"'
  - repeat:
      until: {js: 'window.count >= 3'}
      max: 5
      steps:
        - eval: 'window.count = (window.count || 0) + 1'
  - expect: {js: 'window.branch === "else" && window.count === 3'}
  - network: true
    save: net
  - each:
      items: '${net}'
      as: req
      steps:
        - if: {js: '"${req.item.method}" === "POST"'}
          then:
            - request: '${req.item.index}'
              save: post
`), 0600); err != nil {
		t.Fatal(err)
	}
	out := run("run", flowFile, "--var", "APP_URL="+fixture.URL)
	var report struct {
		OK    bool `json:"ok"`
		Steps []struct {
			Path   string          `json:"path"`
			Action string          `json:"action"`
			Result json.RawMessage `json:"result"`
		} `json:"steps"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil || !report.OK {
		t.Fatalf("report %s: %v", out, err)
	}
	// The flow saves each POST in turn; the last one is the second message.
	var bodies []string
	for _, s := range report.Steps {
		if s.Action == "request" {
			var r struct {
				RequestBody string `json:"requestBody"`
			}
			_ = json.Unmarshal(s.Result, &r)
			bodies = append(bodies, r.RequestBody)
		}
	}
	if len(bodies) != 2 || !strings.Contains(bodies[0], "first follow-up") || !strings.Contains(bodies[1], "second") {
		t.Fatalf("request bodies=%q\n%s", bodies, out)
	}

	var routes []struct {
		Pattern string `json:"pattern"`
		Times   int    `json:"times"`
		Hits    int    `json:"hits"`
	}
	listRoutes := func() {
		t.Helper()
		routes = nil
		if err := json.Unmarshal([]byte(run("route-list")), &routes); err != nil {
			t.Fatal(err)
		}
	}
	// The one-shot route expired after its hit; the base route served the rest.
	listRoutes()
	if len(routes) != 1 || routes[0].Pattern != "**/api/**" || routes[0].Hits != 1 {
		t.Fatalf("routes=%+v", routes)
	}
	// Re-registering an untimed route keeps timed routes stacked on it, and
	// the newest timed route answers first.
	run("route", "**/api/**", "--status", "201", "--times", "1")
	run("route", "**/api/**", "--status", "202", "--times", "1")
	run("route", "**/api/**", "--status", "204")
	listRoutes()
	if len(routes) != 3 {
		t.Fatalf("routes=%+v", routes)
	}
	statuses := run("eval", `async () => { const s = []; for (let i = 0; i < 3; i++) s.push((await fetch('/api/x')).status); return s }`)
	if statuses != "[202,201,204]" {
		t.Fatalf("statuses=%s", statuses)
	}
	run("unroute")

	// Thrown errors report the exception, and failed expects the observed values.
	if _, errOut, e := invoke("eval", "throw new Error('expected 2, got 0')"); e == nil || !strings.Contains(string(errOut), "browser JavaScript: Error: expected 2, got 0") || strings.Contains(string(errOut), "exceptionId") {
		t.Fatalf("eval error=%v %s", e, errOut)
	}
	failFile := filepath.Join(dir, "fail.wok.yaml")
	os.WriteFile(failFile, []byte(`profile: flowctl
steps:
  - eval: 'const n = 2; document.title = `+"`"+`n=$${n}`+"`"+`'
  - expect: {js: 'document.title === "n=2"'}
  - expect: {selector: .msg, count: 5}
    timeout: 300ms
`), 0600)
	failOut, _, e := invoke("run", failFile, "--artifacts", dir)
	if e == nil || !strings.Contains(string(failOut), "count: want 5, got 2") {
		t.Fatalf("expect failure err=%v %s", e, failOut)
	}
}
