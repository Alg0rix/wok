// Package session manages persistent browser profiles and their private state.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/alg0rix/wok/internal/cdp"
	"github.com/alg0rix/wok/internal/provision"
)

var profileName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

type State struct {
	RenderListen  string          `json:"renderListen,omitempty"`
	AuthToken     string          `json:"authToken,omitempty"`
	EmbedOrigins  []string        `json:"embedOrigins,omitempty"`
	DownloadDir   string          `json:"downloadDir,omitempty"`
	Proxy         string          `json:"proxy,omitempty"`
	ProxyBypass   string          `json:"proxyBypass,omitempty"`
	ProxyUsername string          `json:"proxyUsername,omitempty"`
	ProxyPassword string          `json:"proxyPassword,omitempty"`
	Profile       string          `json:"profile"`
	Endpoint      string          `json:"endpoint"`
	PID           int             `json:"pid"`
	Executable    string          `json:"executable"`
	TargetID      string          `json:"targetId,omitempty"`
	RenderURL     string          `json:"renderUrl,omitempty"`
	Token         string          `json:"renderToken,omitempty"`
	Refs          json.RawMessage `json:"refs,omitempty"`
}

type Store struct {
	Dir  string
	Name string
}

func Open(root, name string) (*Store, error) {
	if !profileName.MatchString(name) {
		return nil, fmt.Errorf("invalid profile %q: use 1–64 letters, numbers, underscores or hyphens", name)
	}
	if root == "" {
		root = os.Getenv("WOK_HOME")
	}
	if root == "" {
		base, e := os.UserConfigDir()
		if e != nil {
			return nil, e
		}
		root = filepath.Join(base, "wok")
	}
	dir, err := filepath.Abs(filepath.Join(root, "profiles", name))
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	return &Store{Dir: dir, Name: name}, nil
}
func (s *Store) Load() (*State, error) {
	data, err := os.ReadFile(filepath.Join(s.Dir, "state.json"))
	if err != nil {
		return nil, err
	}
	var state State
	if err = json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("corrupt profile state: %w", err)
	}
	return &state, nil
}
func (s *Store) Save(state *State) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(filepath.Join(s.Dir, "state.json"), data)
}
func AtomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".wok-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func Resolve(ctx context.Context, endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	if u.Scheme == "ws" || u.Scheme == "wss" {
		if u.Host == "" {
			return "", errors.New("CDP endpoint has no host")
		}
		return endpoint, nil
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("CDP endpoint must use http(s) or ws(s)")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/json/version"
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("CDP discovery: HTTP %d", resp.StatusCode)
	}
	var v struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err = json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&v); err != nil {
		return "", err
	}
	if v.WebSocketDebuggerURL == "" {
		return "", errors.New("CDP discovery returned no browser WebSocket")
	}
	return v.WebSocketDebuggerURL, nil
}
func Healthy(ctx context.Context, endpoint string) error {
	c, err := cdp.Dial(ctx, endpoint)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Call(ctx, "", "Browser.getVersion", nil, nil)
}
func (s *Store) Connect(ctx context.Context) (*State, *cdp.Client, error) {
	state, err := s.Load()
	if err != nil {
		return nil, nil, fmt.Errorf("profile %q is not running; run wok --profile %s start: %w", s.Name, s.Name, err)
	}
	c, err := cdp.Dial(ctx, state.Endpoint)
	if err != nil {
		return nil, nil, fmt.Errorf("profile %q is unavailable; run wok --profile %s start to recover: %w", s.Name, s.Name, err)
	}
	if err = c.Call(ctx, "", "Browser.getVersion", nil, nil); err != nil {
		c.Close()
		return nil, nil, err
	}
	return state, c, nil
}

type StartOptions struct {
	RenderListen, AuthToken, DownloadDir, Proxy, ProxyBypass string
	EmbedOrigins                                             []string
	Browser                                                  string
	Headed                                                   bool
	NoSandbox                                                bool
	Render                                                   bool
	Self                                                     string
}

var ErrBrowserMissing = errors.New("no Chromium browser found")

func FindBrowser(override string) (string, error) {
	if override == "" {
		override = os.Getenv("WOK_BROWSER")
	}
	if override != "" {
		return exec.LookPath(override)
	}
	if shell, e := exec.LookPath("chrome-headless-shell"); e == nil {
		return shell, nil
	}
	if cached, e := provision.Cached(); e == nil {
		return cached, nil
	}
	candidates := []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"}
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
		if dir := os.Getenv(env); dir != "" {
			candidates = append(candidates, filepath.Join(dir, "Google", "Chrome", "Application", "chrome.exe"), filepath.Join(dir, "Chromium", "Application", "chrome.exe"), filepath.Join(dir, "Microsoft", "Edge", "Application", "msedge.exe"))
		}
	}
	for _, candidate := range candidates {
		if p, e := exec.LookPath(candidate); e == nil {
			return p, nil
		}
	}
	return "", ErrBrowserMissing
}
func (s *Store) Start(ctx context.Context, opts StartOptions) (*State, error) {
	previous, _ := s.Load()
	if previous == nil {
		if data, e := os.ReadFile(filepath.Join(s.Dir, "render-options.json")); e == nil {
			var recovered State
			if json.Unmarshal(data, &recovered) == nil {
				previous = &recovered
			}
		}
	}
	settings := &State{}
	if err := s.configure(settings, opts, previous); err != nil {
		return nil, err
	}
	previousRefs := json.RawMessage(nil)
	if previous != nil {
		previousRefs = previous.Refs
	}
	if st, e := s.Load(); e == nil {
		probe, cancel := context.WithTimeout(ctx, time.Second)
		err := Healthy(probe, st.Endpoint)
		cancel()
		if err == nil {
			if !configEqual(st, settings) {
				return nil, errors.New("profile is already running with different settings; stop it before changing proxy/remote/download configuration")
			}
			if opts.Render {
				if err = s.EnsureRender(ctx, st, opts.Self); err != nil {
					return nil, err
				}
			}
			return st, nil
		}
	}
	// Recover a still-running managed browser if a previous CLI died before saving state.
	profileDir := filepath.Join(s.Dir, "browser")
	if endpoint, e := activeEndpoint(profileDir); e == nil {
		probe, cancel := context.WithTimeout(ctx, time.Second)
		err := Healthy(probe, endpoint)
		cancel()
		if err == nil {
			st := &State{Profile: s.Name, Endpoint: endpoint, Refs: previousRefs}
			copyConfig(st, settings)
			// Browser-owned process info restores the PID without trusting a stale PID
			// file or inspecting unrelated processes.
			if c, e := cdp.Dial(ctx, endpoint); e == nil {
				var info struct {
					ProcessInfo []struct {
						Type string `json:"type"`
						ID   int    `json:"id"`
					} `json:"processInfo"`
				}
				if c.Call(ctx, "", "SystemInfo.getProcessInfo", nil, &info) == nil {
					for _, p := range info.ProcessInfo {
						if p.Type == "browser" {
							st.PID = p.ID
						}
					}
				}
				c.Close()
			}
			if opts.Render {
				if err = s.EnsureRender(ctx, st, opts.Self); err != nil {
					return nil, err
				}
			}
			return st, s.Save(st)
		}
	}
	executable, err := FindBrowser(opts.Browser)
	if errors.Is(err, ErrBrowserMissing) {
		executable, err = provision.Ensure(ctx)
	}
	if err != nil {
		return nil, err
	}
	// Browser readiness is a separate bounded phase from one-time provisioning.
	ctx, startupCancel := context.WithTimeout(ctx, 15*time.Second)
	defer startupCancel()
	if err = os.MkdirAll(profileDir, 0700); err != nil {
		return nil, err
	}
	if err = os.Remove(filepath.Join(profileDir, "DevToolsActivePort")); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	args := []string{"--user-data-dir=" + profileDir, "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0", "--no-first-run", "--no-default-browser-check", "--disable-background-networking", "--disable-dev-shm-usage", "--window-size=1280,720"}
	if !opts.Headed {
		args = append(args, "--headless")
	}
	if opts.NoSandbox {
		args = append(args, "--no-sandbox")
	}
	if settings.Proxy != "" {
		args = append(args, "--proxy-server="+settings.Proxy)
	}
	if settings.ProxyBypass != "" {
		args = append(args, "--proxy-bypass-list="+settings.ProxyBypass)
	}
	args = append(args, "about:blank")
	log, err := os.OpenFile(filepath.Join(s.Dir, "browser.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	defer log.Close()
	cmd := exec.Command(executable, args...)
	cmd.Stdout = log
	cmd.Stderr = log
	detach(cmd)
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	// Wait reaps the child while this CLI is alive; exiting the CLI leaves Chrome running.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	for {
		if endpoint, e := activeEndpoint(profileDir); e == nil {
			probe, cancel := context.WithTimeout(ctx, time.Second)
			err := Healthy(probe, endpoint)
			cancel()
			if err == nil {
				st := &State{Profile: s.Name, Endpoint: endpoint, PID: cmd.Process.Pid, Executable: executable, Refs: previousRefs}
				copyConfig(st, settings)
				if err = s.Save(st); err != nil {
					_ = cmd.Process.Kill()
					return nil, err
				}
				if opts.Render {
					if err = s.EnsureRender(ctx, st, opts.Self); err != nil {
						return nil, err
					}
				}
				return st, nil
			}
		}
		select {
		case e := <-exited:
			return nil, fmt.Errorf("browser exited (%v); see %s", e, filepath.Join(s.Dir, "browser.log"))
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return nil, fmt.Errorf("browser startup: %w; see browser.log", ctx.Err())
		case <-ticker.C:
		}
	}
}
func activeEndpoint(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "DevToolsActivePort"))
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[1], "/devtools/browser/") {
		return "", errors.New("invalid DevToolsActivePort")
	}
	// Validate the port before interpolating an endpoint from the browser's file.
	u, err := url.Parse("http://127.0.0.1:" + lines[0])
	if err != nil || u.Port() == "" {
		return "", errors.New("invalid debugging port")
	}
	return "ws://127.0.0.1:" + lines[0] + lines[1], nil
}
func (s *Store) EnsureRender(ctx context.Context, st *State, self string) error {
	// Reuse a live viewer if a launcher died after publishing readiness but before
	// saving state. The ready record is tied to this browser's unique endpoint.
	if st.RenderURL == "" {
		data, e := os.ReadFile(filepath.Join(s.Dir, "render-ready.json"))
		var ready struct {
			URL      string `json:"url"`
			Endpoint string `json:"endpoint"`
		}
		if e == nil && json.Unmarshal(data, &ready) == nil && ready.Endpoint == st.Endpoint {
			st.RenderURL = ready.URL
			token, _ := os.ReadFile(filepath.Join(s.Dir, "render-token"))
			st.Token = string(token)
		}
	}
	if st.RenderURL != "" {
		probe, cancel := context.WithTimeout(ctx, time.Second)
		req, e := http.NewRequestWithContext(probe, "GET", st.RenderURL+"/health", nil)
		if e != nil {
			cancel()
			return fmt.Errorf("invalid render URL: %w", e)
		}
		if st.AuthToken != "" {
			req.Header.Set("Authorization", "Bearer "+st.AuthToken)
		}
		resp, e := http.DefaultClient.Do(req)
		cancel()
		if e == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return s.Save(st)
			}
		}
	}
	st.RenderURL = ""
	if self == "" {
		var err error
		self, err = os.Executable()
		if err != nil {
			return err
		}
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return err
	}
	st.Token = hex.EncodeToString(token)
	ready := filepath.Join(s.Dir, "render-ready.json")
	_ = os.Remove(ready)
	log, err := os.OpenFile(filepath.Join(s.Dir, "render.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	// Pass the private shutdown token through a private file, not the process command line.
	tokenFile := filepath.Join(s.Dir, "render-token")
	if err = AtomicWrite(tokenFile, []byte(st.Token)); err != nil {
		return err
	}
	configFile := filepath.Join(s.Dir, "render-options.json")
	if err = AtomicWrite(configFile, controllerConfig(st)); err != nil {
		return err
	}
	listen := st.RenderListen
	if listen == "" {
		listen = "127.0.0.1:0"
	}
	cmd := exec.Command(self, "_render", "--cdp", st.Endpoint, "--ready-file", ready, "--token-file", tokenFile, "--options-file", configFile, "--listen", listen)
	cmd.Stdout = log
	cmd.Stderr = log
	detach(cmd)
	if err = cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	for {
		if data, e := os.ReadFile(ready); e == nil {
			var r struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(data, &r) == nil && r.URL != "" {
				st.RenderURL = r.URL
				return s.Save(st)
			}
		}
		select {
		case e := <-exited:
			return fmt.Errorf("render exited (%v); see render.log", e)
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// CloseBrowser requests shutdown and proves the endpoint has stopped accepting
// connections. A timeout or interrupted request is never evidence of shutdown.
func CloseBrowser(ctx context.Context, c *cdp.Client, endpoint string) error {
	callCtx, cancel := context.WithTimeout(ctx, time.Second)
	err := c.Call(callCtx, "", "Browser.close", nil, nil)
	cancel()
	if ctx.Err() != nil {
		return fmt.Errorf("browser shutdown unconfirmed: %w", ctx.Err())
	}
	var protocolErr *cdp.ProtocolError
	if errors.As(err, &protocolErr) {
		return err
	}
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return fmt.Errorf("browser shutdown unconfirmed: %w", ctx.Err())
		}
		probe, pcancel := context.WithTimeout(ctx, 200*time.Millisecond)
		probeErr := Healthy(probe, endpoint)
		pcancel()
		if ctx.Err() != nil {
			return fmt.Errorf("browser shutdown unconfirmed: %w", ctx.Err())
		}
		if connectionRefused(probeErr) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("browser shutdown unconfirmed: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (s *Store) Stop(ctx context.Context) error {
	st, c, err := s.Connect(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	if err = CloseBrowser(ctx, c, st.Endpoint); err != nil {
		return err
	}
	st.Endpoint = ""
	st.PID = 0
	st.TargetID = ""
	st.RenderURL = ""
	st.Token = ""
	return s.Save(st)
}
