package flow

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestNativeActionsMapToCLIInOrder(t *testing.T) {
	source := []byte(`steps:
 - find: {kind: role, value: button, nth: 2}
 - find: {kind: css, value: '#file'}
 - cookies: {action: set, name: token, value: secret, domain: example.com, path: /, url: 'https://example.com', secure: true, httpOnly: true, sameSite: Lax, expires: 1234.5}
 - cookies: {action: list}
 - cookies: {action: delete, name: token, path: /}
 - cookies: {action: clear}
 - storage: {area: local, action: set, key: token, value: '${TOKEN}'}
 - storage: {area: session, action: get, key: token}
 - storage: {area: local, action: list}
 - storage: {area: local, action: delete, key: token}
 - storage: {area: session, action: clear}
 - upload: {selector: '#file', files: ['${FILE}', other.txt]}
 - console: true
 - errors: true
 - network: true
 - downloads: true
 - dialogs: true
 - dialog: {accept: true, promptText: 'yes please'}
 - dialog: {accept: false}
 - set: {device: iphone-14}
 - set: {media: dark}
 - set: {offline: true}
 - set: {offline: false}
`)
	s := &stub{}
	report, err := Run(context.Background(), source, Options{Vars: map[string]string{"TOKEN": "literal --profile", "FILE": "fixture.txt"}}, s.exec)
	if err != nil || !report.OK || len(report.Steps) != 23 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	want := [][]string{
		{"start"}, {"find", "role", "button", "--nth", "2"}, {"find", "css", "#file"},
		{"cookies", "set", "token", "secret", "--domain", "example.com", "--path", "/", "--url", "https://example.com", "--same-site", "Lax", "--expires", "1234.5", "--secure", "--http-only"},
		{"cookies", "list"}, {"cookies", "delete", "token", "--path", "/"}, {"cookies", "clear"},
		{"storage", "local", "set", "token", "literal --profile"}, {"storage", "session", "get", "token"}, {"storage", "local", "list"}, {"storage", "local", "delete", "token"}, {"storage", "session", "clear"},
		{"upload", "#file", "fixture.txt", "other.txt"}, {"console"}, {"errors"}, {"network"}, {"downloads"}, {"dialogs"}, {"dialog", "accept", "yes please"}, {"dialog", "dismiss"}, {"set", "device", "iphone-14"}, {"set", "media", "dark"}, {"set", "offline", "on"}, {"set", "offline", "off"},
	}
	if !reflect.DeepEqual(s.commands(), want) {
		t.Fatalf("commands=%q; want=%q", s.commands(), want)
	}
}
func TestWaitObjectForms(t *testing.T) {
	source := []byte(`steps:
 - wait: '#legacy'
 - wait: {selector: '#field', state: visible}
 - wait: {text: ready}
 - wait: {url: example.com}
 - wait: {fn: '() => true'}
 - wait: {load: true}
 - wait: {networkIdle: true, idleMs: 700}
 - wait: {download: true}
`)
	s := &stub{}
	report, err := Run(context.Background(), source, Options{}, s.exec)
	if err != nil || !report.OK {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	want := [][]string{{"start"}, {"wait", "--selector", "#legacy"}, {"wait", "--selector", "#field", "--state", "visible"}, {"wait", "--text", "ready"}, {"wait", "--url", "example.com"}, {"wait", "--fn", "() => true"}, {"wait", "--load"}, {"wait", "--network-idle", "--network-idle-ms", "700"}, {"wait", "--download"}}
	if !reflect.DeepEqual(s.commands(), want) {
		t.Fatalf("commands=%q; want=%q", s.commands(), want)
	}
}
func TestUploadInterpolatesSavedArrayValues(t *testing.T) {
	s := &stub{run: func(_ context.Context, args []string) (json.RawMessage, error) {
		if args[0] == "eval" {
			return json.RawMessage(`{"path":"saved.txt"}`), nil
		}
		return json.RawMessage(`{}`), nil
	}}
	source := []byte(`steps:
 - eval: '({path:"saved.txt"})'
   save: result
 - upload: {selector: '#file', files: ['${result.path}', '${FIXTURE}']}
`)
	report, err := Run(context.Background(), source, Options{Vars: map[string]string{"FIXTURE": "known.txt"}}, s.exec)
	if err != nil || !report.OK {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if got := s.commands()[2]; !reflect.DeepEqual(got, []string{"upload", "#file", "saved.txt", "known.txt"}) {
		t.Fatalf("upload args=%q", got)
	}
}
func TestNewNativeSchemasRejectInvalidBeforeStart(t *testing.T) {
	for name, step := range map[string]string{
		"find unknown key":            "find: {kind: css, value: button, other: 1}",
		"find unknown kind":           "find: {kind: invalid, value: button}",
		"find nth zero":               "find: {kind: role, value: button, nth: 0}",
		"find nth string":             "find: {kind: role, value: button, nth: '2'}",
		"cookies unknown key":         "cookies: {action: list, other: 1}",
		"cookies missing value":       "cookies: {action: set, name: token}",
		"cookies invalid same site":   "cookies: {action: set, name: token, value: x, sameSite: invalid}",
		"cookies missing name":        "cookies: {action: delete}",
		"cookies unexpected value":    "cookies: {action: delete, name: token, value: secret}",
		"cookies list options":        "cookies: {action: list, secure: false}",
		"cookies expires type":        "cookies: {action: set, name: x, value: x, expires: '123'}",
		"cookies expires nonfinite":   "cookies: {action: set, name: x, value: x, expires: .inf}",
		"storage unknown key":         "storage: {area: local, action: list, other: 1}",
		"storage bad area":            "storage: {area: invalid, action: list}",
		"storage missing key":         "storage: {area: local, action: get}",
		"storage missing value":       "storage: {area: local, action: set, key: x}",
		"storage unexpected key":      "storage: {area: local, action: clear, key: x}",
		"storage unexpected value":    "storage: {area: local, action: get, key: x, value: y}",
		"upload unknown key":          "upload: {selector: x, files: [x], other: 1}",
		"upload empty files":          "upload: {selector: x, files: []}",
		"upload empty string":         "upload: {selector: x, files: ['']}",
		"upload wrong list type":      "upload: {selector: x, files: [123]}",
		"upload missing variable":     "upload: {selector: x, files: ['${NO_SUCH_FLOW_VAR_929334}']}",
		"recording false":             "console: false",
		"recording string":            "errors: 'true'",
		"recording nested":            "network: {value: true}",
		"dialog unknown key":          "dialog: {accept: true, other: 1}",
		"dialog wrong accept":         "dialog: {accept: 'true'}",
		"set unknown key":             "set: {unknown: x}",
		"set multiple":                "set: {media: dark, offline: true}",
		"set invalid media":           "set: {media: wrong}",
		"set string bool":             "set: {offline: 'true'}",
		"wait unknown key":            "wait: {selector: x, other: 1}",
		"wait multiple":               "wait: {text: x, load: true}",
		"wait wrong state":            "wait: {selector: x, state: wrong}",
		"wait state no selector":      "wait: {text: x, state: hidden}",
		"wait false load":             "wait: {load: false}",
		"wait false download":         "wait: {download: false}",
		"wait idle without condition": "wait: {idleMs: 500}",
		"wait idle wrong condition":   "wait: {text: x, idleMs: 500}",
		"wait negative idle":          "wait: {networkIdle: true, idleMs: -1}",
	} {
		t.Run(name, func(t *testing.T) {
			s := &stub{}
			report, err := Run(context.Background(), []byte("steps:\n - "+step+"\n"), Options{}, s.exec)
			if err == nil || report.OK || len(s.calls) != 0 {
				t.Fatalf("report=%+v err=%v calls=%q", report, err, s.commands())
			}
		})
	}
}
func TestLiteralValidationCannotHideBehindSavedInputs(t *testing.T) {
	for _, step := range []string{
		"find: {kind: invalid, value: '${result}'}",
		"cookies: {action: set, name: token, value: '${result}', sameSite: invalid}",
		"cookies: {action: set, name: '${result}'}",
		"cookies: {action: list, name: '${result}'}",
		"storage: {area: local, action: set, key: '${result}'}",
		"storage: {area: local, action: list, key: '${result}'}",
		"upload: {selector: x, files: ['${result}', '']}",
		"wait: {selector: '${result}', state: wrong}",
	} {
		s := &stub{}
		source := "steps:\n - eval: '1'\n   save: result\n - " + step + "\n"
		_, err := Run(context.Background(), []byte(source), Options{}, s.exec)
		if err == nil || len(s.calls) != 0 {
			t.Errorf("invalid literal escaped preflight: %s; err=%v calls=%q", step, err, s.commands())
		}
	}
}
func TestNativeManagedOnlyExternalPreflight(t *testing.T) {
	for _, step := range []string{"console: true", "errors: true", "network: true", "downloads: true", "dialogs: true", "dialog: {accept: false}", "set: {media: dark}", "wait: {networkIdle: true}", "wait: {download: true}"} {
		s := &stub{}
		_, err := Run(context.Background(), []byte("steps:\n - "+step+"\n"), Options{Endpoint: "http://localhost:9222"}, s.exec)
		if err == nil || !strings.Contains(err.Error(), "managed profile") || len(s.calls) != 0 {
			t.Errorf("external native step %s: err=%v calls=%q", step, err, s.commands())
		}
	}
}
func TestDynamicEnumResolvedFromCapture(t *testing.T) {
	s := &stub{run: func(_ context.Context, args []string) (json.RawMessage, error) {
		if args[0] == "eval" {
			return json.RawMessage(`"text"`), nil
		}
		return json.RawMessage(`{}`), nil
	}}
	report, err := Run(context.Background(), []byte("steps:\n - eval: '\"text\"'\n   save: kind\n - find: {kind: '${kind}', value: hello}\n"), Options{}, s.exec)
	if err != nil || !report.OK {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if got := s.commands()[2]; !reflect.DeepEqual(got, []string{"find", "text", "hello"}) {
		t.Fatalf("dynamic enum args=%q", got)
	}
}
