package flow

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/alg0rix/wok/internal/observe"
	"go.yaml.in/yaml/v3"
)

// objectWithTypes performs strict schema/type checking before interpolation.
func objectWithTypes(n *yaml.Node, schema map[string]string, required ...string) (map[string]any, error) {
	keys := make([]string, 0, len(schema))
	for key := range schema {
		keys = append(keys, key)
	}
	f, err := fields(n, keys...)
	if err != nil {
		return nil, err
	}
	for _, key := range required {
		if f[key] == nil {
			return nil, fmt.Errorf("missing field %q", key)
		}
	}
	out := map[string]any{}
	for key, node := range f {
		var value any
		switch schema[key] {
		case "string":
			value, err = str(node)
		case "bool":
			value, err = boolean(node)
		case "int":
			value, err = integer(node)
		case "number":
			value, err = num(node)
		case "strings":
			if node.Kind != yaml.SequenceNode || len(node.Content) == 0 {
				return nil, fmt.Errorf("%s must be a nonempty string list", key)
			}
			values := make([]any, len(node.Content))
			for i, item := range node.Content {
				values[i], err = str(item)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", key, err)
				}
			}
			value = values
		case "stringmap":
			var m map[string]string
			if node.Kind != yaml.MappingNode || node.Decode(&m) != nil {
				return nil, fmt.Errorf("%s must map names to strings", key)
			}
			values := map[string]any{}
			for k, v := range m {
				values[k] = v
			}
			value = values
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		out[key] = value
	}
	return out, nil
}
func parseNative(action string, n *yaml.Node) (any, error) {
	switch action {
	case "network":
		if n.Kind == yaml.MappingNode {
			return objectWithTypes(n, networkFilterTypes)
		}
		value, err := boolean(n)
		if err != nil || !value {
			return nil, errors.New("network must be true or a filter object (method, url, type, body)")
		}
		return true, nil
	case "console", "errors", "downloads", "dialogs":
		value, err := boolean(n)
		if err != nil || !value {
			return nil, fmt.Errorf("%s must be true", action)
		}
		return true, nil
	case "find":
		return objectWithTypes(n, map[string]string{"kind": "string", "value": "string", "nth": "int"}, "kind", "value")
	case "cookies":
		return objectWithTypes(n, map[string]string{"action": "string", "name": "string", "value": "string", "domain": "string", "path": "string", "url": "string", "secure": "bool", "httpOnly": "bool", "sameSite": "string", "expires": "number"}, "action")
	case "storage":
		return objectWithTypes(n, map[string]string{"area": "string", "action": "string", "key": "string", "value": "string"}, "area", "action")
	case "upload":
		return objectWithTypes(n, map[string]string{"selector": "string", "files": "strings"}, "selector", "files")
	case "dialog":
		return objectWithTypes(n, map[string]string{"accept": "bool", "promptText": "string"}, "accept")
	case "set":
		m, err := objectWithTypes(n, map[string]string{"device": "string", "media": "string", "offline": "bool"})
		if err != nil {
			return nil, err
		}
		if len(m) != 1 {
			return nil, errors.New("set requires exactly one device, media, or offline field")
		}
		return m, nil
	case "wait":
		if n.Kind == yaml.ScalarNode {
			return str(n)
		}
		m, err := objectWithTypes(n, map[string]string{"selector": "string", "state": "string", "text": "string", "url": "string", "fn": "string", "load": "bool", "networkIdle": "bool", "idleMs": "int", "download": "bool"})
		if err != nil {
			return nil, err
		}
		count := 0
		for _, key := range []string{"selector", "text", "url", "fn", "load", "networkIdle", "download"} {
			if _, ok := m[key]; ok {
				count++
			}
		}
		if count != 1 {
			return nil, errors.New("wait requires exactly one condition")
		}
		if _, ok := m["state"]; ok {
			if _, ok := m["selector"]; !ok {
				return nil, errors.New("wait state requires selector")
			}
		}
		if _, ok := m["idleMs"]; ok {
			if _, ok := m["networkIdle"]; !ok {
				return nil, errors.New("idleMs requires networkIdle")
			}
		}
		return m, nil
	}
	return nil, errors.New("unsupported native action")
}
func nativeManaged(action string, value any) bool {
	switch action {
	case "set", "console", "errors", "network", "downloads", "dialogs", "dialog":
		return true
	case "wait":
		if m, ok := value.(map[string]any); ok {
			return m["networkIdle"] != nil || m["download"] != nil
		}
	}
	return false
}
func isNative(action string) bool {
	switch action {
	case "find", "cookies", "storage", "upload", "console", "errors", "network", "downloads", "dialogs", "dialog", "set", "wait":
		return true
	}
	return false
}

// validateKnownNative checks each statically resolved field separately, so a
// dynamic upload/file name or input value cannot hide an invalid literal enum.
func validateKnownNative(action string, value any, vars *variables) error {
	if !isNative(action) {
		return nil
	}
	if !hasDynamic(value) {
		v, err := vars.resolve(value)
		if err != nil {
			return err
		}
		_, err = validateNative(action, v)
		return err
	}
	m, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if action == "upload" {
		for _, file := range m["files"].([]any) {
			if !hasDynamic(file) {
				resolved, err := vars.resolve(file)
				if err != nil {
					return err
				}
				if resolved.(string) == "" {
					return errors.New("upload file names must not be empty")
				}
			}
		}
	}
	known := map[string]any{}
	for key, item := range m {
		if !hasDynamic(item) {
			v, err := vars.resolve(item)
			if err != nil {
				return err
			}
			known[key] = v
		}
	}
	if actionValue, ok := known["action"]; ok {
		if err := nativeShape(action, actionValue.(string), m); err != nil {
			return err
		}
	}
	return validateNativeFields(action, known, false)
}

// Presence rules depend on a known action, not on whether its values are dynamic.
func nativeShape(action, subaction string, fields map[string]any) error {
	require := func(key string) error {
		if _, ok := fields[key]; !ok {
			return fmt.Errorf("missing field %q", key)
		}
		return nil
	}
	switch action {
	case "cookies":
		allowed := map[string]bool{"action": true}
		switch subaction {
		case "set":
			for _, key := range []string{"value", "secure", "httpOnly", "sameSite", "expires"} {
				allowed[key] = true
			}
			if err := require("value"); err != nil {
				return err
			}
			fallthrough
		case "delete":
			for _, key := range []string{"name", "domain", "path", "url"} {
				allowed[key] = true
			}
			if err := require("name"); err != nil {
				return err
			}
		}
		for key := range fields {
			if !allowed[key] {
				return fmt.Errorf("cookies %s does not accept %s", subaction, key)
			}
		}
	case "storage":
		if subaction == "get" || subaction == "set" || subaction == "delete" {
			if err := require("key"); err != nil {
				return err
			}
		} else if _, ok := fields["key"]; ok {
			return errors.New("storage list/clear does not accept key")
		}
		if subaction == "set" {
			return require("value")
		}
		if _, ok := fields["value"]; ok {
			return errors.New("storage value requires set")
		}
	}
	return nil
}
func validateNative(action string, value any) (bool, error) {
	if !isNative(action) {
		return false, nil
	}
	if action == "wait" {
		if s, ok := value.(string); ok {
			if s == "" {
				return true, errors.New("wait selector must not be empty")
			}
			return true, nil
		}
	}
	if _, ok := value.(bool); ok {
		return true, nil
	}
	return true, validateNativeFields(action, value.(map[string]any), true)
}
func validateNativeFields(action string, m map[string]any, complete bool) error {
	nonempty := func(key string) error {
		if value, ok := m[key]; ok && value.(string) == "" {
			return fmt.Errorf("%s must not be empty", key)
		}
		return nil
	}
	enum := func(key string, values ...string) error {
		value, ok := m[key]
		if !ok {
			return nil
		}
		for _, allowed := range values {
			if value == allowed {
				return nil
			}
		}
		return fmt.Errorf("invalid %s", key)
	}
	require := func(key string) error {
		if _, ok := m[key]; !ok && complete {
			return fmt.Errorf("missing field %q", key)
		}
		return nil
	}
	switch action {
	case "network":
		for _, key := range []string{"method", "url", "type"} {
			if err := nonempty(key); err != nil {
				return err
			}
		}
		if u, ok := m["url"].(string); ok {
			if hint := observe.RouteHint(u); hint != "" {
				return errors.New(hint)
			}
		}
	case "find":
		if err := enum("kind", "role", "text", "css"); err != nil {
			return err
		}
		if err := nonempty("value"); err != nil {
			return err
		}
		if v, ok := m["nth"]; ok && v.(int) < 1 {
			return errors.New("nth must be a positive integer")
		}
	case "cookies":
		if err := enum("action", "list", "set", "delete", "clear"); err != nil {
			return err
		}
		actionValue, known := m["action"]
		if known {
			allowed := map[string]bool{"action": true}
			switch actionValue {
			case "set":
				for _, k := range []string{"name", "value", "domain", "path", "url", "secure", "httpOnly", "sameSite", "expires"} {
					allowed[k] = true
				}
				if err := require("value"); err != nil {
					return err
				}
				fallthrough
			case "delete":
				for _, k := range []string{"name", "domain", "path", "url"} {
					allowed[k] = true
				}
				if err := require("name"); err != nil {
					return err
				}
				if err := nonempty("name"); err != nil {
					return err
				}
			}
			for key := range m {
				if !allowed[key] {
					return fmt.Errorf("cookies %s does not accept %s", actionValue, key)
				}
			}
		}
		if err := enum("sameSite", "Strict", "Lax", "None"); err != nil {
			return err
		}
	case "storage":
		if err := enum("area", "local", "session"); err != nil {
			return err
		}
		if err := enum("action", "list", "get", "set", "delete", "clear"); err != nil {
			return err
		}
		if a, ok := m["action"]; ok {
			if a == "get" || a == "set" || a == "delete" {
				if err := require("key"); err != nil {
					return err
				}
			} else if _, ok := m["key"]; ok {
				return errors.New("storage list/clear does not accept key")
			}
			if a == "set" {
				if err := require("value"); err != nil {
					return err
				}
			} else if _, ok := m["value"]; ok {
				return errors.New("storage value requires set")
			}
		}
	case "upload":
		if err := nonempty("selector"); err != nil {
			return err
		}
		if files, ok := m["files"].([]any); ok {
			for _, file := range files {
				if file.(string) == "" {
					return errors.New("upload file names must not be empty")
				}
			}
		}
	case "set":
		if err := nonempty("device"); err != nil {
			return err
		}
		if err := enum("media", "dark", "light", "no-preference"); err != nil {
			return err
		}
	case "wait":
		for _, key := range []string{"selector", "text", "url", "fn"} {
			if err := nonempty(key); err != nil {
				return err
			}
		}
		if err := enum("state", "attached", "detached", "visible", "hidden"); err != nil {
			return err
		}
		for _, key := range []string{"load", "networkIdle", "download"} {
			if value, ok := m[key]; ok && !value.(bool) {
				return fmt.Errorf("wait %s must be true", key)
			}
		}
		if v, ok := m["idleMs"]; ok && (v.(int) < 1 || v.(int) > 300000) {
			return errors.New("idleMs must be between 1 and 300000")
		}
	}
	return nil
}
func nativeArgs(action string, value any) ([]string, bool) {
	if !isNative(action) {
		return nil, false
	}
	if action == "network" {
		return append([]string{"network"}, networkFilterArgs(value)...), true
	}
	if action == "console" || action == "errors" || action == "downloads" || action == "dialogs" {
		return []string{action}, true
	}
	if action == "wait" {
		if s, ok := value.(string); ok {
			return []string{"wait", "--selector", s}, true
		}
	}
	m := value.(map[string]any)
	switch action {
	case "find":
		args := []string{"find", m["kind"].(string), m["value"].(string)}
		if nth, ok := m["nth"]; ok {
			args = append(args, "--nth", strconv.Itoa(nth.(int)))
		}
		return args, true
	case "cookies":
		a := m["action"].(string)
		args := []string{"cookies", a}
		if a == "set" || a == "delete" {
			args = append(args, m["name"].(string))
		}
		if a == "set" {
			args = append(args, m["value"].(string))
		}
		for _, key := range []string{"domain", "path", "url", "sameSite", "expires", "secure", "httpOnly"} {
			if v, ok := m[key]; ok {
				flag := map[string]string{"sameSite": "same-site", "httpOnly": "http-only"}[key]
				if flag == "" {
					flag = key
				}
				switch x := v.(type) {
				case bool:
					if x {
						args = append(args, "--"+flag)
					}
				case float64:
					args = append(args, "--"+flag, strconv.FormatFloat(x, 'g', -1, 64))
				case string:
					args = append(args, "--"+flag, x)
				}
			}
		}
		return args, true
	case "storage":
		a := m["action"].(string)
		args := []string{"storage", m["area"].(string), a}
		if key, ok := m["key"]; ok {
			args = append(args, key.(string))
		}
		if value, ok := m["value"]; ok {
			args = append(args, value.(string))
		}
		return args, true
	case "upload":
		args := []string{"upload", m["selector"].(string)}
		for _, file := range m["files"].([]any) {
			args = append(args, file.(string))
		}
		return args, true
	case "dialog":
		args := []string{"dialog", "dismiss"}
		if m["accept"].(bool) {
			args[1] = "accept"
		}
		if prompt, ok := m["promptText"]; ok {
			args = append(args, prompt.(string))
		}
		return args, true
	case "set":
		for _, key := range []string{"device", "media", "offline"} {
			if v, ok := m[key]; ok {
				val := ""
				if key == "offline" {
					val = "off"
					if v.(bool) {
						val = "on"
					}
				} else {
					val = v.(string)
				}
				return []string{"set", key, val}, true
			}
		}
	case "wait":
		args := []string{"wait"}
		for _, key := range []string{"selector", "state", "text", "url", "fn", "load", "networkIdle", "idleMs", "download"} {
			if v, ok := m[key]; ok {
				flag := map[string]string{"networkIdle": "network-idle", "idleMs": "network-idle-ms"}[key]
				if flag == "" {
					flag = key
				}
				args = append(args, "--"+flag)
				switch x := v.(type) {
				case string:
					args = append(args, x)
				case int:
					args = append(args, strconv.Itoa(x))
				}
			}
		}
		return args, true
	}
	return nil, true
}
