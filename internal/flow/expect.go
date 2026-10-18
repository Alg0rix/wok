package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func jsQuote(v any) string { b, _ := json.Marshal(v); return string(b) }
func expectExpression(m map[string]any) string {
	var b strings.Builder
	b.WriteString("(()=>{")
	conditions := []string{}
	if selector, ok := m["selector"]; ok {
		b.WriteString("const nodes=document.querySelectorAll(" + jsQuote(selector) + ");const el=nodes[0];")
		_, hasCount := m["count"]
		needsElement := !hasCount
		for _, key := range []string{"text", "contains", "value", "visible"} {
			if _, ok := m[key]; ok {
				needsElement = true
			}
		}
		if needsElement {
			conditions = append(conditions, "nodes.length>0")
		}
		if count, ok := m["count"]; ok {
			conditions = append(conditions, "nodes.length==="+jsQuote(count))
		}
		if text, ok := m["text"]; ok {
			conditions = append(conditions, "(el.innerText??el.textContent??'')==="+jsQuote(text))
		}
		if text, ok := m["contains"]; ok {
			conditions = append(conditions, "(el.innerText??el.textContent??'').includes("+jsQuote(text)+")")
		}
		if value, ok := m["value"]; ok {
			conditions = append(conditions, "el.value==="+jsQuote(value))
		}
		if visible, ok := m["visible"]; ok {
			b.WriteString("const isVisible=el?(()=>{const s=getComputedStyle(el),r=el.getBoundingClientRect();return r.width>0&&r.height>0&&s.visibility!=='hidden'&&s.visibility!=='collapse'&&s.display!=='none'})():false;")
			conditions = append(conditions, "isVisible==="+jsQuote(visible))
		}
	}
	if url, ok := m["url"]; ok {
		conditions = append(conditions, "location.href==="+jsQuote(url))
	}
	if js, ok := m["js"]; ok {
		b.WriteString("const predicate=(()=>{return (" + js.(string) + ")})();if(typeof predicate!=='boolean')throw new Error('expect js must return a boolean');")
		conditions = append(conditions, "predicate")
	}
	b.WriteString("return " + strings.Join(conditions, "&&") + ";})()")
	return b.String()
}

// frameSeparator matches the CLI's iframe selector chain separator.
const frameSeparator = " >> "

// conditionArgs builds the eval command for a condition. A frame field, or
// the iframe part of a "frame >> selector" chain, scopes the eval to that
// iframe.
func conditionArgs(m map[string]any) []string {
	return scopedEval(m, expectExpression)
}

func scopedEval(m map[string]any, build func(map[string]any) string) []string {
	frame, _ := m["frame"].(string)
	if selector, ok := m["selector"].(string); ok {
		if i := strings.LastIndex(selector, frameSeparator); i >= 0 {
			outer := strings.TrimSpace(selector[:i])
			if frame != "" {
				outer = frame + frameSeparator + outer
			}
			m = copyWith(m, "selector", strings.TrimSpace(selector[i+len(frameSeparator):]))
			frame = outer
		}
	}
	args := []string{"eval", build(m)}
	if frame != "" {
		args = append([]string{"--frame", frame}, args...)
	}
	return args
}

func copyWith(m map[string]any, key string, value any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	out[key] = value
	return out
}

// observeExpression reports what the page shows for each condition field.
func observeExpression(m map[string]any) string {
	var b strings.Builder
	b.WriteString("(()=>{const r={};")
	if selector, ok := m["selector"]; ok {
		b.WriteString("const nodes=document.querySelectorAll(" + jsQuote(selector) + ");const el=nodes[0];r.count=nodes.length;")
		b.WriteString("if(el){r.text=el.innerText??el.textContent??'';r.value=el.value;const s=getComputedStyle(el),q=el.getBoundingClientRect();r.visible=q.width>0&&q.height>0&&s.visibility!=='hidden'&&s.visibility!=='collapse'&&s.display!=='none'}")
	}
	if _, ok := m["url"]; ok {
		b.WriteString("r.url=location.href;")
	}
	if js, ok := m["js"]; ok {
		b.WriteString("try{r.js=(()=>{return (" + js.(string) + ")})()}catch(e){r.jsError=String(e)}")
	}
	b.WriteString("return r})()")
	return b.String()
}

// mismatch describes the conditions that the observed page state fails.
func mismatch(m map[string]any, raw json.RawMessage) string {
	var got map[string]any
	if json.Unmarshal(raw, &got) != nil {
		return ""
	}
	show := func(v any) string {
		b, _ := json.Marshal(v)
		if s := string(b); len(s) <= 200 {
			return s
		}
		return string(b[:200]) + "..."
	}
	out := []string{}
	if selector, ok := m["selector"]; ok {
		count, _ := got["count"].(float64)
		if want, ok := m["count"]; ok {
			if int(count) != want.(int) {
				out = append(out, fmt.Sprintf("count: want %d, got %d", want, int(count)))
			}
		} else if count == 0 {
			out = append(out, fmt.Sprintf("selector %s matched no elements", show(selector)))
		}
		if count > 0 {
			if want, ok := m["text"]; ok && got["text"] != want {
				out = append(out, fmt.Sprintf("text: want %s, got %s", show(want), show(got["text"])))
			}
			if want, ok := m["contains"]; ok {
				if text, _ := got["text"].(string); !strings.Contains(text, want.(string)) {
					out = append(out, fmt.Sprintf("contains: %s not in %s", show(want), show(got["text"])))
				}
			}
			if want, ok := m["value"]; ok && got["value"] != want {
				out = append(out, fmt.Sprintf("value: want %s, got %s", show(want), show(got["value"])))
			}
		}
		if want, ok := m["visible"]; ok && (got["visible"] == true) != want {
			out = append(out, fmt.Sprintf("visible: want %v, got %v", want, got["visible"] == true))
		}
	}
	if want, ok := m["url"]; ok && got["url"] != want {
		out = append(out, fmt.Sprintf("url: want %s, got %s", show(want), show(got["url"])))
	}
	if _, ok := m["js"]; ok {
		if e, ok := got["jsError"]; ok {
			out = append(out, fmt.Sprintf("js threw %v", e))
		} else if got["js"] != true {
			out = append(out, "js returned "+show(got["js"]))
		}
	}
	return strings.Join(out, "; ")
}

// networkCondition returns the filter of a network condition, or nil.
func networkCondition(value any) map[string]any {
	m, _ := value.(map[string]any)
	n, _ := m["network"].(map[string]any)
	return n
}

// evaluate checks a condition once. For a network condition it also returns
// the number of matching requests.
func evaluate(ctx context.Context, conditions map[string]any, exec Executor) (bool, int, error) {
	if filter := networkCondition(conditions); filter != nil {
		result, err := exec(ctx, append([]string{"network"}, networkFilterArgs(filter)...))
		if err != nil {
			return false, 0, err
		}
		var items []json.RawMessage
		if err = json.Unmarshal(result, &items); err != nil {
			return false, 0, errors.New("network returned an unexpected result")
		}
		if want, ok := filter["count"].(int); ok {
			return len(items) == want, len(items), nil
		}
		return len(items) > 0, len(items), nil
	}
	result, err := exec(ctx, conditionArgs(conditions))
	if err != nil {
		return false, 0, err
	}
	var passed bool
	if err = json.Unmarshal(result, &passed); err != nil || string(result) == "null" {
		return false, 0, errors.New("condition must evaluate to a boolean")
	}
	return passed, 0, nil
}

func assert(ctx context.Context, conditions map[string]any, exec Executor) (json.RawMessage, error) {
	ticker := time.NewTicker(75 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, expectFailed(ctx, conditions, exec, err)
		}
		passed, _, err := evaluate(ctx, conditions, exec)
		if err != nil {
			if ctx.Err() != nil {
				return nil, expectFailed(ctx, conditions, exec, ctx.Err())
			}
			return nil, err
		}
		if passed {
			return json.RawMessage("true"), nil
		}
		select {
		case <-ctx.Done():
			return nil, expectFailed(ctx, conditions, exec, ctx.Err())
		case <-ticker.C:
		}
	}
}

// expectFailed explains a timed-out expect with the last observed values.
func expectFailed(ctx context.Context, conditions map[string]any, exec Executor, cause error) error {
	if !errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	observeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if filter := networkCondition(conditions); filter != nil {
		_, got, err := evaluate(observeCtx, conditions, exec)
		if err != nil {
			return fmt.Errorf("expect timed out (%w); could not read network log: %v", cause, err)
		}
		want := "at least 1"
		if c, ok := filter["count"].(int); ok {
			want = strconv.Itoa(c)
		}
		return fmt.Errorf("expect timed out: network: want %s matching requests, got %d (%w)", want, got, cause)
	}
	raw, err := exec(observeCtx, scopedEval(conditions, observeExpression))
	if err != nil {
		return fmt.Errorf("expect timed out (%w); could not read page state: %v", cause, err)
	}
	if detail := mismatch(conditions, raw); detail != "" {
		return fmt.Errorf("expect timed out: %s (%w)", detail, cause)
	}
	return fmt.Errorf("expect timed out (%w)", cause)
}
