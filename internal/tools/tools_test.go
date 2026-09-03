package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"omniharness/internal/memory"
	"omniharness/internal/session"
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
	// Two classes must be refused. Relocation points git outside the
	// workspace; execution makes git run a command, which matters because the
	// shell tool is separately gated and off by default — `git -c
	// alias.x=!cmd x` ran commands with shell execution disabled.
	for _, arg := range []string{
		"-C", "--git-dir", "--git-dir=/elsewhere", "--work-tree=/elsewhere",
		"--exec-path=/elsewhere", "--namespace=x",
		"-c", "--config-env=alias.x=EVIL",
		"--upload-pack=evil", "--receive-pack=evil", "--exec=evil",
	} {
		if _, err := n.git(context.Background(), map[string]any{"args": []any{arg, "/etc", "status"}}); err == nil {
			t.Errorf("git arg %q must be rejected", arg)
		}
	}
	// A global option is refused wherever it appears, so the subcommand must
	// come first.
	if _, err := n.git(context.Background(), map[string]any{"args": []any{"--no-pager", "status"}}); err == nil {
		t.Error("a leading global option must be rejected: the subcommand comes first")
	}
	if _, err := n.git(context.Background(), map[string]any{"args": []any{}}); err == nil {
		t.Error("an empty args array must be rejected")
	}
}

// The git tool must not become a way to run arbitrary commands. git -c sets
// config for one invocation, and several keys execute: core.pager,
// core.sshCommand, and alias.<name>=!command. The shell tool is gated by
// ShellAllowed and off by default, so this was a way around that setting.
func TestGitCannotRunArbitraryCommands(t *testing.T) {
	workspace := t.TempDir()
	marker := filepath.Join(t.TempDir(), "marker.txt")
	payload := "!sh -c 'echo owned > " + marker + "'"
	if runtime.GOOS == "windows" {
		payload = "!echo owned > " + filepath.ToSlash(marker)
	}

	r := newTestRegistry(t, workspace) // shell not enabled
	if _, err := mustTool(t, r, "git").Run(context.Background(), map[string]any{
		"args": []any{"-c", "alias.pwn=" + payload, "pwn"},
	}); err == nil {
		t.Fatal("git must not accept -c")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("git ran a command with shell execution disabled")
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

// A new file under a symlinked directory must not escape the workspace.
// EvalSymlinks fails outright on a path that does not exist yet, so resolving
// only the full path skipped confinement for every file the agent was about to
// create — reads through a symlink were blocked, writes to new paths were not.
func TestWriteThroughSymlinkedDirIsConfined(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	// A repository can carry a symlink like this; the agent need not create it.
	if err := os.Symlink(outside, filepath.Join(workspace, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	r := newTestRegistry(t, workspace)
	if _, err := mustTool(t, r, "write_file").Run(context.Background(), map[string]any{
		"path": "escape/planted.txt", "content": "written outside",
	}); err == nil {
		t.Fatal("write through a symlinked directory must be rejected")
	}
	if _, err := os.Stat(filepath.Join(outside, "planted.txt")); err == nil {
		t.Fatal("a file was created outside the workspace")
	}

	// Confinement must not break ordinary writes, including new nested paths.
	if _, err := mustTool(t, r, "write_file").Run(context.Background(), map[string]any{
		"path": "sub/dir/new.txt", "content": "inside",
	}); err != nil {
		t.Fatalf("a normal nested write must still work: %v", err)
	}
}

// A workspace root that itself sits behind a symlink is the normal case on
// macOS — /var is a symlink to /private/var, so every os.MkdirTemp workspace
// is one — and it appears on Windows whenever a path holds an 8.3 short name.
// Resolving the path but not the root compared two spellings of the same
// directory and rejected every file operation in the workspace.
func TestWorkspaceRootBehindASymlinkStillWorks(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(real, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// The workspace is named through the symlink, which is what a shell or a
	// temp dir hands you.
	n := NewNative(link)

	if _, err := n.resolvePath(map[string]any{"path": "sub/new.txt"}); err != nil {
		t.Errorf("a new file inside the workspace was rejected: %v", err)
	}
	if _, err := n.resolvePath(map[string]any{"path": "."}); err != nil {
		t.Errorf("the workspace root itself was rejected: %v", err)
	}
	existing := filepath.Join(real, "sub", "here.txt")
	if err := os.WriteFile(existing, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := n.resolvePath(map[string]any{"path": "sub/here.txt"}); err != nil {
		t.Errorf("an existing file inside the workspace was rejected: %v", err)
	}

	// Confinement still holds: the fix must not have turned the check off.
	outside := filepath.Join(base, "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := n.resolvePath(map[string]any{"path": outside}); err == nil {
		t.Error("a path outside the workspace must still be refused")
	}
	if _, err := n.resolvePath(map[string]any{"path": "../outside.txt"}); err == nil {
		t.Error("traversal out of the workspace must still be refused")
	}
}

// request_replan needs no dependency (unlike remember), so it is always
// registered.
func TestRequestReplanToolIsAlwaysRegistered(t *testing.T) {
	r := newTestRegistry(t, t.TempDir())
	if _, ok := r.Get("request_replan"); !ok {
		t.Fatal(`"request_replan" was not registered`)
	}
}

func TestRequestReplanRequiresAReason(t *testing.T) {
	r := newTestRegistry(t, t.TempDir())
	tool := mustTool(t, r, "request_replan")
	if _, err := tool.Run(context.Background(), map[string]any{"reason": ""}); err == nil {
		t.Fatal("expected an error for an empty reason")
	}
	if _, err := tool.Run(context.Background(), map[string]any{}); err == nil {
		t.Fatal("expected an error for a missing reason")
	}
}

func TestRequestReplanMarksTheResult(t *testing.T) {
	r := newTestRegistry(t, t.TempDir())
	tool := mustTool(t, r, "request_replan")
	res, err := tool.Run(context.Background(), map[string]any{"reason": "found a second, unrelated bug"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Replan {
		t.Fatal("Replan = false, want true")
	}
	if !strings.Contains(res.Output, "found a second, unrelated bug") {
		t.Errorf("output = %q, want it to echo the reason", res.Output)
	}
}

// With no Memory configured, "remember" must not appear at all — an agent
// should never see a tool call that can only fail.
func TestRememberToolAbsentWithNoMemoryConfigured(t *testing.T) {
	r := newTestRegistry(t, t.TempDir())
	if _, ok := r.Get("remember"); ok {
		t.Fatal(`"remember" is registered despite no Memory being configured`)
	}
}

func TestRememberToolRoundTrips(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	n := NewNative(dir)
	n.Memory = memory.Project(store)
	r := NewRegistry()
	if err := n.Register(r); err != nil {
		t.Fatal(err)
	}

	tool := mustTool(t, r, "remember")
	res, err := tool.Run(context.Background(), map[string]any{
		"kind": "test-setup", "content": "run with -tags=integration",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "test-setup") {
		t.Errorf("output = %q, want it to confirm the kind", res.Output)
	}

	content, found, err := n.Memory.Recall(dir, "test-setup")
	if err != nil {
		t.Fatal(err)
	}
	if !found || content != "run with -tags=integration" {
		t.Fatalf("recall = %q, %v, want the remembered content", content, found)
	}
}

func TestRememberToolRequiresKindAndContent(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	n := NewNative(dir)
	n.Memory = memory.Project(store)
	r := NewRegistry()
	if err := n.Register(r); err != nil {
		t.Fatal(err)
	}
	tool := mustTool(t, r, "remember")

	for _, args := range []map[string]any{
		{"kind": "", "content": "something"},
		{"kind": "something", "content": ""},
		{},
	} {
		if _, err := tool.Run(context.Background(), args); err == nil {
			t.Errorf("args %+v: expected an error for a missing kind or content", args)
		}
	}
}

// Reusing a kind overwrites its previous content — the tool description
// says so explicitly, and this pins that the store actually behaves that
// way rather than silently keeping the first write.
func TestRememberToolReusingAKindOverwrites(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	n := NewNative(dir)
	n.Memory = memory.Project(store)
	r := NewRegistry()
	if err := n.Register(r); err != nil {
		t.Fatal(err)
	}
	tool := mustTool(t, r, "remember")

	if _, err := tool.Run(context.Background(), map[string]any{"kind": "note", "content": "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"kind": "note", "content": "second"}); err != nil {
		t.Fatal(err)
	}
	content, found, err := n.Memory.Recall(dir, "note")
	if err != nil {
		t.Fatal(err)
	}
	if !found || content != "second" {
		t.Fatalf("recall = %q, %v, want the second write to have replaced the first", content, found)
	}
}
