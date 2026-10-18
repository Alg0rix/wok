package render

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"strings"
	"testing"
)

func TestActionAdapter(t *testing.T) {
	cases := []struct {
		path, query string
		kind, key   string
		dy          float64
	}{
		{"scroll", "dy=-120", "scroll", "", 60},
		{"key", "key=a&mods=ctrl,shift", "key", "ctrl+shift+a", 0},
		{"key", "key=left", "key", "ArrowLeft", 0},
		{"mousedown", "x=2&y=3&button=right&mods=shift", "mousedown", "", 0},
		{"move", "x=4&y=5&buttons=left,right", "move", "", 0},
		{"mouseup", "x=4&y=5&button=left", "mouseup", "", 0},
	}
	for _, tt := range cases {
		t.Run(tt.path+tt.query, func(t *testing.T) {
			q, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatal(err)
			}
			a, err := parseAction("/render/"+tt.path, q, strings.NewReader(""))
			if err != nil {
				t.Fatal(err)
			}
			if a.kind != tt.kind || a.key != tt.key {
				t.Fatalf("%+v", a)
			}
			if tt.path == "scroll" {
				if a.params["deltaY"] != tt.dy || !a.centerX || !a.centerY {
					t.Fatalf("%+v", a)
				}
			}
			if tt.path == "move" && a.params["buttons"] != 3 {
				t.Fatal(a.params)
			}
			if tt.path == "mouseup" && (a.params["type"] != "mouseReleased" || a.params["buttons"] != 0) {
				t.Fatal(a.params)
			}
		})
	}
	a, err := parseAction("/render/type", url.Values{}, strings.NewReader("raw input"))
	if err != nil || a.text != "raw input" {
		t.Fatalf("%+v %v", a, err)
	}
	a, err = parseAction("/render/scroll", url.Values{"dy": {"-120"}, "x": {"12"}}, nil)
	if err != nil || a.centerX || !a.centerY {
		t.Fatalf("%+v %v", a, err)
	}
}
func TestActionRejectsMalformed(t *testing.T) {
	cases := []struct{ path, query string }{{"click", "x=-1&y=2"}, {"move", "x=NaN&y=2"}, {"move", "x=2&y=2&buttons=banana"}, {"scroll", "dy=Inf"}, {"key", "key=a&mods=super"}, {"key", "mods=ctrl"}, {"navigate", "url=javascript:alert(1)"}, {"type", "text=x&unexpected=y"}, {"click", "x=1&x=2&y=1"}, {"mousedown", "x=1&y=1&button=left,right"}}
	for _, tt := range cases {
		q, _ := url.ParseQuery(tt.query)
		if _, err := parseAction("/render/"+tt.path, q, strings.NewReader("")); err == nil {
			t.Errorf("accepted %s %s", tt.path, tt.query)
		}
	}
}
func TestPPMScale(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var data, out bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	if err := writePPM(&out, data.Bytes(), 2, 0); err != nil {
		t.Fatal(err)
	}
	want := append([]byte("P6\n2 1\n255\n"), 255, 0, 0, 255, 0, 0)
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("unexpected PPM: %v", out.Bytes())
	}
	if _, _, err := ppmSize(url.Values{"w": {"-1"}}); err == nil {
		t.Fatal("accepted invalid dimensions")
	}
}
