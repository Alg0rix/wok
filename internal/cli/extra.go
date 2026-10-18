package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/alg0rix/wok/internal/browser"
	"github.com/alg0rix/wok/internal/session"
)

func control(ctx context.Context, store *session.Store, st *session.State, method, path string, value any) (any, error) {
	if err := store.EnsureRender(ctx, st, ""); err != nil {
		return nil, err
	}
	var body io.Reader
	if value != nil {
		b, e := json.Marshal(value)
		if e != nil {
			return nil, e
		}
		body = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, st.RenderURL+path, body)
	if e != nil {
		return nil, e
	}
	if value != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	authorize(req, st)
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	data, e := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if e != nil {
		return nil, e
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("controller HTTP %d: %s", resp.StatusCode, data)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]bool{"ok": true}, nil
	}
	var result any
	if e = json.Unmarshal(data, &result); e != nil {
		return nil, e
	}
	return result, nil
}
func authorize(req *http.Request, st *session.State) {
	if st.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+st.AuthToken)
	}
}

func find(ctx context.Context, p *browser.Page, args []string, refs *browser.Refs) (any, error) {
	if len(args) < 2 {
		return nil, errors.New("usage: wok find role|text|css VALUE [--nth N]")
	}
	nth := 0
	if len(args) == 4 && args[2] == "--nth" {
		n, e := strconv.Atoi(args[3])
		if e != nil || n < 1 {
			return nil, errors.New("--nth must be a positive integer")
		}
		nth = n
	} else if len(args) != 2 {
		return nil, errors.New("invalid find arguments")
	}
	return p.Find(ctx, args[0], args[1], nth, refs)
}
func cookies(ctx context.Context, p *browser.Page, args []string) (any, error) {
	if len(args) == 0 {
		return p.Cookies(ctx, "list", nil)
	}
	action := args[0]
	args = args[1:]
	params := map[string]any{}
	switch action {
	case "list", "clear":
		if len(args) != 0 {
			return nil, errors.New("cookies list/clear takes no arguments")
		}
	case "set":
		if len(args) < 2 {
			return nil, errors.New("usage: wok cookies set NAME VALUE [options]")
		}
		params["name"] = args[0]
		params["value"] = args[1]
		args = args[2:]
	case "delete":
		if len(args) < 1 {
			return nil, errors.New("usage: wok cookies delete NAME [--domain HOST] [--path PATH]")
		}
		params["name"] = args[0]
		args = args[1:]
	default:
		return nil, errors.New("cookies supports list, set, delete, clear")
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--secure", "--http-only":
			if action != "set" {
				return nil, errors.New("cookie boolean options require set")
			}
			key := "secure"
			if a == "--http-only" {
				key = "httpOnly"
			}
			params[key] = true
		case "--domain", "--path", "--url", "--same-site", "--expires":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("%s requires a value", a)
			}
			key := strings.TrimPrefix(a, "--")
			if a == "--same-site" {
				key = "sameSite"
				if args[i] != "Strict" && args[i] != "Lax" && args[i] != "None" {
					return nil, errors.New("--same-site must be Strict, Lax or None")
				}
			}
			if a == "--expires" {
				n, e := strconv.ParseFloat(args[i], 64)
				if e != nil {
					return nil, e
				}
				params[key] = n
			} else {
				params[key] = args[i]
			}
		default:
			return nil, fmt.Errorf("unknown cookie option %q", a)
		}
	}
	return p.Cookies(ctx, action, params)
}
func storage(ctx context.Context, p *browser.Page, args []string) (any, error) {
	if len(args) < 1 {
		return nil, errors.New("usage: wok storage local|session [list|get|set|delete|clear] [KEY] [VALUE]")
	}
	area := args[0]
	action := "list"
	key, value := "", ""
	if len(args) > 1 {
		action = args[1]
	}
	expected := 2
	switch action {
	case "list":
		if len(args) == 1 {
			expected = 1
		}
	case "clear":
	case "get", "delete":
		expected = 3
	case "set":
		expected = 4
	default:
		return nil, errors.New("invalid storage action")
	}
	if len(args) != expected {
		return nil, errors.New("invalid storage arguments")
	}
	if len(args) > 2 {
		key = args[2]
	}
	if len(args) > 3 {
		value = args[3]
	}
	return p.Storage(ctx, area, action, key, value)
}
func wait(ctx context.Context, p *browser.Page, store *session.Store, state *session.State, args []string) error {
	opts := browser.WaitOptions{}
	networkIdle, download := false, false
	idleMS := 500
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--load":
			opts.Load = true
		case "--network-idle":
			networkIdle = true
		case "--download":
			download = true
		case "--selector", "--state", "--text", "--url", "--fn", "--network-idle-ms":
			i++
			if i >= len(args) {
				return fmt.Errorf("%s requires a value", a)
			}
			value := args[i]
			switch a {
			case "--selector":
				opts.Selector = value
			case "--state":
				opts.State = value
			case "--text":
				opts.Text = value
			case "--url":
				opts.URL = value
			case "--fn":
				opts.Function = value
			case "--network-idle-ms":
				n, e := strconv.Atoi(value)
				if e != nil || n < 1 || n > 300000 {
					return errors.New("network idle milliseconds must be 1..300000")
				}
				idleMS = n
			}
		default:
			if strings.HasPrefix(a, "--") || opts.Selector != "" {
				return fmt.Errorf("invalid wait argument %q", a)
			}
			opts.Selector = a
		}
	}
	conditions := 0
	for _, v := range []bool{opts.Selector != "", opts.Text != "", opts.URL != "", opts.Function != "", opts.Load, networkIdle, download} {
		if v {
			conditions++
		}
	}
	if conditions != 1 {
		return errors.New("wait requires exactly one condition")
	}
	if !networkIdle && !download {
		return p.Wait(ctx, opts)
	}
	if opts.State != "" {
		return errors.New("--state requires a selector")
	}
	started := time.Now()
	eligibleDownloads := map[string]bool{}
	ticker := time.NewTicker(75 * time.Millisecond)
	defer ticker.Stop()
	for {
		path := "/runtime/downloads"
		if networkIdle {
			path = "/runtime/network-state?tab=" + url.QueryEscape(p.TargetID)
		}
		value, e := control(ctx, store, state, "GET", path, nil)
		if e != nil {
			return e
		}
		if networkIdle {
			m, ok := value.(map[string]any)
			if !ok {
				return errors.New("invalid network state")
			}
			n, _ := m["inflight"].(float64)
			last, _ := m["lastActivityUnixMs"].(float64)
			quiet := time.Since(started) >= time.Duration(idleMS)*time.Millisecond
			if last > 0 {
				quiet = time.Since(time.UnixMilli(int64(last))) >= time.Duration(idleMS)*time.Millisecond
			}
			if n == 0 && quiet {
				return nil
			}
		} else {
			items, ok := value.([]any)
			if !ok {
				return errors.New("invalid download state")
			}
			for _, item := range items {
				m, _ := item.(map[string]any)
				id, _ := m["guid"].(string)
				if id != "" && m["state"] == "inProgress" {
					eligibleDownloads[id] = true
				}
			}
			if len(eligibleDownloads) == 0 && len(items) > 0 {
				m, _ := items[len(items)-1].(map[string]any)
				id, _ := m["guid"].(string)
				if id != "" {
					eligibleDownloads[id] = true
				}
			}
			completed := 0
			for _, item := range items {
				m, _ := item.(map[string]any)
				id, _ := m["guid"].(string)
				if !eligibleDownloads[id] {
					continue
				}
				switch m["state"] {
				case "canceled":
					return fmt.Errorf("download %s was canceled", id)
				case "completed":
					completed++
				}
			}
			if len(eligibleDownloads) > 0 && completed == len(eligibleDownloads) && time.Since(started) >= 100*time.Millisecond {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
