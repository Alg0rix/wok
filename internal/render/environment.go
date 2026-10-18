package render

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/alg0rix/wok/internal/browser"
)

type environmentConfig struct {
	Media   string `json:"media"`
	Offline bool   `json:"offline"`
}
type environmentPatch struct {
	Media   *string `json:"media,omitempty"`
	Offline *bool   `json:"offline,omitempty"`
}

func decodeEnvironment(r io.Reader) (environmentPatch, error) {
	var patch environmentPatch
	data, err := io.ReadAll(io.LimitReader(r, 4097))
	if err != nil {
		return patch, err
	}
	if len(data) > 4096 {
		return patch, errors.New("environment configuration exceeds 4096 bytes")
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(data, &fields); err != nil {
		return patch, err
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return patch, errors.New("environment fields cannot be null")
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&patch); err != nil {
		return patch, err
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		return patch, errors.New("expected one environment object")
	}
	if patch.Media == nil && patch.Offline == nil {
		return patch, errors.New("media or offline is required")
	}
	if patch.Media != nil && *patch.Media != "dark" && *patch.Media != "light" && *patch.Media != "no-preference" {
		return patch, errors.New("media must be dark, light or no-preference")
	}
	return patch, nil
}
func (s *emulationState) active() bool {
	return !s.config.Reset || s.environment.Offline || (s.environment.Media != "" && s.environment.Media != "no-preference")
}
func (s *emulationState) loadEnvironment(readyFile string) error {
	s.environment.Media = "no-preference"
	if readyFile == "" {
		return nil
	}
	s.environmentFile = filepath.Join(filepath.Dir(readyFile), "environment.json")
	f, err := os.Open(s.environmentFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	patch, err := decodeEnvironment(f)
	if err != nil {
		return fmt.Errorf("invalid %s: %w", s.environmentFile, err)
	}
	if patch.Media != nil {
		s.environment.Media = *patch.Media
	}
	if patch.Offline != nil {
		s.environment.Offline = *patch.Offline
	}
	return nil
}
func (s *emulationState) applyEnvironment(ctx context.Context, p *browser.Page) error {
	features := []map[string]string{}
	if s.environment.Media != "" && s.environment.Media != "no-preference" {
		features = append(features, map[string]string{"name": "prefers-color-scheme", "value": s.environment.Media})
	}
	if err := p.Call(ctx, "Emulation.setEmulatedMedia", map[string]any{"features": features}, nil); err != nil {
		return err
	}
	return p.Call(ctx, "Network.emulateNetworkConditions", map[string]any{"offline": s.environment.Offline, "latency": 0, "downloadThroughput": -1, "uploadThroughput": -1}, nil)
}
func (s *emulationState) updateEnvironment(ctx context.Context, patch environmentPatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.environment
	if patch.Media != nil {
		s.environment.Media = *patch.Media
	}
	if patch.Offline != nil {
		s.environment.Offline = *patch.Offline
	}
	applyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := s.syncPages(applyCtx, true)
	cancel()
	if err == nil && s.environmentFile != "" {
		var data []byte
		data, err = json.Marshal(s.environment)
		if err == nil {
			err = publish(s.environmentFile, data)
		}
	}
	if err != nil {
		s.environment = previous
		rollbackCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		rollbackErr := s.syncPages(rollbackCtx, true)
		stop()
		if !s.active() {
			s.detach()
		}
		if rollbackErr != nil {
			return fmt.Errorf("update environment: %w (restore: %v)", err, rollbackErr)
		}
		return err
	}
	if !s.active() {
		s.detach()
	}
	return nil
}
func (h *handler) environment(w http.ResponseWriter, r *http.Request) {
	s := h.emulationState
	if s == nil {
		http.Error(w, "environment unavailable", 503)
		return
	}
	if r.Method == http.MethodPost {
		patch, err := decodeEnvironment(http.MaxBytesReader(w, r.Body, 4096))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err = s.updateEnvironment(r.Context(), patch); err != nil {
			fail(w, err)
			return
		}
	}
	s.mu.Lock()
	config := s.environment
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(config)
}
