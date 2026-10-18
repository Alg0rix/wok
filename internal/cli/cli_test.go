package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	o, args, e := Parse([]string{"--profile=work", "eval", "--timeout", "500ms", "--", "--profile"})
	if e != nil || o.Profile != "work" || o.Timeout != 500*time.Millisecond || len(args) != 2 || args[1] != "--profile" {
		t.Fatalf("%+v %v %v", o, args, e)
	}
	for _, a := range [][]string{{"--profile"}, {"--timeout", "0s"}, {"--timeout", "-1"}, {"--json=true"}, {"--profile="}} {
		if _, _, e = Parse(a); e == nil {
			t.Fatalf("accepted %v", a)
		}
	}
}
func TestHelpAndVersionWithoutBrowser(t *testing.T) {
	for _, arg := range []string{"--help", "--version"} {
		var out, errs bytes.Buffer
		if code := Run(context.Background(), []string{arg}, &out, &errs); code != 0 || out.Len() == 0 || errs.Len() != 0 {
			t.Fatalf("%s: %d %s", arg, code, errs.String())
		}
	}
}

func TestSkillCommandsOffline(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	t.Setenv("WOK_HOME", stateDir)
	t.Setenv("WOK_BROWSER", "/missing/browser")
	for _, args := range [][]string{{"skills", "list"}, {"skills", "show"}, {"skills", "install", "--path", filepath.Join(root, "skills")}, {"install", "skills", "--path", filepath.Join(root, "skills")}} {
		var out, errs bytes.Buffer
		if code := Run(context.Background(), args, &out, &errs); code != 0 {
			t.Fatalf("%v: %s", args, errs.String())
		}
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "wok", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatal("offline skills command opened browser state", err)
	}
}
