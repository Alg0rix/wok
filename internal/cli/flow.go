package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/alg0rix/wok/internal/flow"
)

type variables map[string]string

func (v variables) String() string { return "KEY=VALUE" }
func (v variables) Set(raw string) error {
	key, value, ok := strings.Cut(raw, "=")
	if !ok || key == "" {
		return errors.New("--var requires KEY=VALUE")
	}
	if _, exists := v[key]; exists {
		return fmt.Errorf("duplicate --var %s", key)
	}
	v[key] = value
	return nil
}
func runFlow(ctx context.Context, args []string, o Options, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: wok run FILE|- [--var KEY=VALUE] [--artifacts DIR]")
	}
	file := args[0]
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	vars := variables{}
	flags.Var(vars, "var", "flow variable KEY=VALUE (repeatable)")
	artifacts := flags.String("artifacts", "", "failure screenshot directory")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected flow arguments")
	}
	var reader io.Reader = os.Stdin
	if file != "-" {
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()
		reader = f
	}
	source, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if err != nil {
		return err
	}
	profile := ""
	if o.ProfileSet {
		profile = o.Profile
	}
	options := flow.Options{Profile: profile, Endpoint: o.Endpoint, TargetID: o.Tab, Vars: vars, ArtifactDir: *artifacts}
	execute := func(stepCtx context.Context, command []string) (json.RawMessage, error) {
		// Reuse CLI behavior in process. The command still takes its profile lock,
		// attaches via CDP and detaches, but does not start another Wok process.
		common := []string{"--json"}
		if o.Browser != "" {
			common = append(common, "--browser", o.Browser)
		}
		if o.NoSandbox {
			common = append(common, "--no-sandbox")
		}
		if o.NoRender {
			common = append(common, "--no-render")
		}
		if o.Headed {
			common = append(common, "--headed")
		}
		command = append(common, command...)
		var output bytes.Buffer
		if err := run(stepCtx, command, &output); err != nil {
			return nil, err
		}
		result := bytes.TrimSpace(output.Bytes())
		if !json.Valid(result) {
			return nil, errors.New("command returned invalid JSON")
		}
		return append(json.RawMessage(nil), result...), nil
	}
	report, runErr := flow.Run(ctx, source, options, execute)
	if err = emit(out, report); err != nil {
		return err
	}
	return runErr
}
