package flow

import (
	"errors"
	"fmt"
	"sort"
	"strconv"

	"go.yaml.in/yaml/v3"
)

const (
	maxDepth          = 8
	maxRepeat         = 10000
	defaultRepeatMax  = 100
	defaultLoopName   = "loop"
	conditionFieldSet = "selector text contains value count visible url js frame"
)

// block holds the nested steps of an if, repeat, or each step. The step's
// value holds the condition (if, repeat while/until) or the items (each), so
// variable compilation and resolution treat it like any other action value.
type block struct {
	then, els, body []step
	cond            string // "if", "while", "until", or "" for counted/each loops
	times, max      int
	as              string
}

func isBlock(action string) bool {
	return action == "if" || action == "repeat" || action == "each"
}

func parseSteps(n *yaml.Node, label string, depth int, count *int) ([]step, error) {
	if n == nil || n.Kind != yaml.SequenceNode || len(n.Content) == 0 {
		return nil, fmt.Errorf("%s must be a nonempty list of steps", label)
	}
	out := make([]step, 0, len(n.Content))
	for i, item := range n.Content {
		s, err := parseStepDepth(item, depth, count)
		if err != nil {
			return nil, fmt.Errorf("%s step %d: %w", label, i+1, err)
		}
		out = append(out, s)
	}
	return out, nil
}

// parseBlock parses an if (with sibling then/else lists), repeat, or each.
func parseBlock(s *step, n *yaml.Node, branches map[string]*yaml.Node, depth int, count *int) error {
	if depth >= maxDepth {
		return fmt.Errorf("blocks may nest at most %d levels", maxDepth)
	}
	b := &block{}
	s.block = b
	switch s.action {
	case "if":
		var err error
		if b.then, err = parseSteps(branches["then"], "then", depth+1, count); err != nil {
			return err
		}
		if branches["else"] != nil {
			if b.els, err = parseSteps(branches["else"], "else", depth+1, count); err != nil {
				return err
			}
		}
		b.cond = "if"
		s.value, err = parseExpect(n)
		if err != nil {
			return fmt.Errorf("condition (%s): %w", conditionFieldSet, err)
		}
		return nil
	case "repeat":
		f, err := fields(n, "times", "while", "until", "max", "as", "steps")
		if err != nil {
			return err
		}
		modes := 0
		for _, k := range []string{"times", "while", "until"} {
			if f[k] != nil {
				modes++
			}
		}
		if modes != 1 {
			return errors.New("repeat requires exactly one of times, while, until")
		}
		if f["times"] != nil {
			if b.times, err = integer(f["times"]); err != nil || b.times < 1 || b.times > maxRepeat {
				return fmt.Errorf("times must be an integer from 1 to %d", maxRepeat)
			}
			if f["max"] != nil {
				return errors.New("max is only valid with while or until")
			}
		} else {
			b.cond = "while"
			if f["until"] != nil {
				b.cond = "until"
			}
			if s.value, err = parseExpect(f[b.cond]); err != nil {
				return fmt.Errorf("%s: %w", b.cond, err)
			}
			b.max = defaultRepeatMax
			if f["max"] != nil {
				if b.max, err = integer(f["max"]); err != nil || b.max < 1 || b.max > maxRepeat {
					return fmt.Errorf("max must be an integer from 1 to %d", maxRepeat)
				}
			}
		}
		if err = parseLoopName(b, f["as"]); err != nil {
			return err
		}
		b.body, err = parseSteps(f["steps"], "repeat", depth+1, count)
		return err
	case "each":
		f, err := fields(n, "items", "as", "steps")
		if err != nil {
			return err
		}
		items := f["items"]
		switch {
		case items == nil:
			return errors.New("each requires items")
		case items.Kind == yaml.SequenceNode:
			if len(items.Content) == 0 || len(items.Content) > maxRepeat {
				return fmt.Errorf("items must contain 1 to %d values", maxRepeat)
			}
			var list []any
			if err = items.Decode(&list); err != nil {
				return fmt.Errorf("items: %w", err)
			}
			s.value = plainStrings(list)
		default:
			// A string, usually "${saved}", that resolves to a JSON array.
			if s.value, err = str(items); err != nil {
				return errors.New("items must be a list or a string holding a JSON array")
			}
		}
		if err = parseLoopName(b, f["as"]); err != nil {
			return err
		}
		b.body, err = parseSteps(f["steps"], "each", depth+1, count)
		return err
	}
	return fmt.Errorf("unknown block %s", s.action)
}

func parseLoopName(b *block, n *yaml.Node) error {
	b.as = defaultLoopName
	if n == nil {
		return nil
	}
	v, err := str(n)
	if err != nil || !saveName.MatchString(v) {
		return errors.New("as must be a variable name containing letters, digits, and underscores")
	}
	b.as = v
	return nil
}

// plainStrings converts decoded YAML scalars to strings so ${...} references
// inside literal items are compiled like other values.
func plainStrings(v any) any {
	switch x := v.(type) {
	case []any:
		for i := range x {
			x[i] = plainStrings(x[i])
		}
		return x
	case map[string]any:
		for k := range x {
			x[k] = plainStrings(x[k])
		}
		return x
	case nil:
		return ""
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}

// parseNetworkStep parses route, unroute, and request steps.
func parseNetworkStep(action string, n *yaml.Node) (any, error) {
	switch action {
	case "route":
		m, err := objectWithTypes(n, map[string]string{"pattern": "string", "status": "int", "body": "string", "bodyFile": "string", "contentType": "string", "headers": "stringmap", "abort": "bool", "times": "int"}, "pattern")
		if err != nil {
			return nil, err
		}
		if status, ok := m["status"].(int); ok && (status < 100 || status > 599) {
			return nil, errors.New("status must be 100..599")
		}
		if times, ok := m["times"].(int); ok && times < 1 {
			return nil, errors.New("times must be a positive integer")
		}
		if _, ok := m["body"]; ok && m["bodyFile"] != nil {
			return nil, errors.New("use body or bodyFile, not both")
		}
		return m, nil
	case "unroute":
		if n.Kind == yaml.ScalarNode && n.Tag == "!!bool" {
			if b, _ := boolean(n); b {
				return "", nil
			}
			return nil, errors.New("unroute must be a pattern or true")
		}
		return str(n)
	case "request":
		if n.Kind == yaml.ScalarNode && (n.Tag == "!!int" || n.Tag == "!!str") {
			return n.Value, nil
		}
		return nil, errors.New("request must be a request index")
	}
	return nil, fmt.Errorf("unsupported action %s", action)
}

func networkArgs(action string, value any) ([]string, bool) {
	switch action {
	case "route":
		m := value.(map[string]any)
		args := []string{"route", m["pattern"].(string)}
		if v, ok := m["status"]; ok {
			args = append(args, "--status", strconv.Itoa(v.(int)))
		}
		if v, ok := m["body"]; ok {
			args = append(args, "--body", v.(string))
		}
		if v, ok := m["bodyFile"]; ok {
			args = append(args, "--body-file", v.(string))
		}
		if v, ok := m["contentType"]; ok {
			args = append(args, "--content-type", v.(string))
		}
		if h, ok := m["headers"].(map[string]any); ok {
			names := make([]string, 0, len(h))
			for k := range h {
				names = append(names, k)
			}
			sort.Strings(names)
			for _, k := range names {
				args = append(args, "--header", k+":"+h[k].(string))
			}
		}
		if v, ok := m["times"]; ok {
			args = append(args, "--times", strconv.Itoa(v.(int)))
		}
		if v, ok := m["abort"]; ok && v.(bool) {
			args = append(args, "--abort")
		}
		return args, true
	case "unroute":
		if value.(string) == "" {
			return []string{"unroute"}, true
		}
		return []string{"unroute", value.(string)}, true
	case "request":
		return []string{"request", value.(string)}, true
	}
	return nil, false
}
