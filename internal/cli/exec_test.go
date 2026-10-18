package cli

import (
	"reflect"
	"testing"
)

func TestSplitLine(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{{`fill '#name' "hello world"`, []string{"fill", "#name", "hello world"}}, {`type '' # comment`, []string{"type", ""}}, {`eval '"$(touch /tmp/no)"'`, []string{"eval", `"$(touch /tmp/no)"`}}, {`# ignored`, nil}} {
		got, e := splitLine(tc.in)
		if e != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%q: %v %v", tc.in, got, e)
		}
	}
	for _, s := range []string{`fill "bad`, "fill \\"} {
		if _, e := splitLine(s); e == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}
