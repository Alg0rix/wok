package observe

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alg0rix/wok/internal/cdp"
)

// Route mocks or blocks requests whose URL matches Pattern (a glob where **
// matches anything and * matches anything except "/"; a pattern without
// wildcards matches as a substring).
type Route struct {
	Pattern     string            `json:"pattern"`
	Status      int               `json:"status,omitempty"`
	Body        string            `json:"body,omitempty"`
	ContentType string            `json:"contentType,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Abort       bool              `json:"abort,omitempty"`
	re          *regexp.Regexp
}

func compileRoute(r *Route) error {
	if r.Pattern == "" {
		return errors.New("route pattern must not be empty")
	}
	if !strings.ContainsAny(r.Pattern, "*") {
		r.re = regexp.MustCompile(regexp.QuoteMeta(r.Pattern))
		return nil
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(r.Pattern); i++ {
		switch {
		case strings.HasPrefix(r.Pattern[i:], "**"):
			b.WriteString(".*")
			i++
		case r.Pattern[i] == '*':
			b.WriteString("[^/]*")
		default:
			b.WriteString(regexp.QuoteMeta(r.Pattern[i : i+1]))
		}
	}
	b.WriteString("$")
	var err error
	r.re, err = regexp.Compile(b.String())
	return err
}

// RecordedAction is one user interaction captured by recording-start.
type RecordedAction struct {
	Type     string   `json:"type"`
	Selector string   `json:"selector,omitempty"`
	Value    string   `json:"value,omitempty"`
	Values   []string `json:"values,omitempty"`
	URL      string   `json:"url,omitempty"`
	Tab      string   `json:"tab"`
}

type recording struct {
	actions []RecordedAction
	scripts map[string]string // tab session -> script identifier
}

type debugState struct {
	breakpoints []string
	paused      map[string]any
}

const recorderScript = `(()=>{if(window.__wokRecorderOn)return;window.__wokRecorderOn=true;` + locatorJS + `;const send=a=>{if(window.__wokRecorderOn&&window.__wokRecord)window.__wokRecord(JSON.stringify(a))};addEventListener('click',e=>{const t=e.target.closest('a,button,input,select,textarea,label,[role],[onclick]')||e.target;if(t.matches('input:not([type=checkbox]):not([type=radio]):not([type=button]):not([type=submit]),textarea,select'))return;if(t.matches('input[type=checkbox],input[type=radio]'))return;send({type:e.detail===2?'dblclick':'click',selector:__wokLocator(t)})},true);addEventListener('input',e=>{const t=e.target;if(t.matches('input[type=checkbox],input[type=radio],select'))return;if(t.matches('input,textarea')||t.isContentEditable)send({type:'fill',selector:__wokLocator(t),value:t.isContentEditable?t.innerText:t.value})},true);addEventListener('change',e=>{const t=e.target;if(t.matches('input[type=checkbox],input[type=radio]'))send({type:t.checked?'check':'uncheck',selector:__wokLocator(t)});else if(t.matches('select'))send({type:'select',selector:__wokLocator(t),values:[...t.selectedOptions].map(o=>o.value)})},true);addEventListener('keydown',e=>{if(['Enter','Escape','Tab'].includes(e.key))send({type:'press',value:e.key})},true)})()`

// locatorJS duplicates browser.LocatorScript; observe cannot import browser.
const locatorJS = `function __wokLocator(el){const q=s=>{try{return document.querySelectorAll(s).length===1}catch{return false}};const esc=CSS.escape;if(el.id&&q('#'+esc(el.id)))return '#'+esc(el.id);for(const a of ['data-testid','data-test','data-qa','name','aria-label','placeholder','title','alt']){const v=el.getAttribute&&el.getAttribute(a);if(v){const s=el.localName+'['+a+'='+JSON.stringify(v)+']';if(q(s))return s}}const parts=[];let n=el;while(n&&n.nodeType===1&&n!==document.documentElement){let part=n.localName;if(n.id&&q('#'+esc(n.id))){parts.unshift('#'+esc(n.id));break}const sib=n.parentElement?[...n.parentElement.children].filter(c=>c.localName===n.localName):[];if(sib.length>1)part+=':nth-of-type('+(sib.indexOf(n)+1)+')';parts.unshift(part);const s=parts.join(' > ');if(q(s))return s;n=n.parentElement}return parts.join(' > ')}`

func (m *Manager) call(session, method string, params, result any) error {
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	defer cancel()
	return m.c.Call(ctx, session, method, params, result)
}

func (m *Manager) loadRoutes() error {
	data, err := os.ReadFile(filepath.Join(m.config.Dir, "routes.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var routes []*Route
	if err = json.Unmarshal(data, &routes); err != nil {
		return fmt.Errorf("load Wok routes: %w", err)
	}
	for _, r := range routes {
		if err = compileRoute(r); err != nil {
			return err
		}
	}
	m.routes = routes
	return nil
}
func (m *Manager) saveRoutesLocked() error {
	data, err := json.Marshal(m.routes)
	if err != nil {
		return err
	}
	name := filepath.Join(m.config.Dir, "routes.json")
	tmp := name + ".tmp"
	if err = os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, name)
}

// fetchParams returns the Fetch.enable parameters for the current routes and
// proxy credentials, or nil when interception should be disabled.
func (m *Manager) fetchParams() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	auth := m.config.ProxyUsername != ""
	if !auth && len(m.routes) == 0 {
		return nil
	}
	patterns := []map[string]string{}
	if auth {
		patterns = append(patterns, map[string]string{"urlPattern": "*"})
	} else {
		for _, r := range m.routes {
			p := strings.ReplaceAll(r.Pattern, "**", "*")
			if !strings.Contains(p, "*") {
				p = "*" + p + "*"
			}
			patterns = append(patterns, map[string]string{"urlPattern": p})
		}
	}
	return map[string]any{"handleAuthRequests": auth, "patterns": patterns}
}
func (m *Manager) applyFetch(session string) error {
	if params := m.fetchParams(); params != nil {
		return m.call(session, "Fetch.enable", params, nil)
	}
	return m.call(session, "Fetch.disable", nil, nil)
}
func (m *Manager) applyFetchAll() error {
	m.mu.Lock()
	sessions := make([]string, 0, len(m.sessions))
	for s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()
	for _, s := range sessions {
		if err := m.applyFetch(s); err != nil {
			return err
		}
	}
	return nil
}

// fetchPaused fulfils matching routes, then falls back to proxy handling.
func (m *Manager) fetchPaused(event cdp.Event) {
	var p struct {
		RequestID string `json:"requestId"`
		Request   struct {
			URL string `json:"url"`
		} `json:"request"`
	}
	if json.Unmarshal(event.Params, &p) != nil || p.RequestID == "" {
		return
	}
	m.mu.Lock()
	var route *Route
	for i := len(m.routes) - 1; i >= 0; i-- {
		if m.routes[i].re.MatchString(p.Request.URL) {
			route = m.routes[i]
			break
		}
	}
	m.mu.Unlock()
	if route == nil {
		if m.config.ProxyUsername != "" {
			m.proxy(event)
		} else {
			_ = m.call(event.SessionID, "Fetch.continueRequest", map[string]any{"requestId": p.RequestID}, nil)
		}
		return
	}
	if route.Abort {
		_ = m.call(event.SessionID, "Fetch.failRequest", map[string]any{"requestId": p.RequestID, "errorReason": "Failed"}, nil)
		return
	}
	status := route.Status
	if status == 0 {
		status = 200
	}
	headers := []map[string]string{}
	contentType := route.ContentType
	if contentType == "" {
		contentType = "text/plain; charset=utf-8"
		if json.Valid([]byte(route.Body)) && strings.TrimSpace(route.Body) != "" {
			contentType = "application/json"
		}
	}
	headers = append(headers, map[string]string{"name": "Content-Type", "value": contentType})
	for k, v := range route.Headers {
		headers = append(headers, map[string]string{"name": k, "value": v})
	}
	_ = m.call(event.SessionID, "Fetch.fulfillRequest", map[string]any{"requestId": p.RequestID, "responseCode": status, "responseHeaders": headers, "body": base64.StdEncoding.EncodeToString([]byte(route.Body))}, nil)
}

// devtoolsEvent handles recorder events for DevTools features. It runs on
// the ordered event goroutine with m.mu held; protocol calls go async.
func (m *Manager) devtoolsEventLocked(t *tab, event cdp.Event) {
	switch event.Method {
	case "Runtime.bindingCalled":
		var p struct {
			Name    string `json:"name"`
			Payload string `json:"payload"`
		}
		if json.Unmarshal(event.Params, &p) != nil || p.Name != "__wokRecord" || m.recording == nil {
			return
		}
		var a RecordedAction
		if json.Unmarshal([]byte(p.Payload), &a) != nil {
			return
		}
		a.Tab = t.id
		actions := m.recording.actions
		// Coalesce keystrokes into one fill and a click+click into a dblclick.
		if n := len(actions); n > 0 {
			last := &actions[n-1]
			if a.Type == "fill" && last.Type == "fill" && last.Selector == a.Selector {
				*last = a
				return
			}
			if a.Type == "dblclick" && n > 1 && last.Type == "click" && actions[n-2].Type == "click" && last.Selector == a.Selector {
				m.recording.actions = append(actions[:n-2], a)
				return
			}
		}
		m.recording.actions = append(actions, a)
	case "Page.screencastFrame":
		var p struct {
			Data      string `json:"data"`
			SessionID int    `json:"sessionId"`
		}
		if json.Unmarshal(event.Params, &p) != nil {
			return
		}
		session := event.SessionID
		go func() { _ = m.call(session, "Page.screencastFrameAck", map[string]any{"sessionId": p.SessionID}, nil) }()
		if v := m.video; v != nil && v.session == session {
			if frame, err := base64.StdEncoding.DecodeString(p.Data); err == nil {
				v.frame(frame, time.Now())
			}
		}
	case "Debugger.paused":
		if t.debug == nil {
			return
		}
		var p struct {
			Reason     string `json:"reason"`
			CallFrames []struct {
				FunctionName string `json:"functionName"`
				URL          string `json:"url"`
				Location     struct {
					LineNumber   int `json:"lineNumber"`
					ColumnNumber int `json:"columnNumber"`
				} `json:"location"`
			} `json:"callFrames"`
		}
		if json.Unmarshal(event.Params, &p) != nil {
			return
		}
		state := map[string]any{"paused": true, "reason": p.Reason}
		if len(p.CallFrames) > 0 {
			f := p.CallFrames[0]
			state["url"] = f.URL
			state["line"] = f.Location.LineNumber + 1
			state["column"] = f.Location.ColumnNumber + 1
			state["function"] = f.FunctionName
		}
		t.debug.paused = state
	case "Debugger.resumed":
		if t.debug != nil {
			t.debug.paused = nil
		}
	}
}

// setupDevtools applies routes and an active recording to a new tab session.
func (m *Manager) setupDevtools(session string) {
	if m.fetchParams() != nil {
		_ = m.applyFetch(session)
	}
	m.mu.Lock()
	active := m.recording != nil
	m.mu.Unlock()
	if active {
		m.startRecording(session)
	}
}
func (m *Manager) startRecording(session string) {
	_ = m.call(session, "Runtime.addBinding", map[string]any{"name": "__wokRecord"}, nil)
	var added struct {
		Identifier string `json:"identifier"`
	}
	if m.call(session, "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": recorderScript}, &added) == nil {
		m.mu.Lock()
		if m.recording != nil {
			m.recording.scripts[session] = added.Identifier
		}
		m.mu.Unlock()
	}
	_ = m.call(session, "Runtime.evaluate", map[string]any{"expression": recorderScript}, nil)
}

func decodeBody(w http.ResponseWriter, r *http.Request, value any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON body: " + err.Error()})
		return false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "expected one JSON object"})
		return false
	}
	return true
}

// serveDevtools handles DevTools routes. It returns false for unknown paths.
func (m *Manager) serveDevtools(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/runtime/request":
		m.serveRequest(w, r)
	case "/runtime/routes":
		m.serveRoutes(w, r)
	case "/runtime/recording":
		m.serveRecording(w, r)
	case "/runtime/tracing":
		m.serveTracing(w, r)
	case "/runtime/video":
		m.serveVideo(w, r)
	case "/runtime/debugger":
		m.serveDebugger(w, r)
	default:
		return false
	}
	return true
}

func (m *Manager) tabFor(w http.ResponseWriter, r *http.Request) *tab {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.tabLocked(r.URL.Query().Get("tab"))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": err.Error()})
		return nil
	}
	return t
}

// serveRequest reports one request from the network log by one-based index.
func (m *Manager) serveRequest(w http.ResponseWriter, r *http.Request) {
	t := m.tabFor(w, r)
	if t == nil {
		return
	}
	index, err := strconv.Atoi(r.URL.Query().Get("index"))
	m.mu.Lock()
	if err != nil || index < 1 || index > len(t.network) {
		count := len(t.network)
		m.mu.Unlock()
		writeJSON(w, 404, map[string]string{"error": fmt.Sprintf("request index must be 1..%d", count)})
		return
	}
	e := *t.network[index-1]
	session := t.session
	m.mu.Unlock()
	part := r.URL.Query().Get("part")
	result := map[string]any{}
	if part == "" || part == "request-body" {
		body := e.postData
		if body == "" && e.hasPostData {
			var data struct {
				PostData string `json:"postData"`
			}
			if m.call(session, "Network.getRequestPostData", map[string]string{"requestId": e.RequestID}, &data) == nil {
				body = data.PostData
			}
		}
		result["requestBody"] = body
	}
	if part == "" || part == "response-body" {
		var data struct {
			Body          string `json:"body"`
			Base64Encoded bool   `json:"base64Encoded"`
		}
		if err := m.call(session, "Network.getResponseBody", map[string]string{"requestId": e.RequestID}, &data); err != nil {
			result["responseBodyError"] = err.Error()
		} else {
			result["responseBody"] = data.Body
			result["base64Encoded"] = data.Base64Encoded
		}
		result["mimeType"] = e.mimeType
	}
	switch part {
	case "":
		result["index"] = index
		result["request"] = e
		result["requestHeaders"] = e.requestHeaders
		result["responseHeaders"] = e.responseHeaders
	case "request-headers":
		result["requestHeaders"] = e.requestHeaders
	case "response-headers":
		result["responseHeaders"] = e.responseHeaders
	case "request-body", "response-body":
	default:
		writeJSON(w, 400, map[string]string{"error": "unknown request part"})
		return
	}
	writeJSON(w, 200, result)
}

func (m *Manager) serveRoutes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		route := &Route{}
		if !decodeBody(w, r, route) {
			return
		}
		if err := compileRoute(route); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		m.mu.Lock()
		kept := m.routes[:0]
		for _, existing := range m.routes {
			if existing.Pattern != route.Pattern {
				kept = append(kept, existing)
			}
		}
		m.routes = append(kept, route)
	case http.MethodDelete:
		pattern := r.URL.Query().Get("pattern")
		m.mu.Lock()
		kept := []*Route{}
		for _, existing := range m.routes {
			if pattern != "" && existing.Pattern != pattern {
				kept = append(kept, existing)
			}
		}
		m.routes = kept
	default:
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	if r.Method != http.MethodGet {
		err := m.saveRoutesLocked()
		m.mu.Unlock()
		if err == nil {
			err = m.applyFetchAll()
		}
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
	}
	m.mu.Lock()
	routes := append([]*Route{}, m.routes...)
	m.mu.Unlock()
	writeJSON(w, 200, routes)
}

func (m *Manager) serveRecording(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Action string `json:"action"`
	}
	if r.Method != http.MethodPost || !decodeBody(w, r, &input) {
		if r.Method != http.MethodPost {
			writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		}
		return
	}
	m.mu.Lock()
	sessions := make([]string, 0, len(m.sessions))
	for s := range m.sessions {
		sessions = append(sessions, s)
	}
	switch input.Action {
	case "start":
		if m.recording != nil {
			m.mu.Unlock()
			writeJSON(w, 409, map[string]string{"error": "recording already started"})
			return
		}
		m.recording = &recording{actions: []RecordedAction{}, scripts: map[string]string{}}
		m.mu.Unlock()
		for _, s := range sessions {
			m.startRecording(s)
		}
		writeJSON(w, 200, map[string]bool{"recording": true})
	case "stop":
		rec := m.recording
		m.recording = nil
		m.mu.Unlock()
		if rec == nil {
			writeJSON(w, 409, map[string]string{"error": "recording not started"})
			return
		}
		for _, s := range sessions {
			if id := rec.scripts[s]; id != "" {
				_ = m.call(s, "Page.removeScriptToEvaluateOnNewDocument", map[string]string{"identifier": id}, nil)
			}
			_ = m.call(s, "Runtime.evaluate", map[string]any{"expression": "window.__wokRecorderOn=false"}, nil)
			_ = m.call(s, "Runtime.removeBinding", map[string]string{"name": "__wokRecord"}, nil)
		}
		lines := []string{}
		for _, a := range rec.actions {
			lines = append(lines, a.command())
		}
		writeJSON(w, 200, map[string]any{"actions": rec.actions, "script": strings.Join(lines, "\n")})
	default:
		m.mu.Unlock()
		writeJSON(w, 400, map[string]string{"error": "action must be start or stop"})
	}
}

// command renders the action as a `wok exec` line.
func (a RecordedAction) command() string {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	switch a.Type {
	case "fill":
		return "fill " + q(a.Selector) + " " + q(a.Value)
	case "press":
		return "press " + a.Value
	case "select":
		parts := []string{"select", q(a.Selector)}
		for _, v := range a.Values {
			parts = append(parts, q(v))
		}
		return strings.Join(parts, " ")
	default:
		return a.Type + " " + q(a.Selector)
	}
}

func (m *Manager) serveTracing(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Action     string `json:"action"`
		Path       string `json:"path"`
		Categories string `json:"categories"`
	}
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	if !decodeBody(w, r, &input) {
		return
	}
	m.traceMu.Lock()
	defer m.traceMu.Unlock()
	switch input.Action {
	case "start":
		if m.tracing {
			writeJSON(w, 409, map[string]string{"error": "tracing already started"})
			return
		}
		categories := input.Categories
		if categories == "" {
			categories = "-*,devtools.timeline,v8.execute,blink.console,blink.user_timing,loading,latencyInfo,disabled-by-default-devtools.timeline,disabled-by-default-devtools.timeline.frame,disabled-by-default-devtools.screenshot,disabled-by-default-v8.cpu_profiler"
		}
		if err := m.call("", "Tracing.start", map[string]any{"transferMode": "ReturnAsStream", "traceConfig": map[string]any{"includedCategories": strings.Split(categories, ",")}}, nil); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		m.tracing = true
		writeJSON(w, 200, map[string]bool{"tracing": true})
	case "stop":
		if !m.tracing {
			writeJSON(w, 409, map[string]string{"error": "tracing not started"})
			return
		}
		if !filepath.IsAbs(input.Path) {
			writeJSON(w, 400, map[string]string{"error": "trace path must be absolute"})
			return
		}
		size, err := m.endTrace(input.Path)
		m.tracing = false
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"path": input.Path, "bytes": size})
	default:
		writeJSON(w, 400, map[string]string{"error": "action must be start or stop"})
	}
}
func (m *Manager) endTrace(path string) (int64, error) {
	complete, unsub := m.c.Subscribe("Tracing.tracingComplete")
	defer unsub()
	if err := m.call("", "Tracing.end", nil, nil); err != nil {
		return 0, err
	}
	var stream string
	select {
	case ev := <-complete:
		var p struct {
			Stream string `json:"stream"`
		}
		_ = json.Unmarshal(ev.Params, &p)
		stream = p.Stream
	case <-time.After(30 * time.Second):
		return 0, errors.New("timed out waiting for trace data")
	case <-m.ctx.Done():
		return 0, m.ctx.Err()
	}
	if stream == "" {
		return 0, errors.New("browser returned no trace stream")
	}
	defer m.call("", "IO.close", map[string]string{"handle": stream}, nil)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var size int64
	for {
		var chunk struct {
			Data          string `json:"data"`
			Base64Encoded bool   `json:"base64Encoded"`
			EOF           bool   `json:"eof"`
		}
		if err = m.call("", "IO.read", map[string]any{"handle": stream, "size": 1 << 20}, &chunk); err != nil {
			return size, err
		}
		data := []byte(chunk.Data)
		if chunk.Base64Encoded {
			if data, err = base64.StdEncoding.DecodeString(chunk.Data); err != nil {
				return size, err
			}
		}
		n, err := f.Write(data)
		size += int64(n)
		if err != nil {
			return size, err
		}
		if chunk.EOF {
			return size, f.Sync()
		}
	}
}

func (m *Manager) serveVideo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	var input struct {
		Action string `json:"action"`
		Path   string `json:"path"`
		Title  string `json:"title"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	switch input.Action {
	case "start":
		t := m.tabFor(w, r)
		if t == nil {
			return
		}
		if !filepath.IsAbs(input.Path) {
			writeJSON(w, 400, map[string]string{"error": "video path must be absolute"})
			return
		}
		v, err := newVideo(input.Path, t.session)
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		m.mu.Lock()
		if m.video != nil {
			m.mu.Unlock()
			v.abort()
			writeJSON(w, 409, map[string]string{"error": "video recording already started"})
			return
		}
		m.video = v
		m.mu.Unlock()
		if err = m.call(t.session, "Page.startScreencast", map[string]any{"format": "jpeg", "quality": 75, "everyNthFrame": 1}, nil); err != nil {
			m.mu.Lock()
			m.video = nil
			m.mu.Unlock()
			v.abort()
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"recording": true, "path": input.Path})
	case "chapter":
		m.mu.Lock()
		v := m.video
		if v != nil {
			v.chapters = append(v.chapters, Chapter{input.Title, time.Since(v.started).Milliseconds()})
		}
		m.mu.Unlock()
		if v == nil {
			writeJSON(w, 409, map[string]string{"error": "video recording not started"})
			return
		}
		// A title card makes the chapter visible in the recording itself.
		card := `(()=>{const d=document.createElement('div');d.textContent=` + strconv.Quote(input.Title) + `;Object.assign(d.style,{position:'fixed',left:'50%',top:'40%',transform:'translate(-50%,-50%)',padding:'18px 28px',font:'600 24px system-ui,sans-serif',color:'#fff',background:'rgba(11,11,11,.82)',borderRadius:'10px',zIndex:2147483647,pointerEvents:'none'});document.documentElement.appendChild(d);setTimeout(()=>d.remove(),1500)})()`
		_ = m.call(v.session, "Runtime.evaluate", map[string]any{"expression": card}, nil)
		writeJSON(w, 200, map[string]any{"chapter": input.Title})
	case "stop":
		m.mu.Lock()
		v := m.video
		m.video = nil
		m.mu.Unlock()
		if v == nil {
			writeJSON(w, 409, map[string]string{"error": "video recording not started"})
			return
		}
		_ = m.call(v.session, "Page.stopScreencast", nil, nil)
		result, err := v.finish()
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, result)
	default:
		writeJSON(w, 400, map[string]string{"error": "action must be start, chapter or stop"})
	}
}

func (m *Manager) serveDebugger(w http.ResponseWriter, r *http.Request) {
	t := m.tabFor(w, r)
	if t == nil {
		return
	}
	state := func() {
		m.mu.Lock()
		result := map[string]any{"paused": false, "breakpoints": []string{}}
		if t.debug != nil {
			result["breakpoints"] = t.debug.breakpoints
			if t.debug.paused != nil {
				for k, v := range t.debug.paused {
					result[k] = v
				}
			}
		}
		m.mu.Unlock()
		writeJSON(w, 200, result)
	}
	if r.Method == http.MethodGet {
		state()
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	var input struct {
		Action   string `json:"action"`
		Location string `json:"location"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	m.mu.Lock()
	enabled := t.debug != nil
	m.mu.Unlock()
	var err error
	switch input.Action {
	case "pause-at":
		if !enabled {
			if err = m.call(t.session, "Debugger.enable", nil, nil); err != nil {
				break
			}
			m.mu.Lock()
			t.debug = &debugState{breakpoints: []string{}}
			m.mu.Unlock()
		}
		params := map[string]any{}
		if input.Location == "next" {
			err = m.call(t.session, "Debugger.pause", nil, nil)
			break
		}
		location, line, column, perr := parseLocation(input.Location)
		if perr != nil {
			err = perr
			break
		}
		params["urlRegex"] = regexp.QuoteMeta(location)
		params["lineNumber"] = line - 1
		if column > 0 {
			params["columnNumber"] = column - 1
		}
		var bp struct {
			BreakpointID string `json:"breakpointId"`
		}
		if err = m.call(t.session, "Debugger.setBreakpointByUrl", params, &bp); err == nil {
			m.mu.Lock()
			t.debug.breakpoints = append(t.debug.breakpoints, bp.BreakpointID)
			m.mu.Unlock()
		}
	case "resume", "step-over":
		if !enabled {
			err = errors.New("debugger is not active; use pause-at first")
			break
		}
		method := map[string]string{"resume": "Debugger.resume", "step-over": "Debugger.stepOver"}[input.Action]
		err = m.call(t.session, method, nil, nil)
		if err == nil && input.Action == "step-over" {
			// Report the next pause location when the step completes promptly.
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				m.mu.Lock()
				paused := t.debug != nil && t.debug.paused != nil
				m.mu.Unlock()
				if paused {
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
		}
	case "clear":
		if enabled {
			err = m.call(t.session, "Debugger.disable", nil, nil)
			m.mu.Lock()
			t.debug = nil
			m.mu.Unlock()
		}
	default:
		err = errors.New("action must be pause-at, resume, step-over or clear")
	}
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	state()
}

// parseLocation splits "URL-or-file:LINE[:COLUMN]".
func parseLocation(raw string) (string, int, int, error) {
	parts := strings.Split(raw, ":")
	nums := []int{}
	for len(parts) > 1 && len(nums) < 2 {
		n, err := strconv.Atoi(parts[len(parts)-1])
		if err != nil {
			break
		}
		nums = append([]int{n}, nums...)
		parts = parts[:len(parts)-1]
	}
	if len(nums) == 0 || nums[0] < 1 {
		return "", 0, 0, errors.New("location must be SCRIPT:LINE[:COLUMN] with a one-based line")
	}
	column := 0
	if len(nums) == 2 {
		column = nums[1]
	}
	return strings.Join(parts, ":"), nums[0], column, nil
}
