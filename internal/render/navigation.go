package render

import (
	"context"
	"time"
)

// prepareNavigation attaches recorders before a document can execute startup
// scripts or issue requests, and applies profile settings to newly opened tabs.
func (h *handler) prepareNavigation(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if h.observer != nil {
		if err := h.observer.Sync(ctx); err != nil {
			return err
		}
	}
	if state := h.emulationState; state != nil {
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.active() {
			return state.syncPages(ctx, false)
		}
	}
	return nil
}
