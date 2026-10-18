package render

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alg0rix/wok/internal/cli"
	wokversion "github.com/alg0rix/wok/internal/version"
)

type toolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"inputSchema"`
}

func property(kind string) map[string]any    { return map[string]any{"type": kind} }
func choice(values ...string) map[string]any { return map[string]any{"type": "string", "enum": values} }
func stringArray() map[string]any {
	return map[string]any{"type": "array", "items": property("string"), "maxItems": 256}
}
func mcpTools() []toolDefinition {
	tools := []toolDefinition{}
	add := func(name, description string, properties map[string]any, required ...string) {
		if properties == nil {
			properties = map[string]any{}
		}
		if required == nil {
			required = []string{}
		}
		properties["tab"] = property("string")
		tools = append(tools, toolDefinition{"browser_" + name, description, map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}})
	}
	add("open", "Navigate the selected browser tab.", map[string]any{"url": property("string")}, "url")
	add("snapshot", "Read accessibility nodes and persistent element refs.", map[string]any{"interactive": property("boolean")})
	add("click", "Click a CSS selector or snapshot ref.", map[string]any{"selector": property("string")}, "selector")
	add("fill", "Replace an input value.", map[string]any{"selector": property("string"), "text": property("string")}, "selector", "text")
	add("type", "Type into the focused element.", map[string]any{"text": property("string")}, "text")
	add("press", "Press a key or modifier combination.", map[string]any{"key": property("string")}, "key")
	add("get", "Read page text, title, URL or an attribute.", map[string]any{"kind": choice("text", "title", "url", "attr"), "selector": property("string"), "name": property("string")}, "kind")
	add("eval", "Evaluate JavaScript in the page.", map[string]any{"expression": property("string")}, "expression")
	add("wait", "Wait for a CSS selector.", map[string]any{"selector": property("string")}, "selector")
	add("screenshot", "Capture the selected viewport as an inline PNG image.", nil)
	add("tabs", "List, open, select or close browser tabs.", map[string]any{"action": choice("list", "new", "select", "close"), "url": property("string"), "id": property("string")})
	add("find", "Find nodes by role, visible text or CSS selector.", map[string]any{"kind": choice("role", "text", "css"), "value": property("string"), "nth": map[string]any{"type": "integer", "minimum": 1.0}}, "kind", "value")
	add("cookies", "List, set, delete or clear browser cookies; args use Wok cookie syntax.", map[string]any{"action": choice("list", "set", "delete", "clear"), "args": stringArray()})
	add("storage", "Read or edit local/session storage.", map[string]any{"area": choice("local", "session"), "action": choice("list", "get", "set", "delete", "clear"), "key": property("string"), "value": property("string")}, "area")
	add("upload", "Set files on an input element.", map[string]any{"selector": property("string"), "files": stringArray()}, "selector", "files")
	for _, name := range []string{"downloads", "console", "errors", "network"} {
		add(name, "Read persistent "+name+" recordings.", nil)
	}
	add("set", "Set profile color media or offline mode.", map[string]any{"setting": choice("media", "offline"), "value": map[string]any{"type": []string{"string", "boolean"}}}, "setting", "value")
	add("viewport", "Set persistent viewport/mobile emulation, or reset it.", map[string]any{"width": map[string]any{"type": "integer", "minimum": 1.0, "maximum": 8192.0}, "height": map[string]any{"type": "integer", "minimum": 1.0, "maximum": 8192.0}, "dpr": map[string]any{"type": "number", "minimum": 0.5, "maximum": 4.0}, "mobile": property("boolean"), "reset": property("boolean")})
	add("skills", "Read the bundled Wok agent instructions.", nil)
	add("status", "Read the managed profile status.", nil)
	add("command", "Run an allowed browser command as an argument array. No shell is used.", map[string]any{"command": choice("open", "snapshot", "click", "fill", "type", "press", "get", "eval", "wait", "screenshot", "viewport", "tab", "find", "cookies", "storage", "upload", "downloads", "console", "errors", "network", "dialogs", "dialog", "set", "status", "skills"), "args": stringArray()}, "command")
	return tools
}
func validateToolArguments(schema map[string]any, args map[string]any) error {
	props := schema["properties"].(map[string]any)
	for _, name := range schema["required"].([]string) {
		if _, ok := args[name]; !ok {
			return fmt.Errorf("%s is required", name)
		}
	}
	for name, value := range args {
		raw, ok := props[name]
		if !ok {
			return fmt.Errorf("unknown argument %s", name)
		}
		s := raw.(map[string]any)
		kind := s["type"]
		matches := func(k string) bool {
			switch k {
			case "string":
				_, ok := value.(string)
				return ok
			case "boolean":
				_, ok := value.(bool)
				return ok
			case "integer":
				n, ok := value.(float64)
				return ok && math.Trunc(n) == n
			case "number":
				_, ok := value.(float64)
				return ok
			case "array":
				_, ok := value.([]any)
				return ok
			}
			return false
		}
		good := false
		switch k := kind.(type) {
		case string:
			good = matches(k)
		case []string:
			for _, v := range k {
				good = good || matches(v)
			}
		}
		if !good {
			return fmt.Errorf("invalid type for %s", name)
		}
		if allowed, ok := s["enum"].([]string); ok {
			found := false
			for _, v := range allowed {
				if value == v {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("invalid %s", name)
			}
		}
		if n, ok := value.(float64); ok {
			if min, ok := s["minimum"].(float64); ok && n < min {
				return fmt.Errorf("%s below minimum", name)
			}
			if max, ok := s["maximum"].(float64); ok && n > max {
				return fmt.Errorf("%s above maximum", name)
			}
		}
		if list, ok := value.([]any); ok {
			if len(list) > 256 {
				return fmt.Errorf("too many %s", name)
			}
			for _, item := range list {
				if _, ok := item.(string); !ok {
					return fmt.Errorf("%s must contain strings", name)
				}
			}
		}
	}
	return nil
}
func toolCommand(name string, a map[string]any) ([]string, error) {
	s := func(k string) string { v, _ := a[k].(string); return v }
	list := func(k string) []string {
		out := []string{}
		v, _ := a[k].([]any)
		for _, item := range v {
			out = append(out, item.(string))
		}
		return out
	}
	args := []string{strings.TrimPrefix(name, "browser_")}
	switch name {
	case "browser_open":
		args = append(args, s("url"))
	case "browser_snapshot":
		if a["interactive"] == true {
			args = append(args, "-i")
		}
	case "browser_click", "browser_wait":
		args = append(args, s("selector"))
	case "browser_fill":
		args = append(args, s("selector"), s("text"))
	case "browser_type":
		args = append(args, s("text"))
	case "browser_press":
		args = append(args, s("key"))
	case "browser_eval":
		args = append(args, s("expression"))
	case "browser_get":
		args = append(args, s("kind"))
		if s("selector") != "" {
			args = append(args, s("selector"))
		}
		if s("kind") == "attr" {
			if s("selector") == "" || s("name") == "" {
				return nil, errors.New("attribute requires selector and name")
			}
			args = append(args, s("name"))
		}
	case "browser_tabs":
		action := s("action")
		if action == "" {
			action = "list"
		}
		args = []string{"tab", action}
		if action == "new" && s("url") != "" {
			args = append(args, s("url"))
		}
		if action == "select" || action == "close" {
			if s("id") == "" {
				return nil, errors.New("id is required")
			}
			args = append(args, s("id"))
		}
	case "browser_find":
		args = append(args, s("kind"), s("value"))
		if n, ok := a["nth"].(float64); ok {
			args = append(args, "--nth", strconv.Itoa(int(n)))
		}
	case "browser_cookies":
		action := s("action")
		if action == "" {
			action = "list"
		}
		args = append(args, action)
		args = append(args, list("args")...)
	case "browser_storage":
		action := s("action")
		if action == "" {
			action = "list"
		}
		args = append(args, s("area"), action)
		if action == "get" || action == "set" || action == "delete" {
			if _, ok := a["key"]; !ok {
				return nil, errors.New("key is required")
			}
			args = append(args, s("key"))
		}
		if action == "set" {
			if _, ok := a["value"]; !ok {
				return nil, errors.New("value is required")
			}
			args = append(args, s("value"))
		}
	case "browser_upload":
		files := list("files")
		if len(files) == 0 {
			return nil, errors.New("files cannot be empty")
		}
		args = append(args, s("selector"))
		args = append(args, files...)
	case "browser_set":
		value := s("value")
		if s("setting") == "media" && value != "dark" && value != "light" && value != "no-preference" {
			return nil, errors.New("invalid media value")
		}
		if s("setting") == "offline" {
			b, ok := a["value"].(bool)
			if !ok {
				return nil, errors.New("offline must be a boolean")
			}
			value = "off"
			if b {
				value = "on"
			}
		}
		args = append(args, s("setting"), value)
	case "browser_viewport":
		if a["reset"] == true {
			for key := range a {
				if key != "reset" && key != "tab" {
					return nil, errors.New("reset cannot include viewport options")
				}
			}
			args = append(args, "reset")
			break
		}
		width, wok := a["width"].(float64)
		height, hok := a["height"].(float64)
		if !wok || !hok {
			return nil, errors.New("width and height are required")
		}
		args = append(args, strconv.Itoa(int(width)), strconv.Itoa(int(height)))
		if a["mobile"] == true {
			args = append(args, "--mobile")
		}
		if dpr, ok := a["dpr"].(float64); ok {
			args = append(args, "--dpr", strconv.FormatFloat(dpr, 'g', -1, 64))
		}
	case "browser_skills":
		args = append(args, "show")
	case "browser_command":
		args = append([]string{s("command")}, list("args")...)
		if args[0] == "skills" && (len(args) < 2 || (args[1] != "show" && args[1] != "list" && args[1] != "get")) {
			return nil, errors.New("only skills show/list/get are allowed")
		}
	}
	return args, nil
}
func (h *handler) callTool(ctx context.Context, name string, args map[string]any) (any, error) {
	var definition *toolDefinition
	for _, tool := range mcpTools() {
		if tool.Name == name {
			copy := tool
			definition = &copy
			break
		}
	}
	if definition == nil {
		return nil, errors.New("unknown tool")
	}
	if err := validateToolArguments(definition.Schema, args); err != nil {
		return nil, err
	}
	if name == "browser_screenshot" {
		data, err := h.mcpScreenshot(ctx, stringValue(args["tab"]))
		if err != nil {
			return toolFailure(err.Error()), nil
		}
		return map[string]any{"content": []any{map[string]any{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(data)}}, "isError": false}, nil
	}
	command, err := toolCommand(name, args)
	if err != nil {
		return nil, err
	}
	profile := h.options.Profile
	if profile == "" {
		profile = "default"
	}
	cliArgs := []string{"--profile", profile, "--json", "--timeout", "2m"}
	if tab := stringValue(args["tab"]); tab != "" {
		cliArgs = append(cliArgs, "--tab", tab)
	}
	cliArgs = append(cliArgs, "--")
	cliArgs = append(cliArgs, command...)
	var stdout, stderr bytes.Buffer
	code := cli.Run(ctx, cliArgs, &stdout, &stderr)
	text := strings.TrimSpace(stdout.String())
	if code != 0 {
		text = strings.TrimSpace(stderr.String())
		if text == "" {
			text = "browser command failed"
		}
		return toolFailure(text), nil
	}
	result := map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": false}
	var structured any
	if json.Unmarshal(stdout.Bytes(), &structured) == nil {
		if object, ok := structured.(map[string]any); ok {
			result["structuredContent"] = object
		}
	}
	return result, nil
}
func stringValue(v any) string { s, _ := v.(string); return s }
func toolFailure(message string) any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": message}}, "isError": true}
}
func (h *handler) mcp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", 405)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	r = r.WithContext(ctx)
	controller := http.NewResponseController(w)
	controller.SetReadDeadline(time.Now().Add(2 * time.Minute))
	defer controller.SetReadDeadline(time.Time{})
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		rpcError(w, nil, -32700, "request body exceeds 1 MiB")
		return
	}
	var request struct {
		Version string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err = json.Unmarshal(body, &request); err != nil {
		rpcError(w, nil, -32700, "parse error")
		return
	}
	if request.Version != "2.0" || request.Method == "" {
		rpcError(w, nil, -32600, "invalid JSON-RPC request")
		return
	}
	if len(request.ID) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var id any
	if err = json.Unmarshal(request.ID, &id); err != nil {
		rpcError(w, nil, -32600, "invalid request id")
		return
	}
	switch id.(type) {
	case string, float64, nil:
	default:
		rpcError(w, nil, -32600, "invalid request id")
		return
	}
	var result any
	switch request.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if len(request.Params) > 0 && json.Unmarshal(request.Params, &params) != nil {
			rpcError(w, id, -32602, "invalid initialize parameters")
			return
		}
		version := params.ProtocolVersion
		if version != "2024-11-05" && version != "2025-03-26" {
			version = "2025-03-26"
		}
		result = map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "serverInfo": map[string]any{"name": "Wok", "version": wokversion.Version}, "instructions": "Call browser_skills to read Wok usage. Browser sessions persist between tool calls."}
	case "ping":
		result = map[string]any{}
	case "tools/list":
		result = map[string]any{"tools": mcpTools()}
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		dec := json.NewDecoder(bytes.NewReader(request.Params))
		dec.DisallowUnknownFields()
		if err = dec.Decode(&params); err != nil || params.Name == "" {
			rpcError(w, id, -32602, "invalid tool parameters")
			return
		}
		if params.Arguments == nil {
			params.Arguments = map[string]any{}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		result, err = h.callTool(ctx, params.Name, params.Arguments)
		cancel()
		if err != nil {
			rpcError(w, id, -32602, err.Error())
			return
		}
	default:
		rpcError(w, id, -32601, "method not found")
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}
func rpcError(w http.ResponseWriter, id any, code int, message string) {
	json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}
