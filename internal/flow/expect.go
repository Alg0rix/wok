package flow

import (
	"context"
	"encoding/json"
	"errors"
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
func assert(ctx context.Context, conditions map[string]any, exec Executor) (json.RawMessage, error) {
	expression := expectExpression(conditions)
	ticker := time.NewTicker(75 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, err := exec(ctx, []string{"eval", expression})
		if err != nil {
			return nil, err
		}
		var passed bool
		if err = json.Unmarshal(result, &passed); err != nil || string(result) == "null" {
			return nil, errors.New("expect eval must return a boolean")
		}
		if passed {
			return json.RawMessage("true"), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
