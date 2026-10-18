package cli

import (
	"context"
	"errors"
	"flag"
	"github.com/alg0rix/wok/internal/browser"
	"github.com/alg0rix/wok/internal/session"
	"github.com/alg0rix/wok/internal/terminal"
	"io"
)

func showTerminal(ctx context.Context, p *browser.Page, st *session.State, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("terminal", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	fps := flags.Int("fps", 5, "frames per second")
	frames := flags.Int("frames", 0, "finite non-interactive frames")
	width := flags.Int("width", 80, "non-interactive columns")
	if e := flags.Parse(args); e != nil {
		return e
	}
	if flags.NArg() != 0 || *frames < 0 || *width < 16 || *width > 240 {
		return errors.New("usage: wok terminal [--fps 1..20] [--frames N] [--width 16..240]")
	}
	return terminal.Run(ctx, p, terminal.Config{FPS: *fps, Frames: *frames, Width: *width, Out: out, Capture: func(c context.Context) ([]byte, error) { return screenshot(c, st, p) }})
}
