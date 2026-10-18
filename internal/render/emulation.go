package render

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/alg0rix/wok/internal/browser"
	"github.com/alg0rix/wok/internal/cdp"
)

// emulationConfig is profile-wide. Its sessions must stay attached because
// Chromium clears device metrics when their owning CDP session detaches.
type emulationConfig struct {
	Width             int     `json:"width,omitempty"`
	Height            int     `json:"height,omitempty"`
	DeviceScaleFactor float64 `json:"deviceScaleFactor,omitempty"`
	Mobile            bool    `json:"mobile"`
	Reset             bool    `json:"reset,omitempty"`
}

func decodeEmulation(r io.Reader) (emulationConfig, error) {
	var raw struct {
		Width             *int     `json:"width"`
		Height            *int     `json:"height"`
		DeviceScaleFactor *float64 `json:"deviceScaleFactor"`
		Mobile            *bool    `json:"mobile"`
		Reset             *bool    `json:"reset"`
	}
	data, err := io.ReadAll(io.LimitReader(r, 4097))
	if err != nil {
		return emulationConfig{}, err
	}
	if len(data) > 4096 {
		return emulationConfig{}, errors.New("emulation configuration exceeds 4096 bytes")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return emulationConfig{}, err
	}
	for name, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return emulationConfig{}, fmt.Errorf("%s cannot be null", name)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return emulationConfig{}, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return emulationConfig{}, errors.New("expected one emulation object")
	}
	if raw.Reset != nil && *raw.Reset {
		if raw.Width != nil || raw.Height != nil || raw.DeviceScaleFactor != nil || raw.Mobile != nil {
			return emulationConfig{}, errors.New("reset cannot include device settings")
		}
		return emulationConfig{Reset: true}, nil
	}
	if raw.Width == nil || raw.Height == nil || raw.DeviceScaleFactor == nil || raw.Mobile == nil {
		return emulationConfig{}, errors.New("width, height, deviceScaleFactor and mobile are required")
	}
	if *raw.Width < 1 || *raw.Width > 8192 || *raw.Height < 1 || *raw.Height > 8192 {
		return emulationConfig{}, errors.New("viewport dimensions must be 1..8192")
	}
	dpr := *raw.DeviceScaleFactor
	if math.IsNaN(dpr) || math.IsInf(dpr, 0) || dpr < 0.5 || dpr > 4 {
		return emulationConfig{}, errors.New("deviceScaleFactor must be 0.5..4")
	}
	if float64(*raw.Width)*float64(*raw.Height)*dpr*dpr > 64*1024*1024 {
		return emulationConfig{}, errors.New("viewport exceeds 64 million device pixels; reduce dimensions or deviceScaleFactor")
	}
	return emulationConfig{Width: *raw.Width, Height: *raw.Height, DeviceScaleFactor: dpr, Mobile: *raw.Mobile}, nil
}
func (c emulationConfig) json() ([]byte, error) {
	if c.Reset {
		return []byte(`{"reset":true}`), nil
	}
	return json.Marshal(c)
}

type emulationState struct {
	mu              sync.Mutex
	client          *cdp.Client
	config          emulationConfig
	file            string
	pages           map[string]*browser.Page
	userAgent       string
	cancel          context.CancelFunc
	done            chan struct{}
	environment     environmentConfig
	environmentFile string
}

func newEmulation(ctx context.Context, c *cdp.Client, readyFile string) (*emulationState, error) {
	ctx, cancel := context.WithCancel(ctx)
	s := &emulationState{client: c, config: emulationConfig{Reset: true}, pages: make(map[string]*browser.Page), cancel: cancel, done: make(chan struct{})}
	if readyFile != "" {
		s.file = filepath.Join(filepath.Dir(readyFile), "viewport.json")
		f, err := os.Open(s.file)
		if err == nil {
			config, decodeErr := decodeEmulation(f)
			f.Close()
			if decodeErr != nil {
				cancel()
				return nil, fmt.Errorf("invalid %s: %w", s.file, decodeErr)
			}
			s.config = config
		} else if !errors.Is(err, os.ErrNotExist) {
			cancel()
			return nil, err
		}
	}
	if err := s.loadEnvironment(readyFile); err != nil {
		cancel()
		return nil, err
	}
	if s.active() {
		applyCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		err := s.syncPages(applyCtx, true)
		stop()
		if err != nil {
			s.detach()
			cancel()
			return nil, fmt.Errorf("restore viewport: %w", err)
		}
	}
	go s.poll(ctx)
	return s, nil
}
func (s *emulationState) poll(ctx context.Context) {
	defer close(s.done)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.client.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			if s.active() {
				callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				s.syncPages(callCtx, false)
				cancel()
			}
			s.mu.Unlock()
		}
	}
}

// syncPages is called under mu, except before the polling goroutine starts.
func (s *emulationState) syncPages(ctx context.Context, reapply bool) error {
	targets, err := browser.Targets(ctx, s.client)
	if err != nil {
		return err
	}
	alive := make(map[string]bool)
	for _, t := range targets {
		if t.Type != "page" {
			continue
		}
		alive[t.TargetID] = true
		p := s.pages[t.TargetID]
		if p == nil {
			p, err = browser.Attach(ctx, s.client, t.TargetID)
			if err != nil {
				return err
			}
			s.pages[t.TargetID] = p
		} else if !reapply {
			continue
		}
		if err = s.apply(ctx, p, s.config); err != nil {
			// A failed attachment must be retried on the next poll.
			s.client.Call(ctx, "", "Target.detachFromTarget", map[string]any{"sessionId": p.SessionID}, nil)
			delete(s.pages, t.TargetID)
			return err
		}
	}
	for id, p := range s.pages {
		if !alive[id] {
			s.client.Call(ctx, "", "Target.detachFromTarget", map[string]any{"sessionId": p.SessionID}, nil)
			delete(s.pages, id)
		}
	}
	return nil
}
func (s *emulationState) apply(ctx context.Context, p *browser.Page, c emulationConfig) error {
	if c.Reset {
		if err := p.Call(ctx, "Emulation.clearDeviceMetricsOverride", nil, nil); err != nil {
			return err
		}
	} else {
		params := map[string]any{"width": c.Width, "height": c.Height, "deviceScaleFactor": c.DeviceScaleFactor, "mobile": c.Mobile, "screenWidth": c.Width, "screenHeight": c.Height}
		if err := p.Call(ctx, "Emulation.setDeviceMetricsOverride", params, nil); err != nil {
			return err
		}
	}
	if err := p.Call(ctx, "Emulation.setTouchEmulationEnabled", map[string]any{"enabled": !c.Reset && c.Mobile}, nil); err != nil {
		return err
	}
	ua := ""
	if !c.Reset && c.Mobile {
		if s.userAgent == "" {
			var version struct {
				Product string `json:"product"`
			}
			if err := s.client.Call(ctx, "", "Browser.getVersion", nil, &version); err != nil {
				return err
			}
			product := strings.SplitN(version.Product, "/", 2)
			if len(product) != 2 || product[1] == "" {
				return errors.New("browser returned invalid version")
			}
			s.userAgent = "Mozilla/5.0 (Linux; Android 13; Wok Mobile) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + product[1] + " Mobile Safari/537.36"
		}
		ua = s.userAgent
	}
	if err := p.Call(ctx, "Emulation.setUserAgentOverride", map[string]any{"userAgent": ua}, nil); err != nil {
		return err
	}
	return s.applyEnvironment(ctx, p)
}
func (s *emulationState) update(ctx context.Context, next emulationConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.config
	s.config = next
	applyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := s.syncPages(applyCtx, true)
	cancel()
	if err == nil && s.file != "" {
		var data []byte
		data, err = next.json()
		if err == nil {
			err = publish(s.file, data)
		}
	}
	if err != nil {
		s.config = previous
		restoreCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		restoreErr := s.syncPages(restoreCtx, true)
		stop()
		if !s.active() {
			s.detach()
		}
		if restoreErr != nil {
			return fmt.Errorf("update viewport: %w (restore previous settings: %v)", err, restoreErr)
		}
		return err
	}
	if !s.active() {
		s.detach()
	}
	return nil
}

// detach uses one shared deadline, so browser shutdown cannot delay each page.
func (s *emulationState) detach() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for id, p := range s.pages {
		s.client.Call(ctx, "", "Target.detachFromTarget", map[string]any{"sessionId": p.SessionID}, nil)
		delete(s.pages, id)
	}
}
func (s *emulationState) close() { s.cancel(); <-s.done; s.mu.Lock(); defer s.mu.Unlock(); s.detach() }
func (h *handler) emulation(w http.ResponseWriter, r *http.Request) {
	if h.emulationState == nil {
		http.Error(w, "emulation unavailable", 503)
		return
	}
	s := h.emulationState
	if r.Method == http.MethodPost {
		c, err := decodeEmulation(http.MaxBytesReader(w, r.Body, 4096))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err = s.update(r.Context(), c); err != nil {
			fail(w, err)
			return
		}
	}
	s.mu.Lock()
	data, err := s.config.json()
	s.mu.Unlock()
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}
