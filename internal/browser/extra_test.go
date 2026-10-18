package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alg0rix/wok/internal/cdp"
)

func TestFindRoleAddsRefsAndDropsOtherDocuments(t *testing.T) {
	page := protocolPage(t, func(method string, params map[string]any) any {
		if method == "DOM.getDocument" {
			return map[string]any{"root": map[string]any{"backendNodeId": 42}}
		}
		return map[string]any{"nodes": []any{
			map[string]any{"backendDOMNodeId": 7, "role": map[string]any{"value": "button"}, "name": map[string]any{"value": "First"}},
			map[string]any{"backendDOMNodeId": 8, "role": map[string]any{"value": "button"}, "name": map[string]any{"value": "Second"}},
		}}
	})
	refs := &Refs{Next: 10, Items: map[string]Ref{"e10": {BackendNodeID: 5, DocumentID: 42, TargetID: "target"}, "e9": {BackendNodeID: 3, DocumentID: 1, TargetID: "target"}}}
	nodes, err := page.Find(context.Background(), "role", "button", 2, refs)
	if err != nil || len(nodes) != 1 || nodes[0].Name != "Second" || nodes[0].Ref != "@e11" {
		t.Fatalf("Find = %+v, %v", nodes, err)
	}
	if len(refs.Items) != 2 || refs.Items["e10"].BackendNodeID != 5 || refs.Items["e11"].BackendNodeID != 8 {
		t.Fatalf("refs = %+v", refs)
	}
	if _, err = page.Find(context.Background(), "role", "button", 3, refs); err == nil {
		t.Fatal("accepted out of range nth")
	}
	nodes, err = page.Find(context.Background(), "role", "unknown-role", 0, refs)
	if err != nil || len(nodes) != 0 || refs.Next != 11 {
		t.Fatalf("unknown role = %+v %v; refs=%+v", nodes, err, refs)
	}
}
func TestFindCSSSortsElementProperties(t *testing.T) {
	page := protocolPage(t, func(method string, params map[string]any) any {
		switch method {
		case "DOM.getDocument":
			return map[string]any{"root": map[string]any{"backendNodeId": 42}}
		case "Accessibility.getFullAXTree":
			return map[string]any{"nodes": []any{}}
		case "Runtime.evaluate":
			return map[string]any{"result": map[string]any{"objectId": "array"}}
		case "Runtime.getProperties":
			return map[string]any{"result": []any{
				map[string]any{"name": "1", "value": map[string]any{"objectId": "second"}},
				map[string]any{"name": "length", "value": map[string]any{"value": 2}},
				map[string]any{"name": "0", "value": map[string]any{"objectId": "first"}},
			}}
		case "DOM.describeNode":
			id := 7
			if params["objectId"] == "second" {
				id = 8
			}
			return map[string]any{"node": map[string]any{"backendNodeId": id, "localName": "div"}}
		default:
			return map[string]any{}
		}
	})
	refs := &Refs{}
	nodes, err := page.Find(context.Background(), "css", ".result", 0, refs)
	if err != nil || len(nodes) != 2 || nodes[0].Ref != "@e1" || refs.Items["e1"].BackendNodeID != 7 || refs.Items["e2"].BackendNodeID != 8 {
		t.Fatalf("CSS nodes=%+v refs=%+v err=%v", nodes, refs, err)
	}
}
func TestCookieMutationsAndDefaultURL(t *testing.T) {
	var set, deleted map[string]any
	var queried []any
	reject := false
	page := protocolPage(t, func(method string, params map[string]any) any {
		switch method {
		case "Runtime.evaluate":
			return map[string]any{"result": map[string]any{"value": "https://example.com/path"}}
		case "Network.setCookie":
			set = params
			return map[string]any{"success": !reject}
		case "Network.deleteCookies":
			deleted = params
			return map[string]any{}
		case "Network.getCookies":
			queried, _ = params["urls"].([]any)
			return map[string]any{"cookies": []any{map[string]any{"name": "token", "value": "abc"}}}
		default:
			return map[string]any{}
		}
	})
	params := map[string]any{"name": "token", "value": "abc", "httpOnly": true}
	if _, err := page.Cookies(context.Background(), "set", params); err != nil {
		t.Fatal(err)
	}
	if set["url"] != "https://example.com/path" || set["httpOnly"] != true {
		t.Fatalf("set params=%+v", set)
	}
	if _, ok := params["url"]; ok {
		t.Fatal("Cookies mutated input params")
	}
	cookies, err := page.Cookies(context.Background(), "list", nil)
	if err != nil || len(cookies.([]map[string]any)) != 1 || !reflect.DeepEqual(queried, []any{"https://example.com/path"}) {
		t.Fatalf("cookies=%+v urls=%+v err=%v", cookies, queried, err)
	}
	if _, err := page.Cookies(context.Background(), "delete", map[string]any{"name": "token"}); err != nil {
		t.Fatal(err)
	}
	if deleted["url"] != "https://example.com/path" {
		t.Fatalf("delete params=%+v", deleted)
	}
	reject = true
	if _, err := page.Cookies(context.Background(), "set", params); err == nil {
		t.Fatal("accepted rejected cookie")
	}
	if _, err := page.Cookies(context.Background(), "clear", nil); err != nil {
		t.Fatal(err)
	}
}
func TestStorageQuotesLiteralKeysAndValues(t *testing.T) {
	var expression string
	page := protocolPage(t, func(method string, params map[string]any) any {
		expression, _ = params["expression"].(string)
		return map[string]any{"result": map[string]any{"value": map[string]bool{"ok": true}}}
	})
	literal := `");throw new Error('injected');//`
	if _, err := page.Storage(context.Background(), "local", "set", literal, literal); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(literal)
	if strings.Count(expression, string(encoded)) != 2 || !strings.Contains(expression, "window.localStorage") {
		t.Fatalf("storage expression=%s", expression)
	}
}
func TestWaitRetriesAndCancellation(t *testing.T) {
	attempts := 0
	page := protocolPage(t, func(method string, params map[string]any) any {
		attempts++
		return map[string]any{"result": map[string]any{"value": attempts >= 2}}
	})
	if err := page.Wait(context.Background(), WaitOptions{Text: "ready"}); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("wait attempts=%d", attempts)
	}
	page = protocolPage(t, func(method string, params map[string]any) any {
		return map[string]any{"result": map[string]any{"value": false}}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := page.Wait(ctx, WaitOptions{Load: true}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait error=%v", err)
	}
}
func TestExtraValidationDoesNotCallBrowser(t *testing.T) {
	page := &Page{}
	for _, opts := range []WaitOptions{{}, {Text: "x", Load: true}, {Text: "x", State: "visible"}, {Selector: "x", State: "bad"}} {
		if err := page.Wait(context.Background(), opts); err == nil {
			t.Errorf("accepted wait=%+v", opts)
		}
	}
	for _, tc := range []struct {
		kind, value string
		nth         int
	}{{"invalid", "x", 0}, {"css", "", 0}, {"role", "button", -1}} {
		if _, err := page.Find(context.Background(), tc.kind, tc.value, tc.nth, &Refs{}); err == nil {
			t.Errorf("accepted find=%+v", tc)
		}
	}
	for _, tc := range []struct {
		action string
		params map[string]any
	}{{"invalid", nil}, {"set", map[string]any{"name": "x"}}, {"delete", map[string]any{}}, {"list", map[string]any{"name": "x"}}, {"set", map[string]any{"name": "x", "value": "x", "unknown": true}}} {
		if _, err := page.Cookies(context.Background(), tc.action, tc.params); err == nil {
			t.Errorf("accepted cookies=%+v", tc)
		}
	}
	if _, err := page.Storage(context.Background(), "invalid", "list", "", ""); err == nil {
		t.Fatal("accepted invalid storage area")
	}
	if _, err := page.Storage(context.Background(), "local", "invalid", "", ""); err == nil {
		t.Fatal("accepted invalid storage action")
	}
	if err := page.Upload(context.Background(), "#file", nil, nil); err == nil {
		t.Fatal("accepted no upload files")
	}
	if err := page.Upload(context.Background(), "#file", []string{t.TempDir()}, nil); err == nil {
		t.Fatal("accepted upload directory")
	}
}

func TestRealBrowserNativeFeatures(t *testing.T) {
	executable := os.Getenv("WOK_TEST_BROWSER")
	if executable == "" {
		t.Skip("set WOK_TEST_BROWSER for browser integration")
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!doctype html><body><button>First</button><button>Second</button><div><span id="needle">Visible needle</span></div><span style="display:none">Visible needle</span><input id="file" type="file"><input id="multi" type="file" multiple><script>window.uploadEvents=[];document.querySelector('#file').addEventListener('input',()=>uploadEvents.push('input'));document.querySelector('#file').addEventListener('change',()=>uploadEvents.push('change'));</script>`))
	}))
	defer fixture.Close()
	page, ctx := extraBrowserPage(t, executable)
	if err := page.Navigate(ctx, fixture.URL); err != nil {
		t.Fatal(err)
	}
	refs := &Refs{}
	nodes, err := page.Find(ctx, "role", "button", 0, refs)
	if err != nil || len(nodes) < 2 || nodes[0].Name != "First" || nodes[1].Name != "Second" {
		t.Fatalf("role find=%+v err=%v", nodes, err)
	}
	old := nodes[0].Ref
	nodes, err = page.Find(ctx, "text", "Visible needle", 0, refs)
	if err != nil || len(nodes) != 1 || nodes[0].Name != "Visible needle" {
		t.Fatalf("text find=%+v err=%v", nodes, err)
	}
	if _, ok := refs.Items[strings.TrimPrefix(old, "@")]; !ok {
		t.Fatal("find discarded current document refs")
	}
	nodes, err = page.Find(ctx, "css", "button", 2, refs)
	if err != nil || len(nodes) != 1 || nodes[0].Name != "Second" {
		t.Fatalf("css nth=%+v err=%v", nodes, err)
	}
	if _, err := page.Storage(ctx, "local", "set", "quote'key", "value\n'\""); err != nil {
		t.Fatal(err)
	}
	value, err := page.Storage(ctx, "local", "get", "quote'key", "")
	if err != nil || value != "value\n'\"" {
		t.Fatalf("storage get=%v err=%v", value, err)
	}
	if _, err := page.Storage(ctx, "session", "set", "session", "yes"); err != nil {
		t.Fatal(err)
	}
	if _, err := page.Storage(ctx, "local", "delete", "quote'key", ""); err != nil {
		t.Fatal(err)
	}
	value, err = page.Storage(ctx, "local", "list", "", "")
	if err != nil || len(value.(map[string]any)) != 0 {
		t.Fatalf("storage list=%v err=%v", value, err)
	}
	if _, err := page.Cookies(ctx, "set", map[string]any{"name": "native", "value": "cookie"}); err != nil {
		t.Fatal(err)
	}
	cookies, err := page.Cookies(ctx, "list", nil)
	if err != nil || len(cookies.([]map[string]any)) != 1 {
		t.Fatalf("cookies=%+v err=%v", cookies, err)
	}
	if _, err := page.Cookies(ctx, "delete", map[string]any{"name": "native"}); err != nil {
		t.Fatal(err)
	}
	for _, options := range []WaitOptions{{Selector: "#needle", State: "visible"}, {Selector: "#absent", State: "hidden"}, {Selector: "#absent", State: "detached"}, {Text: "Visible needle"}, {URL: "127.0.0.1"}, {Function: "async () => true"}, {Load: true}} {
		if err := page.Wait(ctx, options); err != nil {
			t.Fatalf("wait %+v: %v", options, err)
		}
	}
	file := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(file, []byte("native upload"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := page.Upload(ctx, "#file", []string{file}, refs); err != nil {
		t.Fatal(err)
	}
	value, err = page.Eval(ctx, `({name:document.querySelector('#file').files[0].name,events:uploadEvents})`)
	if err != nil {
		t.Fatal(err)
	}
	result := value.(map[string]any)
	if result["name"] != "upload.txt" || !reflect.DeepEqual(result["events"], []any{"input", "change"}) {
		t.Fatalf("upload result=%+v", result)
	}
	if err := page.Upload(ctx, "#file", []string{file, file}, refs); err == nil {
		t.Fatal("accepted multiple files on single input")
	}
	if err := page.Upload(ctx, "#multi", []string{file, file}, refs); err != nil {
		t.Fatal(err)
	}
}
func extraBrowserPage(t *testing.T, executable string) (*Page, context.Context) {
	t.Helper()
	profile := t.TempDir()
	cmd := exec.Command(executable, "--headless", "--no-sandbox", "--disable-gpu", "--remote-debugging-port=0", "--user-data-dir="+profile, "about:blank")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	endpoint := ""
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
	t.Cleanup(func() { client.Close() })
	page, err := Attach(ctx, client, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { page.Close() })
	return page, ctx
}
