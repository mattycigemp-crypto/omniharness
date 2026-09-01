package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestRegistry(t *testing.T, root string) *Registry {
	t.Helper()
	r := NewRegistry()
	if err := NewNative(root).Register(r); err != nil {
		t.Fatal(err)
	}
	return r
}

func mustTool(t *testing.T, r *Registry, name string) Tool {
	t.Helper()
	tool, ok := r.Get(name)
	if !ok {
		t.Fatalf("tool %q not registered", name)
	}
	return tool
}

func TestReadWriteEditFile(t *testing.T) {
	dir := t.TempDir()
	r := newTestRegistry(t, dir)

	res, err := mustTool(t, r, "write_file").Run(context.Background(), map[string]any{
		"path": "a.txt", "content": "hello world",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Artifact {
		t.Fatal("write should be an artifact")
	}

	res, err = mustTool(t, r, "read_file").Run(context.Background(), map[string]any{"path": "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "hello world" {
		t.Fatalf("output %q", res.Output)
	}

	res, err = mustTool(t, r, "edit_file").Run(context.Background(), map[string]any{
		"path": "a.txt", "old_text": "world", "new_text": "there",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(b) != "hello there" {
		t.Fatalf("file content %q", b)
	}

	_, err = mustTool(t, r, "edit_file").Run(context.Background(), map[string]any{
		"path": "a.txt", "old_text": "nope", "new_text": "x",
	})
	if err == nil {
		t.Fatal("expected error for missing old_text")
	}
}

func TestWorkspaceConfinement(t *testing.T) {
	dir := t.TempDir()
	r := newTestRegistry(t, dir)
	_, err := mustTool(t, r, "read_file").Run(context.Background(), map[string]any{"path": t.TempDir()})
	if err == nil {
		t.Fatal("expected confinement error for path outside workspace")
	}
}

func TestListDirAndFindFiles(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main"), 0o644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# hi"), 0o644)
	r := newTestRegistry(t, dir)

	res, err := mustTool(t, r, "list_dir").Run(context.Background(), map[string]any{"path": "."})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "src") || !strings.Contains(res.Output, "README.md") {
		t.Fatalf("list_dir output %q", res.Output)
	}

	res, err = mustTool(t, r, "find_files").Run(context.Background(), map[string]any{"path": ".", "glob": "*.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "main.go") {
		t.Fatalf("find_files output %q", res.Output)
	}
}

func TestSearch(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("func Foo() {}\n// todo\n"), 0o644)
	r := newTestRegistry(t, dir)
	res, err := mustTool(t, r, "search").Run(context.Background(), map[string]any{"pattern": "Foo", "path": "."})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "a.go:1") {
		t.Fatalf("search output %q", res.Output)
	}
}

func TestSearchSkipsGitDir(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("secret"), 0o644)
	os.WriteFile(filepath.Join(dir, "real.txt"), []byte("secret"), 0o644)
	r := newTestRegistry(t, dir)
	res, err := mustTool(t, r, "search").Run(context.Background(), map[string]any{"pattern": "secret", "path": "."})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Output, ".git") {
		t.Fatalf("search must skip .git: %q", res.Output)
	}
	if !strings.Contains(res.Output, "real.txt") {
		t.Fatalf("search should find real.txt: %q", res.Output)
	}
}

func TestShellTool(t *testing.T) {
	dir := t.TempDir()
	r := newTestRegistry(t, dir)
	res, err := mustTool(t, r, "shell").Run(context.Background(), map[string]any{"command": "echo omniharness"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "omniharness") {
		t.Fatalf("shell output %q", res.Output)
	}
}

func TestShellTimeout(t *testing.T) {
	dir := t.TempDir()
	r := newTestRegistry(t, dir)
	start := time.Now()
	_, err := mustTool(t, r, "shell").Run(context.Background(), map[string]any{"command": "sleep 30", "timeout_sec": 1})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error %v", err)
	}
	// The timeout has to actually free the caller, not just describe itself.
	// Killing the shell leaves the grandchild holding the output pipe, so
	// without cmd.WaitDelay this returned the right error a full 30s late —
	// the agent stayed blocked for the whole runaway command.
	if limit := 1*time.Second + shellWaitDelay + 3*time.Second; elapsed > limit {
		t.Fatalf("shell returned after %s; a 1s timeout must not wait for the command (limit %s)", elapsed, limit)
	}
}

func TestResolvePathRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink inside the workspace pointing outside must not escape.
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	n := NewNative(root)
	if _, err := n.readFile(context.Background(), map[string]any{"path": "escape/secret.txt"}); err == nil {
		t.Fatal("symlink escape must be rejected")
	}
}

func TestGitRejectsWorkspaceEscapeFlags(t *testing.T) {
	n := NewNative(t.TempDir())
	for _, arg := range []string{"-C", "--git-dir", "--git-dir=/elsewhere"} {
		if _, err := n.git(context.Background(), map[string]any{"args": []any{arg, "/etc", "status"}}); err == nil {
			t.Fatalf("git arg %q must be rejected", arg)
		}
	}
}

func TestGitTool(t *testing.T) {
	dir := t.TempDir()
	r := newTestRegistry(t, dir)

	// git status outside a repo must fail loudly (no silent success).
	_, err := mustTool(t, r, "git").Run(context.Background(), map[string]any{"args": []any{"status"}})
	if err == nil {
		t.Fatal("git status must fail outside a repository")
	}

	// Init a repo, then status must work.
	if _, err := runCmd(dir, "git", "init"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	res, err := mustTool(t, r, "git").Run(context.Background(), map[string]any{"args": []any{"status"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "No commits yet") && !strings.Contains(res.Output, "On branch") {
		t.Fatalf("git status output: %q", res.Output)
	}
}

func runCmd(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestRegistryErrors(t *testing.T) {
	r := NewRegistry()
	nt := NewNative(t.TempDir())
	if err := nt.Register(r); err != nil {
		t.Fatal(err)
	}
	if err := nt.Register(r); err == nil {
		t.Fatal("duplicate registration must fail")
	}
	if _, ok := r.Get("does_not_exist"); ok {
		t.Fatal("unexpected tool")
	}
	if len(r.Names()) < 10 {
		t.Fatalf("expected >=10 native tools, got %d", len(r.Names()))
	}
	for _, s := range r.List() {
		if s.Name == "" || s.Description == "" || s.Risk == "" {
			t.Fatalf("incomplete spec: %+v", s)
		}
	}
}

func TestDecodeArgs(t *testing.T) {
	m, err := DecodeArgs(`{"path":"a.txt","n":3}`)
	if err != nil {
		t.Fatal(err)
	}
	if m["path"] != "a.txt" {
		t.Fatalf("%v", m)
	}
	if _, err := DecodeArgs("not json"); err == nil {
		t.Fatal("expected error")
	}
}
