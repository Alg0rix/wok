// Package cli implements Wok's agent-facing command line.
package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alg0rix/wok/internal/browser"
	"github.com/alg0rix/wok/internal/cdp"
	"github.com/alg0rix/wok/internal/provision"
	"github.com/alg0rix/wok/internal/session"
	"github.com/alg0rix/wok/internal/version"
	"github.com/alg0rix/wok/skills"
)

const Help = `Wok — persistent browser CLI for agents

Usage: wok [options] COMMAND [arguments]

Lifecycle:
  start                    Start once; install browser automatically if missing
  browser install          Install/reuse cached Chromium headless shell
  stop                     Stop this profile; retain cookies and storage
  status                   Show browser identity, tab and /render URL
  profile list             List known profiles and their running status
  render                   Start/reuse this profile's live /render viewer
  skills list|show|install  Read/install bundled AI agent skill (offline)
  install skills [OPTIONS] Alias for skills install
  run FILE|- [--var K=V]   Run YAML/JSON actions with optional assertions
  exec FILE|-              Batch plain CLI command lines (no shell)
  terminal [--fps N]        Live ANSI viewer with mouse/keyboard input

Browser:
  open URL                 Navigate (missing scheme defaults to HTTPS)
  snapshot [-i]            Accessibility tree with persistent @eN refs
  click SELECTOR|@eN       Real pointer click; reject covered elements
  fill SELECTOR|@eN TEXT   Replace editable contents
  type TEXT                Insert text at focused element
  press KEY                Key such as Enter, Tab, Control+a, Meta+a
  get text|title|url        Read page data
  get attr SELECTOR NAME   Read element attribute
  get text SELECTOR        Read element text
  eval JAVASCRIPT          Evaluate and return JSON; promises supported
  find role|text|css VALUE [--nth N]
  cookies [list|set|delete|clear] [ARGS]
  storage local|session [list|get|set|delete|clear] [ARGS]
  upload TARGET FILE...    Fill a file input
  console|errors|network   Read recorded page events
  downloads|dialogs        Read browser downloads/dialog history
  dialog [accept|dismiss [TEXT]]  Policy for future confirm/prompt dialogs
  wait [SELECTOR] [--state attached|detached|visible|hidden]
  wait --text TEXT|--url PART|--fn JS|--load|--network-idle|--download
  screenshot FILE          Write viewport PNG
  viewport [W H] [--mobile] [--dpr N]  Persistent size/mobile emulation
  viewport reset           Restore desktop defaults
  set device [NAME]        List/apply device viewport presets
  set media dark|light|no-preference
  set offline on|off       Persistent offline emulation
  set viewport W H [DPR]   Viewport shorthand
  tab list|new [URL]|select ID|close [ID]
  cdp METHOD [JSON]         Raw page CDP; --browser-level for browser CDP

Options (before or after command; -- ends option parsing):
  --profile NAME           Isolated persistent profile (default: default)
  --cdp URL                Attach external HTTP or WebSocket CDP browser
  --tab ID                 Select a target for this invocation
  --timeout DURATION       Total timeout (15s; start/browser install/run: 5m)
  --browser PATH           Browser executable (start only; overrides auto-install)
  --headed                 Show browser window (start only)
  --no-sandbox             Explicitly disable browser sandbox (start only)
  --no-render              Start without controller (disables prior-event recording)
  --listen HOST:PORT       Controller HTTP/CDP/MCP address (default loopback:auto)
  --port N                 Shorthand for --listen 127.0.0.1:N
  --auth-token TOKEN|auto   Require bearer/query token on remote APIs
  --embed-origin ORIGIN    Allow embedding/API Origin (repeatable)
  --download-dir DIR       Browser download directory
  --proxy URL              HTTP(S)/SOCKS proxy; HTTP user:pass supported
  --proxy-bypass PATTERNS   Comma-separated Chromium bypass patterns
  --json                   JSON snapshot (other results are always JSON)
  --close-external         Permit stop against an explicitly supplied --cdp
  --browser-level          Send raw CDP to browser instead of active page
  --help, --version

Environment: WOK_HOME overrides private state root; WOK_BROWSER overrides browser.
WOK_BROWSER_CACHE overrides the shared browser download cache.
Prefers chrome-headless-shell, then Chromium/Chrome. Linux and macOS supported.
Every action detaches on exit and leaves the browser running.
`

type Options struct {
	RenderListen, AuthToken, DownloadDir, Proxy, ProxyBypass       string
	EmbedOrigins                                                   []string
	ProfileSet                                                     bool
	TimeoutSet                                                     bool
	Profile, Endpoint, Tab, Browser                                string
	Timeout                                                        time.Duration
	Headed, NoSandbox, NoRender, JSON, CloseExternal, BrowserLevel bool
}

func Parse(args []string) (Options, []string, error) {
	o := Options{Profile: "default", Timeout: 15 * time.Second}
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		key, value, has := strings.Cut(a, "=")
		switch key {
		case "--profile", "--cdp", "--tab", "--timeout", "--browser", "--listen", "--port", "--auth-token", "--embed-origin", "--download-dir", "--proxy", "--proxy-bypass":
			if !has {
				i++
				if i >= len(args) {
					return o, nil, fmt.Errorf("%s requires a value", key)
				}
				value = args[i]
			}
			if value == "" {
				return o, nil, fmt.Errorf("%s requires a nonempty value", key)
			}
			switch key {
			case "--listen":
				o.RenderListen = value
			case "--port":
				n, e := strconv.Atoi(value)
				if e != nil || n < 1 || n > 65535 {
					return o, nil, errors.New("--port must be 1..65535")
				}
				o.RenderListen = "127.0.0.1:" + value
			case "--auth-token":
				o.AuthToken = value
			case "--embed-origin":
				o.EmbedOrigins = append(o.EmbedOrigins, value)
			case "--download-dir":
				o.DownloadDir = value
			case "--proxy":
				o.Proxy = value
			case "--proxy-bypass":
				o.ProxyBypass = value
			case "--profile":
				o.ProfileSet = true
				o.Profile = value
			case "--cdp":
				o.Endpoint = value
			case "--tab":
				o.Tab = value
			case "--browser":
				o.Browser = value
			case "--timeout":
				o.TimeoutSet = true
				d, e := time.ParseDuration(value)
				if e != nil {
					if n, ne := strconv.Atoi(value); ne == nil {
						d = time.Duration(n) * time.Millisecond
						e = nil
					}
				}
				if e != nil || d <= 0 {
					return o, nil, errors.New("--timeout must be a positive duration")
				}
				o.Timeout = d
			}
		case "--headed", "--no-sandbox", "--no-render", "--json", "--close-external", "--browser-level":
			if has {
				return o, nil, fmt.Errorf("%s does not accept a value", key)
			}
			switch key {
			case "--headed":
				o.Headed = true
			case "--no-sandbox":
				o.NoSandbox = true
			case "--no-render":
				o.NoRender = true
			case "--json":
				o.JSON = true
			case "--close-external":
				o.CloseExternal = true
			case "--browser-level":
				o.BrowserLevel = true
			}
		default:
			positional = append(positional, a)
		}
	}
	return o, positional, nil
}
func Run(ctx context.Context, args []string, out, errOut io.Writer) int {
	if err := run(ctx, args, out); err != nil {
		b, _ := json.Marshal(map[string]any{"error": err.Error()})
		fmt.Fprintln(errOut, string(b))
		return 1
	}
	return 0
}
func emit(out io.Writer, value any) error { return json.NewEncoder(out).Encode(value) }
func viewerURL(st *session.State) string {
	u := st.RenderURL + "/render"
	if st.AuthToken != "" {
		u += "?token=" + url.QueryEscape(st.AuthToken)
	}
	return u
}
func public(st *session.State) map[string]any {
	return map[string]any{"profile": st.Profile, "endpoint": st.Endpoint, "pid": st.PID, "browser": st.Executable, "targetId": st.TargetID, "renderUrl": st.RenderURL, "authRequired": st.AuthToken != ""}
}
func run(parent context.Context, args []string, out io.Writer) error {
	o, a, err := Parse(args)
	if err != nil {
		return err
	}
	if len(a) == 0 || a[0] == "help" || a[0] == "--help" || a[0] == "-h" {
		_, err = io.WriteString(out, Help)
		return err
	}
	if a[0] == "--version" || a[0] == "version" {
		return emit(out, map[string]string{"name": "wok", "version": version.Version})
	}
	if !o.TimeoutSet && (a[0] == "start" || a[0] == "browser" || a[0] == "run") {
		o.Timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, o.Timeout)
	if a[0] == "terminal" && !o.TimeoutSet {
		cancel()
		ctx, cancel = context.WithCancel(parent)
	}
	defer cancel()
	command := a[0]
	a = a[1:]
	if command == "exec" {
		return runExec(ctx, a, o, out)
	}
	if command == "run" {
		return runFlow(ctx, a, o, out)
	}
	if command == "browser" {
		if len(a) != 1 || a[0] != "install" {
			return errors.New("usage: wok browser install")
		}
		executable, e := provision.Ensure(ctx)
		if e != nil {
			return e
		}
		return emit(out, map[string]string{"browser": "chrome-headless-shell", "path": executable})
	}
	if command == "install" {
		if len(a) == 0 || a[0] != "skills" {
			return errors.New("usage: wok install skills [options]")
		}
		return skills.Run(append([]string{"install"}, a[1:]...), out)
	}
	switch command {
	case "skills", "profile", "start", "stop", "status", "render", "open", "snapshot", "click", "fill", "type", "press", "get", "eval", "wait", "screenshot", "terminal", "viewport", "set", "find", "cookies", "storage", "upload", "console", "errors", "network", "downloads", "dialogs", "dialog", "tab", "cdp":
	default:
		return fmt.Errorf("unknown command %q; run wok help", command)
	}
	if command == "skills" {
		return skills.Run(a, out)
	}
	if command == "profile" {
		if len(a) != 1 || a[0] != "list" {
			return errors.New("usage: wok profile list")
		}
		return listProfiles(ctx, out)
	}
	external := o.Endpoint != ""
	if external {
		if command == "start" || command == "render" {
			return errors.New("start/render manages local profiles; omit --cdp")
		}
		if o.AuthToken != "" {
			if o.AuthToken == "auto" {
				return errors.New("external CDP requires an explicit auth token")
			}
			u, e := url.Parse(o.Endpoint)
			if e != nil {
				return e
			}
			q := u.Query()
			q.Set("token", o.AuthToken)
			u.RawQuery = q.Encode()
			o.Endpoint = u.String()
		}
		o.Endpoint, err = session.Resolve(ctx, o.Endpoint)
		if err != nil {
			return err
		}
		o.Profile = fmt.Sprintf("external-%x", sha256.Sum256([]byte(o.Endpoint)))[:25]
	}
	store, err := session.Open("", o.Profile)
	if err != nil {
		return err
	}
	unlock, err := store.Lock(ctx)
	if err != nil {
		return err
	}
	unlock = sync.OnceFunc(unlock)
	defer unlock()
	if command == "start" {
		if len(a) != 0 {
			return errors.New("usage: wok start")
		}
		st, e := store.Start(ctx, session.StartOptions{Browser: o.Browser, Headed: o.Headed, NoSandbox: o.NoSandbox, Render: !o.NoRender, RenderListen: o.RenderListen, AuthToken: o.AuthToken, EmbedOrigins: o.EmbedOrigins, DownloadDir: o.DownloadDir, Proxy: o.Proxy, ProxyBypass: o.ProxyBypass})
		if e != nil {
			return e
		}
		return emit(out, public(st))
	}
	var state *session.State
	var client *cdp.Client
	if external {
		state, _ = store.Load()
		if state == nil {
			state = &session.State{Profile: o.Profile, Endpoint: o.Endpoint}
		}
		client, err = cdp.Dial(ctx, o.Endpoint)
	} else {
		state, client, err = store.Connect(ctx)
	}
	if err != nil {
		return err
	}
	defer client.Close()
	switch command {
	case "stop":
		if len(a) != 0 {
			return errors.New("usage: wok stop")
		}
		if external {
			if !o.CloseExternal {
				return errors.New("stopping an external browser requires --close-external")
			}
			err = session.CloseBrowser(ctx, client, state.Endpoint)
		} else {
			err = store.Stop(ctx)
		}
		if err != nil {
			return err
		}
		return emit(out, map[string]any{"profile": o.Profile, "stopped": true})
	case "render":
		if len(a) != 0 {
			return errors.New("usage: wok render")
		}
		if err = store.EnsureRender(ctx, state, ""); err != nil {
			return err
		}
		return emit(out, map[string]string{"url": viewerURL(state)})
	case "set":
		if external {
			return errors.New("persistent environment overrides require a managed profile")
		}
		return set(ctx, store, state, a, out)
	case "viewport":
		if external {
			return errors.New("persistent viewport emulation requires a managed profile; omit --cdp")
		}
		return viewport(ctx, store, state, a, out)
	case "status":
		if len(a) != 0 {
			return errors.New("usage: wok status")
		}
		return emit(out, public(state))
	}
	if command == "cdp" && o.BrowserLevel {
		return rawCDP(ctx, out, client, "", a)
	}
	if command == "tab" {
		if len(a) == 0 {
			return errors.New("usage: wok tab list|new [URL]|select ID|close [ID]")
		}
		switch a[0] {
		case "list":
			if len(a) != 1 {
				return errors.New("usage: wok tab list")
			}
			targets, e := browser.Targets(ctx, client)
			if e != nil {
				return e
			}
			return emit(out, targets)
		case "new":
			if len(a) > 2 {
				return errors.New("usage: wok tab new [URL]")
			}
			var r struct {
				TargetID string `json:"targetId"`
			}
			if err = client.Call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &r); err != nil {
				return err
			}
			state.TargetID = r.TargetID
			if err = store.Save(state); err != nil {
				return err
			}
			if len(a) == 2 {
				p, e := browser.Attach(ctx, client, r.TargetID)
				if e != nil {
					return e
				}
				defer p.Close()
				if err = p.Navigate(ctx, a[1]); err != nil {
					return err
				}
			}
			return emit(out, map[string]string{"targetId": r.TargetID})
		case "select":
			if len(a) != 2 {
				return errors.New("usage: wok tab select ID")
			}
			targets, e := browser.Targets(ctx, client)
			if e != nil {
				return e
			}
			found := false
			for _, t := range targets {
				if t.TargetID == a[1] && t.Type == "page" {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("page target %q not found", a[1])
			}
			state.TargetID = a[1]
			if err = store.Save(state); err != nil {
				return err
			}
			return emit(out, map[string]string{"targetId": a[1]})
		case "close":
			if len(a) > 2 {
				return errors.New("usage: wok tab close [ID]")
			}
			id := state.TargetID
			if o.Tab != "" {
				id = o.Tab
			}
			if len(a) == 2 {
				id = a[1]
			}
			if id == "" {
				return errors.New("specify a tab ID or select a tab first")
			}
			var r struct {
				Success bool `json:"success"`
			}
			if err = client.Call(ctx, "", "Target.closeTarget", map[string]string{"targetId": id}, &r); err != nil {
				return err
			}
			if !r.Success {
				return errors.New("tab could not be closed")
			}
			if state.TargetID == id {
				state.TargetID = ""
			}
			if err = store.Save(state); err != nil {
				return err
			}
			return emit(out, map[string]bool{"closed": true})
		default:
			return errors.New("unknown tab command")
		}
	}
	target := state.TargetID
	if o.Tab != "" {
		target = o.Tab
	}
	p, err := browser.Attach(ctx, client, target)
	// A tab can be closed by the viewer or the page itself. Recover implicit
	// selection only; an explicit --tab must never silently target another page.
	if err != nil && o.Tab == "" && target != "" {
		targets, listErr := browser.Targets(ctx, client)
		if listErr == nil {
			found := false
			for _, t := range targets {
				if t.TargetID == target && t.Type == "page" {
					found = true
					break
				}
			}
			if !found {
				p, err = browser.Attach(ctx, client, "")
			}
		}
	}
	if err != nil {
		return err
	}
	defer p.Close()
	if o.Tab == "" && state.TargetID != p.TargetID {
		state.TargetID = p.TargetID
		if err = store.Save(state); err != nil {
			return err
		}
	}
	refs := browser.Refs{}
	if len(state.Refs) > 0 {
		if err = json.Unmarshal(state.Refs, &refs); err != nil {
			return fmt.Errorf("corrupt refs: %w", err)
		}
	}
	var result any = map[string]bool{"ok": true}
	switch command {
	case "terminal":
		unlock()
		return showTerminal(ctx, p, state, a, out)
	case "open":
		if len(a) != 1 {
			return errors.New("usage: wok open URL")
		}
		err = p.Navigate(ctx, a[0])
		if err == nil {
			result, err = p.Get(ctx, "url", "", "", &refs)
		}
	case "snapshot":
		interactive := false
		if len(a) == 1 && a[0] == "-i" {
			interactive = true
		} else if len(a) > 0 {
			return errors.New("usage: wok snapshot [-i]")
		}
		var nodes []browser.SnapshotNode
		nodes, err = p.Snapshot(ctx, interactive, &refs)
		result = nodes
		if err == nil && !o.JSON {
			if err = saveRefs(store, state, &refs); err != nil {
				return err
			}
			for _, n := range nodes {
				if _, err = fmt.Fprintf(out, "%s\t%s\t%s\n", n.Ref, n.Role, strconv.Quote(n.Name)); err != nil {
					return err
				}
			}
			return nil
		}
	case "click":
		if len(a) != 1 {
			return errors.New("usage: wok click SELECTOR|@eN")
		}
		err = p.Click(ctx, a[0], &refs)
	case "fill":
		if len(a) != 2 {
			return errors.New("usage: wok fill SELECTOR|@eN TEXT")
		}
		err = p.Fill(ctx, a[0], a[1], &refs)
	case "type":
		if len(a) != 1 {
			return errors.New("usage: wok type TEXT")
		}
		err = p.Type(ctx, a[0])
	case "press":
		if len(a) != 1 {
			return errors.New("usage: wok press KEY")
		}
		err = p.Press(ctx, a[0])
	case "eval":
		if len(a) != 1 {
			return errors.New("usage: wok eval JAVASCRIPT")
		}
		result, err = p.Eval(ctx, a[0])
	case "get":
		if len(a) < 1 || len(a) > 3 {
			return errors.New("usage: wok get text|title|url [SELECTOR], or get attr SELECTOR NAME")
		}
		kind := a[0]
		selector, attr := "", ""
		if len(a) > 1 {
			selector = a[1]
		}
		if len(a) > 2 {
			attr = a[2]
		}
		if (kind == "attr" && len(a) != 3) || (kind == "text" && len(a) > 2) || ((kind == "title" || kind == "url") && len(a) != 1) {
			return errors.New("invalid get arguments")
		}
		result, err = p.Get(ctx, kind, selector, attr, &refs)
	case "find":
		result, err = find(ctx, p, a, &refs)
	case "cookies":
		result, err = cookies(ctx, p, a)
	case "storage":
		result, err = storage(ctx, p, a)
	case "upload":
		if len(a) < 2 {
			return errors.New("usage: wok upload SELECTOR|@eN FILE...")
		}
		err = p.Upload(ctx, a[0], a[1:], &refs)
	case "console", "errors", "network", "downloads", "dialogs":
		if external {
			return errors.New("persistent recordings require a managed profile")
		}
		if len(a) != 0 {
			return errors.New("recording commands take no positional arguments")
		}
		path := "/runtime/" + command
		if command != "downloads" {
			path += "?tab=" + url.QueryEscape(p.TargetID)
		}
		result, err = control(ctx, store, state, "GET", path, nil)
	case "dialog":
		if external {
			return errors.New("dialog policy requires a managed profile")
		}
		if len(a) == 0 {
			result, err = control(ctx, store, state, "GET", "/runtime/dialog-policy", nil)
		} else {
			if len(a) > 2 || (a[0] != "accept" && a[0] != "dismiss") {
				return errors.New("usage: wok dialog [accept|dismiss [PROMPT_TEXT]] (policy for future dialogs)")
			}
			policy := map[string]any{"accept": a[0] == "accept"}
			if len(a) == 2 {
				policy["promptText"] = a[1]
			}
			result, err = control(ctx, store, state, "POST", "/runtime/dialog-policy", policy)
		}
	case "wait":
		err = wait(ctx, p, store, state, a)
	case "screenshot":
		if len(a) != 1 {
			return errors.New("usage: wok screenshot FILE")
		}
		var data []byte
		data, err = screenshot(ctx, state, p)
		if err == nil {
			err = os.WriteFile(a[0], data, 0600)
		}
		result = map[string]any{"path": a[0], "bytes": len(data)}
	case "cdp":
		return rawCDP(ctx, out, client, p.SessionID, a)
	default:
		return fmt.Errorf("unknown command %q; run wok help", command)
	}
	if err != nil {
		return err
	}
	if err = saveRefs(store, state, &refs); err != nil {
		return err
	}
	return emit(out, result)
}
func saveRefs(store *session.Store, state *session.State, refs *browser.Refs) error {
	b, err := json.Marshal(refs)
	if err != nil {
		return err
	}
	state.Refs = b
	return store.Save(state)
}
func rawCDP(ctx context.Context, out io.Writer, client *cdp.Client, sid string, args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return errors.New("usage: wok cdp METHOD [JSON]")
	}
	params := json.RawMessage(`{}`)
	if len(args) == 2 {
		params = json.RawMessage(args[1])
		var obj map[string]any
		if json.Unmarshal(params, &obj) != nil || obj == nil {
			return errors.New("CDP params must be a JSON object")
		}
	}
	var result any
	if err := client.Call(ctx, sid, args[0], params, &result); err != nil {
		return err
	}
	return emit(out, result)
}
func listProfiles(ctx context.Context, out io.Writer) error {
	base, err := session.Open("", "default")
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Dir(base.Dir))
	if err != nil {
		return err
	}
	result := []map[string]any{}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), "external-") {
			continue
		}
		store, e := session.Open("", entry.Name())
		if e != nil {
			continue
		}
		st, e := store.Load()
		running := false
		if e == nil && st.Endpoint != "" {
			probe, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
			running = session.Healthy(probe, st.Endpoint) == nil
			cancel()
		}
		result = append(result, map[string]any{"profile": entry.Name(), "running": running})
	}
	return emit(out, result)
}
