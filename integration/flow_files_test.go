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
	"testing"
	"time"
)

// TestFlowFilesAndElementActions runs a flow from another directory: its
// upload fixture and route bodyFile resolve next to the flow file, uploads go
// through a file chooser opened by a button, and element actions reach the page.
func TestFlowFilesAndElementActions(t *testing.T) {
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
		cmd := exec.CommandContext(ctx, bin, append([]string{"--profile", "flowfiles"}, args...)...)
		cmd.Env = append(os.Environ(), "WOK_HOME="+home)
		cmd.Dir = t.TempDir() // relative flow files must not resolve from here
		var out, errOut bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		e := cmd.Run()
		return out.Bytes(), errOut.Bytes(), e
	}
	t.Cleanup(func() { _, _, _ = invoke("stop") })
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><button id="pick">Attach</button><ul id="files"></ul>
<select id="size"><option>s</option><option>m</option></select><input id="agree" type="checkbox">
<button id="hover">menu</button><p id="api"></p>
<script>document.querySelector('#pick').onclick=()=>{const i=document.createElement('input');i.type='file';i.onchange=async()=>{for(const f of i.files){const li=document.createElement('li');li.textContent=f.name+':'+(await f.text());document.querySelector('#files').append(li)}};i.click()};
document.querySelector('#hover').onmouseover=()=>{document.body.dataset.hovered='yes'};
fetch('/mock/data').then(r=>r.json()).then(d=>{document.querySelector('#api').textContent=d.ok})</script>`)
	}))
	defer fixture.Close()
	if out, errOut, e := invoke("start", "--browser", engine, "--no-sandbox", "--no-render"); e != nil {
		t.Fatalf("start: %v %s %s", e, out, errOut)
	}
	flowDir := filepath.Join(dir, "flows")
	if err := os.MkdirAll(filepath.Join(flowDir, "fixtures"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"fixtures/cv.txt": "hello cv", "fixtures/data.json": `{"ok":"mocked"}`} {
		if err := os.WriteFile(filepath.Join(flowDir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	flowFile := filepath.Join(flowDir, "files.wok.yaml")
	if err := os.WriteFile(flowFile, []byte(`profile: flowfiles
steps:
  - unroute: true
  - route: {pattern: '**/mock/data', bodyFile: fixtures/data.json, contentType: application/json}
  - open: '${APP_URL}'
  - expect: {selector: '#api', text: mocked}
  - upload: {selector: '#pick', files: [fixtures/cv.txt]}
  - expect: {selector: '#files li', text: 'cv.txt:hello cv'}
  - select: {selector: '#size', value: m}
  - expect: {selector: '#size', value: m}
  - check: '#agree'
  - expect: {js: 'document.querySelector("#agree").checked'}
  - hover: '#hover'
  - expect: {js: 'document.body.dataset.hovered === "yes"'}
  - reload: true
  - expect: {selector: '#files li', count: 0}
`), 0600); err != nil {
		t.Fatal(err)
	}
	if out, errOut, e := invoke("run", flowFile, "--var", "APP_URL="+fixture.URL, "--artifacts", filepath.Join(dir, "artifacts")); e != nil {
		t.Fatalf("flow: %v %s %s", e, out, errOut)
	}
}
