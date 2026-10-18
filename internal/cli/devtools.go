package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/alg0rix/wok/internal/browser"
	"github.com/alg0rix/wok/internal/cdp"
	"github.com/alg0rix/wok/internal/session"
)

// aliases maps playwright-cli style names onto Wok's own commands.
func alias(command string, a []string) (string, []string) {
	switch command {
	case "open":
		if len(a) == 0 {
			return "start", nil
		}
	case "goto":
		return "open", a
	case "close":
		return "stop", a
	case "list":
		return "profile", []string{"list"}
	case "show":
		return "render", a
	case "resize":
		return "viewport", a
	case "requests":
		return "network", a
	case "install-browser":
		if len(a) == 0 || (len(a) == 1 && (a[0] == "chromium" || a[0] == "chrome" || a[0] == "chrome-headless-shell")) {
			return "browser", []string{"install"}
		}
		return "browser", a
	case "tab-list", "tab-new", "tab-close", "tab-select":
		return "tab", append([]string{strings.TrimPrefix(command, "tab-")}, a...)
	case "cookie-list", "cookie-set", "cookie-delete", "cookie-clear":
		return "cookies", append([]string{strings.TrimPrefix(command, "cookie-")}, a...)
	case "dialog-accept", "dialog-dismiss":
		return "dialog", append([]string{strings.TrimPrefix(command, "dialog-")}, a...)
	case "network-state-set":
		if len(a) == 1 && (a[0] == "online" || a[0] == "offline") {
			return "set", []string{"offline", map[string]string{"online": "off", "offline": "on"}[a[0]]}
		}
		return "set", []string{"offline"}
	}
	for _, area := range []string{"localstorage", "sessionstorage"} {
		if action, ok := strings.CutPrefix(command, area+"-"); ok {
			return "storage", append([]string{strings.TrimSuffix(area, "storage"), action}, a...)
		}
	}
	return command, a
}

// pageCommands run against the selected page through the shared dispatcher.
var pageCommands = map[string]bool{
	"dblclick": true, "hover": true, "drag": true, "drop": true, "select": true, "check": true, "uncheck": true,
	"go-back": true, "go-forward": true, "reload": true, "keydown": true, "keyup": true,
	"mousemove": true, "mousedown": true, "mouseup": true, "mousewheel": true, "pdf": true,
	"state-save": true, "state-load": true, "cookie-get": true,
	"request": true, "request-headers": true, "request-body": true, "response-headers": true, "response-body": true,
	"route": true, "route-list": true, "unroute": true,
	"recording-start": true, "recording-stop": true, "tracing-start": true, "tracing-stop": true,
	"video-start": true, "video-stop": true, "video-chapter": true, "video-show-actions": true, "video-hide-actions": true,
	"pause-at": true, "resume": true, "step-over": true, "generate-locator": true, "highlight": true,
}

// managedOnly lists commands that need the profile's resident controller.
var managedOnly = map[string]bool{
	"request": true, "request-headers": true, "request-body": true, "response-headers": true, "response-body": true,
	"route": true, "route-list": true, "unroute": true, "recording-start": true, "recording-stop": true,
	"tracing-start": true, "tracing-stop": true, "video-start": true, "video-stop": true, "video-chapter": true,
	"pause-at": true, "resume": true, "step-over": true,
}

func absPath(name string) (string, error) {
	if name == "" {
		return "", errors.New("file name must not be empty")
	}
	return filepath.Abs(name)
}
func defaultName(prefix, ext string) string {
	return fmt.Sprintf("%s-%s%s", prefix, time.Now().Format("20060102-150405"), ext)
}

// pageCommand executes the extended page commands. It returns the result to emit.
func pageCommand(ctx context.Context, command string, a []string, p *browser.Page, store *session.Store, state *session.State, refs *browser.Refs) (any, error) {
	ok := map[string]bool{"ok": true}
	tab := "?tab=" + url.QueryEscape(p.TargetID)
	need := func(n int, usage string) error {
		if len(a) != n {
			return errors.New("usage: wok " + usage)
		}
		return nil
	}
	switch command {
	case "dblclick":
		if len(a) < 1 || len(a) > 2 {
			return nil, errors.New("usage: wok dblclick TARGET [left|right|middle]")
		}
		button := ""
		if len(a) == 2 {
			button = a[1]
		}
		return ok, p.ClickButton(ctx, a[0], refs, button, 2)
	case "hover":
		if err := need(1, "hover TARGET"); err != nil {
			return nil, err
		}
		return ok, p.Hover(ctx, a[0], refs)
	case "drag":
		if err := need(2, "drag FROM TO"); err != nil {
			return nil, err
		}
		return ok, p.Drag(ctx, a[0], a[1], refs)
	case "drop":
		if len(a) < 2 {
			return nil, errors.New("usage: wok drop TARGET FILE... | wok drop TARGET --text TEXT")
		}
		if a[1] == "--text" {
			if len(a) != 3 {
				return nil, errors.New("usage: wok drop TARGET --text TEXT")
			}
			return ok, p.Drop(ctx, a[0], nil, a[2], refs)
		}
		return ok, p.Drop(ctx, a[0], a[1:], "", refs)
	case "select":
		if len(a) < 2 {
			return nil, errors.New("usage: wok select TARGET VALUE...")
		}
		return p.Select(ctx, a[0], a[1:], refs)
	case "check", "uncheck":
		if err := need(1, command+" TARGET"); err != nil {
			return nil, err
		}
		return ok, p.SetChecked(ctx, a[0], command == "check", refs)
	case "go-back", "go-forward", "reload":
		if err := need(0, command); err != nil {
			return nil, err
		}
		var err error
		switch command {
		case "go-back":
			err = p.History(ctx, -1)
		case "go-forward":
			err = p.History(ctx, 1)
		default:
			err = p.Reload(ctx)
		}
		if err != nil {
			return nil, err
		}
		return p.Get(ctx, "url", "", "", refs)
	case "keydown", "keyup":
		if err := need(1, command+" KEY"); err != nil {
			return nil, err
		}
		return ok, p.Key(ctx, map[string]string{"keydown": "keyDown", "keyup": "keyUp"}[command], a[0])
	case "mousemove", "mousedown", "mouseup", "mousewheel":
		return mouse(ctx, command, a, p, store, state)
	case "pdf":
		if len(a) > 1 {
			return nil, errors.New("usage: wok pdf [FILE]")
		}
		name := defaultName("page", ".pdf")
		if len(a) == 1 {
			name = a[0]
		}
		data, err := p.PDF(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"path": name, "bytes": len(data)}, os.WriteFile(name, data, 0600)
	case "state-save":
		if len(a) > 1 {
			return nil, errors.New("usage: wok state-save [FILE]")
		}
		name := "storage-state.json"
		if len(a) == 1 {
			name = a[0]
		}
		pages, closeAll, err := allPages(ctx, p)
		if err != nil {
			return nil, err
		}
		defer closeAll()
		st, err := browser.SaveState(ctx, pages)
		if err != nil {
			return nil, err
		}
		data, _ := json.MarshalIndent(st, "", "  ")
		if err = os.WriteFile(name, append(data, '\n'), 0600); err != nil {
			return nil, err
		}
		return map[string]any{"path": name, "cookies": len(st.Cookies), "origins": len(st.Origins)}, nil
	case "state-load":
		if err := need(1, "state-load FILE"); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(a[0])
		if err != nil {
			return nil, err
		}
		var st browser.StorageState
		if err = json.Unmarshal(data, &st); err != nil {
			return nil, fmt.Errorf("storage state %s: %w", a[0], err)
		}
		if err = p.LoadState(ctx, &st); err != nil {
			return nil, err
		}
		return map[string]any{"cookies": len(st.Cookies), "origins": len(st.Origins)}, nil
	case "cookie-get":
		if err := need(1, "cookie-get NAME"); err != nil {
			return nil, err
		}
		list, err := p.Cookies(ctx, "list", nil)
		if err != nil {
			return nil, err
		}
		for _, c := range list.([]map[string]any) {
			if c["name"] == a[0] {
				return c, nil
			}
		}
		return nil, fmt.Errorf("cookie %q not found", a[0])
	case "request", "request-headers", "request-body", "response-headers", "response-body":
		if err := need(1, command+" INDEX"); err != nil {
			return nil, err
		}
		path := "/runtime/request" + tab + "&index=" + url.QueryEscape(a[0])
		if command != "request" {
			path += "&part=" + command
		}
		value, err := control(ctx, store, state, "GET", path, nil)
		if err != nil || command != "response-body" {
			return value, err
		}
		return responseBody(value, a[0])
	case "route":
		return route(ctx, a, store, state)
	case "route-list":
		if err := need(0, "route-list"); err != nil {
			return nil, err
		}
		return control(ctx, store, state, "GET", "/runtime/routes", nil)
	case "unroute":
		if len(a) > 1 {
			return nil, errors.New("usage: wok unroute [PATTERN]")
		}
		path := "/runtime/routes"
		if len(a) == 1 {
			path += "?pattern=" + url.QueryEscape(a[0])
		}
		return control(ctx, store, state, "DELETE", path, nil)
	case "recording-start", "recording-stop":
		if err := need(0, command); err != nil {
			return nil, err
		}
		return control(ctx, store, state, "POST", "/runtime/recording", map[string]string{"action": strings.TrimPrefix(command, "recording-")})
	case "tracing-start":
		if len(a) > 1 {
			return nil, errors.New("usage: wok tracing-start [CATEGORIES]")
		}
		body := map[string]string{"action": "start"}
		if len(a) == 1 {
			body["categories"] = a[0]
		}
		return control(ctx, store, state, "POST", "/runtime/tracing", body)
	case "tracing-stop":
		if len(a) > 1 {
			return nil, errors.New("usage: wok tracing-stop [FILE]")
		}
		name := defaultName("trace", ".json")
		if len(a) == 1 {
			name = a[0]
		}
		path, err := absPath(name)
		if err != nil {
			return nil, err
		}
		return control(ctx, store, state, "POST", "/runtime/tracing", map[string]string{"action": "stop", "path": path})
	case "video-start":
		if len(a) > 1 {
			return nil, errors.New("usage: wok video-start [FILE.avi|FILE.mp4|FILE.webm]")
		}
		name := defaultName("video", ".avi")
		if len(a) == 1 {
			name = a[0]
		}
		path, err := absPath(name)
		if err != nil {
			return nil, err
		}
		return control(ctx, store, state, "POST", "/runtime/video"+tab, map[string]string{"action": "start", "path": path})
	case "video-stop":
		if err := need(0, "video-stop"); err != nil {
			return nil, err
		}
		return control(ctx, store, state, "POST", "/runtime/video", map[string]string{"action": "stop"})
	case "video-chapter":
		if err := need(1, "video-chapter TITLE"); err != nil {
			return nil, err
		}
		return control(ctx, store, state, "POST", "/runtime/video", map[string]string{"action": "chapter", "title": a[0]})
	case "video-show-actions", "video-hide-actions":
		if err := need(0, command); err != nil {
			return nil, err
		}
		flag := filepath.Join(store.Dir, "show-actions")
		if command == "video-show-actions" {
			return map[string]bool{"showActions": true}, os.WriteFile(flag, nil, 0600)
		}
		if err := os.Remove(flag); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		return map[string]bool{"showActions": false}, nil
	case "pause-at":
		if err := need(1, "pause-at SCRIPT:LINE[:COLUMN]|next|--clear"); err != nil {
			return nil, err
		}
		body := map[string]string{"action": "pause-at", "location": a[0]}
		if a[0] == "--clear" {
			body = map[string]string{"action": "clear"}
		}
		return control(ctx, store, state, "POST", "/runtime/debugger"+tab, body)
	case "resume", "step-over":
		if err := need(0, command); err != nil {
			return nil, err
		}
		return control(ctx, store, state, "POST", "/runtime/debugger"+tab, map[string]string{"action": command})
	case "generate-locator":
		if err := need(1, "generate-locator TARGET"); err != nil {
			return nil, err
		}
		return p.Locator(ctx, a[0], refs)
	case "highlight":
		if len(a) == 1 && a[0] == "--hide" || len(a) == 0 {
			if len(a) == 0 {
				return nil, errors.New("usage: wok highlight TARGET [--hide] | wok highlight --hide")
			}
			return ok, p.HideHighlights(ctx)
		}
		if len(a) == 2 && a[1] == "--hide" {
			_, err := p.EvalOn(ctx, a[0], `el=>{const r=el.getBoundingClientRect();document.querySelectorAll('[data-wok-highlight]').forEach(d=>{if(Math.abs(parseFloat(d.style.left)-(r.left+scrollX-2))<1&&Math.abs(parseFloat(d.style.top)-(r.top+scrollY-2))<1)d.remove()});return true}`, refs)
			return ok, err
		}
		if err := need(1, "highlight TARGET [--hide]"); err != nil {
			return nil, err
		}
		return ok, p.Highlight(ctx, a[0], refs)
	}
	return nil, fmt.Errorf("unknown command %q; run wok help", command)
}

// responseBody prints textual bodies inline and saves binary ones to a file.
func responseBody(value any, index string) (any, error) {
	m, _ := value.(map[string]any)
	if m == nil {
		return value, nil
	}
	if e, _ := m["responseBodyError"].(string); e != "" {
		return nil, errors.New("response body unavailable: " + e)
	}
	body, _ := m["responseBody"].(string)
	if encoded, _ := m["base64Encoded"].(bool); !encoded {
		return body, nil
	}
	data, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return nil, err
	}
	name := "response-" + index + ".bin"
	if mime, _ := m["mimeType"].(string); mime != "" {
		if _, sub, ok := strings.Cut(mime, "/"); ok && sub != "" && !strings.ContainsAny(sub, `/\.`) {
			name = "response-" + index + "." + strings.TrimPrefix(strings.Split(sub, "+")[0], "x-")
		}
	}
	return map[string]any{"path": name, "bytes": len(data), "mimeType": m["mimeType"]}, os.WriteFile(name, data, 0600)
}

func route(ctx context.Context, a []string, store *session.Store, state *session.State) (any, error) {
	if len(a) < 1 {
		return nil, errors.New("usage: wok route PATTERN [--status N] [--body TEXT|--body-file FILE] [--content-type TYPE] [--header K:V] [--abort]")
	}
	r := map[string]any{"pattern": a[0]}
	headers := map[string]string{}
	for i := 1; i < len(a); i++ {
		if a[i] == "--abort" {
			r["abort"] = true
			continue
		}
		if i+1 >= len(a) {
			return nil, fmt.Errorf("%s requires a value", a[i])
		}
		value := a[i+1]
		switch a[i] {
		case "--status":
			n, err := strconv.Atoi(value)
			if err != nil || n < 100 || n > 599 {
				return nil, errors.New("--status must be 100..599")
			}
			r["status"] = n
		case "--body":
			r["body"] = value
		case "--body-file":
			data, err := os.ReadFile(value)
			if err != nil {
				return nil, err
			}
			r["body"] = string(data)
		case "--content-type":
			r["contentType"] = value
		case "--header":
			k, v, ok := strings.Cut(value, ":")
			if !ok || strings.TrimSpace(k) == "" {
				return nil, errors.New("--header must be NAME:VALUE")
			}
			headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
		default:
			return nil, fmt.Errorf("unknown route option %q", a[i])
		}
		i++
	}
	if len(headers) > 0 {
		r["headers"] = headers
	}
	return control(ctx, store, state, "POST", "/runtime/routes", r)
}

// mouse keeps pointer position and pressed buttons in profile state.
func mouse(ctx context.Context, command string, a []string, p *browser.Page, store *session.Store, state *session.State) (any, error) {
	at := browser.Point{}
	if len(state.Mouse) == 2 {
		at = browser.Point{X: state.Mouse[0], Y: state.Mouse[1]}
	}
	nums := func() (float64, float64, error) {
		if len(a) != 2 {
			return 0, 0, fmt.Errorf("usage: wok %s X Y", command)
		}
		x, e1 := strconv.ParseFloat(a[0], 64)
		y, e2 := strconv.ParseFloat(a[1], 64)
		if e1 != nil || e2 != nil {
			return 0, 0, errors.New("coordinates must be numbers")
		}
		return x, y, nil
	}
	var err error
	switch command {
	case "mousemove":
		var x, y float64
		if x, y, err = nums(); err != nil {
			return nil, err
		}
		at = browser.Point{X: x, Y: y}
		err = p.Mouse(ctx, "mouseMoved", at, "", state.MouseButtons, 0)
	case "mousewheel":
		var dx, dy float64
		if dx, dy, err = nums(); err != nil {
			return nil, err
		}
		err = p.Wheel(ctx, at, dx, dy)
	default:
		if len(a) > 1 {
			return nil, fmt.Errorf("usage: wok %s [left|right|middle]", command)
		}
		button := ""
		if len(a) == 1 {
			button = a[0]
		}
		name, mask, e := browser.MouseButton(button)
		if e != nil {
			return nil, e
		}
		if command == "mousedown" {
			state.MouseButtons |= mask
			err = p.Mouse(ctx, "mousePressed", at, name, state.MouseButtons, 1)
		} else {
			state.MouseButtons &^= mask
			err = p.Mouse(ctx, "mouseReleased", at, name, state.MouseButtons, 1)
		}
	}
	if err != nil {
		return nil, err
	}
	state.Mouse = []float64{at.X, at.Y}
	return map[string]any{"x": at.X, "y": at.Y, "buttons": state.MouseButtons}, store.Save(state)
}

// allPages attaches to every page target, starting with the current page.
func allPages(ctx context.Context, current *browser.Page) ([]*browser.Page, func(), error) {
	pages := []*browser.Page{current}
	closeAll := func() {
		for _, p := range pages[1:] {
			p.Close()
		}
	}
	targets, err := browser.Targets(ctx, current.Client)
	if err != nil {
		return nil, closeAll, err
	}
	for _, t := range targets {
		if t.Type != "page" || t.TargetID == current.TargetID {
			continue
		}
		p, err := browser.Attach(ctx, current.Client, t.TargetID)
		if err == nil {
			pages = append(pages, p)
		}
	}
	return pages, closeAll, nil
}

// annotate shows a callout naming the action, and outlines its target, when
// video-show-actions is on. Failures never block the action itself.
func annotate(ctx context.Context, p *browser.Page, store *session.Store, command string, a []string, refs *browser.Refs) {
	if _, err := os.Stat(filepath.Join(store.Dir, "show-actions")); err != nil {
		return
	}
	label := strings.TrimSpace(command + " " + strings.Join(a, " "))
	if len(label) > 80 {
		label = label[:77] + "..."
	}
	callout := `const c=document.createElement('div');c.setAttribute('data-wok-action','');c.textContent=` + strconv.Quote(label) + `;Object.assign(c.style,{position:'fixed',right:'16px',bottom:'16px',padding:'8px 12px',font:'500 14px system-ui,sans-serif',color:'#fff',background:'rgba(42,120,214,.92)',borderRadius:'6px',zIndex:2147483647,pointerEvents:'none'});document.documentElement.appendChild(c);setTimeout(()=>c.remove(),1200);`
	if len(a) > 0 && targetCommands[command] {
		if _, err := p.EvalOn(ctx, a[0], `el=>{`+callout+`const r=el.getBoundingClientRect();const d=document.createElement('div');Object.assign(d.style,{position:'fixed',left:(r.left-3)+'px',top:(r.top-3)+'px',width:(r.width+6)+'px',height:(r.height+6)+'px',border:'2px solid #eb6834',borderRadius:'4px',zIndex:2147483647,pointerEvents:'none'});document.documentElement.appendChild(d);setTimeout(()=>d.remove(),1200);return true}`, refs); err == nil {
			time.Sleep(300 * time.Millisecond)
			return
		}
	}
	if _, err := p.Eval(ctx, `(()=>{`+callout+`})()`); err == nil {
		time.Sleep(300 * time.Millisecond)
	}
}

var targetCommands = map[string]bool{"click": true, "dblclick": true, "fill": true, "hover": true, "select": true, "check": true, "uncheck": true, "drag": true, "drop": true, "upload": true}
var annotatedCommands = map[string]bool{"click": true, "dblclick": true, "fill": true, "type": true, "press": true, "hover": true, "select": true, "check": true, "uncheck": true, "drag": true, "drop": true, "upload": true, "open": true, "go-back": true, "go-forward": true, "reload": true}

// tabByIndex resolves a zero-based page index; target IDs pass through.
func tabByIndex(ctx context.Context, c *cdp.Client, id string) (string, error) {
	n, err := strconv.Atoi(id)
	if err != nil || len(id) > 4 {
		return id, nil
	}
	targets, err := browser.Targets(ctx, c)
	if err != nil {
		return "", err
	}
	pages := []browser.Target{}
	for _, t := range targets {
		if t.Type == "page" {
			pages = append(pages, t)
		}
	}
	if n < 0 || n >= len(pages) {
		return "", fmt.Errorf("tab index %d out of range 0..%d", n, len(pages)-1)
	}
	return pages[n].TargetID, nil
}

// consoleFilter keeps messages at or above a minimum level.
func consoleFilter(value any, level string) (any, error) {
	rank := map[string]int{"debug": 0, "verbose": 0, "log": 1, "info": 1, "warning": 2, "warn": 2, "error": 3}
	min, ok := rank[level]
	if !ok {
		return nil, errors.New("console level must be debug, info, warning or error")
	}
	items, _ := value.([]any)
	out := []any{}
	for _, item := range items {
		m, _ := item.(map[string]any)
		typ, _ := m["type"].(string)
		r, known := rank[typ]
		if typ == "assert" {
			r, known = 3, true
		}
		if !known {
			r = 1
		}
		if r >= min {
			out = append(out, item)
		}
	}
	return out, nil
}

// closeAll stops every running managed profile; kill forcefully terminates
// browser processes that no longer answer CDP.
func closeAll(ctx context.Context, out io.Writer, kill bool) error {
	base, err := session.Open("", "default")
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Dir(base.Dir))
	if err != nil {
		return err
	}
	results := []map[string]any{}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), "external-") {
			continue
		}
		store, err := session.Open("", entry.Name())
		if err != nil {
			continue
		}
		st, err := store.Load()
		if err != nil || (st.Endpoint == "" && st.PID == 0) {
			continue
		}
		result := map[string]any{"profile": entry.Name()}
		if kill {
			result["killed"] = killProfile(store, st)
		} else {
			probe, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
			healthy := session.Healthy(probe, st.Endpoint) == nil
			cancel()
			if !healthy {
				continue
			}
			unlock, err := store.Lock(ctx)
			if err != nil {
				return err
			}
			err = store.Stop(ctx)
			unlock()
			result["stopped"] = err == nil
			if err != nil {
				result["error"] = err.Error()
			}
		}
		results = append(results, result)
	}
	return emit(out, results)
}

func killProfile(store *session.Store, st *session.State) bool {
	killed := false
	if st.PID > 0 && ownsProcess(st.PID, store.Dir) {
		if proc, err := os.FindProcess(st.PID); err == nil && proc.Kill() == nil {
			killed = true
		}
	}
	st.Endpoint, st.PID, st.TargetID, st.RenderURL, st.Token = "", 0, "", "", ""
	_ = store.Save(st)
	_ = os.Remove(filepath.Join(store.Dir, "render-ready.json"))
	return killed
}

// ownsProcess guards against PID reuse: on Linux the process command line
// must reference this profile; elsewhere the browser endpoint must answer.
func ownsProcess(pid int, dir string) bool {
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		return err == nil && bytes.Contains(data, []byte(filepath.Join(dir, "browser")))
	}
	return true
}

// deleteData stops the profile and removes its browser data and Wok state.
func deleteData(ctx context.Context, store *session.Store, out io.Writer) error {
	if st, err := store.Load(); err == nil && st.Endpoint != "" {
		probe, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		healthy := session.Healthy(probe, st.Endpoint) == nil
		cancel()
		if healthy {
			if err = store.Stop(ctx); err != nil {
				return err
			}
		}
	}
	for _, name := range []string{"browser", "state.json", "routes.json", "dialog-policy.json", "show-actions", "render-ready.json"} {
		target := filepath.Join(store.Dir, name)
		var err error
		for deadline := time.Now().Add(3 * time.Second); ; {
			if err = os.RemoveAll(target); err == nil || !time.Now().Before(deadline) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if err != nil {
			return err
		}
	}
	return emit(out, map[string]any{"profile": store.Name, "deleted": true})
}

// attachFile records an external CDP endpoint used when --cdp is omitted.
func attachFile(profile string) (string, error) {
	store, err := session.Open("", profile)
	if err != nil {
		return "", err
	}
	return filepath.Join(store.Dir, "attach"), nil
}

func resolveAttachTarget(ctx context.Context, target string) (string, error) {
	if _, err := strconv.Atoi(target); err == nil {
		return session.Resolve(ctx, "http://127.0.0.1:"+target)
	}
	if !strings.Contains(target, "://") && !strings.Contains(target, ":") {
		if other, err := session.Open("", target); err == nil {
			if st, err := other.Load(); err == nil && st.Endpoint != "" {
				if session.Healthy(ctx, st.Endpoint) == nil {
					return st.Endpoint, nil
				}
			}
		}
	}
	return session.Resolve(ctx, target)
}

func isSelectorTarget(arg string, refs *browser.Refs) bool {
	if strings.HasSuffix(strings.ToLower(arg), ".png") || strings.ContainsAny(arg, `/\`) {
		return false
	}
	if strings.HasPrefix(arg, "@e") || strings.HasPrefix(arg, "#") || strings.HasPrefix(arg, "[") {
		return true
	}
	if refs != nil {
		if _, ok := refs.Items[arg]; ok {
			return true
		}
	}
	return filepath.Ext(arg) == ""
}

// rawWriter unwraps JSON-encoded strings when --raw is enabled.
type rawWriter struct {
	w io.Writer
}

func (r *rawWriter) Write(p []byte) (int, error) {
	var s string
	if json.Unmarshal(bytes.TrimSpace(p), &s) == nil {
		if !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		_, err := io.WriteString(r.w, s)
		return len(p), err
	}
	return r.w.Write(p)
}

var helpSummaries = map[string]string{
	"open":                  "wok open [URL] — Start browser or navigate the active tab to URL",
	"goto":                  "wok goto URL — Navigate the active tab to URL",
	"start":                 "wok start — Start or reuse the profile browser",
	"stop":                  "wok stop — Stop the profile browser and retain profile storage",
	"close":                 "wok close — Stop the profile browser",
	"status":                "wok status — Show profile browser identity, active tab and viewer URL",
	"attach":                "wok attach [NAME|CDP_URL] — Attach profile commands to another profile or CDP endpoint",
	"detach":                "wok detach — Detach from an external CDP endpoint",
	"delete-data":           "wok delete-data — Stop the profile and remove its browser storage and state",
	"type":                  "wok type TEXT — Insert text into the focused element",
	"click":                 "wok click TARGET [left|right|middle] — Click an element",
	"dblclick":              "wok dblclick TARGET [left|right|middle] — Double-click an element",
	"fill":                  "wok fill TARGET TEXT — Replace editable element contents",
	"drag":                  "wok drag START END — Drag from one element to another",
	"drop":                  "wok drop TARGET FILE... | wok drop TARGET --text TEXT — Drop files or text onto an element",
	"hover":                 "wok hover TARGET — Move pointer over an element",
	"select":                "wok select TARGET VALUE... — Select <select> option(s) by value or label",
	"upload":                "wok upload TARGET FILE... — Set files on an <input type=file>",
	"check":                 "wok check TARGET — Check a checkbox or radio button",
	"uncheck":               "wok uncheck TARGET — Uncheck a checkbox",
	"snapshot":              "wok snapshot [-i] [TARGET] — Accessibility tree with persistent @eN refs",
	"find":                  "wok find [role|text|css] VALUE [--nth N] — Find matching elements",
	"eval":                  "wok eval FUNC_OR_EXPR [TARGET] — Evaluate JavaScript on the page or an element",
	"get":                   "wok get text|title|url [SELECTOR] | wok get attr SELECTOR NAME — Read page or element data",
	"wait":                  "wok wait [SELECTOR] [--state S] | --text T | --url U | --fn JS | --load | --network-idle | --download",
	"dialog":                "wok dialog [accept|dismiss [PROMPT_TEXT]] — Read or set policy for future dialogs",
	"dialog-accept":         "wok dialog-accept [PROMPT_TEXT] — Accept future JavaScript dialogs",
	"dialog-dismiss":        "wok dialog-dismiss — Dismiss future JavaScript dialogs",
	"viewport":              "wok viewport [W H] [--mobile] [--dpr N] | reset — Set or reset viewport emulation",
	"resize":                "wok resize W H — Resize viewport",
	"set":                   "wok set viewport|device|media|offline [ARGS] — Persistent environment overrides",
	"go-back":               "wok go-back — Navigate back in session history",
	"go-forward":            "wok go-forward — Navigate forward in session history",
	"reload":                "wok reload — Reload the active page",
	"press":                 "wok press KEY — Press a key or shortcut (e.g. Enter, Control+a)",
	"keydown":               "wok keydown KEY — Dispatch a keyDown event",
	"keyup":                 "wok keyup KEY — Dispatch a keyUp event",
	"mousemove":             "wok mousemove X Y — Move pointer to viewport coordinates",
	"mousedown":             "wok mousedown [left|right|middle] — Press mouse button at current pointer position",
	"mouseup":               "wok mouseup [left|right|middle] — Release mouse button at current pointer position",
	"mousewheel":            "wok mousewheel DX DY — Scroll wheel by deltaX, deltaY",
	"screenshot":            "wok screenshot [FILE] [TARGET] — Save viewport or element PNG screenshot",
	"pdf":                   "wok pdf [FILE] — Save page as PDF",
	"tab":                   "wok tab list|new [URL]|select ID|close [ID] — Manage browser tabs",
	"tab-list":              "wok tab-list — List open browser tabs",
	"tab-new":               "wok tab-new [URL] — Open and select a new tab",
	"tab-close":             "wok tab-close [INDEX|ID] — Close a tab by index or target ID",
	"tab-select":            "wok tab-select INDEX|ID — Select a tab by zero-based index or target ID",
	"state-save":            "wok state-save [FILE] — Save cookies and localStorage to JSON",
	"state-load":            "wok state-load FILE — Load cookies and localStorage from JSON",
	"cookies":               "wok cookies [list|set|delete|clear] [ARGS] — Manage browser cookies",
	"cookie-list":           "wok cookie-list — List cookies",
	"cookie-get":            "wok cookie-get NAME — Read a cookie by name",
	"cookie-set":            "wok cookie-set NAME VALUE [options] — Set a cookie",
	"cookie-delete":         "wok cookie-delete NAME [--domain HOST] [--path PATH] — Delete a cookie",
	"cookie-clear":          "wok cookie-clear — Clear all cookies",
	"storage":               "wok storage local|session [list|get|set|delete|clear] [KEY] [VALUE] — Manage web storage",
	"localstorage-list":     "wok localstorage-list — List localStorage entries",
	"localstorage-get":      "wok localstorage-get KEY — Read a localStorage key",
	"localstorage-set":      "wok localstorage-set KEY VALUE — Set a localStorage key",
	"localstorage-delete":   "wok localstorage-delete KEY — Delete a localStorage key",
	"localstorage-clear":    "wok localstorage-clear — Clear localStorage",
	"sessionstorage-list":   "wok sessionstorage-list — List sessionStorage entries",
	"sessionstorage-get":    "wok sessionstorage-get KEY — Read a sessionStorage key",
	"sessionstorage-set":    "wok sessionstorage-set KEY VALUE — Set a sessionStorage key",
	"sessionstorage-delete": "wok sessionstorage-delete KEY — Delete a sessionStorage key",
	"sessionstorage-clear":  "wok sessionstorage-clear — Clear sessionStorage",
	"network":               "wok network — List recorded network requests",
	"requests":              "wok requests — List recorded network requests with 1-based indices",
	"request":               "wok request INDEX — Inspect a recorded request and response",
	"request-headers":       "wok request-headers INDEX — Read request headers for a recorded request",
	"request-body":          "wok request-body INDEX — Read request body for a recorded request",
	"response-headers":      "wok response-headers INDEX — Read response headers for a recorded request",
	"response-body":         "wok response-body INDEX — Read or save response body for a recorded request",
	"route":                 "wok route PATTERN [--status N] [--body TEXT|--body-file FILE] [--content-type TYPE] [--header K:V] [--abort]",
	"route-list":            "wok route-list — List active network routes",
	"unroute":               "wok unroute [PATTERN] — Remove a matching route or all routes",
	"network-state-set":     "wok network-state-set online|offline — Toggle offline network emulation",
	"console":               "wok console [debug|info|warning|error] — Read recorded console messages",
	"errors":                "wok errors — Read recorded page errors",
	"downloads":             "wok downloads — Read recorded browser downloads",
	"dialogs":               "wok dialogs — Read recorded JavaScript dialogs",
	"run-code":              "wok run-code [CODE] — Run newline-separated Wok command lines",
	"exec":                  "wok exec FILE|- — Run newline-separated Wok command lines from a file or stdin",
	"run":                   "wok run FILE|- [--var K=V] — Run a YAML/JSON action flow",
	"recording-start":       "wok recording-start — Start recording user interactions in the page",
	"recording-stop":        "wok recording-stop — Stop recording and emit captured actions and script",
	"tracing-start":         "wok tracing-start [CATEGORIES] — Start Chromium trace recording",
	"tracing-stop":          "wok tracing-stop [FILE] — Stop trace recording and write trace JSON",
	"video-start":           "wok video-start [FILE] — Start screencast video recording (.avi built-in; .mp4/.webm via ffmpeg)",
	"video-stop":            "wok video-stop — Stop video recording and write output file",
	"video-chapter":         "wok video-chapter TITLE — Add a chapter marker and title overlay to the active video",
	"video-show-actions":    "wok video-show-actions — Show visual callouts and target outlines during actions",
	"video-hide-actions":    "wok video-hide-actions — Hide visual action callouts",
	"render":                "wok render — Start or reuse the live HTTP viewer",
	"show":                  "wok show — Start or reuse the live HTTP viewer",
	"terminal":              "wok terminal [--fps N] — Interactive ANSI terminal browser viewer",
	"pause-at":              "wok pause-at SCRIPT:LINE[:COLUMN]|next|--clear — Set a JS debugger breakpoint or pause",
	"resume":                "wok resume — Resume JavaScript execution",
	"step-over":             "wok step-over — Step over the current JavaScript statement",
	"generate-locator":      "wok generate-locator TARGET — Generate a stable CSS selector and accessible locator",
	"highlight":             "wok highlight TARGET [--hide] | wok highlight --hide — Highlight an element on the page",
	"install":               "wok install [skills] [options] — Install bundled Wok agent skill",
	"skills":                "wok skills list|show|install [options] — Manage bundled Wok agent skill",
	"browser":               "wok browser install — Install cached Chromium headless shell",
	"install-browser":       "wok install-browser [chromium] — Install cached Chromium headless shell",
	"profile":               "wok profile list — List known profiles and running status",
	"list":                  "wok list — List known profiles and running status",
	"close-all":             "wok close-all — Gracefully stop all running managed profiles",
	"kill-all":              "wok kill-all — Forcefully terminate all managed browser processes",
	"cdp":                   "wok cdp METHOD [JSON] [--browser-level] — Send raw CDP command",
}

func commandHelp(out io.Writer, name string) error {
	name = strings.TrimPrefix(name, "--")
	if summary, ok := helpSummaries[name]; ok {
		_, err := fmt.Fprintln(out, summary)
		return err
	}
	return fmt.Errorf("unknown command %q; run wok help", name)
}
