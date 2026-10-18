package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// FrameSeparator chains an <iframe> selector and a selector inside that frame,
// for example `#checkout >> button[type=submit]`.
const FrameSeparator = " >> "

// frameScope links a frame-scoped Page to the tab that owns it.
type frameScope struct {
	top    *Page // tab page: receives Input and screenshot calls
	parent *Page // scope containing the <iframe> element
	owner  int   // backend node ID of the <iframe> in parent
}

// Top returns the tab-level page for a frame-scoped page, or p itself.
func (p *Page) Top() *Page {
	if p.frame != nil {
		return p.frame.top
	}
	return p
}

// Frame returns a view of the page scoped to the document inside the <iframe>
// matched by selector (CSS or @ref). Chain nested frames with FrameSeparator.
// Same-process and out-of-process (cross-site) frames are both supported.
func (p *Page) Frame(ctx context.Context, selector string, refs *Refs) (*Page, error) {
	scope := p
	for _, part := range strings.Split(selector, FrameSeparator) {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, errors.New("empty frame selector")
		}
		q, id, err := scope.resolve(ctx, part, refs)
		if err != nil {
			return nil, fmt.Errorf("frame %s: %w", part, err)
		}
		var d struct {
			Node struct {
				FrameID       string `json:"frameId"`
				BackendNodeID int    `json:"backendNodeId"`
			} `json:"node"`
		}
		err = q.Call(ctx, "DOM.describeNode", map[string]any{"objectId": id}, &d)
		_ = q.Call(ctx, "Runtime.releaseObject", map[string]any{"objectId": id}, nil)
		if err != nil {
			return nil, err
		}
		if d.Node.FrameID == "" {
			return nil, fmt.Errorf("%s is not an iframe or frame element", part)
		}
		if scope, err = q.childFrame(ctx, d.Node.FrameID, d.Node.BackendNodeID); err != nil {
			return nil, fmt.Errorf("frame %s: %w", part, err)
		}
	}
	return scope, nil
}

// childFrame scopes to frameID, whose <iframe> element owner lives in p.
func (p *Page) childFrame(ctx context.Context, frameID string, owner int) (*Page, error) {
	top := p.Top()
	child := &Page{Client: p.Client, TargetID: top.TargetID, FrameID: frameID, frame: &frameScope{top: top, parent: p, owner: owner}}
	session, err := top.frameSession(ctx, frameID)
	if err != nil {
		return nil, err
	}
	if session != "" {
		// An out-of-process frame is the main frame of its own target.
		child.SessionID = session
		return child, nil
	}
	child.SessionID = p.SessionID
	if child.ContextID, err = p.frameContext(ctx, frameID); err != nil {
		return nil, err
	}
	return child, nil
}

// frameSession attaches to frameID when it is an out-of-process iframe target
// and returns its session, or "" for a frame rendered in its parent's process.
func (p *Page) frameSession(ctx context.Context, frameID string) (string, error) {
	if s, ok := p.sessions[frameID]; ok {
		return s, nil
	}
	targets, err := Targets(ctx, p.Client)
	if err != nil {
		return "", err
	}
	for _, t := range targets {
		if t.Type != "iframe" || t.TargetID != frameID {
			continue
		}
		var r struct {
			SessionID string `json:"sessionId"`
		}
		if err = p.Client.Call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": frameID, "flatten": true}, &r); err != nil {
			return "", err
		}
		if p.sessions == nil {
			p.sessions = map[string]string{}
		}
		p.sessions[frameID] = r.SessionID
		return r.SessionID, nil
	}
	return "", nil
}

// frameContext returns the main-world execution context of a frame rendered
// in this session. Runtime.enable reports existing contexts before replying.
func (p *Page) frameContext(ctx context.Context, frameID string) (int, error) {
	events, unsub := p.Client.SubscribeBuffer("Runtime.executionContextCreated", 1024)
	defer unsub()
	if err := p.Client.Call(ctx, p.SessionID, "Runtime.enable", nil, nil); err != nil {
		return 0, err
	}
	defer p.Client.Call(context.WithoutCancel(ctx), p.SessionID, "Runtime.disable", nil, nil)
	for {
		select {
		case ev := <-events:
			if ev.SessionID != p.SessionID {
				continue
			}
			var e struct {
				Context struct {
					ID      int `json:"id"`
					AuxData struct {
						FrameID   string `json:"frameId"`
						IsDefault bool   `json:"isDefault"`
					} `json:"auxData"`
				} `json:"context"`
			}
			if json.Unmarshal(ev.Params, &e) == nil && e.Context.AuxData.FrameID == frameID && e.Context.AuxData.IsDefault {
				return e.Context.ID, nil
			}
		default:
			return 0, errors.New("frame has no JavaScript context; it may still be loading")
		}
	}
}

// frameByID scopes the tab to any frame in it, for refs from a snapshot.
func (p *Page) frameByID(ctx context.Context, frameID string) (*Page, error) {
	top := p.Top()
	if frameID == "" || frameID == top.TargetID {
		return top, nil
	}
	parentID, err := top.frameParent(ctx, frameID)
	if err != nil {
		return nil, err
	}
	parent, err := top.frameByID(ctx, parentID)
	if err != nil {
		return nil, err
	}
	var o struct {
		BackendNodeID int `json:"backendNodeId"`
	}
	if err = parent.Call(ctx, "DOM.getFrameOwner", map[string]any{"frameId": frameID}, &o); err != nil {
		return nil, fmt.Errorf("frame owner: %w", err)
	}
	return parent.childFrame(ctx, frameID, o.BackendNodeID)
}

// frameParent finds the parent frame ID of frameID in the tab, searching
// out-of-process iframe targets and every attached session's frame tree.
func (p *Page) frameParent(ctx context.Context, frameID string) (string, error) {
	targets, err := Targets(ctx, p.Client)
	if err != nil {
		return "", err
	}
	sessions := []string{p.SessionID}
	for _, t := range targets {
		if t.Type != "iframe" {
			continue
		}
		if t.TargetID == frameID {
			return t.ParentFrameID, nil
		}
		s, err := p.frameSession(ctx, t.TargetID)
		if err == nil && s != "" {
			sessions = append(sessions, s)
		}
	}
	type tree struct {
		Frame struct {
			ID       string `json:"id"`
			ParentID string `json:"parentId"`
		} `json:"frame"`
		ChildFrames []tree `json:"childFrames"`
	}
	var find func(t tree) (string, bool)
	find = func(t tree) (string, bool) {
		if t.Frame.ID == frameID {
			return t.Frame.ParentID, true
		}
		for _, c := range t.ChildFrames {
			if id, ok := find(c); ok {
				return id, true
			}
		}
		return "", false
	}
	for _, s := range sessions {
		var r struct {
			FrameTree tree `json:"frameTree"`
		}
		if p.Client.Call(ctx, s, "Page.getFrameTree", nil, &r) != nil {
			continue
		}
		if id, ok := find(r.FrameTree); ok && id != "" {
			return id, nil
		}
	}
	return "", errors.New("frame no longer exists; take a new snapshot")
}

// frameOffset is the position of this frame's viewport in tab coordinates.
func (p *Page) frameOffset(ctx context.Context) (Point, error) {
	if p.frame == nil {
		return Point{}, nil
	}
	base, err := p.frame.parent.frameOffset(ctx)
	if err != nil {
		return Point{}, err
	}
	var r struct {
		Object struct {
			ObjectID string `json:"objectId"`
		} `json:"object"`
	}
	parent := p.frame.parent
	if err = parent.Call(ctx, "DOM.resolveNode", map[string]any{"backendNodeId": p.frame.owner}, &r); err != nil {
		return Point{}, fmt.Errorf("frame element: %w", err)
	}
	defer parent.Call(ctx, "Runtime.releaseObject", map[string]any{"objectId": r.Object.ObjectID}, nil)
	var v remoteResult
	err = parent.Call(ctx, "Runtime.callFunctionOn", map[string]any{"objectId": r.Object.ObjectID, "functionDeclaration": `function(){const r=this.getBoundingClientRect(),s=getComputedStyle(this);return {x:r.left+this.clientLeft+parseFloat(s.paddingLeft),y:r.top+this.clientTop+parseFloat(s.paddingTop)}}`, "returnByValue": true}, &v)
	if err == nil {
		err = remoteError(v)
	}
	if err != nil {
		return Point{}, err
	}
	m, _ := v.Result.Value.(map[string]any)
	x, _ := m["x"].(float64)
	y, _ := m["y"].(float64)
	return Point{base.X + x, base.Y + y}, nil
}

// toTab converts a point in this frame's viewport to tab coordinates.
func (p *Page) toTab(ctx context.Context, at Point) (Point, error) {
	off, err := p.frameOffset(ctx)
	if err != nil {
		return Point{}, err
	}
	tab := Point{at.X + off.X, at.Y + off.Y}
	if p.SessionID != p.Top().SessionID {
		if err = p.awaitPointer(ctx, tab); err != nil {
			return Point{}, err
		}
	}
	return tab, nil
}

// awaitPointer hovers at (tab coordinates) until this out-of-process frame
// receives pointer events. The browser routes input to a freshly loaded
// frame only after its first presentation; until then clicks land in the
// parent. A frame that has seen pointer input is not hovered again.
func (p *Page) awaitPointer(ctx context.Context, at Point) error {
	const probe = `(()=>{if(window.__wokPointer)return true;if(!window.__wokPointerHook){window.__wokPointerHook=true;const mark=()=>{window.__wokPointer=true};for(const t of ['pointermove','pointerdown','mousemove'])addEventListener(t,mark,{capture:true,passive:true})}return false})()`
	for i := 0; ; i++ {
		var v remoteResult
		if err := p.Call(ctx, "Runtime.evaluate", map[string]any{"expression": probe, "returnByValue": true}, &v); err != nil {
			return err
		}
		if v.Result.Value == true {
			return nil
		}
		if i == 40 {
			return errors.New("iframe does not receive input yet; retry when it has rendered")
		}
		// Alternate by a fraction of a pixel so every move is a real move.
		x := at.X + float64(i%2)*0.5
		if err := p.Top().Call(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": x, "y": at.Y}, nil); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
