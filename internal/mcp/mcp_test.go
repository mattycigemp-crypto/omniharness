package mcp

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// fakeServerScript is a minimal MCP server: it handles initialize, tools/list
// and tools/call over newline-delimited JSON-RPC.
const fakeServerScript = `
import json, sys
for line in sys.stdin:
    try:
        msg = json.loads(line)
    except Exception:
        continue
    if "id" not in msg:
        continue
    method = msg.get("method")
    if method == "initialize":
        result = {"protocolVersion": "2025-03-26", "capabilities": {"tools": {}}, "serverInfo": {"name": "fake", "version": "1.0"}}
    elif method == "tools/list":
        result = {"tools": [{"name": "echo", "description": "echoes text", "inputSchema": {"type": "object", "properties": {"text": {"type": "string"}}}}]}
    elif method == "tools/call":
        params = msg.get("params", {})
        name = params.get("name")
        args = params.get("arguments", {})
        if name == "echo":
            result = {"content": [{"type": "text", "text": "echo: " + args.get("text", "")}], "isError": False}
        elif name == "boom":
            result = {"content": [{"type": "text", "text": "exploded"}], "isError": True}
        else:
            result = {"content": [{"type": "text", "text": "unknown tool"}], "isError": True}
    else:
        result = {}
    sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": msg["id"], "result": result}) + "\n")
    sys.stdout.flush()
`

func pythonAvailable() bool {
	_, err := exec.LookPath("python")
	return err == nil
}

func startFake(t *testing.T) *Client {
	t.Helper()
	if !pythonAvailable() {
		t.Skip("python not available")
	}
	script := t.TempDir() + "/fake_mcp.py"
	if err := writeFile(script, fakeServerScript); err != nil {
		t.Fatal(err)
	}
	c := &Client{server: Server{Name: "fake", Command: "python", Args: []string{script}}}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

func TestServerCrashFailsCallCleanly(t *testing.T) {
	if !pythonAvailable() {
		t.Skip("python not available")
	}
	// A server that survives the handshake and crashes on the first tool call
	// — a crash mid-conversation. (Exiting right after initialize would race
	// the client's notifications/initialized write against the process death
	// and make Start nondeterministic.)
	crash := `
import json, sys
for line in sys.stdin:
    try:
        msg = json.loads(line)
    except Exception:
        continue
    if msg.get("method") == "initialize":
        sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": msg["id"], "result": {"protocolVersion": "2025-03-26", "capabilities": {}, "serverInfo": {"name": "crashy", "version": "1"}}}) + "\n")
        sys.stdout.flush()
    else:
        raise SystemExit(1)
`
	script := t.TempDir() + "/crash_mcp.py"
	if err := writeFile(script, crash); err != nil {
		t.Fatal(err)
	}
	c := &Client{server: Server{Name: "crashy", Command: "python", Args: []string{script}}}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The server died; the call must return a clean error, not hang or panic.
	if _, err := c.CallTool(ctx, "echo", map[string]any{}); err == nil {
		t.Fatal("call against a dead server must error")
	} else if ctx.Err() != nil {
		t.Fatal("error must come from the server death, not the timeout")
	}
}

func TestListToolsAndCall(t *testing.T) {
	c := startFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools %+v", tools)
	}

	res, err := c.CallTool(ctx, "echo", map[string]any{"text": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || len(res.Content) == 0 || res.Content[0].Text != "echo: hi" {
		t.Fatalf("result %+v", res)
	}
}

func TestCallToolError(t *testing.T) {
	c := startFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := c.CallTool(ctx, "boom", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected isError")
	}
}

func TestAdapter(t *testing.T) {
	c := startFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a := &ToolAdapter{Client: c, Info: tools[0]}
	spec := a.Spec()
	if spec.Name != "mcp:fake:echo" {
		t.Fatalf("name %q", spec.Name)
	}
	if spec.Risk != "high" {
		t.Fatalf("risk %q", spec.Risk)
	}
	res, err := a.Run(ctx, map[string]any{"text": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "echo: x" {
		t.Fatalf("output %q", res.Output)
	}
	server, tool, ok := ParseToolName("mcp:fake:echo")
	if !ok || server != "fake" || tool != "echo" {
		t.Fatalf("parse %q %q %v", server, tool, ok)
	}
}

func TestServerDeathFailsCalls(t *testing.T) {
	if !pythonAvailable() {
		t.Skip("python not available")
	}
	// A server that exits immediately.
	script := t.TempDir() + "/dead.py"
	if err := writeFile(script, "import sys\nsys.exit(1)\n"); err != nil {
		t.Fatal(err)
	}
	c := &Client{server: Server{Name: "dead", Command: "python", Args: []string{script}}}
	if err := c.Start(context.Background()); err == nil {
		t.Fatal("expected handshake failure for dead server")
	}
}

func TestToolNameRoundTrip(t *testing.T) {
	if ToolName("srv", "tool_x") != "mcp:srv:tool_x" {
		t.Fatal("ToolName mismatch")
	}
	if s, n, ok := ParseToolName("mcp:svr:some-tool"); !ok || s != "svr" || n != "some-tool" {
		t.Fatalf("%q %q %v", s, n, ok)
	}
	if _, _, ok := ParseToolName("native"); ok {
		t.Fatal("native names must not parse")
	}
	if !strings.HasPrefix(ToolName("a", "b"), "mcp:") {
		t.Fatal("prefix")
	}
}
