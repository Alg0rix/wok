package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/alg0rix/wok/internal/browser"
	"github.com/alg0rix/wok/internal/session"
)

func screenshot(ctx context.Context, state *session.State, p *browser.Page) ([]byte, error) {
	if state.RenderURL == "" {
		return p.Screenshot(ctx)
	}
	// The resident controller owns persistent emulation sessions. Capturing through
	// that session preserves device scale as well as the logical viewport.
	endpoint := state.RenderURL + "/render/screenshot.png?tab=" + url.QueryEscape(p.TargetID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	authorize(req, state)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("screenshot: HTTP %d: %s", resp.StatusCode, body)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (64<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 64<<20 {
		return nil, fmt.Errorf("screenshot exceeds 64 MiB")
	}
	return data, nil
}
