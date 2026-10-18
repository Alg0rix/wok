package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/alg0rix/wok/internal/cli"
	"github.com/alg0rix/wok/internal/render"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if len(os.Args) > 1 && os.Args[1] == "_render" {
		f := flag.NewFlagSet("_render", flag.ContinueOnError)
		endpoint := f.String("cdp", "", "")
		ready := f.String("ready-file", "", "")
		tokenFile := f.String("token-file", "", "")
		optionsFile := f.String("options-file", "", "")
		listen := f.String("listen", "127.0.0.1:0", "")
		if err := f.Parse(os.Args[2:]); err != nil {
			os.Exit(1)
		}
		token, err := os.ReadFile(*tokenFile)
		if err == nil {
			options := render.Options{}
			if *optionsFile != "" {
				var data []byte
				data, err = os.ReadFile(*optionsFile)
				if err == nil {
					err = json.Unmarshal(data, &options)
				}
			}
			if err == nil {
				err = render.ServeWithOptions(ctx, *endpoint, *listen, *ready, string(token), options)
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	os.Exit(cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
