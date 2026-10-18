package cli

import (
	"context"
	"errors"
	"io"
	"strconv"

	"github.com/alg0rix/wok/internal/session"
)

type device struct {
	Width  int     `json:"width"`
	Height int     `json:"height"`
	DPR    float64 `json:"dpr"`
	Mobile bool    `json:"mobile"`
}

var devices = map[string]device{
	"desktop": {1280, 720, 1, false}, "iphone-13": {390, 844, 3, true}, "iphone-14": {390, 844, 3, true}, "iphone-14-pro": {393, 852, 3, true}, "pixel-7": {412, 915, 2.625, true}, "ipad-mini": {768, 1024, 2, true},
}

func set(ctx context.Context, store *session.Store, state *session.State, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: wok set viewport|device|media|offline [ARGS]")
	}
	switch args[0] {
	case "viewport":
		a := args[1:]
		if len(a) == 3 {
			a = []string{a[0], a[1], "--dpr", a[2]}
		}
		return viewport(ctx, store, state, a, out)
	case "device":
		if len(args) == 1 {
			return emit(out, devices)
		}
		if len(args) != 2 {
			return errors.New("usage: wok set device [NAME]")
		}
		d, ok := devices[args[1]]
		if !ok {
			return errors.New("unknown device; run wok set device to list presets")
		}
		a := []string{strconv.Itoa(d.Width), strconv.Itoa(d.Height), "--dpr", strconv.FormatFloat(d.DPR, 'g', -1, 64)}
		if d.Mobile {
			a = append(a, "--mobile")
		}
		return viewport(ctx, store, state, a, out)
	case "media":
		if len(args) != 2 || (args[1] != "dark" && args[1] != "light" && args[1] != "no-preference") {
			return errors.New("usage: wok set media dark|light|no-preference")
		}
		result, e := control(ctx, store, state, "POST", "/render/environment", map[string]any{"media": args[1]})
		if e != nil {
			return e
		}
		return emit(out, result)
	case "offline":
		if len(args) != 2 || (args[1] != "on" && args[1] != "off") {
			return errors.New("usage: wok set offline on|off")
		}
		result, e := control(ctx, store, state, "POST", "/render/environment", map[string]any{"offline": args[1] == "on"})
		if e != nil {
			return e
		}
		return emit(out, result)
	default:
		return errors.New("set supports viewport, device, media and offline")
	}
}
