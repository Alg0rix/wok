package cli

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestNetworkFilter(t *testing.T) {
	items := func() []any {
		return []any{
			map[string]any{"index": 1, "method": "GET", "url": "http://h/app.js", "type": "Script"},
			map[string]any{"index": 2, "method": "POST", "url": "http://h/api/chat/stream", "type": "Fetch"},
			map[string]any{"index": 3, "method": "POST", "url": "http://h/api/other", "type": "XHR"},
		}
	}
	indexes := func(out []any) []int {
		got := []int{}
		for _, item := range out {
			got = append(got, item.(map[string]any)["index"].(int))
		}
		return got
	}
	for args, want := range map[string][]int{
		"":                                   {1, 2, 3},
		"--method post":                      {2, 3},
		"--method=POST --url **/api/chat/**": {2},
		"--type fetch":                       {2},
		"--url /api/":                        {2, 3},
	} {
		var a []string
		if args != "" {
			a = strings.Fields(args)
		}
		f, err := parseNetworkFilter(a)
		if err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		out, err := f.apply(context.Background(), items(), nil)
		if err != nil || !reflect.DeepEqual(indexes(out), want) {
			t.Errorf("%s: got %v want %v (%v)", args, indexes(out), want, err)
		}
	}
	f, _ := parseNetworkFilter([]string{"--method", "POST", "--body"})
	asked := []int{}
	out, err := f.apply(context.Background(), items(), func(index int) (any, error) {
		asked = append(asked, index)
		if index == 2 {
			return map[string]any{"requestBody": `{"message":"hi"}`}, nil
		}
		return map[string]any{"requestBody": "plain"}, nil
	})
	if err != nil || !reflect.DeepEqual(asked, []int{2, 3}) {
		t.Fatalf("asked=%v err=%v", asked, err)
	}
	first, second := out[0].(map[string]any), out[1].(map[string]any)
	if !reflect.DeepEqual(first["requestJson"], map[string]any{"message": "hi"}) || second["requestBody"] != "plain" || second["requestJson"] != nil {
		t.Fatalf("out=%v", out)
	}
	for _, bad := range [][]string{{"--url", "*/api/*"}, {"--method"}, {"--status", "200"}, {"--method="}, {"--body=yes"}} {
		if _, err := parseNetworkFilter(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}
