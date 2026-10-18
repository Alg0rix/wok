// Package flow runs declarative browser actions and optional assertions through
// an in-process Wok executor. It never launches a shell or another CLI process.
package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type Executor func(context.Context, []string) (json.RawMessage, error)
type Options struct {
	Profile     string
	Endpoint    string
	TargetID    string
	Vars        map[string]string
	ArtifactDir string
}
type StepReport struct {
	Index      int             `json:"index"`
	Name       string          `json:"name,omitempty"`
	Action     string          `json:"action"`
	OK         bool            `json:"ok"`
	DurationMS int64           `json:"durationMs"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
}
type Report struct {
	OK              bool         `json:"ok"`
	Profile         string       `json:"profile"`
	Steps           []StepReport `json:"steps"`
	Error           string       `json:"error,omitempty"`
	Screenshot      string       `json:"screenshot,omitempty"`
	ScreenshotError string       `json:"screenshotError,omitempty"`
}

func Run(ctx context.Context, source []byte, opts Options, exec Executor) (report Report, runErr error) {
	report.Steps = []StepReport{}
	report.Profile = opts.Profile
	if report.Profile == "" {
		report.Profile = "default"
	}
	vars := newVariables(opts.Vars)
	failValidation := func(err error) (Report, error) {
		err = vars.redact(err)
		report.Error = err.Error()
		return report, err
	}
	doc, err := parse(source)
	if err != nil {
		return failValidation(err)
	}
	profile := opts.Profile
	if profile == "" {
		profile = doc.profile
	}
	if profile == "" {
		profile = "default"
	}
	compiled, err := vars.compile(profile, map[string]bool{})
	if err != nil {
		return failValidation(err)
	}
	resolved, _ := vars.resolve(compiled)
	profile = resolved.(string)
	report.Profile = profile
	if !profileName.MatchString(profile) {
		return failValidation(errors.New("invalid profile name"))
	}
	if opts.Endpoint != "" && (doc.close || doc.viewport != nil) {
		return failValidation(errors.New("external CDP flows cannot set close or viewport"))
	}
	if exec == nil {
		return failValidation(errors.New("flow executor is required"))
	}
	saved := map[string]bool{}
	for i := range doc.steps {
		s := &doc.steps[i]
		if opts.Endpoint != "" && nativeManaged(s.action, s.value) {
			return failValidation(fmt.Errorf("step %d: %s requires a managed profile", i+1, s.action))
		}
		s.value, err = vars.compileValue(s.value, saved)
		if err != nil {
			return failValidation(fmt.Errorf("step %d: %w", i+1, err))
		}
		s.timeout, err = vars.compileValue(s.timeout, saved)
		if err != nil {
			return failValidation(fmt.Errorf("step %d: %w", i+1, err))
		}
		if !hasDynamic(s.timeout) {
			value, _ := vars.resolve(s.timeout)
			if _, err = duration(value); err != nil {
				return failValidation(fmt.Errorf("step %d: %w", i+1, err))
			}
		}
		if err = validateKnownNative(s.action, s.value, vars); err != nil {
			return failValidation(fmt.Errorf("step %d: %w", i+1, err))
		}
		// Check statically available action semantics before starting a browser.
		if !hasDynamic(s.value) {
			value, _ := vars.resolve(s.value)
			if err = validateAction(s.action, value); err != nil {
				return failValidation(fmt.Errorf("step %d: %w", i+1, err))
			}
		}
		if s.save != "" {
			saved[s.save] = true
		}
	}
	route := func(args []string, timeout time.Duration) []string {
		prefix := []string{"--profile", profile}
		if opts.Endpoint != "" {
			prefix = []string{"--cdp", opts.Endpoint}
		}
		if opts.TargetID != "" {
			prefix = append(prefix, "--tab", opts.TargetID)
		}
		prefix = append(prefix, "--timeout", timeout.String(), "--json", "--")
		return append(prefix, args...)
	}
	failedIndex := 0
	defer func() {
		if runErr != nil {
			screenshotCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			path, err := failurePath(opts.ArtifactDir, failedIndex)
			if err == nil {
				_, err = exec(screenshotCtx, route([]string{"screenshot", path}, 5*time.Second))
				if err == nil {
					report.Screenshot = path
				}
			}
			cancel()
			if err != nil {
				report.ScreenshotError = vars.redact(err).Error()
			}
		}
		if doc.close && opts.Endpoint == "" {
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := exec(closeCtx, route([]string{"stop"}, 5*time.Second))
			cancel()
			if err != nil && runErr == nil {
				runErr = vars.redact(fmt.Errorf("close profile: %w", err))
				report.Error = runErr.Error()
			}
		}
		report.OK = runErr == nil
	}()
	fail := func(err error) (Report, error) {
		err = vars.redact(err)
		report.Error = err.Error()
		return report, err
	}
	if err = ctx.Err(); err != nil {
		return fail(err)
	}
	if opts.Endpoint == "" {
		startCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		_, err = exec(startCtx, route([]string{"start"}, 5*time.Minute))
		cancel()
		if err != nil {
			return fail(fmt.Errorf("start browser: %w", err))
		}
	}
	if doc.viewport != nil {
		args := []string{"viewport", strconv.Itoa(doc.viewport["width"].(int)), strconv.Itoa(doc.viewport["height"].(int))}
		if doc.viewport["mobile"].(bool) {
			args = append(args, "--mobile")
		}
		args = append(args, "--dpr", strconv.FormatFloat(doc.viewport["dpr"].(float64), 'g', -1, 64))
		callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		_, err = exec(callCtx, route(args, 15*time.Second))
		cancel()
		if err != nil {
			return fail(fmt.Errorf("set viewport: %w", err))
		}
	}
	for i, s := range doc.steps {
		failedIndex = i + 1
		began := time.Now()
		sr := StepReport{Index: i + 1, Name: s.name, Action: s.action}
		value, e := vars.resolve(s.value)
		timeoutValue, timeoutErr := vars.resolve(s.timeout)
		if e == nil {
			e = timeoutErr
		}
		var timeout time.Duration
		if e == nil {
			timeout, e = duration(timeoutValue)
		}
		if e == nil {
			e = validateAction(s.action, value)
		}
		var result json.RawMessage
		if e == nil {
			stepCtx, cancel := context.WithTimeout(ctx, timeout)
			if e = stepCtx.Err(); e == nil {
				if s.action == "expect" {
					result, e = assert(stepCtx, value.(map[string]any), func(c context.Context, args []string) (json.RawMessage, error) { return exec(c, route(args, timeout)) })
				} else {
					var args []string
					args, e = actionArgs(s.action, value)
					if e == nil {
						result, e = exec(stepCtx, route(args, timeout))
					}
				}
			}
			cancel()
		}
		if e == nil && len(result) > 0 && !json.Valid(result) {
			e = errors.New("executor returned invalid JSON")
		}
		if e == nil && s.save != "" {
			e = vars.capture(s.save, result)
		}
		sr.DurationMS = time.Since(began).Milliseconds()
		sr.OK = e == nil
		if e != nil {
			e = vars.redact(e)
			sr.Error = e.Error()
		} else {
			sr.Result = result
		}
		report.Steps = append(report.Steps, sr)
		if e != nil {
			return fail(fmt.Errorf("step %d (%s): %w", i+1, s.action, e))
		}
	}
	failedIndex = 0
	return report, nil
}
func failurePath(dir string, index int) (string, error) {
	if dir == "" {
		base := filepath.Join(".wok", "artifacts")
		if err := os.MkdirAll(base, 0700); err != nil {
			return "", err
		}
		var err error
		dir, err = os.MkdirTemp(base, "run-")
		if err != nil {
			return "", err
		}
	} else if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("failed-step-%d.png", index)), nil
}
func validateAction(action string, value any) error {
	require := func(v any, label string) error {
		s, ok := v.(string)
		if !ok || s == "" {
			return fmt.Errorf("%s must be a nonempty string", label)
		}
		return nil
	}
	if handled, err := validateNative(action, value); handled {
		return err
	}
	switch action {
	case "open", "click", "press", "eval", "screenshot":
		return require(value, action)
	case "fill":
		return require(value.(map[string]any)["selector"], "selector")
	case "tab":
		for k, v := range value.(map[string]any) {
			if k != "new" {
				return require(v, "tab target")
			}
		}
	case "get":
		m := value.(map[string]any)
		kind := m["kind"]
		if _, ok := m["attr"]; ok && kind != "attr" && kind != "attribute" {
			return errors.New("attr is only valid for get attr")
		}
		switch kind {
		case "title", "url":
			if len(m) != 1 {
				return errors.New("get title/url does not accept selector or attr")
			}
		case "text":
			if _, ok := m["selector"]; ok {
				return require(m["selector"], "selector")
			}
		case "html", "value":
			return require(m["selector"], "selector")
		case "attr", "attribute":
			if err := require(m["selector"], "selector"); err != nil {
				return err
			}
			return require(m["attr"], "attr")
		default:
			return errors.New("invalid get kind")
		}
	case "expect":
		m := value.(map[string]any)
		for _, k := range []string{"selector", "js"} {
			if v, ok := m[k]; ok {
				if err := require(v, k); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func actionArgs(action string, value any) ([]string, error) {
	if args, ok := nativeArgs(action, value); ok {
		return args, nil
	}
	switch action {
	case "open", "click", "type", "press", "eval", "screenshot":
		return []string{action, value.(string)}, nil
	case "fill":
		m := value.(map[string]any)
		return []string{"fill", m["selector"].(string), m["value"].(string)}, nil
	case "get":
		m := value.(map[string]any)
		kind := m["kind"].(string)
		if kind == "attribute" {
			kind = "attr"
		}
		args := []string{"get", kind}
		if selector, ok := m["selector"]; ok {
			args = append(args, selector.(string))
		}
		if attr, ok := m["attr"]; ok {
			args = append(args, attr.(string))
		}
		return args, nil
	case "snapshot":
		args := []string{"snapshot"}
		if value.(map[string]any)["interactive"].(bool) {
			args = append(args, "-i")
		}
		return args, nil
	case "tab":
		for k, v := range value.(map[string]any) {
			args := []string{"tab", k}
			if v.(string) != "" {
				args = append(args, v.(string))
			}
			return args, nil
		}
	}
	return nil, fmt.Errorf("unsupported action %s", action)
}
