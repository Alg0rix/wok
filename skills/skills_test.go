package skills

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func run(t *testing.T, args ...string) []installResult {
	t.Helper()
	var out bytes.Buffer
	if err := Run(args, &out); err != nil {
		t.Fatal(err)
	}
	var results []installResult
	if err := json.Unmarshal(out.Bytes(), &results); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	return results
}
func TestListAndShow(t *testing.T) {
	var out bytes.Buffer
	if err := Run([]string{"list"}, &out); err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out.Bytes(), &entries); err != nil || len(entries) != 1 || entries[0].Name != "wok" {
		t.Fatalf("list = %q, %v", out.String(), err)
	}
	want, err := bundled.ReadFile("wok/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"show"}, {"show", "wok"}, {"get", "wok"}} {
		out.Reset()
		if err := Run(args, &out); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out.Bytes(), want) {
			t.Fatal("show did not return canonical skill")
		}
	}
}
func TestDefaultInstallIdempotent(t *testing.T) {
	// The working directory is reported canonically (macOS /var is a symlink).
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	results := run(t, "install")
	target := filepath.Join(dir, ".agents", "skills", "wok")
	if len(results) != 1 || results[0].Path != target || results[0].Status != "installed" {
		t.Fatalf("install = %+v", results)
	}
	for _, name := range assetNames {
		path := filepath.Join(target, filepath.FromSlash(name))
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0644 {
			t.Fatalf("asset permissions = %v", info.Mode().Perm())
		}
		want, _ := bundled.ReadFile("wok/" + name)
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, want) {
			t.Fatalf("installed %s differs: %v", name, err)
		}
	}
	results = run(t, "install")
	if results[0].Status != "unchanged" {
		t.Fatalf("second install = %+v", results)
	}
}
func TestCustomConflictAndForce(t *testing.T) {
	root := filepath.Join(t.TempDir(), "custom")
	results := run(t, "install", "--path", root)
	target := filepath.Join(root, "wok")
	if results[0].Path != target {
		t.Fatalf("custom path = %+v", results)
	}
	note := filepath.Join(target, "notes.txt")
	if err := os.WriteFile(note, []byte("my notes"), 0600); err != nil {
		t.Fatal(err)
	}
	asset := filepath.Join(target, "SKILL.md")
	if err := os.WriteFile(asset, []byte("my edits"), 0644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Run([]string{"install", "--path", root}, &out); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("conflict error = %v", err)
	}
	data, _ := os.ReadFile(asset)
	if string(data) != "my edits" {
		t.Fatal("conflict overwrote user changes")
	}
	results = run(t, "install", "--path", root, "--force")
	if results[0].Status != "updated" {
		t.Fatalf("force = %+v", results)
	}
	data, _ = os.ReadFile(note)
	if string(data) != "my notes" {
		t.Fatal("force changed unrelated file")
	}
}
func TestAllPreflightsBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	conflict := filepath.Join(dir, ".claude", "skills", "wok", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(conflict), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conflict, []byte("local changes"), 0644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Run([]string{"install", "--agent", "all"}, &out); err == nil {
		t.Fatal("accepted conflicting second target")
	}
	if _, err := os.Stat(filepath.Join(dir, ".agents")); !os.IsNotExist(err) {
		t.Fatalf("first target modified before preflight completed: %v", err)
	}
	results := run(t, "install", "--agent", "all", "--force")
	if len(results) != 3 {
		t.Fatalf("all = %+v", results)
	}
	for _, prefix := range []string{".agents", ".claude", ".cursor"} {
		if _, err := os.Stat(filepath.Join(dir, prefix, "skills", "wok", "SKILL.md")); err != nil {
			t.Fatal(err)
		}
	}
}
func TestGlobalAgentTargetsUseFakeHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, tc := range []struct{ agent, folder string }{{"codex", ".agents"}, {"claude", ".claude"}, {"cursor", ".cursor"}} {
		results := run(t, "install", "--agent", tc.agent, "--global")
		want := filepath.Join(home, tc.folder, "skills", "wok")
		if len(results) != 1 || results[0].Path != want {
			t.Fatalf("%s global = %+v; want %s", tc.agent, results, want)
		}
	}
}
func TestRejectSymlinkDestinations(t *testing.T) {
	for _, where := range []string{"ancestor", "asset", "reference-directory"} {
		t.Run(where, func(t *testing.T) {
			dir := t.TempDir()
			outside := t.TempDir()
			root := filepath.Join(dir, "skills")
			switch where {
			case "ancestor":
				if err := os.Symlink(outside, root); err != nil {
					t.Fatal(err)
				}
			case "asset":
				if err := os.MkdirAll(filepath.Join(root, "wok"), 0755); err != nil {
					t.Fatal(err)
				}
				victim := filepath.Join(outside, "victim")
				if err := os.WriteFile(victim, []byte("keep"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(victim, filepath.Join(root, "wok", "SKILL.md")); err != nil {
					t.Fatal(err)
				}
			case "reference-directory":
				if err := os.MkdirAll(filepath.Join(root, "wok"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(root, "wok", "references")); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			if err := Run([]string{"install", "--path", root, "--force"}, &out); err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("symlink accepted: %v", err)
			}
			if where == "reference-directory" {
				if _, err := os.Stat(filepath.Join(root, "wok", "SKILL.md")); !os.IsNotExist(err) {
					t.Fatal("preflight created an earlier asset")
				}
			}
			if where == "asset" {
				data, _ := os.ReadFile(filepath.Join(outside, "victim"))
				if string(data) != "keep" {
					t.Fatal("symlink victim changed")
				}
			}
		})
	}
}
func TestInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{}, {"unknown"}, {"list", "extra"}, {"show", "other"}, {"get", "wok", "extra"},
		{"install", "extra"}, {"install", "--agent", "unknown"}, {"install", "--agent"},
		{"install", "--path", ""}, {"install", "--path", "x", "--global"}, {"install", "--path", "x", "--agent", "codex"},
		{"install", "--unknown"},
	} {
		var out bytes.Buffer
		if err := Run(args, &out); err == nil {
			t.Errorf("accepted invalid arguments %q", args)
		}
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Error(err)
		}
	})
}

// macOS temp and home directories often live below a symlink such as /var.
func TestSymlinkedAncestorAccepted(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symbolic links unavailable:", err)
	}
	var out bytes.Buffer
	if err := Run([]string{"install", "--path", filepath.Join(link, "project", "skills")}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(real, "project", "skills", "wok", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}
