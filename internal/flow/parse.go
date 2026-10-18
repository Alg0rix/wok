package flow

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"time"

	"go.yaml.in/yaml/v3"
)

type document struct {
	profile  string
	close    bool
	viewport map[string]any
	steps    []step
}
type step struct {
	name, action, save string
	timeout            any
	value              any
}

var saveName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var profileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func fields(n *yaml.Node, allowed ...string) (map[string]*yaml.Node, error) {
	if n.Kind != yaml.MappingNode {
		return nil, errors.New("expected an object")
	}
	keys := map[string]bool{}
	for _, k := range allowed {
		keys[k] = true
	}
	out := map[string]*yaml.Node{}
	for i := 0; i < len(n.Content); i += 2 {
		k := n.Content[i]
		if k.Kind != yaml.ScalarNode || k.Tag != "!!str" {
			return nil, errors.New("object keys must be strings")
		}
		if !keys[k.Value] {
			return nil, fmt.Errorf("unknown field %q", k.Value)
		}
		if _, ok := out[k.Value]; ok {
			return nil, fmt.Errorf("duplicate field %q", k.Value)
		}
		out[k.Value] = n.Content[i+1]
	}
	return out, nil
}
func scalar(n *yaml.Node, tag string) (string, error) {
	if n.Kind != yaml.ScalarNode || n.Tag != tag {
		return "", fmt.Errorf("expected %s", tag[2:])
	}
	return n.Value, nil
}
func str(n *yaml.Node) (string, error) { return scalar(n, "!!str") }
func boolean(n *yaml.Node) (bool, error) {
	v, err := scalar(n, "!!bool")
	if err != nil {
		return false, err
	}
	return strconv.ParseBool(v)
}
func integer(n *yaml.Node) (int, error) {
	if _, err := scalar(n, "!!int"); err != nil {
		return 0, err
	}
	var v int
	if err := n.Decode(&v); err != nil {
		return 0, errors.New("invalid integer")
	}
	return v, nil
}
func num(n *yaml.Node) (float64, error) {
	if n.Kind != yaml.ScalarNode || (n.Tag != "!!int" && n.Tag != "!!float") {
		return 0, errors.New("expected a number")
	}
	var v float64
	if err := n.Decode(&v); err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, errors.New("expected a finite number")
	}
	return v, nil
}
func stringObject(n *yaml.Node, required []string, optional ...string) (map[string]any, error) {
	f, err := fields(n, append(required, optional...)...)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, key := range required {
		if f[key] == nil {
			return nil, fmt.Errorf("missing field %q", key)
		}
	}
	for k, v := range f {
		s, err := str(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = s
	}
	return out, nil
}
func parse(source []byte) (document, error) {
	var doc document
	if len(source) > 1<<20 {
		return doc, errors.New("flow exceeds 1 MiB")
	}
	dec := yaml.NewDecoder(bytes.NewReader(source))
	var node yaml.Node
	if err := dec.Decode(&node); err != nil {
		return doc, fmt.Errorf("parse flow: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		if err != nil {
			return doc, fmt.Errorf("parse flow: %w", err)
		}
		return doc, errors.New("flow must contain exactly one document")
	}
	if len(node.Content) != 1 {
		return doc, errors.New("flow must be an object")
	}
	root, err := fields(node.Content[0], "version", "profile", "close", "viewport", "steps")
	if err != nil {
		return doc, err
	}
	if n := root["version"]; n != nil {
		v, err := integer(n)
		if err != nil || v != 1 {
			return doc, errors.New("version must be 1")
		}
	}
	if n := root["profile"]; n != nil {
		doc.profile, err = str(n)
		if err != nil {
			return doc, fmt.Errorf("profile: %w", err)
		}
	}
	if n := root["close"]; n != nil {
		doc.close, err = boolean(n)
		if err != nil {
			return doc, fmt.Errorf("close: %w", err)
		}
	}
	if n := root["viewport"]; n != nil {
		f, err := fields(n, "width", "height", "mobile", "dpr")
		if err != nil {
			return doc, fmt.Errorf("viewport: %w", err)
		}
		doc.viewport = map[string]any{"mobile": false, "dpr": float64(1)}
		for _, k := range []string{"width", "height"} {
			if f[k] == nil {
				return doc, fmt.Errorf("viewport requires %s", k)
			}
			v, err := integer(f[k])
			if err != nil || v <= 0 {
				return doc, fmt.Errorf("viewport %s must be a positive integer", k)
			}
			doc.viewport[k] = v
		}
		if n := f["mobile"]; n != nil {
			v, err := boolean(n)
			if err != nil {
				return doc, err
			}
			doc.viewport["mobile"] = v
		}
		if n := f["dpr"]; n != nil {
			v, err := num(n)
			if err != nil || v <= 0 {
				return doc, errors.New("viewport dpr must be positive")
			}
			doc.viewport["dpr"] = v
		}
	}
	steps := root["steps"]
	if steps == nil || steps.Kind != yaml.SequenceNode || len(steps.Content) < 1 || len(steps.Content) > 1000 {
		return doc, errors.New("steps must contain between 1 and 1000 actions")
	}
	for i, n := range steps.Content {
		s, err := parseStep(n)
		if err != nil {
			return doc, fmt.Errorf("step %d: %w", i+1, err)
		}
		doc.steps = append(doc.steps, s)
	}
	return doc, nil
}
func parseStep(n *yaml.Node) (step, error) {
	var s step
	f, err := fields(n, "name", "timeout", "save", "open", "click", "fill", "type", "press", "eval", "get", "wait", "screenshot", "snapshot", "tab", "expect", "find", "cookies", "storage", "upload", "console", "errors", "network", "downloads", "dialogs", "dialog", "set")
	if err != nil {
		return s, err
	}
	for _, key := range []string{"name", "timeout", "save"} {
		if n := f[key]; n != nil {
			v, err := str(n)
			if err != nil {
				return s, fmt.Errorf("%s: %w", key, err)
			}
			switch key {
			case "name":
				s.name = v
			case "timeout":
				s.timeout = v
			case "save":
				if !saveName.MatchString(v) {
					return s, errors.New("save must be a variable name containing letters, digits, and underscores")
				}
				s.save = v
			}
			delete(f, key)
		}
	}
	if len(f) != 1 {
		return s, errors.New("exactly one action is required")
	}
	var payload *yaml.Node
	for k, v := range f {
		s.action = k
		payload = v
	}
	switch s.action {
	case "open", "click", "type", "press", "eval", "screenshot":
		s.value, err = str(payload)
	case "fill":
		s.value, err = stringObject(payload, []string{"selector", "value"})
	case "get":
		var m map[string]any
		m, err = stringObject(payload, []string{"kind"}, "selector", "attr")

		s.value = m
	case "snapshot":
		if payload.Kind == yaml.ScalarNode {
			var b bool
			b, err = boolean(payload)
			if err == nil && !b {
				err = errors.New("snapshot must be true or an object")
			}
			s.value = map[string]any{"interactive": false}
		} else {
			var m map[string]*yaml.Node
			m, err = fields(payload, "interactive")
			if err == nil {
				if m["interactive"] == nil {
					err = errors.New("snapshot requires interactive")
				} else {
					var b bool
					b, err = boolean(m["interactive"])
					s.value = map[string]any{"interactive": b}
				}
			}
		}
	case "tab":
		var m map[string]*yaml.Node
		m, err = fields(payload, "new", "select", "close")
		if err == nil {
			if len(m) != 1 {
				err = errors.New("tab requires exactly one of new, select, close")
			} else {
				out := map[string]any{}
				for k, v := range m {
					out[k], err = str(v)
				}
				s.value = out
			}
		}
	case "expect":
		s.value, err = parseExpect(payload)
	case "wait", "find", "cookies", "storage", "upload", "console", "errors", "network", "downloads", "dialogs", "dialog", "set":
		s.value, err = parseNative(s.action, payload)
	}
	if err != nil {
		return s, fmt.Errorf("%s: %w", s.action, err)
	}
	if s.timeout == nil {
		if s.action == "expect" {
			s.timeout = "10s"
		} else {
			s.timeout = "15s"
		}
	}
	return s, nil
}
func parseExpect(n *yaml.Node) (map[string]any, error) {
	f, err := fields(n, "selector", "text", "contains", "value", "count", "visible", "url", "js")
	if err != nil {
		return nil, err
	}
	if len(f) == 0 {
		return nil, errors.New("expect requires a condition")
	}
	out := map[string]any{}
	for k, n := range f {
		switch k {
		case "count":
			v, e := integer(n)
			if e != nil || v < 0 {
				return nil, errors.New("count must be a nonnegative integer")
			}
			out[k] = v
		case "visible":
			v, e := boolean(n)
			if e != nil {
				return nil, e
			}
			out[k] = v
		default:
			v, e := str(n)
			if e != nil {
				return nil, e
			}
			out[k] = v
		}
	}
	for _, k := range []string{"text", "contains", "value", "count", "visible"} {
		if _, ok := out[k]; ok && f["selector"] == nil {
			return nil, fmt.Errorf("%s requires selector", k)
		}
	}
	return out, nil
}
func duration(v any) (time.Duration, error) {
	s, ok := v.(string)
	if !ok {
		return 0, errors.New("timeout must be a duration string")
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, errors.New("timeout must be a positive duration")
	}
	return d, nil
}
