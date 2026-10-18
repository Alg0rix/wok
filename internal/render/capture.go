package render

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/alg0rix/wok/internal/browser"
)

// capture uses the session owning device emulation. Chromium uses the calling
// session's device scale factor for screenshots, even though temporary sessions
// can observe the same emulated viewport through Runtime.evaluate.
func (h *handler) capture(ctx context.Context, fallback *browser.Page, format string) ([]byte, error) {
	page := fallback
	if state := h.emulationState; state != nil {
		state.mu.Lock()
		defer state.mu.Unlock()
		if !state.config.Reset {
			page = state.pages[fallback.TargetID]
			if page == nil {
				if err := state.syncPages(ctx, false); err != nil {
					return nil, err
				}
				page = state.pages[fallback.TargetID]
			}
			if page == nil {
				return nil, fmt.Errorf("capture target %q is unavailable", fallback.TargetID)
			}
		}
	}
	params := map[string]any{"format": format, "captureBeyondViewport": false}
	if format == "jpeg" {
		params["quality"] = 65
	}
	var shot struct {
		Data string `json:"data"`
	}
	if err := page.Call(ctx, "Page.captureScreenshot", params, &shot); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(shot.Data)
}
