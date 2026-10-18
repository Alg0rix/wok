package render

import (
	"context"
	"errors"
	"fmt"

	"github.com/alg0rix/wok/internal/browser"
	"github.com/alg0rix/wok/internal/session"
)

// mcpScreenshot follows the CLI's persisted tab selection under the same profile
// lock. Closed implicit selections recover to an existing page; explicit tabs
// never recover, and a screenshot never creates a tab.
func (h *handler) mcpScreenshot(ctx context.Context, explicit string) ([]byte, error) {
	profile := h.options.Profile
	if profile == "" {
		profile = "default"
	}
	store, err := session.Open("", profile)
	if err != nil {
		return nil, err
	}
	unlock, err := store.Lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	state, err := store.Load()
	if err != nil {
		return nil, err
	}
	if h.endpoint != "" && state.Endpoint != h.endpoint {
		return nil, errors.New("profile browser changed; reconnect to its current controller")
	}
	target := state.TargetID
	if explicit != "" {
		target = explicit
	}
	targets, err := browser.Targets(ctx, h.client)
	if err != nil {
		return nil, err
	}
	found, first := false, ""
	for _, t := range targets {
		if t.Type != "page" {
			continue
		}
		if first == "" {
			first = t.TargetID
		}
		if t.TargetID == target {
			found = true
		}
	}
	if !found {
		if explicit != "" {
			return nil, fmt.Errorf("unknown tab %q", explicit)
		}
		target = first
	}
	if target == "" {
		return nil, errors.New("no browser page is available to capture")
	}
	p, err := browser.Attach(ctx, h.client, target)
	if err != nil {
		return nil, err
	}
	defer p.Close()
	if explicit == "" && state.TargetID != target {
		state.TargetID = target
		if err = store.Save(state); err != nil {
			return nil, err
		}
	}
	return h.capture(ctx, p, "png")
}
