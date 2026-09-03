package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"omniharness/internal/envguard"
	"omniharness/internal/memory"
)

// Native bundles the built-in filesystem, search, shell, git and process
// tools. WorkspaceRoot confines filesystem operations (defense in depth —
// policy still applies on top).
type Native struct {
	WorkspaceRoot string
	// DefaultShellTimeout bounds shell execution when the caller omits it.
	DefaultShellTimeout time.Duration
	// MaxOutput caps tool output sizes.
	MaxOutput int
	// Memory backs the "remember" tool. Nil omits that tool from
	// registration entirely, rather than registering one that always fails —
	// an agent should never see a tool it cannot possibly use.
	Memory *memory.ProjectMemories
}

// NewNative returns a Native with sane caps.
func NewNative(workspaceRoot string) *Native {
	return &Native{
		WorkspaceRoot:       workspaceRoot,
		DefaultShellTimeout: 60 * time.Second,
		MaxOutput:           64 << 10,
	}
}

func (n *Native) tool(name, desc string, risk Risk, params map[string]any, fn func(context.Context, map[string]any) (Result, error)) Tool {
	return &funcTool{
		spec: Spec{Name: name, Description: desc, Parameters: params, Risk: risk},
		fn:   fn,
	}
}

type funcTool struct {
	spec Spec
	fn   func(context.Context, map[string]any) (Result, error)
}

func (f *funcTool) Spec() Spec { return f.spec }
func (f *funcTool) Run(ctx context.Context, in map[string]any) (Result, error) {
	return f.fn(ctx, in)
}

// Register adds all native tools to the registry.
func (n *Native) Register(r *Registry) error {
	tools := []Tool{
		n.tool("read_file", "Read the contents of a text file. Use for inspecting source, configs and docs.", RiskLow, schema(map[string]any{
			"path": map[string]any{"type": "string", "description": "path to read"},
		}), n.readFile),
		n.tool("write_file", "Write content to a file, creating or overwriting it.", RiskMedium, schema(map[string]any{
			"path":    map[string]any{"type": "string", "description": "path to write"},
			"content": map[string]any{"type": "string", "description": "full file content"},
		}), n.writeFile),
		n.tool("edit_file", "Replace one exact substring in a file with new text.", RiskMedium, schema(map[string]any{
			"path":     map[string]any{"type": "string", "description": "path to edit"},
			"old_text": map[string]any{"type": "string", "description": "exact text to replace"},
			"new_text": map[string]any{"type": "string", "description": "replacement text"},
		}), n.editFile),
		n.tool("list_dir", "List entries in a directory.", RiskLow, schema(map[string]any{
			"path": map[string]any{"type": "string", "description": "directory to list"},
		}), n.listDir),
		n.tool("find_files", "Find files by glob pattern under a directory.", RiskLow, schema(map[string]any{
			"path":  map[string]any{"type": "string", "description": "root directory"},
			"glob":  map[string]any{"type": "string", "description": "glob pattern like **/*.go"},
			"limit": map[string]any{"type": "integer", "description": "max results"},
		}), n.findFiles),
		n.tool("search", "Regex search over file contents. Returns file:line matches.", RiskLow, schema(map[string]any{
			"pattern": map[string]any{"type": "string", "description": "regular expression"},
			"path":    map[string]any{"type": "string", "description": "root directory"},
			"limit":   map[string]any{"type": "integer", "description": "max matches"},
		}), n.search),
		n.tool("shell", "Execute a shell command. Use sparingly; prefer specific tools.", RiskHigh, schema(map[string]any{
			"command":     map[string]any{"type": "string", "description": "command to run"},
			"timeout_sec": map[string]any{"type": "integer", "description": "timeout in seconds (max 300)"},
		}), n.shell),
		n.tool("git", "Run a git operation in the workspace. Subcommands: status, diff, log, add, commit, push, checkout, stash.", RiskHigh, schema(map[string]any{
			"args": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "git arguments"},
		}), n.git),
		n.tool("process_list", "List running processes.", RiskLow, schema(map[string]any{}), n.processList),
		n.tool("process_kill", "Terminate a process by PID.", RiskHigh, schema(map[string]any{
			"pid": map[string]any{"type": "integer", "description": "process id"},
		}), n.processKill),
		n.tool("request_replan",
			"Call this when you discover the task needs real structure the current plan doesn't have — not a bug, a scope change: the request turned out to need several distinct pieces of work, or touches something the plan never accounted for. Do not call it for routine difficulty, a failing test you can fix yourself, or anything you can just finish. This does not do the extra work itself; it tells the harness to restructure execution around what you found, once this step finishes.",
			RiskLow, schema(map[string]any{
				"reason": map[string]any{"type": "string", "description": "specifically what you found that the current plan does not account for"},
			}), n.requestReplan),
	}
	if n.Memory != nil {
		tools = append(tools, n.tool("remember",
			"Save a durable note about this project — a convention, a gotcha, a decision — for future tasks in this workspace. Recalled automatically into every agent's instructions; use it sparingly, for things worth knowing next time, not routine progress notes. kind is a slot: remembering again with the same kind overwrites what was there, so use a specific kind per distinct fact (e.g. \"test-setup\", \"known-issue-flaky-ci\") rather than one generic kind for everything.",
			RiskLow, schema(map[string]any{
				"kind":    map[string]any{"type": "string", "description": "short, specific slot name, e.g. \"test-setup\" or \"known-issue-flaky-ci\" — reusing a kind replaces its old content"},
				"content": map[string]any{"type": "string", "description": "what to remember, in one or two sentences"},
			}), n.remember))
	}
	for _, t := range tools {
		if err := r.Register(t); err != nil {
			return err
		}
	}
	return nil
}

func (n *Native) requestReplan(ctx context.Context, in map[string]any) (Result, error) {
	reason, _ := in["reason"].(string)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Result{}, fmt.Errorf("request_replan requires a reason")
	}
	return Result{Output: "replan requested: " + reason, Replan: true}, nil
}

func (n *Native) remember(ctx context.Context, in map[string]any) (Result, error) {
	kind, _ := in["kind"].(string)
	content, _ := in["content"].(string)
	kind, content = strings.TrimSpace(kind), strings.TrimSpace(content)
	if kind == "" || content == "" {
		return Result{}, fmt.Errorf("remember requires both kind and content")
	}
	if err := n.Memory.Remember(n.WorkspaceRoot, kind, content); err != nil {
		return Result{}, err
	}
	return Result{Output: fmt.Sprintf("remembered (%s): %s", kind, content)}, nil
}

func schema(props map[string]any) map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
}

// resolvePath confines a path to the workspace root. Relative paths resolve
// against the workspace root (never the process cwd); absolute paths must
// stay inside the workspace. The check is lexical AND symlink-aware: an
// existing path whose real location (after following symlinks) leaves the
// root is rejected, so a symlink planted inside the workspace cannot escape
// to /etc or $HOME.
func (n *Native) resolvePath(input map[string]any) (string, error) {
	raw, _ := input["path"].(string)
	if raw == "" {
		raw = "."
	}
	if n.WorkspaceRoot == "" {
		abs, err := filepath.Abs(raw)
		if err != nil {
			return "", fmt.Errorf("resolve path: %w", err)
		}
		return abs, nil
	}
	root, err := filepath.Abs(n.WorkspaceRoot)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(raw) {
		raw = filepath.Join(root, raw)
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	if err := n.confine(root, abs); err != nil {
		return "", err
	}
	// Follow symlinks; the resolved location must also stay inside the root.
	// EvalSymlinks fails outright on a path that does not exist yet, so
	// resolving only the full path skipped this check for every file the agent
	// was about to create — a new file under a symlinked directory escaped.
	//
	// The root has to be resolved the same way, or the comparison is between
	// two different spellings of the same directory and every path looks like
	// an escape. That is not hypothetical: on macOS /var is a symlink to
	// /private/var, so a workspace anywhere under it failed every read and
	// write, and on Windows a root holding an 8.3 short name (RUNNER~1)
	// never matched its resolved long form.
	realRoot, err := resolveDeepest(root)
	if err != nil {
		realRoot = root
	}
	if real, err := resolveDeepest(abs); err == nil {
		if err := n.confine(realRoot, real); err != nil {
			return "", err
		}
	}
	return abs, nil
}

// resolveDeepest resolves symlinks as far down the path as actually exists,
// then rejoins the components that do not exist yet. For an existing path this
// is filepath.EvalSymlinks; for a path being created it is the resolved
// location of its nearest existing ancestor, which is what confinement has to
// judge.
func resolveDeepest(p string) (string, error) {
	var trailing []string
	current := p
	for {
		real, err := filepath.EvalSymlinks(current)
		if err == nil {
			return filepath.Join(append([]string{real}, trailing...)...), nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			// Reached the volume root without resolving anything.
			return "", err
		}
		trailing = append([]string{filepath.Base(current)}, trailing...)
		current = parent
	}
}

func (n *Native) confine(root, p string) error {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q is outside workspace root %q", p, root)
	}
	return nil
}

func (n *Native) readFile(ctx context.Context, in map[string]any) (Result, error) {
	p, err := n.resolvePath(in)
	if err != nil {
		return Result{}, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return Result{}, err
	}
	if len(b) > n.MaxOutput {
		b = append(b[:n.MaxOutput], []byte("\n...[truncated]")...)
	}
	return Result{Output: string(b)}, nil
}

func (n *Native) writeFile(ctx context.Context, in map[string]any) (Result, error) {
	p, err := n.resolvePath(in)
	if err != nil {
		return Result{}, err
	}
	content, err := StringArg(in, "content")
	if err != nil {
		return Result{}, err
	}
	if dir := filepath.Dir(p); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Result{}, err
		}
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return Result{}, err
	}
	return Result{Output: fmt.Sprintf("wrote %d bytes to %s", len(content), p), Artifact: true}, nil
}

func (n *Native) editFile(ctx context.Context, in map[string]any) (Result, error) {
	p, err := n.resolvePath(in)
	if err != nil {
		return Result{}, err
	}
	oldText, err := StringArg(in, "old_text")
	if err != nil {
		return Result{}, err
	}
	newText, err := StringArg(in, "new_text")
	if err != nil {
		return Result{}, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return Result{}, err
	}
	s := string(b)
	idx := strings.Index(s, oldText)
	if idx < 0 {
		return Result{}, fmt.Errorf("old_text not found in %s", p)
	}
	s = s[:idx] + newText + s[idx+len(oldText):]
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		return Result{}, err
	}
	return Result{Output: fmt.Sprintf("edited %s (%d -> %d bytes)", p, len(b), len(s)), Artifact: true}, nil
}

func (n *Native) listDir(ctx context.Context, in map[string]any) (Result, error) {
	p, err := n.resolvePath(in)
	if err != nil {
		return Result{}, err
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return Result{}, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var b strings.Builder
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		kind := "file"
		if e.IsDir() {
			kind = "dir"
		}
		fmt.Fprintf(&b, "%s\t%s\t%d\n", kind, e.Name(), info.Size())
	}
	return Result{Output: b.String()}, nil
}

func (n *Native) findFiles(ctx context.Context, in map[string]any) (Result, error) {
	root, err := n.resolvePath(in)
	if err != nil {
		return Result{}, err
	}
	glob, err := StringArg(in, "glob")
	if err != nil {
		return Result{}, err
	}
	limit := intArg(in, "limit", 100)
	var matches []string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		ok, err := filepath.Match(glob, filepath.Base(path))
		if err != nil {
			return nil
		}
		if !ok {
			ok, err = filepath.Match(glob, path)
			if err != nil {
				return nil
			}
		}
		if ok {
			matches = append(matches, path)
			if len(matches) >= limit {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil {
		return Result{}, walkErr
	}
	return Result{Output: strings.Join(matches, "\n")}, nil
}

func (n *Native) search(ctx context.Context, in map[string]any) (Result, error) {
	root, err := n.resolvePath(in)
	if err != nil {
		return Result{}, err
	}
	pattern, err := StringArg(in, "pattern")
	if err != nil {
		return Result{}, err
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return Result{}, fmt.Errorf("invalid regex: %w", err)
	}
	limit := intArg(in, "limit", 50)
	var b strings.Builder
	count := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if count >= limit {
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil || info.Size() > 4<<20 {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for i, line := range strings.Split(string(content), "\n") {
			if re.MatchString(line) {
				if len(line) > 300 {
					line = line[:300]
				}
				fmt.Fprintf(&b, "%s:%d:%s\n", path, i+1, line)
				count++
				if count >= limit {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	return Result{Output: strings.TrimSuffix(b.String(), "\n")}, nil
}

// shellWaitDelay bounds how long a killed command's output pipes are drained
// before they are force-closed. Long enough to collect the output of a command
// that exits promptly, short enough that a stuck grandchild cannot hold the
// caller open.
const shellWaitDelay = 2 * time.Second

func (n *Native) shell(ctx context.Context, in map[string]any) (Result, error) {
	command, err := StringArg(in, "command")
	if err != nil {
		return Result{}, err
	}
	timeout := n.DefaultShellTimeout
	if sec := intArg(in, "timeout_sec", 0); sec > 0 {
		timeout = time.Duration(sec) * time.Second
		if timeout > 300*time.Second {
			timeout = 300 * time.Second
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, shellName(), shellFlag(), command)
	cmd.Dir = n.WorkspaceRoot
	// Never leak the OmniRoute credential into the command's environment.
	cmd.Env = envguard.Filter()
	// CommandContext kills the shell at the deadline, but CombinedOutput goes on
	// waiting for the output pipe to close — and a grandchild that outlived the
	// shell still holds its write end. Without a bound, `sleep 30` with a 1s
	// timeout returns a timeout error 30 seconds late: the agent is blocked for
	// the full runaway command. WaitDelay caps that wait, so the timeout the
	// caller asked for is the timeout it gets.
	cmd.WaitDelay = shellWaitDelay
	out, err := cmd.CombinedOutput()
	if len(out) > n.MaxOutput {
		out = append(out[:n.MaxOutput], []byte("\n...[output truncated]")...)
	}
	if err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return Result{Output: string(out)}, fmt.Errorf("command timed out after %s", timeout)
		}
		return Result{Output: string(out)}, fmt.Errorf("command failed: %w", err)
	}
	return Result{Output: string(out)}, nil
}

func shellName() string {
	if runtime.GOOS == "windows" {
		return "cmd.exe"
	}
	return "/bin/sh"
}

func shellFlag() string {
	if runtime.GOOS == "windows" {
		return "/C"
	}
	return "-c"
}

// gitForbiddenArgs are the git options this tool must never pass through.
//
// They fall into two groups, and both defeat guarantees made elsewhere:
//
//   - Relocation. -C, --git-dir, --work-tree, --exec-path and --namespace
//     point git at a directory outside the workspace, so confinement stops
//     meaning anything.
//   - Execution. -c and --config-env set arbitrary config for the invocation,
//     and several config keys run commands: core.pager, core.sshCommand,
//     core.editor, and alias.<name>=!command. --upload-pack, --receive-pack
//     and --exec name a command to run for a transfer. Any of them turns this
//     tool into a shell, which matters because the shell tool is separately
//     gated and off by default: `git -c alias.x='!cmd' x` ran commands with
//     shell execution disabled.
//
// A denylist is not usually the right shape, but git's global options are a
// closed, documented set; the positional check in git() is what actually bounds
// them, and this is the specific, named backstop.
var gitForbiddenArgs = []string{
	"-C", "-c",
	"--git-dir", "--work-tree", "--exec-path", "--namespace",
	"--config-env", "--upload-pack", "--receive-pack", "--exec",
}

// checkGitArg rejects an argument that relocates git or makes it run a command.
func checkGitArg(arg string) error {
	for _, forbidden := range gitForbiddenArgs {
		if arg == forbidden || strings.HasPrefix(arg, forbidden+"=") {
			return fmt.Errorf("git arg %q is not allowed: it can relocate git or make it run a command", arg)
		}
	}
	return nil
}

func (n *Native) git(ctx context.Context, in map[string]any) (Result, error) {
	argsAny, ok := in["args"].([]any)
	if !ok {
		return Result{}, fmt.Errorf("git requires an args array")
	}
	args := make([]string, 0, len(argsAny))
	for _, a := range argsAny {
		s, ok := a.(string)
		if !ok {
			return Result{}, fmt.Errorf("git args must be strings")
		}
		if err := checkGitArg(s); err != nil {
			return Result{}, err
		}
		args = append(args, s)
	}
	if len(args) == 0 {
		return Result{}, fmt.Errorf("git requires a subcommand")
	}
	// The first argument must be the subcommand. Everything git accepts before
	// it is a global option, and that is where the escapes live — see
	// checkGitArg. Nothing this tool offers needs one.
	if strings.HasPrefix(args[0], "-") {
		return Result{}, fmt.Errorf("git arg %q is not allowed: pass the subcommand first, without global options", args[0])
	}
	runCtx, cancel := context.WithTimeout(ctx, n.DefaultShellTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "git", args...)
	cmd.Dir = n.WorkspaceRoot
	cmd.Env = envguard.Filter()
	out, err := cmd.CombinedOutput()
	if len(out) > n.MaxOutput {
		out = append(out[:n.MaxOutput], []byte("\n...[output truncated]")...)
	}
	if err != nil {
		return Result{Output: string(out)}, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return Result{Output: string(out)}, nil
}

func (n *Native) processList(ctx context.Context, in map[string]any) (Result, error) {
	cmd := exec.CommandContext(ctx, "tasklist")
	if runtime.GOOS != "windows" {
		cmd = exec.CommandContext(ctx, "ps", "aux")
	}
	cmd.Env = envguard.Filter()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return Result{}, err
	}
	return Result{Output: string(out)}, nil
}

func (n *Native) processKill(ctx context.Context, in map[string]any) (Result, error) {
	pid := intArg(in, "pid", 0)
	if pid <= 0 {
		return Result{}, fmt.Errorf("valid pid required")
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return Result{}, err
	}
	if err := proc.Kill(); err != nil {
		return Result{}, err
	}
	return Result{Output: fmt.Sprintf("killed pid %d", pid)}, nil
}

func intArg(in map[string]any, key string, def int) int {
	v, ok := in[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case float64:
		return int(n)
	case int64:
		return int(n)
	case int:
		return n
	}
	return def
}
