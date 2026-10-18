package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type foundElement struct {
	backendID int
	node      SnapshotNode
}

// Find returns fresh refs without discarding refs from the current document.
// nth is one-based; zero returns every match in document/accessibility order.
func (p *Page) Find(ctx context.Context, kind, value string, nth int, refs *Refs) ([]SnapshotNode, error) {
	if kind != "role" && kind != "text" && kind != "css" {
		return nil, errors.New("find kind must be role, text, or css")
	}
	if strings.TrimSpace(value) == "" {
		return nil, errors.New("find value must not be empty")
	}
	if nth < 0 {
		return nil, errors.New("find nth must be zero or a positive index")
	}
	if refs == nil {
		return nil, errors.New("refs store required")
	}
	doc, err := p.documentID(ctx)
	if err != nil {
		return nil, err
	}
	var ax struct {
		Nodes []struct {
			Ignored   bool `json:"ignored"`
			BackendID int  `json:"backendDOMNodeId"`
			Role      struct {
				Value string `json:"value"`
			} `json:"role"`
			Name struct {
				Value string `json:"value"`
			} `json:"name"`
			Value struct {
				Value any `json:"value"`
			} `json:"value"`
		} `json:"nodes"`
	}
	if err = p.axTree(ctx, &ax); err != nil {
		return nil, err
	}
	metadata := map[int]SnapshotNode{}
	matches := []foundElement{}
	for _, n := range ax.Nodes {
		if n.Ignored || n.BackendID == 0 {
			continue
		}
		sn := SnapshotNode{Role: n.Role.Value, Name: n.Name.Value, Value: n.Value.Value}
		metadata[n.BackendID] = sn
		if kind == "role" && strings.EqualFold(n.Role.Value, value) {
			matches = append(matches, foundElement{n.BackendID, sn})
		}
	}
	if kind != "role" {
		expression := `Array.from(document.querySelectorAll(` + quote(value) + `))`
		if kind == "text" {
			expression = `(()=>{const visible=e=>{const s=getComputedStyle(e),r=e.getBoundingClientRect();return r.width>0&&r.height>0&&s.display!=='none'&&s.visibility!=='hidden'&&s.visibility!=='collapse'};const text=e=>e.innerText??e.textContent??'';const matches=e=>visible(e)&&text(e).includes(` + quote(value) + `);return document.body?[document.body,...document.body.querySelectorAll('*')].filter(e=>matches(e)&&!Array.from(e.children).some(matches)):[]})()`
		}
		var evaluated remoteResult
		if err = p.Call(ctx, "Runtime.evaluate", map[string]any{"expression": expression}, &evaluated); err != nil {
			return nil, err
		}
		if err = remoteError(evaluated); err != nil {
			return nil, err
		}
		arrayID := evaluated.Result.ObjectID
		if arrayID == "" {
			return nil, errors.New("find did not return an element list")
		}
		defer p.Call(ctx, "Runtime.releaseObject", map[string]any{"objectId": arrayID}, nil)
		var props struct {
			Result []struct {
				Name  string `json:"name"`
				Value struct {
					ObjectID string `json:"objectId"`
				} `json:"value"`
			} `json:"result"`
			Exception json.RawMessage `json:"exceptionDetails"`
		}
		if err = p.Call(ctx, "Runtime.getProperties", map[string]any{"objectId": arrayID, "ownProperties": true}, &props); err != nil {
			return nil, err
		}
		if len(props.Exception) > 0 && string(props.Exception) != "null" {
			return nil, errors.New("cannot read find element list")
		}
		type property struct {
			index int
			id    string
		}
		elements := []property{}
		for _, prop := range props.Result {
			index, e := strconv.Atoi(prop.Name)
			if e == nil && index >= 0 && prop.Value.ObjectID != "" {
				elements = append(elements, property{index, prop.Value.ObjectID})
			}
		}
		sort.Slice(elements, func(i, j int) bool { return elements[i].index < elements[j].index })
		// Release every returned remote object even when a later DOM lookup fails.
		defer func() {
			for _, element := range elements {
				p.Call(ctx, "Runtime.releaseObject", map[string]any{"objectId": element.id}, nil)
			}
		}()
		for _, element := range elements {
			var described struct {
				Node struct {
					BackendID int    `json:"backendNodeId"`
					LocalName string `json:"localName"`
					NodeName  string `json:"nodeName"`
				} `json:"node"`
			}
			if err = p.Call(ctx, "DOM.describeNode", map[string]any{"objectId": element.id}, &described); err != nil {
				return nil, err
			}
			id := described.Node.BackendID
			if id == 0 {
				continue
			}
			sn, ok := metadata[id]
			if !ok {
				sn = SnapshotNode{Role: described.Node.LocalName}
				if sn.Role == "" {
					sn.Role = strings.ToLower(described.Node.NodeName)
				}
			}
			if sn.Name == "" {
				var named remoteResult
				if err = p.Call(ctx, "Runtime.callFunctionOn", map[string]any{
					"objectId":            element.id,
					"functionDeclaration": `function(){return this.getAttribute('aria-label')||this.innerText||this.textContent||this.getAttribute('alt')||this.getAttribute('title')||'';}`,
					"returnByValue":       true,
				}, &named); err != nil {
					return nil, err
				}
				if err = remoteError(named); err != nil {
					return nil, err
				}
				sn.Name, _ = named.Result.Value.(string)
			}
			matches = append(matches, foundElement{id, sn})
		}
	}
	if nth > len(matches) {
		return nil, fmt.Errorf("find nth %d exceeds %d matches", nth, len(matches))
	}
	if nth > 0 {
		matches = matches[nth-1 : nth]
	}
	current, err := p.documentID(ctx)
	if err != nil {
		return nil, err
	}
	if current != doc {
		return nil, errors.New("document changed during find; retry on the current page")
	}
	if refs.Items == nil {
		refs.Items = map[string]Ref{}
	}
	for key, ref := range refs.Items {
		if ref.TargetID != p.TargetID || (ref.FrameID == p.FrameID && ref.DocumentID != doc) {
			delete(refs.Items, key)
		}
	}
	out := make([]SnapshotNode, 0, len(matches))
	for _, match := range matches {
		refs.Next++
		key := fmt.Sprintf("e%d", refs.Next)
		sn := match.node
		sn.Ref = "@" + key
		refs.Items[key] = Ref{BackendNodeID: match.backendID, DocumentID: doc, TargetID: p.TargetID, FrameID: p.FrameID}
		out = append(out, sn)
	}
	return out, nil
}

// Cookies reads or mutates cookies for this page's browser context.
func (p *Page) Cookies(ctx context.Context, action string, params map[string]any) (any, error) {
	options := map[string]any{}
	for k, v := range params {
		options[k] = v
	}
	allowed := map[string]bool{}
	switch action {
	case "list", "clear":
		if len(options) != 0 {
			return nil, fmt.Errorf("cookies %s does not accept parameters", action)
		}
	case "set":
		for _, k := range []string{"name", "value", "url", "domain", "path", "expires", "httpOnly", "secure", "sameSite"} {
			allowed[k] = true
		}
	case "delete":
		for _, k := range []string{"name", "url", "domain", "path"} {
			allowed[k] = true
		}
	default:
		return nil, errors.New("cookies action must be list, set, delete, or clear")
	}
	for k := range options {
		if !allowed[k] {
			return nil, fmt.Errorf("unsupported cookie parameter %q", k)
		}
	}
	if action == "set" || action == "delete" {
		name, ok := options["name"].(string)
		if !ok || name == "" {
			return nil, errors.New("cookie name must be a nonempty string")
		}
		if action == "set" {
			if _, ok := options["value"].(string); !ok {
				return nil, errors.New("cookie value must be a string")
			}
		}
		if _, ok := options["url"]; !ok {
			u, err := p.Eval(ctx, "location.href")
			if err != nil {
				return nil, err
			}
			options["url"] = u
		}
	}
	switch action {
	case "list":
		u, err := p.Eval(ctx, "location.href")
		if err != nil {
			return nil, err
		}
		var result struct {
			Cookies []map[string]any `json:"cookies"`
		}
		if err = p.Call(ctx, "Network.getCookies", map[string]any{"urls": []any{u}}, &result); err != nil {
			return nil, err
		}
		if result.Cookies == nil {
			result.Cookies = []map[string]any{}
		}
		return result.Cookies, nil
	case "set":
		var result struct {
			Success bool `json:"success"`
		}
		if err := p.Call(ctx, "Network.setCookie", options, &result); err != nil {
			return nil, err
		}
		if !result.Success {
			return nil, errors.New("browser rejected cookie")
		}
	case "delete":
		if err := p.Call(ctx, "Network.deleteCookies", options, nil); err != nil {
			return nil, err
		}
	case "clear":
		if err := p.Call(ctx, "Network.clearBrowserCookies", nil, nil); err != nil {
			return nil, err
		}
	}
	return map[string]bool{"ok": true}, nil
}

// Storage uses the current origin's local or session storage.
func (p *Page) Storage(ctx context.Context, area, action, key, value string) (any, error) {
	var storage string
	switch area {
	case "local":
		storage = "localStorage"
	case "session":
		storage = "sessionStorage"
	default:
		return nil, errors.New("storage area must be local or session")
	}
	var expression string
	switch action {
	case "list":
		expression = `Object.fromEntries(Array.from({length:s.length},(_,i)=>{const k=s.key(i);return [k,s.getItem(k)]}))`
	case "get":
		expression = `s.getItem(` + quote(key) + `)`
	case "set":
		expression = `(s.setItem(` + quote(key) + `,` + quote(value) + `),{ok:true})`
	case "delete":
		expression = `(s.removeItem(` + quote(key) + `),{ok:true})`
	case "clear":
		expression = `(s.clear(),{ok:true})`
	default:
		return nil, errors.New("storage action must be list, get, set, delete, or clear")
	}
	return p.Eval(ctx, `(()=>{const s=window.`+storage+`;return `+expression+`;})()`)
}

type WaitOptions struct {
	Selector string
	State    string
	Text     string
	URL      string
	Function string
	Load     bool
}

// Wait polls a single page condition until it passes or the context ends.
// URL matches a substring. Function accepts a JavaScript expression or function.
func (p *Page) Wait(ctx context.Context, options WaitOptions) error {
	count := 0
	for _, set := range []bool{options.Selector != "", options.Text != "", options.URL != "", options.Function != "", options.Load} {
		if set {
			count++
		}
	}
	if count != 1 {
		return errors.New("wait requires exactly one selector, text, URL, function, or load condition")
	}
	if options.Selector == "" && options.State != "" {
		return errors.New("wait state requires a selector")
	}
	// "IFRAME >> SELECTOR" waits inside the frame; a missing frame counts as
	// a missing element.
	frame, selector := "", options.Selector
	if i := strings.LastIndex(selector, FrameSeparator); i >= 0 {
		frame, selector = selector[:i], strings.TrimSpace(selector[i+len(FrameSeparator):])
	}
	var expression string
	switch {
	case options.Selector != "":
		state := options.State
		if state == "" {
			state = "attached"
		}
		start := `(()=>{const el=document.querySelector(` + quote(selector) + `);`
		visible := `const visible=el?(()=>{const s=getComputedStyle(el),r=el.getBoundingClientRect();return r.width>0&&r.height>0&&s.visibility!=='hidden'&&s.visibility!=='collapse'&&s.display!=='none'})():false;`
		switch state {
		case "attached":
			expression = start + `return el!==null;})()`
		case "detached":
			expression = start + `return el===null;})()`
		case "visible":
			expression = start + visible + `return visible;})()`
		case "hidden":
			expression = start + visible + `return !visible;})()`
		default:
			return errors.New("wait selector state must be attached, detached, visible, or hidden")
		}
	case options.Text != "":
		expression = `(document.body?.innerText??'').includes(` + quote(options.Text) + `)`
	case options.URL != "":
		expression = `location.href.includes(` + quote(options.URL) + `)`
	case options.Function != "":
		expression = `(async()=>{const value=(` + options.Function + `);return Boolean(await (typeof value==='function'?value():value));})()`
	case options.Load:
		expression = `document.readyState==='complete'`
	}
	ticker := time.NewTicker(75 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var value any
		scope, err := p, error(nil)
		if frame != "" {
			scope, err = p.Frame(ctx, frame, nil)
		}
		if err != nil {
			value = options.State == "detached" || options.State == "hidden"
		} else if value, err = scope.Eval(ctx, expression); err != nil {
			return err
		}
		if value == true {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Upload sets real files on a file input using CDP, which emits input/change
// events. When the target is not a file input, Upload clicks it and answers
// the file chooser it opens, which covers styled buttons and inputs that
// pages create on demand.
func (p *Page) Upload(ctx context.Context, selector string, files []string, refs *Refs) error {
	if len(files) == 0 {
		return errors.New("upload requires at least one file")
	}
	absolute := make([]string, len(files))
	for i, file := range files {
		path, err := filepath.Abs(file)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("upload file %s: %w", file, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("upload file is not a regular file: %s", file)
		}
		absolute[i] = path
	}
	q, id, err := p.resolve(ctx, selector, refs)
	if err != nil {
		return err
	}
	defer q.Call(ctx, "Runtime.releaseObject", map[string]any{"objectId": id}, nil)
	var verified remoteResult
	fn := `function(){if(!this.isConnected)throw new Error('stale ref: element removed');if(!(this instanceof HTMLInputElement)||this.type!=='file')return false;if(this.matches(':disabled'))throw new Error('file input is disabled');if(` + strconv.Itoa(len(files)) + `>1&&!this.multiple)throw new Error('multiple files require a multiple file input');return true;}`
	if err = q.Call(ctx, "Runtime.callFunctionOn", map[string]any{"objectId": id, "functionDeclaration": fn, "returnByValue": true}, &verified); err != nil {
		return err
	}
	if err = remoteError(verified); err != nil {
		return err
	}
	if isInput, _ := verified.Result.Value.(bool); !isInput {
		return p.uploadViaChooser(ctx, selector, absolute, refs)
	}
	return q.Call(ctx, "DOM.setFileInputFiles", map[string]any{"files": absolute, "objectId": id}, nil)
}

// chooserWait bounds how long a click may take to open a file chooser.
const chooserWait = 5 * time.Second

func (p *Page) uploadViaChooser(ctx context.Context, selector string, files []string, refs *Refs) error {
	sessions := map[string]bool{p.SessionID: true, p.Top().SessionID: true}
	events, unsub := p.Client.Subscribe("Page.fileChooserOpened")
	defer unsub()
	for session := range sessions {
		if err := p.Client.Call(ctx, session, "Page.setInterceptFileChooserDialog", map[string]any{"enabled": true}, nil); err != nil && session == p.Top().SessionID {
			return err
		}
		defer p.Client.Call(context.WithoutCancel(ctx), session, "Page.setInterceptFileChooserDialog", map[string]any{"enabled": false}, nil)
	}
	if err := p.Click(ctx, selector, refs); err != nil {
		return err
	}
	timer := time.NewTimer(chooserWait)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return errors.New("browser connection closed")
			}
			if !sessions[ev.SessionID] {
				continue
			}
			var opened struct {
				Mode          string `json:"mode"`
				BackendNodeID int    `json:"backendNodeId"`
			}
			if err := json.Unmarshal(ev.Params, &opened); err != nil {
				return err
			}
			if len(files) > 1 && opened.Mode != "selectMultiple" {
				return errors.New("multiple files require a multiple file input")
			}
			return p.Client.Call(ctx, ev.SessionID, "DOM.setFileInputFiles", map[string]any{"files": files, "backendNodeId": opened.BackendNodeID}, nil)
		case <-timer.C:
			return fmt.Errorf("upload target %s is not a file input and clicking it opened no file chooser within %s (showOpenFilePicker is not supported)", selector, chooserWait)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
