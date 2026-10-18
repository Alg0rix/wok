package flow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type part struct {
	text     string
	variable bool
}
type template []part

var variableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

// jsonSuffix marks ${NAME|json}, which inserts the value as a JSON literal.
// Captured values keep their JSON form under NAME|json.
const jsonSuffix = "|json"

// under reports whether key is name or one of its fields or forms.
func under(key, name string) bool {
	return key == name || strings.HasPrefix(key, name+".") || strings.HasPrefix(key, name+jsonSuffix)
}

type variables struct {
	values  map[string]string
	secrets map[string]bool
}

func newVariables(initial map[string]string) *variables {
	v := &variables{values: map[string]string{}, secrets: map[string]bool{}}
	for k, val := range initial {
		v.values[k] = val
		if val != "" {
			v.secrets[val] = true
		}
	}
	return v
}
func (v *variables) lookup(name string) (string, bool) {
	if val, ok := v.values[name]; ok {
		return val, true
	}
	if val, ok := os.LookupEnv(name); ok {
		v.values[name] = val
		if val != "" {
			v.secrets[val] = true
		}
		return val, true
	}
	return "", false
}
func (v *variables) compile(s string, saved map[string]bool) (template, error) {
	out := template{}
	for {
		before, after, found := strings.Cut(s, "${")
		if !found {
			out = append(out, part{text: s})
			break
		}
		if strings.HasSuffix(before, "$") {
			// $${...} is a literal ${...}, e.g. a JavaScript template literal.
			out = append(out, part{text: before[:len(before)-1] + "${"})
			s = after
			continue
		}
		out = append(out, part{text: before})
		name, rest, closed := strings.Cut(after, "}")
		name, asJSON := strings.CutSuffix(name, jsonSuffix)
		if !closed || !variableName.MatchString(name) {
			return nil, errors.New("invalid ${NAME} variable reference (write $${...} for a literal ${...})")
		}
		prefix, _, _ := strings.Cut(name, ".")
		if saved[prefix] {
			if asJSON {
				name += jsonSuffix
			}
			out = append(out, part{text: name, variable: true})
		} else if value, ok := v.lookup(name); ok {
			if asJSON {
				value = jsQuote(value)
			}
			out = append(out, part{text: value})
		} else {
			return nil, fmt.Errorf("missing variable %s (write $${%s} for a literal ${%s})", name, name, name)
		}
		s = rest
	}
	return out, nil
}
func (v *variables) compileValue(value any, saved map[string]bool) (any, error) {
	switch x := value.(type) {
	case string:
		return v.compile(x, saved)
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			compiled, err := v.compileValue(item, saved)
			if err != nil {
				return nil, err
			}
			out[i] = compiled
		}
		return out, nil
	case map[string]any:
		out := map[string]any{}
		for k, item := range x {
			compiled, err := v.compileValue(item, saved)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			out[k] = compiled
		}
		return out, nil
	default:
		return value, nil
	}
}
func (v *variables) resolve(value any) (any, error) {
	switch x := value.(type) {
	case template:
		var out strings.Builder
		for _, p := range x {
			if !p.variable {
				out.WriteString(p.text)
				continue
			}
			val, ok := v.values[p.text]
			if name, asJSON := strings.CutSuffix(p.text, jsonSuffix); asJSON && !ok {
				// Values set without a JSON form are text.
				if val, ok = v.values[name]; ok {
					val = jsQuote(val)
				}
			}
			if !ok {
				if name, asJSON := strings.CutSuffix(p.text, jsonSuffix); asJSON {
					return nil, fmt.Errorf("saved variable %s has no value", name)
				}
				return nil, fmt.Errorf("saved variable %s has no scalar value (use ${%s|json} for its JSON form)", p.text, p.text)
			}
			out.WriteString(val)
		}
		return out.String(), nil
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			resolved, err := v.resolve(item)
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	case map[string]any:
		out := map[string]any{}
		for k, item := range x {
			resolved, err := v.resolve(item)
			if err != nil {
				return nil, err
			}
			out[k] = resolved
		}
		return out, nil
	default:
		return value, nil
	}
}
func hasDynamic(value any) bool {
	switch x := value.(type) {
	case template:
		for _, p := range x {
			if p.variable {
				return true
			}
		}
	case []any:
		for _, v := range x {
			if hasDynamic(v) {
				return true
			}
		}
	case map[string]any:
		for _, v := range x {
			if hasDynamic(v) {
				return true
			}
		}
	}
	return false
}
func (v *variables) capture(name string, raw json.RawMessage) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return fmt.Errorf("cannot save result: %w", err)
	}
	for k := range v.values {
		if under(k, name) {
			delete(v.values, k)
		}
	}
	return v.flatten(name, value, true)
}

// setLocal stores a loop variable. Loop values are not page data, so they are
// not treated as secrets for error redaction.
func (v *variables) setLocal(name string, value any) {
	for k := range v.values {
		if under(k, name) {
			delete(v.values, k)
		}
	}
	_ = v.flatten(name, value, false)
}

// snapshot copies the values stored under name and name.*.
func (v *variables) snapshot(name string) map[string]string {
	out := map[string]string{}
	for k, val := range v.values {
		if under(k, name) {
			out[k] = val
		}
	}
	return out
}

// restore replaces the values under name and name.* with a snapshot.
func (v *variables) restore(name string, saved map[string]string) {
	for k := range v.values {
		if under(k, name) {
			delete(v.values, k)
		}
	}
	for k, val := range saved {
		v.values[k] = val
	}
}

// flatten stores scalars under name, object fields under name.FIELD, and
// arrays as JSON text under name plus name.N items and name.length.
func (v *variables) flatten(name string, value any, secret bool) error {
	if data, err := json.Marshal(value); err == nil {
		v.values[name+jsonSuffix] = string(data)
	}
	switch x := value.(type) {
	case map[string]any:
		for k, item := range x {
			if err := v.flatten(name+"."+k, item, secret); err != nil {
				return err
			}
		}
		return nil
	case []any:
		data, err := json.Marshal(x)
		if err != nil {
			return err
		}
		v.values[name] = string(data)
		v.values[name+".length"] = strconv.Itoa(len(x))
		v.values[name+".length"+jsonSuffix] = strconv.Itoa(len(x))
		for i, item := range x {
			if err := v.flatten(name+"."+strconv.Itoa(i), item, secret); err != nil {
				return err
			}
		}
		return nil
	default:
		var s string
		text, isText := value.(string)
		if isText {
			s = text
		} else {
			data, err := json.Marshal(value)
			if err != nil {
				return err
			}
			s = string(data)
		}
		v.values[name] = s
		// Numbers and booleans are not redacted: short values such as "1"
		// would otherwise mangle step paths and messages.
		if secret && isText && s != "" {
			v.secrets[s] = true
		}
		return nil
	}
}

type redactedError struct {
	message  string
	original error
}

func (e redactedError) Error() string { return e.message }
func (e redactedError) Unwrap() error { return e.original }
func (v *variables) redact(err error) error {
	if err == nil {
		return nil
	}
	secrets := make([]string, 0, len(v.secrets))
	for secret := range v.secrets {
		secrets = append(secrets, secret)
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	replacements := make([]string, 0, len(secrets)*2)
	for _, secret := range secrets {
		replacements = append(replacements, secret, "[redacted]")
	}
	message := strings.NewReplacer(replacements...).Replace(err.Error())
	if message == err.Error() {
		return err
	}
	return redactedError{message, err}
}
