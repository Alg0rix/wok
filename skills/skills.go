// Package skills distributes Wok's bundled agent skill without a network dependency.
package skills

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed wok/SKILL.md wok/references/commands.md wok/references/flows.md
var bundled embed.FS

const description = "Browse and interact with websites using Wok's persistent CDP browser CLI."

var assetNames = []string{"SKILL.md", "references/commands.md", "references/flows.md"}

// Run handles the arguments following `wok skills`.
func Run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: wok skills list|show [wok]|install [options]")
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("usage: wok skills list")
		}
		return json.NewEncoder(out).Encode([]map[string]string{{"name": "wok", "description": description}})
	case "show", "get":
		if len(args) > 2 || (len(args) == 2 && args[1] != "wok") {
			return errors.New("usage: wok skills show [wok]")
		}
		data, err := bundled.ReadFile("wok/SKILL.md")
		if err != nil {
			return err
		}
		_, err = out.Write(data)
		return err
	case "install":
		return install(args[1:], out)
	default:
		return fmt.Errorf("unknown skills command %q; use list, show, or install", args[0])
	}
}

type installResult struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}
type assetPlan struct {
	path  string
	data  []byte
	write bool
}
type targetPlan struct {
	result installResult
	assets []assetPlan
}

func install(args []string, out io.Writer) error {
	options := flag.NewFlagSet("skills install", flag.ContinueOnError)
	options.SetOutput(io.Discard)
	agent := options.String("agent", "codex", "codex, claude, cursor, or all")
	global := options.Bool("global", false, "install in the home directory")
	custom := options.String("path", "", "explicit skills root")
	force := options.Bool("force", false, "overwrite changed bundled files")
	if err := options.Parse(args); err != nil {
		return fmt.Errorf("skills install: %w", err)
	}
	if options.NArg() != 0 {
		return errors.New("usage: wok skills install [--agent codex|claude|cursor|all] [--global] [--path DIR] [--force]")
	}
	specified := map[string]bool{}
	options.Visit(func(f *flag.Flag) { specified[f.Name] = true })
	if specified["path"] {
		if strings.TrimSpace(*custom) == "" {
			return errors.New("--path requires a nonempty directory")
		}
		if specified["global"] || specified["agent"] {
			return errors.New("--path cannot be combined with --global or --agent")
		}
	}
	agents := []string{*agent}
	switch *agent {
	case "codex", "claude", "cursor":
	case "all":
		agents = []string{"codex", "claude", "cursor"}
	default:
		return fmt.Errorf("unsupported agent %q", *agent)
	}
	base, err := os.Getwd()
	if err != nil {
		return err
	}
	if *global {
		base, err = os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("home directory: %w", err)
		}
	}
	// Symlinks are refused below the anchor only: the working directory, home,
	// or the parent of --path may legitimately live under one (macOS /var).
	roots := []string{}
	anchor := base
	if specified["path"] {
		root, err := filepath.Abs(*custom)
		if err != nil {
			return err
		}
		roots = append(roots, root)
		anchor = filepath.Dir(root)
	} else {
		for _, name := range agents {
			dir := map[string]string{"codex": ".agents", "claude": ".claude", "cursor": ".cursor"}[name]
			roots = append(roots, filepath.Join(base, dir, "skills"))
		}
	}
	// Preflight every destination before creating directories or changing files.
	plans := make([]targetPlan, 0, len(roots))
	for _, root := range roots {
		dir := filepath.Join(root, "wok")
		plan := targetPlan{result: installResult{Path: dir, Status: "unchanged"}}
		for _, name := range assetNames {
			path := filepath.Join(dir, filepath.FromSlash(name))
			if err := checkPath(anchor, path); err != nil {
				return err
			}
			data, err := fs.ReadFile(bundled, "wok/"+name)
			if err != nil {
				return err
			}
			existing, err := os.ReadFile(path)
			write := false
			switch {
			case errors.Is(err, os.ErrNotExist):
				write = true
				if plan.result.Status != "updated" {
					plan.result.Status = "installed"
				}
			case err != nil:
				return fmt.Errorf("read %s: %w", path, err)
			case !bytes.Equal(existing, data):
				if !*force {
					return fmt.Errorf("modified skill asset %s; use --force to overwrite", path)
				}
				write = true
				plan.result.Status = "updated"
			}
			plan.assets = append(plan.assets, assetPlan{path, data, write})
		}
		plans = append(plans, plan)
	}
	results := make([]installResult, 0, len(plans))
	for _, plan := range plans {
		for _, asset := range plan.assets {
			if !asset.write {
				continue
			}
			if err := checkPath(anchor, asset.path); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(asset.path), 0755); err != nil {
				return err
			}
			if err := checkPath(anchor, asset.path); err != nil {
				return err
			}
			if err := atomicWrite(asset.path, asset.data); err != nil {
				return err
			}
		}
		results = append(results, plan.result)
	}
	return json.NewEncoder(out).Encode(results)
}

// checkPath rejects symlinks anywhere between anchor and path, including an
// existing final asset. It also rejects special files and non-directory parents.
func checkPath(anchor, path string) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if anchor, err = filepath.Abs(anchor); err != nil {
		return err
	}
	relative, err := filepath.Rel(anchor, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("skill destination %s is outside %s", path, anchor)
	}
	current := anchor
	parts := strings.Split(relative, string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink in skill destination: %s", current)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("skill destination parent is not a directory: %s", current)
		}
		if i == len(parts)-1 && !info.Mode().IsRegular() {
			return fmt.Errorf("skill asset is not a regular file: %s", current)
		}
	}
	return nil
}

func atomicWrite(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".wok-skill-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if err = file.Chmod(0644); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, path)
}
