package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPersistentCLI(t *testing.T) {
	executable := os.Getenv("WOK_TEST_BROWSER")
	if executable == "" {
		t.Skip("set WOK_TEST_BROWSER to run real browser integration")
	}
	executable, err := filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "wok"+exeSuffix())
	build := exec.Command("go", "build", "-o", bin, "../cmd/wok")
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build: %v: %s", e, out)
	}
	home := t.TempDir()
	invoke := func(profile string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, append([]string{"--profile", profile}, args...)...)
		cmd.Env = append(os.Environ(), "WOK_HOME="+home)
		out, e := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), e
	}
	run := func(profile string, args ...string) string {
		t.Helper()
		out, e := invoke(profile, args...)
		if e != nil {
			t.Fatalf("%s %v: %v: %s", profile, args, e, out)
		}
		return out
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Wok fixture</title><style>button{display:block;margin-top:10px}#cover{position:fixed;inset:0;background:#fff8;z-index:9}</style><label>Name<input id="name"></label><button id="go" onclick="document.querySelector('#result').textContent=document.querySelector('#name').value;window.clicks=(window.clicks||0)+1">Submit</button><div id="result"></div><div style="height:1500px"></div>`)
	}))
	defer fixture.Close()
	t.Cleanup(func() {
		for _, p := range []string{"work", "personal", "race"} {
			_, _ = invoke(p, "stop")
		}
	})
	var work, personal map[string]any
	if err = json.Unmarshal([]byte(run("work", "start", "--browser", executable, "--no-sandbox")), &work); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(run("personal", "start", "--browser", executable, "--no-sandbox")), &personal); err != nil {
		t.Fatal(err)
	}
	if work["endpoint"] == personal["endpoint"] || work["pid"] == personal["pid"] {
		t.Fatal("profiles must use distinct browsers")
	}
	var again map[string]any
	json.Unmarshal([]byte(run("work", "start")), &again)
	if work["endpoint"] != again["endpoint"] || work["pid"] != again["pid"] {
		t.Fatal("start did not reuse browser")
	}
	run("work", "open", fixture.URL)
	run("work", "eval", `localStorage.setItem('profile','work');document.cookie='profile=work;path=/'; true`)
	run("personal", "open", fixture.URL)
	if out := run("personal", "eval", `({local:localStorage.getItem('profile'),cookie:document.cookie})`); out != `{"cookie":"","local":null}` {
		t.Fatalf("profile leak: %s", out)
	}
	snapshot := run("work", "snapshot", "-i", "--json")
	var nodes []struct{ Ref, Role, Name string }
	if e := json.Unmarshal([]byte(snapshot), &nodes); e != nil {
		t.Fatal(e)
	}
	refs := map[string]string{}
	for _, n := range nodes {
		refs[n.Role] = n.Ref
	}
	if !strings.HasPrefix(refs["textbox"], "@e") {
		t.Fatalf("missing refs: %s", snapshot)
	}
	run("work", "fill", refs["textbox"], "persistent input")
	run("work", "click", refs["button"])
	if out := run("work", "get", "text", "#result"); out != `"persistent input"` {
		t.Fatal(out)
	}
	if out := run("work", "get", "text"); !strings.Contains(out, "Submit") {
		t.Fatal(out)
	}
	// Commands have not relaunched browser.
	var status map[string]any
	json.Unmarshal([]byte(run("work", "status")), &status)
	if status["pid"] != work["pid"] || status["endpoint"] != work["endpoint"] {
		t.Fatal("browser changed across commands")
	}
	run("work", "eval", `document.body.insertAdjacentHTML('beforeend','<div id="cover"></div>');true`)
	if out, e := invoke("work", "click", refs["button"]); e == nil || !strings.Contains(out, "obscured") {
		t.Fatalf("covered click accepted: %s %v", out, e)
	}
	run("work", "eval", `document.querySelector('#cover').remove(); true`)
	if out, e := invoke("work", "click", "div"); e == nil || !strings.Contains(out, "require exactly one") {
		t.Fatalf("ambiguous selector: %s %v", out, e)
	}
	run("work", "open", fixture.URL+"/next")
	if out, e := invoke("work", "click", refs["button"]); e == nil || !strings.Contains(out, "stale") {
		t.Fatalf("stale ref accepted: %s %v", out, e)
	}
	if _, e := invoke("work", "--timeout", "200ms", "wait", "--selector", "#absent"); e == nil {
		t.Fatal("missing selector should time out")
	}
	run("work", "eval", `setTimeout(()=>document.body.insertAdjacentHTML('beforeend','<p id="later">ready</p>'),100);true`)
	run("work", "wait", "--selector", "#later")
	shot := filepath.Join(t.TempDir(), "page.png")
	run("work", "screenshot", shot)
	data, e := os.ReadFile(shot)
	if e != nil || !bytes.HasPrefix(data, []byte("\x89PNG")) {
		t.Fatal("invalid screenshot", e)
	}
	var tab map[string]any
	json.Unmarshal([]byte(run("work", "tab", "new", fixture.URL)), &tab)
	run("work", "tab", "select", tab["targetId"].(string))
	run("work", "tab", "close", tab["targetId"].(string))
	run("work", "open", fixture.URL)
	if out := run("work", "cdp", "Runtime.evaluate", `{"expression":"2+2","returnByValue":true}`); !strings.Contains(out, `"value":4`) {
		t.Fatal(out)
	}
	// External attachment must neither close nor overwrite managed profile state.
	cmd := exec.Command(bin, "--cdp", work["endpoint"].(string), "eval", "document.title")
	cmd.Env = append(os.Environ(), "WOK_HOME="+home)
	if b, e := cmd.CombinedOutput(); e != nil || !strings.Contains(string(b), "Wok fixture") {
		t.Fatalf("external: %s %v", b, e)
	}
	base := work["renderUrl"].(string)
	client := &http.Client{Timeout: 4 * time.Second}
	get := func(path string) *http.Response {
		t.Helper()
		resp, e := client.Get(base + path)
		if e != nil {
			t.Fatal(e)
		}
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("GET %s: %d %s", path, resp.StatusCode, b)
		}
		return resp
	}
	resp := get("/render")
	html, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Contains(html, []byte("/render/stream")) {
		t.Fatal("missing live viewer")
	}
	resp = get("/render/screenshot")
	data, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.HasPrefix(data, []byte("\x89PNG")) {
		t.Fatal("render screenshot invalid")
	}
	resp = get("/render/stream")
	reader := multipart.NewReader(resp.Body, "frame")
	part, e := reader.NextPart()
	if e != nil {
		resp.Body.Close()
		t.Fatal(e)
	}
	data, e = io.ReadAll(part)
	resp.Body.Close()
	if e != nil || !bytes.HasPrefix(data, []byte{0xff, 0xd8}) {
		t.Fatal("invalid MJPEG frame", e)
	}
	run("work", "eval", `document.querySelector('#name').focus();true`)
	resp, e = client.Post(base+"/render/input", "application/json", strings.NewReader(`{"type":"text","text":"viewer input"}`))
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("viewer text HTTP %d", resp.StatusCode)
	}
	if out := run("work", "eval", `document.querySelector('#name').value`); out != `"viewer input"` {
		t.Fatal(out)
	}
	// Emulation remains applied after the setting CLI has exited, and new tabs inherit it.
	run("work", "viewport", "390", "844", "--mobile", "--dpr", "3")
	wantMobile := `{"dpr":3,"height":844,"mobile":true,"touch":1,"width":390}`
	metricsExpr := `({width:innerWidth,height:innerHeight,dpr:devicePixelRatio,touch:navigator.maxTouchPoints,mobile:navigator.userAgent.includes('Mobile')})`
	if out := run("work", "eval", metricsExpr); out != wantMobile {
		t.Fatalf("mobile override not persistent: %s", out)
	}
	if out := run("personal", "eval", "innerWidth"); out == "390" {
		t.Fatal("viewport leaked to other profile")
	}
	var mobileTab map[string]any
	json.Unmarshal([]byte(run("work", "tab", "new", fixture.URL+"/mobile")), &mobileTab)
	deadlineMobile := time.Now().Add(3 * time.Second)
	for {
		out := run("work", "eval", metricsExpr)
		if out == wantMobile {
			break
		}
		if time.Now().After(deadlineMobile) {
			t.Fatalf("new tab did not inherit emulation: %s", out)
		}
		time.Sleep(25 * time.Millisecond)
	}
	run("work", "tab", "close", mobileTab["targetId"].(string))
	run("work", "get", "title")
	mobilePNG := filepath.Join(t.TempDir(), "mobile.png")
	run("work", "screenshot", mobilePNG)
	mobileData, e := os.ReadFile(mobilePNG)
	if e != nil {
		t.Fatal(e)
	}
	dimensions, e := png.DecodeConfig(bytes.NewReader(mobileData))
	if e != nil || dimensions.Width != 1170 || dimensions.Height != 2532 {
		t.Fatalf("mobile pixels %+v %v", dimensions, e)
	}
	// Origin protection prevents a website from controlling the local viewer.
	req, _ := http.NewRequest("POST", base+"/render/input", strings.NewReader(`{"type":"text","text":"bad"}`))
	req.Header.Set("Origin", "https://evil.example")
	resp, e = client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("cross-origin input accepted")
	}
	// Render routes return real frames/source and validated tab controls.
	resp = get("/render/screenshot.png")
	if resp.Header.Get("X-Wok-Viewport-Width") == "" {
		t.Fatal("missing logical viewport headers")
	}
	resp.Body.Close()
	resp = get("/render/screenshot.ppm?w=80&h=60")
	ppm, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.HasPrefix(ppm, []byte("P6\n")) {
		t.Fatal("invalid PPM")
	}
	resp = get("/render/html")
	source, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Contains(source, []byte(`id="name"`)) || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("DOM source missing or not sandboxed")
	}
	resp, e = client.Get(base + "/render/screenshot.png?tab=missing")
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatal("missing tab did not return 404")
	}
	var selected map[string]any
	json.Unmarshal([]byte(run("work", "status")), &selected)
	originalTab := selected["targetId"].(string)
	resp, e = client.Post(base+"/render/tab/new?url="+url.QueryEscape(fixture.URL), "", nil)
	if e != nil {
		t.Fatal(e)
	}
	var created map[string]string
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created["id"] == "" {
		t.Fatal("new render tab missing ID")
	}
	run("work", "tab", "select", originalTab)
	resp, e = client.Post(base+"/render/tab/close?tab="+originalTab, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("render tab close failed")
	}
	// Persisted selection recovers; explicit IDs continue to fail instead of misrouting.
	run("work", "wait", "--selector", "#name")
	if out := run("work", "get", "title"); out != `"Wok fixture"` {
		t.Fatal(out)
	}
	if _, e = invoke("work", "--tab", originalTab, "get", "title"); e == nil {
		t.Fatal("explicit closed --tab silently changed target")
	}
	resp, e = client.Post(base+"/render/tab/close?tab="+created["id"], "", nil)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatal("render should preserve last tab")
	}
	// Recover missing metadata using Chrome's live identity and ready viewer record.
	if e = os.Remove(filepath.Join(home, "profiles", "work", "state.json")); e != nil {
		t.Fatal(e)
	}
	var recovered map[string]any
	json.Unmarshal([]byte(run("work", "start")), &recovered)
	if recovered["endpoint"] != work["endpoint"] || recovered["pid"] != work["pid"] || recovered["renderUrl"] != base {
		t.Fatalf("recovery replaced live browser/viewer: %v", recovered)
	}
	// A crashed/stopped viewer can be recreated without restarting the browser.
	raw, e := os.ReadFile(filepath.Join(home, "profiles", "work", "state.json"))
	if e != nil {
		t.Fatal(e)
	}
	var private struct {
		Token string `json:"renderToken"`
	}
	json.Unmarshal(raw, &private)
	req, _ = http.NewRequest("POST", base+"/_shutdown", nil)
	req.Header.Set("Authorization", "Bearer "+private.Token)
	resp, e = client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	for deadline := time.Now().Add(3 * time.Second); ; {
		resp, e = client.Get(base + "/health")
		if e != nil {
			break
		}
		resp.Body.Close()
		if time.Now().After(deadline) {
			t.Fatal("viewer shutdown timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
	json.Unmarshal([]byte(run("work", "start")), &recovered)
	if recovered["endpoint"] != work["endpoint"] || recovered["pid"] != work["pid"] {
		t.Fatal("viewer recovery restarted browser")
	}
	base = recovered["renderUrl"].(string)
	if out := run("work", "eval", metricsExpr); out != wantMobile {
		t.Fatalf("viewer restart lost emulation: %s", out)
	}
	// Concurrent separate CLI processes serialize startup into one browser.
	var wg sync.WaitGroup
	results := make(chan string, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, e := invoke("race", "start", "--browser", executable, "--no-sandbox", "--no-render")
			results <- out
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	endpoints := map[string]bool{}
	for out := range results {
		var st map[string]any
		if json.Unmarshal([]byte(out), &st) != nil {
			t.Fatal(out)
		}
		endpoints[st["endpoint"].(string)] = true
	}
	if len(endpoints) != 1 {
		t.Fatal("concurrent start created duplicate browsers")
	}
	run("work", "stop")
	run("personal", "eval", "document.title")
	// Viewer process exits when the managed browser closes.
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, e := client.Get(base + "/health")
		if e != nil {
			break
		}
		resp.Body.Close()
		if time.Now().After(deadline) {
			t.Fatal("viewer outlived browser")
		}
		time.Sleep(30 * time.Millisecond)
	}
	run("work", "start", "--browser", executable, "--no-sandbox")
	run("work", "open", fixture.URL)
	if out := run("work", "eval", `localStorage.getItem('profile')`); out != `"work"` {
		t.Fatalf("storage lost after restart: %s", out)
	}
	if out := run("work", "eval", metricsExpr); out != wantMobile {
		t.Fatalf("browser restart lost emulation: %s", out)
	}
	run("work", "viewport", "reset")
	if out := run("work", "eval", `({touch:navigator.maxTouchPoints,mobile:navigator.userAgent.includes('Mobile')})`); out != `{"mobile":false,"touch":0}` {
		t.Fatalf("emulation reset failed: %s", out)
	}
	run("work", "stop")
	run("personal", "stop")
	run("race", "stop")
}
