package flow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type call struct {
	args []string
	ctx  context.Context
}
type stub struct {
	calls []call
	run   func(context.Context, []string) (json.RawMessage, error)
}

func (s *stub) exec(ctx context.Context, args []string) (json.RawMessage, error) {
	s.calls = append(s.calls, call{append([]string(nil), args...), ctx})
	if s.run != nil {
		return s.run(ctx, command(args))
	}
	return json.RawMessage(`{"ok":true}`), nil
}
func command(args []string) []string {
	for i, arg := range args {
		if arg == "--" {
			return args[i+1:]
		}
	}
	return args
}
func (s *stub) commands() [][]string {
	out := [][]string{}
	for _, c := range s.calls {
		out = append(out, command(c.args))
	}
	return out
}
func TestBatchOrderAndDefaults(t *testing.T) {
	source := []byte(`version: 1
profile: work
viewport: {width: 400, height: 800, mobile: true, dpr: 2}
steps:
  - name: Navigate
    open: https://example.com
  - fill: {selector: '#email', value: 'a@example.com'}
  - press: Enter
  - wait: '#ready'
  - get: {kind: text}
  - snapshot: {interactive: true}
  - screenshot: page.png
`)
	s := &stub{}
	report, err := Run(context.Background(), source, Options{}, s.exec)
	if err != nil || !report.OK || report.Profile != "work" || len(report.Steps) != 7 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	want := [][]string{{"start"}, {"viewport", "400", "800", "--mobile", "--dpr", "2"}, {"open", "https://example.com"}, {"fill", "#email", "a@example.com"}, {"press", "Enter"}, {"wait", "--selector", "#ready"}, {"get", "text"}, {"snapshot", "-i"}, {"screenshot", "page.png"}}
	if !reflect.DeepEqual(s.commands(), want) {
		t.Fatalf("calls=%q; want %q", s.commands(), want)
	}
	for _, c := range s.calls {
		if c.args[0] != "--profile" || c.args[1] != "work" {
			t.Fatalf("missing consistent profile: %q", c.args)
		}
	}
	if report.Steps[0].Index != 1 || report.Steps[0].Name != "Navigate" || report.Steps[0].Action != "open" {
		t.Fatalf("step metadata=%+v", report.Steps[0])
	}
}
func TestJSONExternalAndTabOverride(t *testing.T) {
	s := &stub{}
	report, err := Run(context.Background(), []byte(`{"profile":"authored","steps":[{"type":"--profile"}]}`), Options{Profile: "override", Endpoint: "http://localhost:9222", TargetID: "exact"}, s.exec)
	if err != nil || !report.OK || report.Profile != "override" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if len(s.calls) != 1 || !reflect.DeepEqual(command(s.calls[0].args), []string{"type", "--profile"}) {
		t.Fatalf("external calls=%q", s.commands())
	}
	args := s.calls[0].args
	if !reflect.DeepEqual(args[:4], []string{"--cdp", "http://localhost:9222", "--tab", "exact"}) {
		t.Fatalf("routing=%q", args)
	}
}
func TestAssertionsRetryAndAND(t *testing.T) {
	attempts := 0
	s := &stub{run: func(_ context.Context, args []string) (json.RawMessage, error) {
		if args[0] == "eval" {
			attempts++
			if attempts < 2 {
				return json.RawMessage(`false`), nil
			}
			return json.RawMessage(`true`), nil
		}
		return json.RawMessage(`{}`), nil
	}}
	report, err := Run(context.Background(), []byte(`steps:
 - expect: {selector: '#status', text: ready, contains: read, value: yes, count: 1, visible: true, url: 'https://example.com', js: 'document.readyState === "complete"'}
`), Options{}, s.exec)
	if err != nil || !report.OK || attempts != 2 || string(report.Steps[0].Result) != "true" {
		t.Fatalf("report=%+v err=%v attempts=%d", report, err, attempts)
	}
	expression := s.commands()[1][1]
	for _, fragment := range []string{"document.querySelectorAll(\"#status\")", "nodes.length===1", ".includes(\"read\")", "location.href===\"https://example.com\"", "typeof predicate!=='boolean'", "&&"} {
		if !strings.Contains(expression, fragment) {
			t.Errorf("expression missing %q: %s", fragment, expression)
		}
	}
}
func TestAssertionTimeoutScreenshotAndClose(t *testing.T) {
	dir := t.TempDir()
	attempts := 0
	s := &stub{run: func(_ context.Context, args []string) (json.RawMessage, error) {
		if args[0] == "eval" {
			attempts++
			return json.RawMessage(`false`), nil
		}
		return json.RawMessage(`{}`), nil
	}}
	report, err := Run(context.Background(), []byte("close: true\nsteps:\n - expect: {selector: '#missing'}\n   timeout: 110ms\n - click: '#never'\n"), Options{ArtifactDir: dir}, s.exec)
	if !errors.Is(err, context.DeadlineExceeded) || report.OK || len(report.Steps) != 1 || report.Steps[0].OK || attempts < 2 {
		t.Fatalf("report=%+v err=%v attempts=%d", report, err, attempts)
	}
	commands := s.commands()
	if commands[len(commands)-2][0] != "screenshot" || commands[len(commands)-1][0] != "stop" {
		t.Fatalf("cleanup calls=%q", commands)
	}
	if report.Screenshot != filepath.Join(dir, "failed-step-1.png") || report.ScreenshotError != "" {
		t.Fatalf("screenshot=%+v", report)
	}
}
func TestCaptureVariablesAndEnvironmentPrecedence(t *testing.T) {
	t.Setenv("WOK_FLOW_DOMAIN", "environment.example")
	s := &stub{run: func(_ context.Context, args []string) (json.RawMessage, error) {
		if args[0] == "tab" && args[1] == "new" {
			return json.RawMessage(`{"targetId":"target-123","nested":{"value":7}}`), nil
		}
		if args[0] == "get" {
			return json.RawMessage(`"captured"`), nil
		}
		return json.RawMessage(`{}`), nil
	}}
	source := []byte(`steps:
 - open: 'https://${WOK_FLOW_DOMAIN}'
 - tab: {new: 'about:blank'}
   save: tab1
 - tab: {select: '${tab1.targetId}'}
 - type: '${tab1.nested.value}'
 - get: {kind: title}
   save: title
 - type: '${title}'
`)
	report, err := Run(context.Background(), source, Options{Vars: map[string]string{"WOK_FLOW_DOMAIN": "option.example", "title": "old"}}, s.exec)
	if err != nil || !report.OK {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	commands := s.commands()
	if commands[1][1] != "https://option.example" || commands[3][2] != "target-123" || commands[4][1] != "7" || commands[6][1] != "captured" {
		t.Fatalf("interpolation=%q", commands)
	}
	// Interpolation never recursively interprets data returned by a variable.
	s = &stub{}
	_, err = Run(context.Background(), []byte("steps:\n - type: '${LITERAL}'\n"), Options{Vars: map[string]string{"LITERAL": "$(touch /tmp/never) ${NO_SUCH}"}}, s.exec)
	if err != nil || s.commands()[1][1] != "$(touch /tmp/never) ${NO_SUCH}" {
		t.Fatalf("literal variable content=%q err=%v", s.commands(), err)
	}
}
func TestPreflightRejectsBeforeAnyCalls(t *testing.T) {
	for name, source := range map[string]string{
		"unknown root":       "other: 1\nsteps: [{open: x}]",
		"unknown action":     "steps: [{shell: whoami}]",
		"multiple actions":   "steps: [{click: '#a', press: Enter}]",
		"nested fill":        "steps: [{fill: {selector: '#a', value: x, other: x}}]",
		"nested viewport":    "viewport: {width: 100, height: 100, other: 1}\nsteps: [{open: x}]",
		"nested expect":      "steps: [{expect: {js: 'true', other: 1}}]",
		"nested tab":         "steps: [{tab: {new: x, select: y}}]",
		"nested snapshot":    "steps: [{snapshot: {interactive: true, other: true}}]",
		"typed string":       "steps: [{open: 123}]",
		"typed bool":         "close: 'true'\nsteps: [{open: x}]",
		"missing variable":   "steps: [{open: '${WOK_FLOW_MISSING_928112}'}]",
		"future saved":       "steps: [{type: '${future}'}, {eval: '1', save: future}]",
		"invalid variable":   "steps: [{type: '${}'}]",
		"bad timeout":        "steps: [{open: x, timeout: 0s}]",
		"wrong version":      "version: 2\nsteps: [{open: x}]",
		"empty steps":        "steps: []",
		"multiple documents": "steps: [{open: x}]\n---\nsteps: [{open: y}]",
		"duplicate key":      "steps: [{open: x, open: y}]",
		"text no selector":   "steps: [{expect: {text: x}}]",
		"negative count":     "steps: [{expect: {selector: x, count: -1}}]",
		"invalid get":        "steps: [{get: {kind: no}}]",
		"missing get attr":   "steps: [{get: {kind: attr, selector: x}}]",
		"empty selector":     "steps: [{click: ''}]",
	} {
		t.Run(name, func(t *testing.T) {
			s := &stub{}
			report, err := Run(context.Background(), []byte(source), Options{}, s.exec)
			if err == nil || report.OK || report.Error == "" || len(s.calls) != 0 {
				t.Fatalf("report=%+v err=%v calls=%q", report, err, s.commands())
			}
		})
	}
	s := &stub{}
	_, err := Run(context.Background(), []byte(strings.Repeat(" ", 1<<20+1)), Options{}, s.exec)
	if err == nil || len(s.calls) != 0 {
		t.Fatal("accepted oversized flow")
	}
}
func TestExternalCannotCloseOrSetViewport(t *testing.T) {
	for _, source := range []string{"close: true\nsteps: [{open: x}]", "viewport: {width: 100, height: 100}\nsteps: [{open: x}]"} {
		s := &stub{}
		_, err := Run(context.Background(), []byte(source), Options{Endpoint: "ws://example.com"}, s.exec)
		if err == nil || len(s.calls) != 0 {
			t.Fatalf("external preflight err=%v calls=%q", err, s.commands())
		}
	}
}
func TestFailurePreservesPrimaryErrorAndRedactsInputs(t *testing.T) {
	dir := t.TempDir()
	primary := errors.New("operation failed with supersecret")
	s := &stub{run: func(ctx context.Context, args []string) (json.RawMessage, error) {
		switch args[0] {
		case "fill":
			return nil, primary
		case "screenshot":
			return nil, errors.New("screenshot failed")
		case "stop":
			return nil, errors.New("stop failed")
		}
		return json.RawMessage(`{}`), nil
	}}
	source := []byte("close: true\nsteps:\n - fill: {selector: '#token', value: '${TOKEN}'}\n - click: '#never'\n")
	report, err := Run(context.Background(), source, Options{ArtifactDir: dir, Vars: map[string]string{"TOKEN": "supersecret"}}, s.exec)
	if !errors.Is(err, primary) || report.Screenshot != "" || report.ScreenshotError != "screenshot failed" || !strings.Contains(report.Error, "[redacted]") {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	data, _ := json.Marshal(report)
	if strings.Contains(string(data), "supersecret") {
		t.Fatalf("report leaked substituted input: %s", data)
	}
	if got := s.commands(); len(got) != 4 || got[3][0] != "stop" {
		t.Fatalf("failure cleanup=%q", got)
	}
}
func TestCleanupUsesIndependentContextAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &stub{run: func(callctx context.Context, args []string) (json.RawMessage, error) {
		switch args[0] {
		case "eval":
			cancel()
			return nil, callctx.Err()
		case "screenshot", "stop":
			if callctx.Err() != nil {
				t.Errorf("cleanup context canceled: %v", callctx.Err())
			}
			deadline, ok := callctx.Deadline()
			if !ok || time.Until(deadline) > 5*time.Second {
				t.Error("cleanup context is not bounded")
			}
		}
		return json.RawMessage(`{}`), nil
	}}
	report, err := Run(ctx, []byte("close: true\nsteps: [{eval: 'true'}]"), Options{ArtifactDir: t.TempDir()}, s.exec)
	if !errors.Is(err, context.Canceled) || report.OK || report.Screenshot == "" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if got := s.commands(); len(got) != 4 || got[2][0] != "screenshot" || got[3][0] != "stop" {
		t.Fatalf("cleanup order=%q", got)
	}
}
func TestCloseFailureFailsOtherwiseSuccessfulRun(t *testing.T) {
	s := &stub{run: func(_ context.Context, args []string) (json.RawMessage, error) {
		if args[0] == "stop" {
			return nil, errors.New("shutdown unconfirmed")
		}
		return json.RawMessage(`true`), nil
	}}
	report, err := Run(context.Background(), []byte("close: true\nsteps: [{eval: 'true'}]"), Options{}, s.exec)
	if err == nil || report.OK || !strings.Contains(report.Error, "shutdown unconfirmed") || !report.Steps[0].OK {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}
func TestInvalidAssertionReturnsImmediately(t *testing.T) {
	for _, mode := range []string{"exception", "nonboolean"} {
		t.Run(mode, func(t *testing.T) {
			attempts := 0
			s := &stub{run: func(_ context.Context, args []string) (json.RawMessage, error) {
				if args[0] == "eval" {
					attempts++
					if mode == "exception" {
						return nil, errors.New("invalid selector")
					}
					return json.RawMessage(`"true"`), nil
				}
				return json.RawMessage(`{}`), nil
			}}
			_, err := Run(context.Background(), []byte("steps: [{expect: {selector: '['}}]"), Options{ArtifactDir: t.TempDir()}, s.exec)
			if err == nil || attempts != 1 {
				t.Fatalf("err=%v attempts=%d", err, attempts)
			}
		})
	}
}
func TestMissingCapturedPropertyFailsFast(t *testing.T) {
	s := &stub{run: func(_ context.Context, args []string) (json.RawMessage, error) {
		return json.RawMessage(`{"targetId":"id"}`), nil
	}}
	report, err := Run(context.Background(), []byte("steps:\n - eval: '({targetId:\"id\"})'\n   save: result\n - type: '${result.missing}'\n"), Options{ArtifactDir: t.TempDir()}, s.exec)
	if err == nil || len(report.Steps) != 2 || report.Steps[1].OK {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	for _, args := range s.commands() {
		if args[0] == "type" {
			t.Fatal("executed missing variable action")
		}
	}
}
func TestFailureArtifactDirectoryOnlyOnFailure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new-artifacts")
	s := &stub{}
	_, err := Run(context.Background(), []byte("steps: [{eval: 'true'}]"), Options{ArtifactDir: dir}, s.exec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("successful run created artifact directory: %v", err)
	}
}

func TestDefaultFailureArtifactsUseUniqueRunDirectories(t *testing.T) {
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Error(err)
		}
	})
	paths := []string{}
	for i := 0; i < 2; i++ {
		s := &stub{run: func(_ context.Context, args []string) (json.RawMessage, error) {
			if args[0] == "click" {
				return nil, errors.New("missing element")
			}
			return json.RawMessage(`{}`), nil
		}}
		report, err := Run(context.Background(), []byte("steps: [{click: '#missing'}]"), Options{}, s.exec)
		if err == nil || report.Screenshot == "" {
			t.Fatalf("report=%+v err=%v", report, err)
		}
		paths = append(paths, report.Screenshot)
		if filepath.Base(report.Screenshot) != "failed-step-1.png" {
			t.Fatalf("artifact path=%q", report.Screenshot)
		}
		if _, err := os.Stat(filepath.Dir(report.Screenshot)); err != nil {
			t.Fatal(err)
		}
	}
	if paths[0] == paths[1] {
		t.Fatal("failure runs reused an artifact directory")
	}
}
func TestAssertionValuesAreJSONQuoted(t *testing.T) {
	payload := `");throw new Error('injected');//`
	expression := expectExpression(map[string]any{"selector": payload, "text": payload})
	quoted, _ := json.Marshal(payload)
	if strings.Count(expression, string(quoted)) != 2 {
		t.Fatalf("selector/expected text not safely quoted: %s", expression)
	}
	expression = expectExpression(map[string]any{"selector": ".absent", "count": 0, "url": "https://example.com"})
	if strings.Contains(expression, "nodes.length>0") {
		t.Fatalf("count zero incorrectly requires an element: %s", expression)
	}
}
