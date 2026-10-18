package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/alg0rix/wok/internal/observe"
)

// networkFilter narrows `wok network` output. Matching entries keep their
// index in the full log so `wok request INDEX` still works.
type networkFilter struct {
	method, kind string
	url          func(string) bool
	body         bool
}

const networkUsage = "usage: wok network [--method METHOD] [--url GLOB] [--type TYPE] [--body]"

func parseNetworkFilter(args []string) (networkFilter, error) {
	var f networkFilter
	for i := 0; i < len(args); i++ {
		key, value, has := strings.Cut(args[i], "=")
		if key == "--body" {
			if has {
				return f, errors.New(networkUsage)
			}
			f.body = true
			continue
		}
		if key != "--method" && key != "--url" && key != "--type" {
			return f, errors.New(networkUsage)
		}
		if !has {
			if i++; i >= len(args) {
				return f, errors.New(networkUsage)
			}
			value = args[i]
		}
		if value == "" {
			return f, errors.New(key + " must not be empty")
		}
		switch key {
		case "--method":
			f.method = strings.ToUpper(value)
		case "--type":
			f.kind = value
		case "--url":
			if hint := observe.RouteHint(value); hint != "" {
				return f, errors.New(strings.Replace(hint, "route pattern", "url pattern", 1))
			}
			match, err := observe.GlobMatcher(value)
			if err != nil {
				return f, err
			}
			f.url = match
		}
	}
	return f, nil
}

// apply keeps matching entries and, with --body, adds each requestBody and,
// when the body is JSON, its parsed requestJson.
func (f networkFilter) apply(ctx context.Context, items []any, requestBody func(index int) (any, error)) ([]any, error) {
	out := []any{}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if method, _ := m["method"].(string); f.method != "" && method != f.method {
			continue
		}
		if kind, _ := m["type"].(string); f.kind != "" && !strings.EqualFold(kind, f.kind) {
			continue
		}
		if u, _ := m["url"].(string); f.url != nil && !f.url(u) {
			continue
		}
		if f.body {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			value, err := requestBody(m["index"].(int))
			if err != nil {
				return nil, err
			}
			if body, ok := value.(map[string]any); ok {
				m["requestBody"] = body["requestBody"]
				var parsed any
				if text, _ := body["requestBody"].(string); text != "" && json.Unmarshal([]byte(text), &parsed) == nil {
					m["requestJson"] = parsed
				}
			}
		}
		out = append(out, m)
	}
	return out, nil
}
