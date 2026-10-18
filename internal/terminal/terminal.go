// Package terminal displays and controls a browser using ANSI half-block frames.
package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"io"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alg0rix/wok/internal/browser"
	"golang.org/x/term"
)

type Config struct {
	FPS, Frames, Width int
	Out                io.Writer
	Capture            func(context.Context) ([]byte, error)
}

var mouse = regexp.MustCompile(`^\x1b\[<(\d+);(\d+);(\d+)([Mm])`)

func Run(ctx context.Context, p *browser.Page, c Config) error {
	if c.Capture == nil {
		return errors.New("terminal capture callback is required")
	}
	if c.FPS < 1 || c.FPS > 20 {
		return errors.New("terminal FPS must be 1..20")
	}
	if c.Out == nil {
		c.Out = os.Stdout
	}
	if c.Width == 0 {
		c.Width = 80
	}
	interactive := c.Frames == 0
	if interactive && (!term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd()))) {
		return errors.New("terminal requires a TTY; use --frames N for non-interactive ANSI output")
	}
	if interactive {
		state, e := term.MakeRaw(int(os.Stdin.Fd()))
		if e != nil {
			return e
		}
		defer term.Restore(int(os.Stdin.Fd()), state)
		fmt.Fprint(c.Out, "\x1b[?1049h\x1b[?25l\x1b[?1002h\x1b[?1006h")
		defer fmt.Fprint(c.Out, "\x1b[0m\x1b[?1002l\x1b[?1006l\x1b[?25h\x1b[?1049l")
	}
	chunks := make(chan []byte, 16)
	inputCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if interactive {
		go func() {
			for {
				b := make([]byte, 256)
				n, e := os.Stdin.Read(b)
				if n > 0 {
					select {
					case chunks <- b[:n]:
					case <-inputCtx.Done():
						return
					}
				}
				if e != nil {
					return
				}
			}
		}()
	}
	var pending []byte
	lastInput := time.Now()
	logicalW, logicalH := 1280.0, 720.0
	cols, rows := c.Width, 24
	draw := func() error {
		if interactive {
			if w, h, e := term.GetSize(int(os.Stdout.Fd())); e == nil {
				cols = w
				rows = h - 2
			}
		}
		cols = max(16, min(240, cols))
		rows = max(4, min(120, rows))
		call, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		data, e := c.Capture(call)
		if e != nil {
			return e
		}
		image, e := png.Decode(bytes.NewReader(data))
		if e != nil {
			return e
		}
		if v, e := p.Eval(call, `({w:innerWidth,h:innerHeight})`); e == nil {
			if m, ok := v.(map[string]any); ok {
				logicalW, _ = m["w"].(float64)
				logicalH, _ = m["h"].(float64)
			}
		}
		bounds := image.Bounds()
		height := int(math.Round(float64(bounds.Dy()) / float64(bounds.Dx()) * float64(cols) / 2))
		height = max(1, min(rows, height))
		rows = height
		var out strings.Builder
		if interactive {
			out.WriteString("\x1b[H")
		}
		out.WriteString("Wok · Ctrl+C exits · mouse/keyboard control\r\n")
		for y := 0; y < rows; y++ {
			for x := 0; x < cols; x++ {
				r1, g1, b1, _ := image.At(bounds.Min.X+x*bounds.Dx()/cols, bounds.Min.Y+(y*2)*bounds.Dy()/(rows*2)).RGBA()
				r2, g2, b2, _ := image.At(bounds.Min.X+x*bounds.Dx()/cols, bounds.Min.Y+(y*2+1)*bounds.Dy()/(rows*2)).RGBA()
				fmt.Fprintf(&out, "\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm▀", r1>>8, g1>>8, b1>>8, r2>>8, g2>>8, b2>>8)
			}
			out.WriteString("\x1b[0m\x1b[K\r\n")
		}
		out.WriteString("\x1b[0m")
		_, e = io.WriteString(c.Out, out.String())
		return e
	}
	if e := draw(); e != nil {
		return e
	}
	rendered := 1
	if c.Frames == 1 {
		return nil
	}
	ticker := time.NewTicker(time.Second / time.Duration(c.FPS))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if len(pending) > 0 && pending[0] == 27 && time.Since(lastInput) > 50*time.Millisecond {
				call, stop := context.WithTimeout(ctx, 3*time.Second)
				e := p.Press(call, "Escape")
				stop()
				if e != nil {
					return e
				}
				pending = pending[1:]
				for len(pending) > 0 {
					if pending[0] == 3 {
						return nil
					}
					call, stop := context.WithTimeout(ctx, 3*time.Second)
					n, e := keyInput(call, p, pending, cols, rows, logicalW, logicalH)
					stop()
					if e != nil {
						return e
					}
					if n == 0 {
						break
					}
					pending = pending[n:]
				}
			}
			if e := draw(); e != nil {
				return e
			}
			rendered++
			if c.Frames > 0 && rendered >= c.Frames {
				return nil
			}
		case chunk := <-chunks:
			lastInput = time.Now()
			pending = append(pending, chunk...)
			for len(pending) > 0 {
				if pending[0] == 3 {
					return nil
				}
				call, stop := context.WithTimeout(ctx, 3*time.Second)
				consumed, e := keyInput(call, p, pending, cols, rows, logicalW, logicalH)
				stop()
				if e != nil {
					return e
				}
				if consumed == 0 {
					break
				}
				pending = pending[consumed:]
			}
		}
	}
}
func keyInput(ctx context.Context, p *browser.Page, b []byte, cols, rows int, width, height float64) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	if b[0] == 27 {
		if match := mouse.FindSubmatch(b); match != nil {
			n, _ := strconv.Atoi(string(match[1]))
			cx, _ := strconv.Atoi(string(match[2]))
			cy, _ := strconv.Atoi(string(match[3]))
			if cy < 2 {
				return len(match[0]), nil
			}
			x := math.Max(0, math.Min(width-1, (float64(cx)-.5)*width/float64(cols)))
			y := math.Max(0, math.Min(height-1, (float64(cy)-1.5)*height/float64(rows)))
			params := map[string]any{"type": "mouseMoved", "x": x, "y": y}
			mods := 0
			if n&4 != 0 {
				mods |= 8
			}
			if n&8 != 0 {
				mods |= 1
			}
			if n&16 != 0 {
				mods |= 2
			}
			params["modifiers"] = mods
			if n&64 != 0 {
				params["type"] = "mouseWheel"
				params["deltaX"] = 0
				dy := -80
				if n&1 != 0 {
					dy = 80
				}
				params["deltaY"] = dy
			} else {
				button := []string{"left", "middle", "right", "none"}[n&3]
				params["button"] = button
				if match[4][0] == 'm' {
					params["type"] = "mouseReleased"
					params["buttons"] = 0
				} else if n&32 == 0 {
					params["type"] = "mousePressed"
					params["clickCount"] = 1
				}
				if match[4][0] != 'm' {
					params["buttons"] = []int{1, 4, 2, 0}[n&3]
				}
			}
			return len(match[0]), p.Call(ctx, "Input.dispatchMouseEvent", params, nil)
		}
		sequences := map[string]string{"\x1b[A": "ArrowUp", "\x1b[B": "ArrowDown", "\x1b[C": "ArrowRight", "\x1b[D": "ArrowLeft", "\x1b[H": "Home", "\x1b[F": "End", "\x1b[2~": "Insert", "\x1b[3~": "Delete", "\x1b[5~": "PageUp", "\x1b[6~": "PageDown", "\x1b[Z": "Shift+Tab"}
		for seq, key := range sequences {
			if bytes.HasPrefix(b, []byte(seq)) {
				return len(seq), p.Press(ctx, key)
			}
		}
		for seq := range sequences {
			if strings.HasPrefix(seq, string(b)) {
				return 0, nil
			}
		}
		if len(b) >= 2 && b[1] != '[' && b[1] != 'O' {
			return 1, p.Press(ctx, "Escape")
		}
		if len(b) < 3 || bytes.HasPrefix(b, []byte("\x1b[<")) && len(b) < 32 {
			return 0, nil
		}
		return 1, p.Press(ctx, "Escape")
	}
	if b[0] == '\r' || b[0] == '\n' {
		return 1, p.Press(ctx, "Enter")
	}
	if b[0] == '\t' {
		return 1, p.Press(ctx, "Tab")
	}
	if b[0] == 127 {
		return 1, p.Press(ctx, "Backspace")
	}
	if b[0] > 0 && b[0] < 27 {
		return 1, p.Press(ctx, "Control+"+string(rune('a'+b[0]-1)))
	}
	if !utf8.FullRune(b) {
		return 0, nil
	}
	r, n := utf8.DecodeRune(b)
	return n, p.Type(ctx, string(r))
}
