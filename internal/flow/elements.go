package flow

import (
	"errors"
	"fmt"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// Element, keyboard, navigation, and file actions that map one-to-one to CLI
// commands of the same name.
var (
	// targetActions take a selector or @ref, or a key for keydown/keyup.
	targetActions = map[string]bool{"dblclick": true, "hover": true, "check": true, "uncheck": true, "keydown": true, "keyup": true}
	// fileActions take a path: pdf and state-save write it, state-load reads it.
	fileActions = map[string]bool{"pdf": true, "state-save": true, "state-load": true}
	// historyActions take true.
	historyActions = map[string]bool{"go-back": true, "go-forward": true, "reload": true}
)

func isElementAction(action string) bool {
	return targetActions[action] || fileActions[action] || historyActions[action] || action == "select" || action == "drag" || action == "drop"
}

func parseElementAction(action string, n *yaml.Node) (any, error) {
	switch {
	case targetActions[action], fileActions[action]:
		return str(n)
	case historyActions[action]:
		value, err := boolean(n)
		if err != nil || !value {
			return nil, fmt.Errorf("%s must be true", action)
		}
		return true, nil
	case action == "select":
		m, err := objectWithTypes(n, map[string]string{"selector": "string", "value": "string", "values": "strings"}, "selector")
		if err != nil {
			return nil, err
		}
		_, one := m["value"]
		_, many := m["values"]
		if one == many {
			return nil, errors.New("select requires value or values")
		}
		return m, nil
	case action == "drag":
		return objectWithTypes(n, map[string]string{"from": "string", "to": "string"}, "from", "to")
	case action == "drop":
		m, err := objectWithTypes(n, map[string]string{"selector": "string", "files": "strings", "text": "string"}, "selector")
		if err != nil {
			return nil, err
		}
		_, files := m["files"]
		_, text := m["text"]
		if files == text {
			return nil, errors.New("drop requires files or text")
		}
		return m, nil
	}
	return nil, fmt.Errorf("unsupported action %s", action)
}

func validateElementAction(action string, value any) (bool, error) {
	if !isElementAction(action) {
		return false, nil
	}
	nonempty := func(v any, label string) error {
		if s, ok := v.(string); ok && s == "" {
			return fmt.Errorf("%s must not be empty", label)
		}
		return nil
	}
	switch m := value.(type) {
	case string:
		return true, nonempty(m, action)
	case map[string]any:
		for _, key := range []string{"selector", "from", "to", "text"} {
			if err := nonempty(m[key], key); err != nil {
				return true, err
			}
		}
		if files, ok := m["files"].([]any); ok {
			for _, file := range files {
				if file.(string) == "" {
					return true, errors.New("file names must not be empty")
				}
			}
		}
	}
	return true, nil
}

func elementArgs(action string, value any) ([]string, bool) {
	if !isElementAction(action) {
		return nil, false
	}
	if historyActions[action] {
		return []string{action}, true
	}
	if s, ok := value.(string); ok {
		return []string{action, s}, true
	}
	m := value.(map[string]any)
	switch action {
	case "select":
		args := []string{"select", m["selector"].(string)}
		if v, ok := m["value"]; ok {
			return append(args, v.(string)), true
		}
		for _, v := range m["values"].([]any) {
			args = append(args, v.(string))
		}
		return args, true
	case "drag":
		return []string{"drag", m["from"].(string), m["to"].(string)}, true
	case "drop":
		args := []string{"drop", m["selector"].(string)}
		if text, ok := m["text"]; ok {
			return append(args, "--text", text.(string)), true
		}
		for _, file := range m["files"].([]any) {
			args = append(args, file.(string))
		}
		return args, true
	}
	return nil, false
}

// inFlowDir resolves files a step reads (upload and drop files, route
// bodyFile, state-load) against the flow file's directory. Files a step writes
// stay relative to the working directory.
func inFlowDir(action string, value any, dir string) any {
	if dir == "" {
		return value
	}
	join := func(path string) string {
		if filepath.IsAbs(path) {
			return path
		}
		return filepath.Join(dir, path)
	}
	switch action {
	case "state-load":
		return join(value.(string))
	case "upload", "drop", "route":
		m := value.(map[string]any)
		out := make(map[string]any, len(m))
		for k, v := range m {
			out[k] = v
		}
		if files, ok := m["files"].([]any); ok {
			rebased := make([]any, len(files))
			for i, file := range files {
				rebased[i] = join(file.(string))
			}
			out["files"] = rebased
		}
		if file, ok := m["bodyFile"].(string); ok {
			out["bodyFile"] = join(file)
		}
		return out
	}
	return value
}
