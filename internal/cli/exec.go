package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
)

func splitLine(line string) ([]string, error) {
	var args []string
	var word strings.Builder
	quote := rune(0)
	escaped, started := false, false
	flush := func() {
		if started {
			args = append(args, word.String())
			word.Reset()
			started = false
		}
	}
	for _, ch := range line {
		if escaped {
			word.WriteRune(ch)
			started = true
			escaped = false
			continue
		}
		if ch == '\\' && quote != '\'' {
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			} else {
				word.WriteRune(ch)
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			started = true
			continue
		}
		if ch == '#' && !started {
			break
		}
		if unicode.IsSpace(ch) {
			flush()
			continue
		}
		word.WriteRune(ch)
		started = true
	}
	if escaped || quote != 0 {
		return nil, errors.New("unfinished quote or escape")
	}
	flush()
	return args, nil
}
func runExec(ctx context.Context, args []string, o Options, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: wok exec FILE|-")
	}
	var source io.Reader = os.Stdin
	if args[0] != "-" {
		file, e := os.Open(args[0])
		if e != nil {
			return e
		}
		defer file.Close()
		source = file
	}
	return runExecSource(ctx, source, o, out)
}

// runExecSource runs newline-separated command lines from source.
func runExecSource(ctx context.Context, source io.Reader, o Options, out io.Writer) error {
	scanner := bufio.NewScanner(io.LimitReader(source, (1<<20)+1))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	type lineResult struct {
		Line   int             `json:"line"`
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result,omitempty"`
		Error  string          `json:"error,omitempty"`
	}
	results := []lineResult{}
	line := 0
	total := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		total += len(text) + 1
		if total > 1<<20 {
			return errors.New("exec input exceeds 1 MiB")
		}
		command, e := splitLine(text)
		if e != nil {
			return fmt.Errorf("line %d: %w", line, e)
		}
		if len(command) == 0 {
			continue
		}
		if command[0] == "exec" || command[0] == "run" || command[0] == "run-code" || command[0] == "terminal" {
			return errors.New("nested batches and interactive terminal are not allowed in exec")
		}
		prefix := []string{"--profile", o.Profile, "--json"}
		if o.Endpoint != "" {
			prefix = []string{"--cdp", o.Endpoint, "--json"}
		}
		if o.Tab != "" {
			prefix = append(prefix, "--tab", o.Tab)
		}
		if o.NoSandbox {
			prefix = append(prefix, "--no-sandbox")
		}
		if o.Browser != "" {
			prefix = append(prefix, "--browser", o.Browser)
		}
		prefix = append(prefix, "--")
		var b bytes.Buffer
		e = run(ctx, append(prefix, command...), &b)
		entry := lineResult{Line: line, OK: e == nil}
		if e != nil {
			entry.Error = e.Error()
		} else if json.Valid(bytes.TrimSpace(b.Bytes())) {
			entry.Result = append(json.RawMessage(nil), bytes.TrimSpace(b.Bytes())...)
		}
		results = append(results, entry)
		if e != nil {
			_ = emit(out, map[string]any{"ok": false, "lines": results})
			return fmt.Errorf("line %d: %w", line, e)
		}
	}
	if e := scanner.Err(); e != nil {
		return e
	}
	return emit(out, map[string]any{"ok": true, "lines": results})
}
