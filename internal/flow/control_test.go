package flow

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// pageStub answers condition evals from a queue and records other commands.
func pageStub(conditions ...bool) *stub {
	return &stub{run: func(_ context.Context, args []string) (json.RawMessage, error) {
		if args[0] == "eval" && strings.HasPrefix(args[1], "(()=>{") {
			if len(conditions) == 0 {
				return json.RawMessage(`false`), nil
			}
			ok := conditions[0]
			conditions = conditions[1:]
			return json.RawMessage(map[bool]string{true: "true", false: "false"}[ok]), nil
		}
		if args[0] == "eval" {
			return json.RawMessage(`["hi","bye"]`), nil
		}
		return json.RawMessage(`{}`), nil
	}}
}

func actions(s *stub) [][]string {
	out := [][]string{}
	for _, c := range s.commands() {
		if c[0] != "start" && !(c[0] == "eval" && strings.HasPrefix(c[1], "(()=>{")) {
			out = append(out, c)
		}
	}
	return out
}

func TestIfBranches(t *testing.T) {
	source := []byte(`steps:
  - if: {selector: '#banner', visible: true}
    then:
      - click: '#close'
    else:
      - press: Escape
  - if: {js: 'location.hash === ""'}
    then:
      - click: '#a'
`)
	s := pageStub(false, true)
	report, err := Run(context.Background(), source, Options{}, s.exec)
	if err != nil || !report.OK {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	want := [][]string{{"press", "Escape"}, {"click", "#a"}}
	if !reflect.DeepEqual(actions(s), want) {
		t.Fatalf("actions=%q want %q", actions(s), want)
	}
	paths := []string{}
	for _, st := range report.Steps {
		paths = append(paths, st.Path)
	}
	if !reflect.DeepEqual(paths, []string{"", "1.else.1", "", "2.then.1"}) || string(report.Steps[0].Result) != `{"matched":false}` {
		t.Fatalf("steps=%+v", report.Steps)
	}
}

func TestRepeatAndEachWithLoopVariables(t *testing.T) {
	source := []byte(`steps:
  - repeat:
      times: 2
      steps:
        - fill: {selector: '#n', value: 'row ${loop.index}/${loop.number}'}
  - eval: 'messages()'
    save: msgs
  - each:
      items: '${msgs}'
      as: msg
      steps:
        - fill: {selector: '.chat-input', value: '${msg.item}'}
  - each:
      items: [{text: one}, {text: two}]
      steps:
        - type: '${loop.item.text}'
  - type: '${msgs.1} ${msgs.length}'
`)
	s := pageStub()
	report, err := Run(context.Background(), source, Options{}, s.exec)
	if err != nil || !report.OK {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	want := [][]string{
		{"fill", "#n", "row 0/1"}, {"fill", "#n", "row 1/2"},
		{"eval", "messages()"},
		{"fill", ".chat-input", "hi"}, {"fill", ".chat-input", "bye"},
		{"type", "one"}, {"type", "two"},
		{"type", "bye 2"},
	}
	if !reflect.DeepEqual(actions(s), want) {
		t.Fatalf("actions=%q\nwant %q", actions(s), want)
	}
	if report.Steps[2].Path != "1[2].1" || string(report.Steps[0].Result) != `{"iterations":2}` {
		t.Fatalf("steps=%+v", report.Steps)
	}
}

func TestRepeatWhileUntilAndMax(t *testing.T) {
	s := pageStub(true, true, false, false, true)
	report, err := Run(context.Background(), []byte(`steps:
  - repeat:
      while: {selector: '.pending'}
      steps: [{click: '#next'}]
  - repeat:
      until: {selector: '.done'}
      steps: [{click: '#more'}]
`), Options{}, s.exec)
	if err != nil || !report.OK {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	want := [][]string{{"click", "#next"}, {"click", "#next"}, {"click", "#more"}, {"click", "#more"}}
	if !reflect.DeepEqual(actions(s), want) {
		t.Fatalf("actions=%q want %q", actions(s), want)
	}

	s = pageStub()
	report, err = Run(context.Background(), []byte(`steps:
  - repeat:
      until: {selector: '.never'}
      max: 3
      steps: [{click: '#more'}]
`), Options{}, s.exec)
	if err == nil || !strings.Contains(err.Error(), "until condition still false after max 3") || len(actions(s)) != 4 || actions(s)[3][0] != "screenshot" {
		t.Fatalf("err=%v actions=%q", err, actions(s))
	}
	if report.OK || report.Steps[0].OK {
		t.Fatalf("report=%+v", report)
	}
}

func TestNestedFailureReportsPath(t *testing.T) {
	s := &stub{run: func(_ context.Context, args []string) (json.RawMessage, error) {
		if args[0] == "click" && args[1] == "#bad" {
			return nil, context.DeadlineExceeded
		}
		return json.RawMessage(`true`), nil
	}}
	report, err := Run(context.Background(), []byte(`steps:
  - open: https://example.com
  - repeat:
      times: 2
      steps:
        - if: {selector: x}
          then: [{click: '#bad'}]
`), Options{}, s.exec)
	if err == nil || !strings.Contains(err.Error(), "step 2[1].1.then.1 (click)") {
		t.Fatalf("err=%v", err)
	}
	last := report.Steps[len(report.Steps)-1]
	if last.Index != 2 || last.Path != "2[1].1.then.1" || last.OK {
		t.Fatalf("last=%+v", last)
	}
	if !strings.HasSuffix(report.Screenshot, "failed-step-2.png") {
		t.Fatalf("screenshot=%q", report.Screenshot)
	}
}

func TestRouteUnrouteRequestSteps(t *testing.T) {
	s := pageStub()
	report, err := Run(context.Background(), []byte(`steps:
  - route:
      pattern: '**/api/**'
      status: 200
      body: '{}'
  - route:
      pattern: '**/api/chat/stream'
      status: 500
      times: 1
      contentType: text/event-stream
      headers: {X-B: two, X-A: one}
  - route: {pattern: '**/*.png', abort: true}
  - request: 3
  - unroute: '**/*.png'
  - unroute: true
`), Options{}, s.exec)
	if err != nil || !report.OK {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	want := [][]string{
		{"route", "**/api/**", "--status", "200", "--body", "{}"},
		{"route", "**/api/chat/stream", "--status", "500", "--content-type", "text/event-stream", "--header", "X-A:one", "--header", "X-B:two", "--times", "1"},
		{"route", "**/*.png", "--abort"},
		{"request", "3"},
		{"unroute", "**/*.png"},
		{"unroute"},
	}
	if !reflect.DeepEqual(actions(s), want) {
		t.Fatalf("actions=%q\nwant %q", actions(s), want)
	}
}

func TestControlValidation(t *testing.T) {
	for source, want := range map[string]string{
		`steps: [{route: {pattern: '*/api/*'}}]`:                                   "cannot match http(s) URLs",
		`steps: [{if: {selector: x}}]`:                                             "then must be a nonempty list",
		`steps: [{if: {}, then: [{click: a}]}]`:                                    "expect requires a condition",
		`steps: [{click: a, then: [{click: a}]}]`:                                  "then is only valid with if",
		`steps: [{repeat: {times: 2, until: {selector: x}, steps: [{click: a}]}}]`: "exactly one of times, while, until",
		`steps: [{repeat: {times: 2, max: 3, steps: [{click: a}]}}]`:               "max is only valid",
		`steps: [{repeat: {times: 2, steps: [{click: a}]}, save: x}]`:              "save is not supported on blocks",
		`steps: [{each: {items: [], steps: [{click: a}]}}]`:                        "items must contain",
		`steps: [{each: {items: [a], as: 'bad name', steps: [{click: a}]}}]`:       "as must be a variable name",
		`steps: [{each: {items: [a], steps: [{click: '${nope.item}'}]}}]`:          "missing variable nope.item",
		`steps: [{request: 0}]`:                                                    "positive request index",
		`steps: [{route: {pattern: '**/x', body: a, bodyFile: b}}]`:                "body or bodyFile",
	} {
		_, err := Run(context.Background(), []byte(source), Options{}, (&stub{}).exec)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err=%v want %q", source, err, want)
		}
	}
	_, err := Run(context.Background(), []byte(`steps: [{route: {pattern: '**/x'}}]`), Options{Endpoint: "http://localhost:9222"}, (&stub{}).exec)
	if err == nil || !strings.Contains(err.Error(), "requires a managed profile") {
		t.Fatalf("external route err=%v", err)
	}
}

func TestLoopScopingRedactionAndPrecision(t *testing.T) {
	s := &stub{run: func(_ context.Context, args []string) (json.RawMessage, error) {
		switch {
		case args[0] == "eval" && args[1] == "ids()":
			return json.RawMessage(`[1, 9007199254740993]`), nil
		case args[0] == "eval" && args[1] == "user()":
			return json.RawMessage(`{"name":"ana"}`), nil
		case args[0] == "click" && args[1] == "#boom":
			return nil, errors.New("boom 1")
		}
		return json.RawMessage(`{}`), nil
	}}
	report, err := Run(context.Background(), []byte(`steps:
  - eval: 'user()'
    save: user
  - eval: 'ids()'
    save: ids
  - repeat:
      times: 2
      steps:
        - repeat:
            times: 3
            steps: [{type: 'inner ${loop.index}'}]
        - type: 'outer ${loop.index}'
  - each:
      items: '${ids}'
      as: user
      steps: [{type: 'id ${user.item}'}]
  - type: 'name ${user.name}'
  - each:
      items: '${ids}'
      steps: [{click: '#boom'}]
`), Options{}, s.exec)
	got := []string{}
	for _, c := range actions(s) {
		if c[0] == "type" {
			got = append(got, c[1])
		}
	}
	want := []string{"inner 0", "inner 1", "inner 2", "outer 0", "inner 0", "inner 1", "inner 2", "outer 1", "id 1", "id 9007199254740993", "name ana"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("typed=%q\nwant %q", got, want)
	}
	if err == nil || !strings.HasPrefix(err.Error(), "step 6[1].1 (click): boom 1") || report.Error != err.Error() {
		t.Fatalf("err=%v report.Error=%q", err, report.Error)
	}
	_, err = Run(context.Background(), []byte(`steps:
  - repeat: {times: 1, steps: [{click: a}]}
  - type: '${loop.index}'
`), Options{}, (&stub{}).exec)
	if err == nil || !strings.Contains(err.Error(), "missing variable loop.index") {
		t.Fatalf("loop variable leaked past its loop: %v", err)
	}
}

func TestConditionsInFrames(t *testing.T) {
	s := &stub{run: func(_ context.Context, args []string) (json.RawMessage, error) {
		return json.RawMessage(`true`), nil
	}}
	report, err := Run(context.Background(), []byte(`steps:
  - expect: {selector: '#pay >> #inner >> button', text: Pay}
  - if: {frame: '#pay', js: 'window.ready'}
    then: [{click: '#pay >> button'}]
  - expect: {frame: '#pay', selector: '#inner >> #ok'}
`), Options{}, s.exec)
	if err != nil || !report.OK {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	frames := []string{}
	for _, c := range s.calls {
		head, rest, _ := strings.Cut(strings.Join(c.args, " "), " -- ")
		if !strings.HasPrefix(rest, "eval ") {
			continue
		}
		if i := strings.Index(head, "--frame "); i >= 0 {
			frames = append(frames, strings.SplitN(head[i+8:], " --", 2)[0])
		} else {
			frames = append(frames, "")
		}
		if strings.Contains(rest, ">>") {
			t.Fatalf("chain leaked into eval: %s", rest)
		}
	}
	want := []string{"#pay >> #inner", "#pay", "#pay >> #inner"}
	if !reflect.DeepEqual(frames, want) {
		t.Fatalf("frames=%q want %q", frames, want)
	}
	_, err = Run(context.Background(), []byte(`steps: [{expect: {frame: '#pay'}}]`), Options{}, (&stub{}).exec)
	if err == nil || !strings.Contains(err.Error(), "expect requires a condition") {
		t.Fatalf("frame-only err=%v", err)
	}
}

func TestLiteralTemplateEscape(t *testing.T) {
	s := &stub{run: func(context.Context, []string) (json.RawMessage, error) { return json.RawMessage(`true`), nil }}
	report, err := Run(context.Background(), []byte(`steps:
  - eval: 'const n = 2; throwIf(`+"`"+`got $${n}, want ${WANT}`+"`"+`)'
  - expect: {js: '`+"`"+`$${location.hash}`+"`"+` === ""'}
`), Options{Vars: map[string]string{"WANT": "3"}}, s.exec)
	if err != nil || !report.OK {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if got := s.commands()[1]; got[1] != "const n = 2; throwIf(`got ${n}, want 3`)" {
		t.Fatalf("eval=%q", got[1])
	}
	if !strings.Contains(s.commands()[2][1], "`${location.hash}` === \"\"") {
		t.Fatalf("expect=%q", s.commands()[2][1])
	}
	_, err = Run(context.Background(), []byte("steps: [{eval: '`${n}`'}]"), Options{}, (&stub{}).exec)
	if err == nil || !strings.Contains(err.Error(), "write $${n} for a literal") {
		t.Fatalf("err=%v", err)
	}
}

func TestExpectTimeoutShowsObservedValues(t *testing.T) {
	for _, tc := range []struct{ expect, observed, want string }{
		{`{selector: '#total', text: '3 items'}`, `{"count":1,"text":"2 items","visible":true}`, `text: want "3 items", got "2 items"`},
		{`{selector: '.row', count: 3}`, `{"count":1,"text":"a"}`, `count: want 3, got 1`},
		{`{selector: '#missing'}`, `{"count":0}`, `selector "#missing" matched no elements`},
		{`{selector: '#m', contains: 'Saved', visible: true}`, `{"count":1,"text":"Error","visible":false}`, `contains: "Saved" not in "Error"; visible: want true, got false`},
		{`{url: 'https://x/done'}`, `{"url":"https://x/start"}`, `url: want "https://x/done", got "https://x/start"`},
		{`{js: 'window.n === 2'}`, `{"js":false}`, `js returned false`},
		{`{frame: '#f', js: 'a.b'}`, `{"jsError":"TypeError: a is undefined"}`, `js threw TypeError: a is undefined`},
	} {
		var observeArgs []string
		s := &stub{run: func(_ context.Context, args []string) (json.RawMessage, error) {
			if strings.HasPrefix(args[len(args)-1], "(()=>{const r={};") {
				observeArgs = args
				return json.RawMessage(tc.observed), nil
			}
			return json.RawMessage(`false`), nil
		}}
		_, err := Run(context.Background(), []byte("steps: [{expect: "+tc.expect+", timeout: 50ms}]"), Options{}, s.exec)
		if err == nil || !strings.Contains(err.Error(), "expect timed out: "+tc.want+" (context deadline exceeded)") {
			t.Errorf("%s: err=%v", tc.expect, err)
		}
		if observeArgs == nil {
			t.Errorf("%s: page state was not observed", tc.expect)
		}
	}
}

func TestNetworkFiltersConditionsAndJSON(t *testing.T) {
	var network [][]string
	posts := 0
	s := &stub{run: func(_ context.Context, args []string) (json.RawMessage, error) {
		switch args[0] {
		case "network":
			network = append(network, args)
			if len(args) > 1 && args[1] == "--method=POST" {
				posts++
				if posts < 3 {
					return json.RawMessage(`[{"index":4}]`), nil
				}
				return json.RawMessage(`[{"index":4,"requestBody":"{\"message\":\"say ` + "`" + `hi` + "`" + `\"}","requestJson":{"message":"say ` + "`" + `hi` + "`" + `"}},{"index":7,"requestJson":{"message":"two"}}]`), nil
			}
			return json.RawMessage(`[]`), nil
		}
		return json.RawMessage(`true`), nil
	}}
	report, err := Run(context.Background(), []byte(`steps:
  - expect: {network: {method: POST, url: '**/chat/stream', count: 2}}
  - network: {method: POST, url: '**/chat/stream', body: true}
    save: sent
  - eval: 'check(${sent.0.requestBody|json}, ${sent.0.requestJson|json}, ${sent|json}.length, ${MODE|json}, ${sent.length|json})'
  - each:
      items: '${sent}'
      steps: [{eval: 'seen(${loop.item.requestJson.message|json}, ${loop.index|json})'}]
  - if: {network: {url: '**/missing'}}
    then: [{click: x}]
`), Options{Vars: map[string]string{"MODE": `a"b`}}, s.exec)
	if err != nil || !report.OK {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if want := []string{"network", "--method=POST", "--url=**/chat/stream", "--body"}; !reflect.DeepEqual(network[len(network)-2], want) {
		t.Fatalf("network args=%q", network)
	}
	if last := network[len(network)-1]; !reflect.DeepEqual(last, []string{"network", "--url=**/missing"}) {
		t.Fatalf("condition args=%q", last)
	}
	evals := []string{}
	for _, c := range actions(s) {
		if c[0] == "eval" {
			evals = append(evals, c[1])
		}
	}
	want := []string{
		"check(\"{\\\"message\\\":\\\"say `hi`\\\"}\", {\"message\":\"say `hi`\"}, " + `[{"index":4,"requestBody":"{\"message\":\"say ` + "`hi`" + `\"}","requestJson":{"message":"say ` + "`hi`" + `"}},{"index":7,"requestJson":{"message":"two"}}].length, "a\"b", 2)`,
		"seen(\"say `hi`\", 0)",
		`seen("two", 1)`,
	}
	if !reflect.DeepEqual(evals, want) {
		t.Fatalf("evals=\n%s\nwant\n%s", strings.Join(evals, "\n"), strings.Join(want, "\n"))
	}

	s = &stub{run: func(_ context.Context, args []string) (json.RawMessage, error) {
		return json.RawMessage(`[{"index":1}]`), nil
	}}
	_, err = Run(context.Background(), []byte(`steps: [{expect: {network: {method: POST, count: 2}}, timeout: 50ms}]`), Options{}, s.exec)
	if err == nil || !strings.Contains(err.Error(), "network: want 2 matching requests, got 1") {
		t.Fatalf("err=%v", err)
	}
	for source, want := range map[string]string{
		`steps: [{expect: {network: {url: '*/api/*'}}}]`:      "cannot match http(s) URLs",
		`steps: [{expect: {network: {url: x}, selector: a}}]`: "cannot be combined",
		`steps: [{network: {url: '**/x', status: 2}}]`:        "status",
		`steps: [{eval: '${MISSING|json}'}]`:                  "missing variable MISSING",
	} {
		_, err := Run(context.Background(), []byte(source), Options{}, (&stub{}).exec)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err=%v want %q", source, err, want)
		}
	}
	_, err = Run(context.Background(), []byte(`steps: [{expect: {network: {url: '**/x'}}}]`), Options{Endpoint: "http://localhost:9222"}, (&stub{}).exec)
	if err == nil || !strings.Contains(err.Error(), "requires a managed profile") {
		t.Fatalf("external network condition err=%v", err)
	}
}

func TestRedactionOptOut(t *testing.T) {
	failing := func(context.Context, []string) (json.RawMessage, error) {
		return nil, errors.New("got secret-token")
	}
	flow := []byte("steps: [{eval: '${TOKEN}'}]")
	vars := map[string]string{"TOKEN": "secret-token"}
	_, err := Run(context.Background(), flow, Options{Vars: vars}, (&stub{run: failing}).exec)
	if err == nil || strings.Contains(err.Error(), "secret-token") || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("default err=%v", err)
	}
	_, err = Run(context.Background(), flow, Options{Vars: vars, NoRedact: true}, (&stub{run: failing}).exec)
	if err == nil || !strings.Contains(err.Error(), "got secret-token") {
		t.Fatalf("--no-redact err=%v", err)
	}
	_, err = Run(context.Background(), append([]byte("redact: false\n"), flow...), Options{Vars: vars}, (&stub{run: failing}).exec)
	if err == nil || !strings.Contains(err.Error(), "got secret-token") {
		t.Fatalf("redact: false err=%v", err)
	}
	if _, err = Run(context.Background(), []byte("redact: nope\nsteps: [{eval: x}]"), Options{}, (&stub{}).exec); err == nil || !strings.Contains(err.Error(), "redact") {
		t.Fatalf("invalid redact err=%v", err)
	}
}
