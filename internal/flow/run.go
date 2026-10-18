package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/alg0rix/wok/internal/observe"
)

// prepareSteps compiles variables and validates static values before a
// browser starts. saved holds names captured by earlier steps; loop bodies
// also see their loop variable as a runtime value.
func prepareSteps(steps []step, vars *variables, external bool, saved map[string]bool, prefix string) error {
	var err error
	for i := range steps {
		s := &steps[i]
		label := prefix + strconv.Itoa(i+1)
		fail := func(err error) error { return fmt.Errorf("step %s: %w", label, err) }
		if external && (nativeManaged(s.action, s.value) || s.action == "route" || s.action == "unroute" || s.action == "request" || networkCondition(s.value) != nil) {
			return fail(fmt.Errorf("%s requires a managed profile", s.action))
		}
		if s.value, err = vars.compileValue(s.value, saved); err != nil {
			return fail(err)
		}
		if s.timeout, err = vars.compileValue(s.timeout, saved); err != nil {
			return fail(err)
		}
		if !hasDynamic(s.timeout) {
			value, _ := vars.resolve(s.timeout)
			if _, err = duration(value); err != nil {
				return fail(err)
			}
		}
		if err = validateKnownNative(s.action, s.value, vars); err != nil {
			return fail(err)
		}
		// Check statically available action semantics before starting a browser.
		if !hasDynamic(s.value) {
			value, _ := vars.resolve(s.value)
			if err = validateAction(s.action, value); err != nil {
				return fail(err)
			}
		}
		if b := s.block; b != nil {
			if s.action == "if" {
				if err = prepareSteps(b.then, vars, external, saved, label+".then."); err != nil {
					return err
				}
				if err = prepareSteps(b.els, vars, external, saved, label+".else."); err != nil {
					return err
				}
			} else {
				// The loop variable exists only inside the body.
				shadowed := saved[b.as]
				saved[b.as] = true
				if err = prepareSteps(b.body, vars, external, saved, label+"."); err != nil {
					return err
				}
				saved[b.as] = shadowed
			}
		}
		if s.save != "" {
			saved[s.save] = true
		}
	}
	return nil
}

// validateFlowAction checks actions added for flow control and networking.
func validateFlowAction(action string, value any) (bool, error) {
	switch action {
	case "if", "repeat":
		if value == nil {
			return true, nil
		}
		return true, validateAction("expect", value)
	case "each":
		if list, ok := value.([]any); ok && len(list) == 0 {
			return true, errors.New("items must not be empty")
		}
		return true, nil
	case "route":
		pattern, _ := value.(map[string]any)["pattern"].(string)
		if pattern == "" {
			return true, errors.New("route pattern must be a nonempty string")
		}
		if hint := observe.RouteHint(pattern); hint != "" {
			return true, errors.New(hint)
		}
		return true, nil
	case "request":
		if n, err := strconv.Atoi(value.(string)); err != nil || n < 1 {
			return true, errors.New("request must be a positive request index")
		}
		return true, nil
	case "unroute":
		return true, nil
	}
	return false, nil
}

type runner struct {
	vars        *variables
	report      *Report
	failedIndex *int
	exec        func(context.Context, []string, time.Duration) (json.RawMessage, error)
}

// nestedError marks an error already reported by a nested step.
type nestedError struct{ error }

func (e nestedError) Unwrap() error { return e.error }

// run executes steps in order. top is the enclosing top-level step index, or
// 0 for the top level; prefix labels nested steps in reports and errors.
func (r *runner) run(ctx context.Context, steps []step, top int, prefix string) error {
	for i := range steps {
		s := &steps[i]
		index := top
		if prefix == "" {
			index = i + 1
			*r.failedIndex = index
		}
		label := prefix + strconv.Itoa(i+1)
		began := time.Now()
		pos := len(r.report.Steps)
		sr := StepReport{Index: index, Name: s.name, Action: s.action}
		if prefix != "" {
			sr.Path = label
		}
		r.report.Steps = append(r.report.Steps, sr)
		result, e := r.step(ctx, s, index, label)
		report := &r.report.Steps[pos]
		report.DurationMS = time.Since(began).Milliseconds()
		var nested nestedError
		switch {
		case errors.As(e, &nested):
			report.Error = "nested step failed"
			return e
		case e != nil:
			e = r.vars.redact(e)
			report.Error = e.Error()
			return nestedError{fmt.Errorf("step %s (%s): %w", label, s.action, e)}
		}
		report.OK = true
		report.Result = result
	}
	return nil
}

func (r *runner) step(ctx context.Context, s *step, index int, label string) (json.RawMessage, error) {
	value, e := r.vars.resolve(s.value)
	timeoutValue, timeoutErr := r.vars.resolve(s.timeout)
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
	if e != nil {
		return nil, e
	}
	if s.block != nil {
		return r.block(ctx, s, value, timeout, index, label)
	}
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if e = stepCtx.Err(); e != nil {
		return nil, e
	}
	var result json.RawMessage
	if s.action == "expect" {
		result, e = assert(stepCtx, value.(map[string]any), func(c context.Context, args []string) (json.RawMessage, error) { return r.exec(c, args, timeout) })
	} else {
		var args []string
		if args, e = actionArgs(s.action, value); e == nil {
			result, e = r.exec(stepCtx, args, timeout)
		}
	}
	if e == nil && len(result) > 0 && !json.Valid(result) {
		e = errors.New("executor returned invalid JSON")
	}
	if e == nil && s.save != "" {
		e = r.vars.capture(s.save, result)
	}
	return result, e
}

// check evaluates a condition once, without polling.
func (r *runner) check(ctx context.Context, condition any, timeout time.Duration) (bool, error) {
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ok, _, err := evaluate(checkCtx, condition.(map[string]any), func(c context.Context, args []string) (json.RawMessage, error) { return r.exec(c, args, timeout) })
	return ok, err
}

func (r *runner) block(ctx context.Context, s *step, value any, timeout time.Duration, index int, label string) (json.RawMessage, error) {
	b := s.block
	if s.action == "if" {
		matched, err := r.check(ctx, value, timeout)
		if err != nil {
			return nil, err
		}
		branch, name := b.then, "then"
		if !matched {
			branch, name = b.els, "else"
		}
		result, _ := json.Marshal(map[string]any{"matched": matched})
		return result, r.run(ctx, branch, index, label+"."+name+".")
	}
	var items []any
	if s.action == "each" {
		switch x := value.(type) {
		case []any:
			items = x
		case string:
			dec := json.NewDecoder(strings.NewReader(x))
			dec.UseNumber()
			if err := dec.Decode(&items); err != nil || items == nil {
				return nil, errors.New("items must resolve to a JSON array")
			}
		}
		if len(items) > maxRepeat {
			return nil, fmt.Errorf("items may contain at most %d values", maxRepeat)
		}
	}
	// Restore an outer loop's or saved variable with the same name on exit.
	outer := r.vars.snapshot(b.as)
	defer r.vars.restore(b.as, outer)
	n := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if s.action == "each" && n >= len(items) || b.times > 0 && n >= b.times {
			break
		}
		if b.cond == "while" {
			ok, err := r.check(ctx, value, timeout)
			if err != nil {
				return nil, err
			}
			if !ok {
				break
			}
			if n >= b.max {
				return nil, fmt.Errorf("while condition still true after max %d iterations", b.max)
			}
		}
		loop := map[string]any{"index": n, "number": n + 1}
		if s.action == "each" {
			loop["item"] = items[n]
		}
		r.vars.setLocal(b.as, loop)
		if err := r.run(ctx, b.body, index, fmt.Sprintf("%s[%d].", label, n+1)); err != nil {
			return nil, err
		}
		n++
		if b.cond == "until" {
			ok, err := r.check(ctx, value, timeout)
			if err != nil {
				return nil, err
			}
			if ok {
				break
			}
			if n >= b.max {
				return nil, fmt.Errorf("until condition still false after max %d iterations", b.max)
			}
		}
	}
	result, _ := json.Marshal(map[string]any{"iterations": n})
	return result, nil
}
