package observe

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// Handler serves JSON recorder state. Its owner must enforce viewer authentication.
func (m *Manager) Handler() http.Handler { return http.HandlerFunc(m.serveHTTP) }
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func (m *Manager) serveHTTP(w http.ResponseWriter, r *http.Request) {
	route := r.URL.Path
	if route == "/runtime/dialog-policy" {
		m.servePolicy(w, r)
		return
	}
	if m.serveDevtools(w, r) {
		return
	}
	switch route {
	case "/runtime/console", "/runtime/errors", "/runtime/network", "/runtime/downloads", "/runtime/dialogs", "/runtime/network-state":
	default:
		writeJSON(w, 404, map[string]string{"error": "unknown Wok runtime route"})
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	m.mu.Lock()
	snapshot := func(status int, value any) {
		data, err := json.Marshal(value)
		m.mu.Unlock()
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_, _ = w.Write(append(data, '\n'))
	}
	id := r.URL.Query().Get("tab")
	if route == "/runtime/downloads" {
		if id != "" {
			if _, err := m.tabLocked(id); err != nil {
				snapshot(404, map[string]string{"error": err.Error()})
				return
			}
		}
		entries := make([]DownloadEntry, 0, len(m.downloads))
		for _, record := range m.downloads {
			if id == "" || record.Tab == id {
				entries = append(entries, *record)
			}
		}
		snapshot(200, entries)
		return
	}
	t, err := m.tabLocked(id)
	if err != nil {
		snapshot(404, map[string]string{"error": err.Error()})
		return
	}
	switch route {
	case "/runtime/console":
		snapshot(200, t.console)
	case "/runtime/errors":
		snapshot(200, t.errors)
	case "/runtime/network":
		snapshot(200, t.network)
	case "/runtime/dialogs":
		snapshot(200, t.dialogs)
	case "/runtime/network-state":
		snapshot(200, NetworkState{len(t.inflight), t.lastActivity})
	}
}
func (m *Manager) servePolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		m.mu.Lock()
		policy := m.policy
		m.mu.Unlock()
		writeJSON(w, 200, policy)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	var input struct {
		Accept     *bool  `json:"accept"`
		PromptText string `json:"promptText"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&input)
	if err == nil {
		var extra any
		if tailErr := decoder.Decode(&extra); tailErr != io.EOF {
			err = errors.New("expected one JSON object")
		}
	}
	if err != nil || input.Accept == nil {
		writeJSON(w, 400, map[string]string{"error": "expected JSON {accept: boolean, promptText?: string}"})
		return
	}
	policy := DialogPolicy{*input.Accept, input.PromptText}
	m.policyMu.Lock()
	defer m.policyMu.Unlock()
	f, err := os.CreateTemp(m.config.Dir, ".dialog-policy-")
	if err == nil {
		name := f.Name()
		defer os.Remove(name)
		err = json.NewEncoder(f).Encode(policy)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(name, filepath.Join(m.config.Dir, "dialog-policy.json"))
		}
	}
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "cannot save Wok dialog policy: " + err.Error()})
		return
	}
	m.mu.Lock()
	m.policy = policy
	m.mu.Unlock()
	writeJSON(w, 200, policy)
}
