package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Point is a viewport coordinate in CSS pixels.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

const pointScript = `this.scrollIntoView({block:'center',inline:'center'});const r=this.getBoundingClientRect();const cx=(Math.max(0,r.left)+Math.min(r.right,innerWidth))/2,cy=(Math.max(0,r.top)+Math.min(r.bottom,innerHeight))/2;if(r.width<=0||r.height<=0||cx<0||cy<0||cx>=innerWidth||cy>=innerHeight)throw new Error('element is not visible');const hit=document.elementFromPoint(cx,cy);if(!hit||!(hit===this||this.contains(hit)))throw new Error('element is obscured');return {x:cx,y:cy};`
const enabledScript = `if(this.matches(':disabled')||this.getAttribute('aria-disabled')==='true')throw new Error('element is disabled');`

// ElementPoint scrolls an element into view and returns its unobscured center.
func (p *Page) ElementPoint(ctx context.Context, selector string, refs *Refs, requireEnabled bool) (Point, error) {
	script := pointScript
	if requireEnabled {
		script = enabledScript + script
	}
	v, scope, err := p.onElementIn(ctx, selector, refs, script)
	if err != nil {
		return Point{}, err
	}
	b, _ := json.Marshal(v)
	var point Point
	if err = json.Unmarshal(b, &point); err != nil {
		return Point{}, errors.New("missing element coordinates")
	}
	return scope.toTab(ctx, point)
}

// MouseButton validates a CDP mouse button name.
func MouseButton(name string) (string, int, error) {
	switch name {
	case "", "left":
		return "left", 1, nil
	case "right":
		return "right", 2, nil
	case "middle":
		return "middle", 4, nil
	case "back":
		return "back", 8, nil
	case "forward":
		return "forward", 16, nil
	}
	return "", 0, fmt.Errorf("unknown mouse button %q; use left, right, middle, back or forward", name)
}

// Mouse dispatches one raw mouse event. buttons is the pressed-button bitmask.
func (p *Page) Mouse(ctx context.Context, typ string, at Point, button string, buttons, clickCount int) error {
	params := map[string]any{"type": typ, "x": at.X, "y": at.Y, "buttons": buttons}
	if button != "" {
		params["button"] = button
		params["clickCount"] = clickCount
	}
	return p.Call(ctx, "Input.dispatchMouseEvent", params, nil)
}

// Wheel scrolls at a viewport position.
func (p *Page) Wheel(ctx context.Context, at Point, dx, dy float64) error {
	if err := p.Call(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseWheel", "x": at.X, "y": at.Y, "deltaX": dx, "deltaY": dy}, nil); err != nil {
		return err
	}
	_, _ = p.Eval(ctx, "new Promise(r => setTimeout(r, 25))")
	return nil
}

// ClickButton clicks an element count times (2 for double click) with button.
func (p *Page) ClickButton(ctx context.Context, selector string, refs *Refs, button string, count int) error {
	name, mask, err := MouseButton(button)
	if err != nil {
		return err
	}
	at, err := p.ElementPoint(ctx, selector, refs, true)
	if err != nil {
		return err
	}
	if err = p.Mouse(ctx, "mouseMoved", at, "", 0, 0); err != nil {
		return err
	}
	for i := 1; i <= count; i++ {
		if err = p.Mouse(ctx, "mousePressed", at, name, mask, i); err != nil {
			return err
		}
		if err = p.Mouse(ctx, "mouseReleased", at, name, 0, i); err != nil {
			return err
		}
	}
	return nil
}

// Hover moves the pointer over an element's center.
func (p *Page) Hover(ctx context.Context, selector string, refs *Refs) error {
	at, err := p.ElementPoint(ctx, selector, refs, false)
	if err != nil {
		return err
	}
	return p.Mouse(ctx, "mouseMoved", at, "", 0, 0)
}

// Drag performs a pointer drag between two elements. HTML5 drag and drop is
// intercepted and replayed through Input.dispatchDragEvent, which headless
// Chromium does not synthesize from mouse events alone.
func (p *Page) Drag(ctx context.Context, from, to string, refs *Refs) error {
	start, err := p.ElementPoint(ctx, from, refs, true)
	if err != nil {
		return err
	}
	events, unsub := p.Client.Subscribe("Input.dragIntercepted")
	defer unsub()
	if err = p.Call(ctx, "Input.setInterceptDrags", map[string]any{"enabled": true}, nil); err != nil {
		return err
	}
	defer p.Call(context.WithoutCancel(ctx), "Input.setInterceptDrags", map[string]any{"enabled": false}, nil)
	if err = p.Mouse(ctx, "mouseMoved", start, "", 0, 0); err != nil {
		return err
	}
	if err = p.Mouse(ctx, "mousePressed", start, "left", 1, 1); err != nil {
		return err
	}
	// The target may scroll into view only after the press; resolve it now.
	end, err := p.ElementPoint(ctx, to, refs, false)
	if err != nil {
		_ = p.Mouse(ctx, "mouseReleased", start, "left", 0, 1)
		return err
	}
	for i := 1; i <= 5; i++ {
		step := Point{start.X + (end.X-start.X)*float64(i)/5, start.Y + (end.Y-start.Y)*float64(i)/5}
		if err = p.Mouse(ctx, "mouseMoved", step, "left", 1, 0); err != nil {
			return err
		}
	}
	var data json.RawMessage
	timer := time.NewTimer(80 * time.Millisecond)
	defer timer.Stop()
wait:
	for {
		select {
		case ev := <-events:
			if ev.SessionID != p.Top().SessionID {
				continue
			}
			var e struct {
				Data json.RawMessage `json:"data"`
			}
			if json.Unmarshal(ev.Params, &e) == nil {
				data = e.Data
			}
			break wait
		case <-timer.C:
			break wait
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if data != nil {
		for _, typ := range []string{"dragEnter", "dragOver", "drop"} {
			if err = p.Call(ctx, "Input.dispatchDragEvent", map[string]any{"type": typ, "x": end.X, "y": end.Y, "data": data}, nil); err != nil {
				return err
			}
		}
	} else {
		_, _ = p.Eval(ctx, fmt.Sprintf(`(()=>{const s=document.elementFromPoint(%v,%v),t=document.elementFromPoint(%v,%v);if(!s||!t)return;const src=s.closest('[draggable="true"],a,img')||s;const dt=new DataTransfer();if(!src.dispatchEvent(new DragEvent('dragstart',{bubbles:true,cancelable:true,dataTransfer:dt})))return;t.dispatchEvent(new DragEvent('dragenter',{bubbles:true,cancelable:true,dataTransfer:dt}));t.dispatchEvent(new DragEvent('dragover',{bubbles:true,cancelable:true,dataTransfer:dt}));t.dispatchEvent(new DragEvent('drop',{bubbles:true,cancelable:true,dataTransfer:dt}));src.dispatchEvent(new DragEvent('dragend',{bubbles:true,cancelable:true,dataTransfer:dt}))})()`, start.X, start.Y, end.X, end.Y))
	}
	return p.Mouse(ctx, "mouseReleased", end, "left", 0, 1)
}

// Drop drops files and/or text onto an element as an external drag would.
func (p *Page) Drop(ctx context.Context, selector string, files []string, text string, refs *Refs) error {
	if len(files) == 0 && text == "" {
		return errors.New("drop requires files or --text")
	}
	data := map[string]any{"items": []map[string]string{}, "dragOperationsMask": 1}
	if text != "" {
		data["items"] = []map[string]string{{"mimeType": "text/plain", "data": text}}
	}
	if len(files) > 0 {
		absolute := make([]string, len(files))
		for i, file := range files {
			path, err := filepath.Abs(file)
			if err != nil {
				return err
			}
			if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("drop file is not a regular file: %s", file)
			}
			absolute[i] = path
		}
		data["files"] = absolute
	}
	at, err := p.ElementPoint(ctx, selector, refs, false)
	if err != nil {
		return err
	}
	for _, typ := range []string{"dragEnter", "dragOver", "drop"} {
		if err = p.Call(ctx, "Input.dispatchDragEvent", map[string]any{"type": typ, "x": at.X, "y": at.Y, "data": data}, nil); err != nil {
			return err
		}
	}
	return nil
}

// Select chooses <select> options by value or visible label.
func (p *Page) Select(ctx context.Context, selector string, values []string, refs *Refs) (any, error) {
	if len(values) == 0 {
		return nil, errors.New("select requires at least one value")
	}
	b, _ := json.Marshal(values)
	return p.onElement(ctx, selector, refs, enabledScript+`if(!(this instanceof HTMLSelectElement))throw new Error('element is not a <select>');const want=`+string(b)+`;if(want.length>1&&!this.multiple)throw new Error('multiple values require a multiple <select>');const picked=[];for(const o of this.options){const hit=want.includes(o.value)||want.includes(o.label)||want.includes(o.text.trim());o.selected=hit;if(hit)picked.push(o.value)}if(picked.length!==want.length)throw new Error('option not found: '+want.join(', '));this.dispatchEvent(new Event('input',{bubbles:true}));this.dispatchEvent(new Event('change',{bubbles:true}));return picked;`)
}

// SetChecked clicks a checkbox or radio when its state differs from want.
func (p *Page) SetChecked(ctx context.Context, selector string, want bool, refs *Refs) error {
	state, err := p.onElement(ctx, selector, refs, `const box=this.matches('input[type=checkbox],input[type=radio]');const role=this.getAttribute('role');if(!box&&role!=='checkbox'&&role!=='radio'&&role!=='switch')throw new Error('element is not a checkbox or radio');return box?this.checked:this.getAttribute('aria-checked')==='true';`)
	if err != nil {
		return err
	}
	if state == want {
		return nil
	}
	if !want {
		if radio, _ := p.onElement(ctx, selector, refs, `return this.matches('input[type=radio]')||this.getAttribute('role')==='radio';`); radio == true {
			return errors.New("cannot uncheck a radio button")
		}
	}
	if err = p.ClickButton(ctx, selector, refs, "left", 1); err != nil {
		return err
	}
	state, err = p.onElement(ctx, selector, refs, `return this.matches('input')?this.checked:this.getAttribute('aria-checked')==='true';`)
	if err == nil && state != want {
		err = errors.New("click did not change checked state")
	}
	return err
}

// EvalOn calls a JavaScript function with the element as its argument.
func (p *Page) EvalOn(ctx context.Context, selector, function string, refs *Refs) (any, error) {
	return p.onElement(ctx, selector, refs, `return (`+function+`)(this);`)
}

// SnapshotOf returns the accessibility subtree rooted at an element.
func (p *Page) SnapshotOf(ctx context.Context, selector string, interactive bool, refs *Refs) ([]SnapshotNode, error) {
	scope, id, err := p.resolve(ctx, selector, refs)
	if err != nil {
		return nil, err
	}
	var described struct {
		Node struct {
			BackendNodeID int `json:"backendNodeId"`
		} `json:"node"`
	}
	err = scope.Call(ctx, "DOM.describeNode", map[string]any{"objectId": id}, &described)
	_ = scope.Call(ctx, "Runtime.releaseObject", map[string]any{"objectId": id}, nil)
	if err != nil {
		return nil, err
	}
	return scope.snapshot(ctx, interactive, refs, described.Node.BackendNodeID)
}

// History moves through session history by delta (-1 back, 1 forward).
func (p *Page) History(ctx context.Context, delta int) error {
	p = p.Top()
	var h struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID int `json:"id"`
		} `json:"entries"`
	}
	if err := p.Call(ctx, "Page.getNavigationHistory", nil, &h); err != nil {
		return err
	}
	i := h.CurrentIndex + delta
	if i < 0 || i >= len(h.Entries) {
		if delta < 0 {
			return errors.New("no previous page in history")
		}
		return errors.New("no next page in history")
	}
	return p.waitLoad(ctx, func() error {
		return p.Call(ctx, "Page.navigateToHistoryEntry", map[string]any{"entryId": h.Entries[i].ID}, nil)
	})
}

// Reload reloads the page and waits for its load event.
func (p *Page) Reload(ctx context.Context) error {
	p = p.Top()
	return p.waitLoad(ctx, func() error { return p.Call(ctx, "Page.reload", nil, nil) })
}

// waitLoad runs action and waits for the next main-frame load event.
// Same-document history entries complete without one, so a short quiet
// period after navigatedWithinDocument also counts.
func (p *Page) waitLoad(ctx context.Context, action func() error) error {
	events, unsub := p.Client.Subscribe("Page.loadEventFired")
	defer unsub()
	within, unsubWithin := p.Client.Subscribe("Page.navigatedWithinDocument")
	defer unsubWithin()
	if err := action(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.Client.Done():
			return p.Client.Err()
		case ev := <-events:
			if ev.SessionID == p.SessionID {
				return nil
			}
		case ev := <-within:
			if ev.SessionID == p.SessionID {
				return nil
			}
		}
	}
}

// Key dispatches a single keyDown or keyUp event.
func (p *Page) Key(ctx context.Context, typ, key string) error {
	k, err := parseKey(key)
	if err != nil {
		return err
	}
	params := map[string]any{"type": typ, "key": k.Key, "code": k.Code, "windowsVirtualKeyCode": k.VK, "nativeVirtualKeyCode": k.VK, "modifiers": k.Modifiers}
	if typ == "keyDown" && k.Text != "" {
		params["text"] = k.Text
		params["unmodifiedText"] = k.Text
	}
	return p.Call(ctx, "Input.dispatchKeyEvent", params, nil)
}

// PDF prints the page. Chromium supports printing only in headless mode.
func (p *Page) PDF(ctx context.Context) ([]byte, error) {
	var r struct {
		Data string `json:"data"`
	}
	if err := p.Call(ctx, "Page.printToPDF", map[string]any{"printBackground": true}, &r); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(r.Data)
}

// ElementScreenshot captures an element's bounding box as PNG.
func (p *Page) ElementScreenshot(ctx context.Context, selector string, refs *Refs) ([]byte, error) {
	v, scope, err := p.onElementIn(ctx, selector, refs, `this.scrollIntoView({block:'center',inline:'center'});const r=this.getBoundingClientRect();if(r.width<=0||r.height<=0)throw new Error('element is not visible');return {x:r.left,y:r.top,width:r.width,height:r.height};`)
	if err != nil {
		return nil, err
	}
	clip, _ := v.(map[string]any)
	x, _ := clip["x"].(float64)
	y, _ := clip["y"].(float64)
	// The clip is in tab document coordinates: frame offset plus tab scroll.
	at, err := scope.toTab(ctx, Point{x, y})
	if err != nil {
		return nil, err
	}
	var scroll remoteResult
	if err = p.Top().Call(ctx, "Runtime.evaluate", map[string]any{"expression": "[scrollX,scrollY]", "returnByValue": true}, &scroll); err != nil {
		return nil, err
	}
	if xy, ok := scroll.Result.Value.([]any); ok && len(xy) == 2 {
		sx, _ := xy[0].(float64)
		sy, _ := xy[1].(float64)
		at = Point{at.X + sx, at.Y + sy}
	}
	clip["x"], clip["y"], clip["scale"] = at.X, at.Y, 1
	var r struct {
		Data string `json:"data"`
	}
	if err = p.Call(ctx, "Page.captureScreenshot", map[string]any{"format": "png", "clip": clip, "captureBeyondViewport": true}, &r); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(r.Data)
}

// LocatorScript defines __wokLocator(el), shared by generate-locator and the
// action recorder. It prefers stable attributes, then a short CSS path.
const LocatorScript = `function __wokLocator(el){const q=s=>{try{return document.querySelectorAll(s).length===1}catch{return false}};const esc=CSS.escape;if(el.id&&q('#'+esc(el.id)))return '#'+esc(el.id);for(const a of ['data-testid','data-test','data-qa','name','aria-label','placeholder','title','alt']){const v=el.getAttribute&&el.getAttribute(a);if(v){const s=el.localName+'['+a+'='+JSON.stringify(v)+']';if(q(s))return s}}const parts=[];let n=el;while(n&&n.nodeType===1&&n!==document.documentElement){let part=n.localName;if(n.id&&q('#'+esc(n.id))){parts.unshift('#'+esc(n.id));break}const sib=n.parentElement?[...n.parentElement.children].filter(c=>c.localName===n.localName):[];if(sib.length>1)part+=':nth-of-type('+(sib.indexOf(n)+1)+')';parts.unshift(part);const s=parts.join(' > ');if(q(s))return s;n=n.parentElement}return parts.join(' > ')}`

// Locator returns a unique CSS selector plus accessible role and name.
func (p *Page) Locator(ctx context.Context, selector string, refs *Refs) (any, error) {
	return p.onElement(ctx, selector, refs, LocatorScript+`;const css=__wokLocator(this);const role=this.getAttribute('role')||({a:'link',button:'button',select:'combobox',textarea:'textbox',h1:'heading',h2:'heading',h3:'heading',img:'img'})[this.localName]||(this.localName==='input'?({checkbox:'checkbox',radio:'radio',button:'button',submit:'button'})[this.type]||'textbox':'');const name=(this.getAttribute('aria-label')||this.innerText||this.value||this.getAttribute('placeholder')||this.getAttribute('alt')||'').trim().slice(0,80);return {css,role,name,wok:'wok click '+JSON.stringify(css)};`)
}

// Highlight draws a labelled outline over an element until hidden.
func (p *Page) Highlight(ctx context.Context, selector string, refs *Refs) error {
	_, err := p.onElement(ctx, selector, refs, `const r=this.getBoundingClientRect();const d=document.createElement('div');d.setAttribute('data-wok-highlight','');Object.assign(d.style,{position:'absolute',left:(r.left+scrollX-2)+'px',top:(r.top+scrollY-2)+'px',width:(r.width+4)+'px',height:(r.height+4)+'px',outline:'2px solid #2a78d6',background:'rgba(42,120,214,.15)',zIndex:2147483647,pointerEvents:'none',boxSizing:'border-box'});document.documentElement.appendChild(d);return true;`)
	return err
}

// HideHighlights removes every Wok highlight overlay from the page.
func (p *Page) HideHighlights(ctx context.Context) error {
	_, err := p.Eval(ctx, `document.querySelectorAll('[data-wok-highlight]').forEach(e=>e.remove()),true`)
	return err
}

// StorageState mirrors Playwright's storage state: every cookie in the browser
// plus localStorage for each open page origin.
type StorageState struct {
	Cookies []map[string]any `json:"cookies"`
	Origins []OriginStorage  `json:"origins"`
}
type OriginStorage struct {
	Origin       string      `json:"origin"`
	LocalStorage []NameValue `json:"localStorage"`
}
type NameValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SaveState collects cookies and localStorage from every page target.
func SaveState(ctx context.Context, pages []*Page) (*StorageState, error) {
	if len(pages) == 0 {
		return nil, errors.New("no page to read storage from")
	}
	state := &StorageState{Cookies: []map[string]any{}, Origins: []OriginStorage{}}
	var cookies struct {
		Cookies []map[string]any `json:"cookies"`
	}
	if err := pages[0].Call(ctx, "Storage.getCookies", nil, &cookies); err != nil {
		return nil, err
	}
	if cookies.Cookies != nil {
		state.Cookies = cookies.Cookies
	}
	seen := map[string]bool{}
	for _, p := range pages {
		v, err := p.Eval(ctx, `(()=>{try{const s=localStorage;return {origin:location.origin,items:Array.from({length:s.length},(_,i)=>{const k=s.key(i);return {name:k,value:s.getItem(k)}})}}catch{return null}})()`)
		if err != nil || v == nil {
			continue
		}
		b, _ := json.Marshal(v)
		var o struct {
			Origin string      `json:"origin"`
			Items  []NameValue `json:"items"`
		}
		if json.Unmarshal(b, &o) != nil || o.Origin == "" || o.Origin == "null" || seen[o.Origin] {
			continue
		}
		seen[o.Origin] = true
		if o.Items == nil {
			o.Items = []NameValue{}
		}
		state.Origins = append(state.Origins, OriginStorage{o.Origin, o.Items})
	}
	return state, nil
}

// LoadState sets cookies and writes localStorage per origin through DOMStorage,
// which does not require a page to be open at that origin.
func (p *Page) LoadState(ctx context.Context, state *StorageState) error {
	cookies := make([]map[string]any, 0, len(state.Cookies))
	for _, c := range state.Cookies {
		cookie := map[string]any{}
		for _, k := range []string{"name", "value", "domain", "path", "expires", "httpOnly", "secure", "sameSite"} {
			if v, ok := c[k]; ok {
				cookie[k] = v
			}
		}
		if e, ok := cookie["expires"].(float64); ok && e <= 0 {
			delete(cookie, "expires")
		}
		cookies = append(cookies, cookie)
	}
	if len(cookies) > 0 {
		if err := p.Call(ctx, "Storage.setCookies", map[string]any{"cookies": cookies}, nil); err != nil {
			return err
		}
	}
	if len(state.Origins) == 0 {
		return nil
	}
	if err := p.Call(ctx, "DOMStorage.enable", nil, nil); err != nil {
		return err
	}
	defer p.Call(context.WithoutCancel(ctx), "DOMStorage.disable", nil, nil)
	for _, o := range state.Origins {
		origin := strings.TrimSuffix(o.Origin, "/")
		// First-party storage keys are the origin plus "/"; older Chromium
		// identifies storage by security origin instead.
		ids := []map[string]any{{"storageKey": origin + "/", "isLocalStorage": true}, {"securityOrigin": origin, "isLocalStorage": true}}
		for _, item := range o.LocalStorage {
			var err error
			for _, id := range ids {
				if err = p.Call(ctx, "DOMStorage.setDOMStorageItem", map[string]any{"storageId": id, "key": item.Name, "value": item.Value}, nil); err == nil {
					ids = []map[string]any{id}
					break
				}
			}
			if err != nil {
				return fmt.Errorf("localStorage %s: %w", origin, err)
			}
		}
	}
	return nil
}
