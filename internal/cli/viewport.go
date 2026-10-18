package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"

	"github.com/alg0rix/wok/internal/session"
)

func viewport(ctx context.Context, store *session.Store, state *session.State, args []string, out io.Writer) error {
	method := http.MethodGet
	var settings any
	if len(args) > 0 {
		method = http.MethodPost
		if args[0] == "reset" {
			if len(args) != 1 {
				return errors.New("usage: wok viewport reset")
			}
			settings = map[string]any{"reset": true}
		} else {
			if len(args) < 2 {
				return errors.New("usage: wok viewport WIDTH HEIGHT [--mobile] [--dpr N]")
			}
			width, e1 := strconv.Atoi(args[0])
			height, e2 := strconv.Atoi(args[1])
			if e1 != nil || e2 != nil || width < 1 || height < 1 || width > 8192 || height > 8192 {
				return errors.New("viewport dimensions must be integers between 1 and 8192")
			}
			flags := flag.NewFlagSet("viewport", flag.ContinueOnError)
			flags.SetOutput(io.Discard)
			mobile := flags.Bool("mobile", false, "emulate mobile layout, touch and user agent")
			dpr := flags.Float64("dpr", 1, "device pixel ratio")
			if err := flags.Parse(args[2:]); err != nil {
				return err
			}
			if flags.NArg() != 0 || math.IsNaN(*dpr) || math.IsInf(*dpr, 0) || *dpr < 0.5 || *dpr > 4 {
				return errors.New("viewport DPR must be 0.5..4; use --dpr N")
			}
			settings = map[string]any{"width": width, "height": height, "deviceScaleFactor": *dpr, "mobile": *mobile}
		}
	}
	if err := store.EnsureRender(ctx, state, ""); err != nil {
		return err
	}
	var body io.Reader
	if settings != nil {
		data, err := json.Marshal(settings)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, state.RenderURL+"/render/emulation", body)
	if err != nil {
		return err
	}
	if settings != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	authorize(req, state)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("viewport: HTTP %d: %s", resp.StatusCode, data)
	}
	var result any
	if err = json.Unmarshal(data, &result); err != nil {
		return err
	}
	return emit(out, result)
}
