package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/alg0rix/wok/internal/cdp"
)

type Target struct {
	TargetID string `json:"targetId"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	URL      string `json:"url"`
}
type Page struct {
	Client    *cdp.Client
	SessionID string
	TargetID  string
}

func Targets(ctx context.Context, c *cdp.Client) ([]Target, error) {
	var r struct {
		TargetInfos []Target `json:"targetInfos"`
	}
	err := c.Call(ctx, "", "Target.getTargets", nil, &r)
	return r.TargetInfos, err
}
func Attach(ctx context.Context, c *cdp.Client, targetID string) (*Page, error) {
	if targetID == "" {
		ts, err := Targets(ctx, c)
		if err != nil {
			return nil, err
		}
		for _, t := range ts {
			if t.Type == "page" {
				targetID = t.TargetID
				break
			}
		}
		if targetID == "" {
			var r struct {
				TargetID string `json:"targetId"`
			}
			if err = c.Call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &r); err != nil {
				return nil, err
			}
			targetID = r.TargetID
		}
	}
	var r struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.Call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": targetID, "flatten": true}, &r); err != nil {
		return nil, err
	}
	p := &Page{c, r.SessionID, targetID}
	if err := p.Call(ctx, "Page.enable", nil, nil); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}
func (p *Page) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return p.Client.Call(ctx, "", "Target.detachFromTarget", map[string]any{"sessionId": p.SessionID}, nil)
}
func (p *Page) Call(ctx context.Context, method string, params, result any) error {
	return p.Client.Call(ctx, p.SessionID, method, params, result)
}

type remoteResult struct {
	Result struct {
		Type        string `json:"type"`
		Value       any    `json:"value"`
		ObjectID    string `json:"objectId"`
		Description string `json:"description"`
	} `json:"result"`
	Exception json.RawMessage `json:"exceptionDetails"`
}

func remoteError(r remoteResult) error {
	if len(r.Exception) > 0 && string(r.Exception) != "null" {
		return fmt.Errorf("browser JavaScript: %s", r.Exception)
	}
	return nil
}
// Eval evaluates an expression and returns its JSON value. When the
// expression is a function, such as '() => ...', Eval calls it and returns
// the awaited result instead.
func (p *Page) Eval(ctx context.Context, expression string) (any, error) {
	var r remoteResult
	if err := p.Call(ctx, "Runtime.evaluate", map[string]any{"expression": expression, "awaitPromise": true}, &r); err != nil {
		return nil, err
	}
	if err := remoteError(r); err != nil || r.Result.ObjectID == "" {
		return r.Result.Value, err
	}
	defer p.Call(context.WithoutCancel(ctx), "Runtime.releaseObject", map[string]any{"objectId": r.Result.ObjectID}, nil)
	body := "function(){return this}"
	if r.Result.Type == "function" {
		body = "function(){return this()}"
	}
	var v remoteResult
	if err := p.Call(ctx, "Runtime.callFunctionOn", map[string]any{"objectId": r.Result.ObjectID, "functionDeclaration": body, "returnByValue": true, "awaitPromise": true}, &v); err != nil {
		return nil, err
	}
	return v.Result.Value, remoteError(v)
}
func normalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("URL is empty")
	}
	if !strings.Contains(raw, "://") && !strings.HasPrefix(raw, "about:") && !strings.HasPrefix(raw, "data:") && !strings.HasPrefix(raw, "file:") && !strings.HasPrefix(strings.ToLower(raw), "javascript:") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "about", "data", "file":
		return raw, nil
	default:
		return "", fmt.Errorf("unsupported navigation scheme %q", u.Scheme)
	}
}
func (p *Page) Navigate(ctx context.Context, raw string) error {
	u, err := normalizeURL(raw)
	if err != nil {
		return err
	}
	events, unsub := p.Client.Subscribe("Page.lifecycleEvent")
	defer unsub()
	if err = p.Call(ctx, "Page.setLifecycleEventsEnabled", map[string]any{"enabled": true}, nil); err != nil {
		return err
	}
	var r struct {
		LoaderID   string `json:"loaderId"`
		FrameID    string `json:"frameId"`
		ErrorText  string `json:"errorText"`
		IsDownload bool   `json:"isDownload"`
	}
	if err = p.Call(ctx, "Page.navigate", map[string]any{"url": u}, &r); err != nil {
		return err
	}
	if r.ErrorText != "" {
		return errors.New(r.ErrorText)
	}
	if r.IsDownload {
		return errors.New("navigation initiated a download")
	}
	if r.LoaderID == "" {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.Client.Done():
			return p.Client.Err()
		case ev, ok := <-events:
			if !ok {
				return errors.New("CDP event stream closed")
			}
			if ev.SessionID != p.SessionID {
				continue
			}
			var e struct {
				LoaderID string `json:"loaderId"`
				FrameID  string `json:"frameId"`
				Name     string `json:"name"`
			}
			if json.Unmarshal(ev.Params, &e) == nil && e.LoaderID == r.LoaderID && e.FrameID == r.FrameID && e.Name == "load" {
				return nil
			}
		}
	}
}
func (p *Page) WaitSelector(ctx context.Context, selector string) error {
	ticker := time.NewTicker(75 * time.Millisecond)
	defer ticker.Stop()
	for {
		v, err := p.Eval(ctx, "Boolean(document.querySelector("+quote(selector)+"))")
		if err != nil {
			return err
		}
		if v == true {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func (p *Page) Screenshot(ctx context.Context) ([]byte, error) {
	var r struct {
		Data string `json:"data"`
	}
	if err := p.Call(ctx, "Page.captureScreenshot", map[string]any{"format": "png", "captureBeyondViewport": false}, &r); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(r.Data)
}
func quote(s string) string { b, _ := json.Marshal(s); return string(b) }

type Ref struct {
	BackendNodeID int    `json:"backendNodeId"`
	DocumentID    int    `json:"documentId"`
	TargetID      string `json:"targetId"`
}
type Refs struct {
	Next  int            `json:"next"`
	Items map[string]Ref `json:"items"`
}
type SnapshotNode struct {
	Ref   string `json:"ref,omitempty"`
	Role  string `json:"role"`
	Name  string `json:"name"`
	Value any    `json:"value,omitempty"`
	Level int    `json:"level,omitempty"`
}

func (p *Page) documentID(ctx context.Context) (int, error) {
	var r struct {
		Root struct {
			BackendNodeID int `json:"backendNodeId"`
		} `json:"root"`
	}
	err := p.Call(ctx, "DOM.getDocument", map[string]any{"depth": 0}, &r)
	return r.Root.BackendNodeID, err
}
func (p *Page) Snapshot(ctx context.Context, interactive bool, refs *Refs) ([]SnapshotNode, error) {
	return p.snapshot(ctx, interactive, refs, 0)
}

// snapshot limits output to the subtree of root (a backend DOM node) when set.
func (p *Page) snapshot(ctx context.Context, interactive bool, refs *Refs, root int) ([]SnapshotNode, error) {
	if refs == nil {
		return nil, errors.New("refs store required")
	}
	doc, err := p.documentID(ctx)
	if err != nil {
		return nil, err
	}
	var r struct {
		Nodes []struct {
			NodeID        string `json:"nodeId"`
			ParentID      string `json:"parentId"`
			Ignored       bool   `json:"ignored"`
			BackendNodeID int    `json:"backendDOMNodeId"`
			Role          struct {
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
	if err = p.Call(ctx, "Accessibility.getFullAXTree", nil, &r); err != nil {
		return nil, err
	}
	inside := map[string]bool{}
	if root != 0 {
		children := map[string][]string{}
		queue := []string{}
		for _, n := range r.Nodes {
			children[n.ParentID] = append(children[n.ParentID], n.NodeID)
			if n.BackendNodeID == root {
				queue = append(queue, n.NodeID)
			}
		}
		if len(queue) == 0 {
			return nil, errors.New("element has no accessibility node")
		}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			if !inside[id] {
				inside[id] = true
				queue = append(queue, children[id]...)
			}
		}
	}
	refs.Items = map[string]Ref{}
	out := []SnapshotNode{}
	for _, n := range r.Nodes {
		if n.Ignored || (root != 0 && !inside[n.NodeID]) {
			continue
		}
		isInteractive := strings.Contains("|button|link|textbox|searchbox|checkbox|radio|combobox|listbox|option|slider|spinbutton|switch|tab|menuitem|menuitemcheckbox|menuitemradio|treeitem|", "|"+n.Role.Value+"|")
		if interactive && !isInteractive {
			continue
		}
		sn := SnapshotNode{Role: n.Role.Value, Name: n.Name.Value, Value: n.Value.Value}
		if n.BackendNodeID != 0 {
			refs.Next++
			key := fmt.Sprintf("e%d", refs.Next)
			sn.Ref = "@" + key
			refs.Items[key] = Ref{n.BackendNodeID, doc, p.TargetID}
		}
		out = append(out, sn)
	}
	return out, nil
}
func (p *Page) element(ctx context.Context, selector string, refs *Refs) (string, error) {
	if strings.HasPrefix(selector, "@") {
		key := strings.TrimPrefix(selector, "@")
		if refs == nil {
			return "", errors.New("snapshot required before using refs")
		}
		ref, ok := refs.Items[key]
		if !ok {
			return "", fmt.Errorf("unknown or stale ref %s; take a new snapshot", selector)
		}
		doc, err := p.documentID(ctx)
		if err != nil {
			return "", err
		}
		if ref.TargetID != p.TargetID || ref.DocumentID != doc {
			return "", errors.New("stale ref after navigation or target change; take a new snapshot")
		}
		var r struct {
			Object struct {
				ObjectID string `json:"objectId"`
			} `json:"object"`
		}
		if err = p.Call(ctx, "DOM.resolveNode", map[string]any{"backendNodeId": ref.BackendNodeID}, &r); err != nil {
			return "", fmt.Errorf("stale ref: %w", err)
		}
		if r.Object.ObjectID == "" {
			return "", errors.New("ref no longer resolves to an element")
		}
		return r.Object.ObjectID, nil
	}
	var r remoteResult
	expr := `(()=>{const nodes=document.querySelectorAll(` + quote(selector) + `);if(nodes.length!==1)throw new Error('selector matched '+nodes.length+' elements; require exactly one');return nodes[0]})()`
	if err := p.Call(ctx, "Runtime.evaluate", map[string]any{"expression": expr}, &r); err != nil {
		return "", err
	}
	if err := remoteError(r); err != nil {
		return "", err
	}
	return r.Result.ObjectID, nil
}
func (p *Page) onElement(ctx context.Context, selector string, refs *Refs, fn string) (any, error) {
	id, err := p.element(ctx, selector, refs)
	if err != nil {
		return nil, err
	}
	defer p.Call(ctx, "Runtime.releaseObject", map[string]any{"objectId": id}, nil)
	var r remoteResult
	err = p.Call(ctx, "Runtime.callFunctionOn", map[string]any{"objectId": id, "functionDeclaration": `function(){if(!this.isConnected)throw new Error('stale ref: element removed');` + fn + `}`, "returnByValue": true, "awaitPromise": true}, &r)
	if err != nil {
		return nil, err
	}
	return r.Result.Value, remoteError(r)
}
func (p *Page) Click(ctx context.Context, selector string, refs *Refs) error {
	v, err := p.onElement(ctx, selector, refs, `if(this.matches(':disabled')||this.getAttribute('aria-disabled')==='true')throw new Error('element is disabled');this.scrollIntoView({block:'center',inline:'center'});const r=this.getBoundingClientRect();const cx=(Math.max(0,r.left)+Math.min(r.right,innerWidth))/2,cy=(Math.max(0,r.top)+Math.min(r.bottom,innerHeight))/2;if(r.width<=0||r.height<=0||cx<0||cy<0||cx>=innerWidth||cy>=innerHeight)throw new Error('element is not visible');const hit=document.elementFromPoint(cx,cy);if(!hit||!(hit===this||this.contains(hit)))throw new Error('element is obscured');return {x:cx,y:cy};`)
	if err != nil {
		return err
	}
	point, ok := v.(map[string]any)
	if !ok {
		return errors.New("missing click coordinates")
	}
	for _, typ := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
		params := map[string]any{"type": typ, "x": point["x"], "y": point["y"]}
		if typ != "mouseMoved" {
			params["button"] = "left"
			params["clickCount"] = 1
		}
		if err = p.Call(ctx, "Input.dispatchMouseEvent", params, nil); err != nil {
			return err
		}
	}
	return nil
}
func (p *Page) Fill(ctx context.Context, selector, text string, refs *Refs) error {
	_, err := p.onElement(ctx, selector, refs, `if(this.matches(':disabled')||this.getAttribute('aria-disabled')==='true'||this.readOnly)throw new Error('element is disabled or readonly');const input=this instanceof HTMLInputElement, area=this instanceof HTMLTextAreaElement;if(!(area||(input&&['text','search','email','url','tel','password','number'].includes(this.type))||this.isContentEditable))throw new Error('element is not editable');this.focus();if(document.activeElement!==this)throw new Error('element cannot be focused');if(this.isContentEditable){const r=document.createRange();r.selectNodeContents(this);const s=getSelection();s.removeAllRanges();s.addRange(r)}else{this.select();}return true;`)
	if err != nil {
		return err
	}
	if text == "" {
		return p.Press(ctx, "Backspace")
	}
	return p.Type(ctx, text)
}
func (p *Page) Type(ctx context.Context, text string) error {
	return p.Call(ctx, "Input.insertText", map[string]any{"text": text}, nil)
}
func (p *Page) Get(ctx context.Context, kind, selector, attr string, refs *Refs) (any, error) {
	switch kind {
	case "url":
		return p.Eval(ctx, "location.href")
	case "title":
		return p.Eval(ctx, "document.title")
	}
	var fn string
	switch kind {
	case "text":
		if selector == "" {
			return p.Eval(ctx, "document.body?.innerText ?? ''")
		}
		fn = "return this.innerText ?? this.textContent"
	case "html":
		fn = "return this.outerHTML"
	case "value":
		fn = "return this.value"
	case "attr", "attribute":
		fn = "return this.getAttribute(" + quote(attr) + ")"
	default:
		return nil, fmt.Errorf("unknown get kind %q", kind)
	}
	return p.onElement(ctx, selector, refs, fn)
}

type keySpec struct {
	Key       string
	Code      string
	VK        int
	Text      string
	Modifiers int
}

func parseKey(raw string) (keySpec, error) {
	parts := strings.Split(raw, "+")
	var k keySpec
	for _, m := range parts[:len(parts)-1] {
		switch strings.ToLower(m) {
		case "alt", "option":
			k.Modifiers |= 1
		case "ctrl", "control":
			k.Modifiers |= 2
		case "meta", "cmd", "command":
			k.Modifiers |= 4
		case "shift":
			k.Modifiers |= 8
		default:
			return k, fmt.Errorf("unknown key modifier %q", m)
		}
	}
	name := parts[len(parts)-1]
	switch strings.ToLower(name) {
	case "enter", "return":
		k.Key = "Enter"
		k.Code = "Enter"
		k.VK = 13
		k.Text = "\r"
	case "tab":
		k.Key = "Tab"
		k.Code = "Tab"
		k.VK = 9
	case "escape", "esc":
		k.Key = "Escape"
		k.Code = "Escape"
		k.VK = 27
	case "backspace":
		k.Key = "Backspace"
		k.Code = "Backspace"
		k.VK = 8
	case "insert":
		k.Key = "Insert"
		k.Code = "Insert"
		k.VK = 45
	case "delete", "del":
		k.Key = "Delete"
		k.Code = "Delete"
		k.VK = 46
	case "arrowleft", "left":
		k.Key = "ArrowLeft"
		k.Code = "ArrowLeft"
		k.VK = 37
	case "arrowup", "up":
		k.Key = "ArrowUp"
		k.Code = "ArrowUp"
		k.VK = 38
	case "arrowright", "right":
		k.Key = "ArrowRight"
		k.Code = "ArrowRight"
		k.VK = 39
	case "arrowdown", "down":
		k.Key = "ArrowDown"
		k.Code = "ArrowDown"
		k.VK = 40
	case "home":
		k.Key = "Home"
		k.Code = "Home"
		k.VK = 36
	case "end":
		k.Key = "End"
		k.Code = "End"
		k.VK = 35
	case "pageup":
		k.Key = "PageUp"
		k.Code = "PageUp"
		k.VK = 33
	case "pagedown":
		k.Key = "PageDown"
		k.Code = "PageDown"
		k.VK = 34
	case "space":
		k.Key = " "
		k.Code = "Space"
		k.VK = 32
		k.Text = " "
	case "shift", "shiftleft", "shiftright":
		k.Key = "Shift"
		k.Code = "ShiftLeft"
		k.VK = 16
	case "ctrl", "control", "controlleft", "controlright":
		k.Key = "Control"
		k.Code = "ControlLeft"
		k.VK = 17
	case "alt", "option", "altleft", "altright":
		k.Key = "Alt"
		k.Code = "AltLeft"
		k.VK = 18
	case "meta", "cmd", "command", "metaleft", "metaright":
		k.Key = "Meta"
		k.Code = "MetaLeft"
		k.VK = 91
	default:
		if strings.HasPrefix(strings.ToLower(name), "f") {
			n, err := strconv.Atoi(name[1:])
			if err == nil && n >= 1 && n <= 12 {
				k.Key = fmt.Sprintf("F%d", n)
				k.Code = k.Key
				k.VK = 111 + n
				return k, nil
			}
		}
		if len([]rune(name)) != 1 {
			return k, fmt.Errorf("unsupported key %q", name)
		}
		k.Key = name
		k.Text = name
		if len(name) == 1 {
			ch := name[0]
			if ch >= 'a' && ch <= 'z' {
				k.Code = "Key" + strings.ToUpper(name)
				k.VK = int(ch - 'a' + 'A')
				if k.Modifiers&8 != 0 {
					k.Key = strings.ToUpper(name)
					k.Text = k.Key
				}
			} else if ch >= 'A' && ch <= 'Z' {
				k.Code = "Key" + name
				k.VK = int(ch)
			} else if ch >= '0' && ch <= '9' {
				k.Code = "Digit" + name
				k.VK = int(ch)
			} else if ch == ' ' {
				k.Code = "Space"
				k.VK = 32
			}
		}
	}
	if k.Modifiers&(1|2|4) != 0 {
		k.Text = ""
	}
	return k, nil
}
func (p *Page) Press(ctx context.Context, key string) error {
	k, err := parseKey(key)
	if err != nil {
		return err
	}
	for _, typ := range []string{"keyDown", "keyUp"} {
		params := map[string]any{"type": typ, "key": k.Key, "code": k.Code, "windowsVirtualKeyCode": k.VK, "nativeVirtualKeyCode": k.VK, "modifiers": k.Modifiers}
		if typ == "keyDown" && k.Text != "" {
			params["text"] = k.Text
			params["unmodifiedText"] = k.Text
		}
		if err = p.Call(ctx, "Input.dispatchKeyEvent", params, nil); err != nil {
			return err
		}
	}
	return nil
}
