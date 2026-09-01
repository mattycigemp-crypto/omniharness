package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"omniharness/internal/config"
	"omniharness/internal/task"
	"omniharness/internal/testutil"
)

// captureStdout runs fn with os.Stdout swapped for a pipe.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	w.Close()
	return <-done
}

// captureStderr swaps os.Stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	w.Close()
	return <-done
}

func TestDoctorAuthenticatedDoesNotLeakSecret(t *testing.T) {
	const key = "sk-doctor-secret-42"
	fake := testutil.NewFakeOmniRoute(t)
	fake.RequireAPIKey = key
	t.Setenv("OMNIHARNESS_DATA_DIR", t.TempDir())
	t.Setenv("OMNIROUTE_URL", fake.URL())
	t.Setenv("OMNIROUTE_API_KEY", key)

	root := NewRootCmd()
	root.SetArgs([]string{"doctor"})
	out := captureStdout(t, func() {
		if err := root.Execute(); err != nil {
			t.Fatalf("doctor failed: %v", err)
		}
	})
	if strings.Contains(out, key) {
		t.Fatalf("doctor leaked the API key:\n%s", out)
	}
	if !strings.Contains(out, "key_"+key[len(key)-4:]) {
		t.Fatalf("doctor should show the masked key id (key_<last4>):\n%s", out)
	}
	if !strings.Contains(out, "authenticated") {
		t.Fatalf("doctor should report authenticated state:\n%s", out)
	}
}

func TestDoctorReportsRejectedKey(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t)
	fake.RequireAPIKey = "sk-real-key"
	t.Setenv("OMNIHARNESS_DATA_DIR", t.TempDir())
	t.Setenv("OMNIROUTE_URL", fake.URL())
	t.Setenv("OMNIROUTE_API_KEY", "sk-wrong-key") // valid prefix, wrong value

	root := NewRootCmd()
	root.SetArgs([]string{"doctor"})
	out := captureStdout(t, func() {
		_ = root.Execute() // nonzero exit expected; we only assert output
	})
	if strings.Contains(out, "sk-wrong-key") || strings.Contains(out, "sk-real-key") {
		t.Fatalf("doctor leaked a credential:\n%s", out)
	}
	if !strings.Contains(out, "key rejected") {
		t.Fatalf("doctor should report key rejection:\n%s", out)
	}
}

func TestConfigShowRedactsKey(t *testing.T) {
	const key = "sk-config-show-secret"
	t.Setenv("OMNIROUTE_API_KEY", key)
	root := NewRootCmd()
	root.SetArgs([]string{"config", "show"})
	out := captureStdout(t, func() {
		if err := root.Execute(); err != nil {
			t.Fatalf("config show failed: %v", err)
		}
	})
	if strings.Contains(out, key) {
		t.Fatalf("config show leaked the API key:\n%s", out)
	}
	if !strings.Contains(out, "***") {
		t.Fatalf("config show should redact the key field:\n%s", out)
	}
}

func TestRunHeadlessEndToEnd(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Content: "all done"})
	dir := t.TempDir()
	testutil.InitFakeWorkspace(t, dir)
	t.Setenv("OMNIHARNESS_DATA_DIR", dir)
	t.Setenv("OMNIHARNESS_WORKSPACE", dir)
	t.Setenv("OMNIHARNESS_ENDPOINT", fake.URL())

	root := NewRootCmd()
	root.SetArgs([]string{"run", "Fix the typo in README.md.", "--headless", "--json"})
	stderr := captureStderr(t, func() {
		_ = root.Execute()
	})
	if !strings.Contains(stderr, "strategy: direct") {
		t.Fatalf("stderr missing strategy line: %s", stderr)
	}

	// Rerun with stdout captured for the JSON result.
	root2 := NewRootCmd()
	root2.SetArgs([]string{"run", "Fix the typo in README.md.", "--headless", "--json"})
	_ = captureStderr(t, func() {})
	stdout := captureStdout(t, func() {
		_ = root2.Execute()
	})
	var tsk task.Task
	if err := json.Unmarshal([]byte(stdout), &tsk); err != nil {
		t.Fatalf("stdout not task JSON: %v\n%s", err, stdout)
	}
	if tsk.Status != task.StatusCompleted {
		t.Fatalf("status = %s (%s)", tsk.Status, tsk.Error)
	}
	if !strings.Contains(tsk.Result.Output, "all done") {
		t.Fatalf("output = %q", tsk.Result.Output)
	}
}

func TestSessionsCommandListsCreatedSession(t *testing.T) {
	fake := testutil.NewFakeOmniRoute(t, testutil.FakeStep{Content: "ok"})
	dir := t.TempDir()
	t.Setenv("OMNIHARNESS_DATA_DIR", dir)
	t.Setenv("OMNIHARNESS_WORKSPACE", dir)
	t.Setenv("OMNIHARNESS_ENDPOINT", fake.URL())

	_ = captureStderr(t, func() {})
	root := NewRootCmd()
	root.SetArgs([]string{"run", "do a thing", "--headless"})
	_ = captureStdout(t, func() { _ = root.Execute() })

	root2 := NewRootCmd()
	root2.SetArgs([]string{"sessions"})
	stdout := captureStdout(t, func() { _ = root2.Execute() })
	if !strings.Contains(stdout, "do a thing") {
		t.Fatalf("sessions missing new session: %s", stdout)
	}
}

func TestDoctorReportsUnreachableEndpoint(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OMNIHARNESS_DATA_DIR", dir)
	t.Setenv("OMNIHARNESS_WORKSPACE", dir)
	t.Setenv("OMNIHARNESS_ENDPOINT", "http://127.0.0.1:1")

	root := NewRootCmd()
	root.SetArgs([]string{"doctor"})
	err := root.Execute()
	if err == nil {
		t.Fatal("doctor should fail when OmniRoute is unreachable")
	}
}

func TestNoPromptErrors(t *testing.T) {
	root := NewRootCmd()
	root.SetArgs([]string{"run"})
	root.SetOut(io.Discard)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "no prompt") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolvePromptSources(t *testing.T) {
	if p, err := resolvePrompt("", "", []string{"arg"}); err != nil || p != "arg" {
		t.Fatalf("%q %v", p, err)
	}
	if p, err := resolvePrompt("flag", "", nil); err != nil || p != "flag" {
		t.Fatalf("%q %v", p, err)
	}
	f := t.TempDir() + "/prompt.txt"
	if err := os.WriteFile(f, []byte("from file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p, err := resolvePrompt("", f, nil); err != nil || p != "from file" {
		t.Fatalf("%q %v", p, err)
	}
}

func TestConfigCommandWritesDefault(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/cfg.toml"
	root := NewRootCmd()
	root.SetArgs([]string{"config", "init", "--config", path})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OmniRoute.Endpoint == "" {
		t.Fatal("default config not loaded")
	}
}

// ensure cobra context works with SetArgs (silences unused-param linting).
var _ = context.Background
var _ = time.Second
var _ = cobra.Command{}
