// Package mcp implements a Model Context Protocol client over stdio.
// MCP is a first-class tool protocol in OmniHarness: MCP tools are registered
// into the same tool registry as native tools, so policy applies identically.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"omniharness/internal/envguard"
)

// Server describes an MCP server process to spawn.
type Server struct {
	Name    string   `json:"name" toml:"name"`
	Command string   `json:"command" toml:"command"`
	Args    []string `json:"args,omitempty" toml:"args,omitempty"`
	Env     []string `json:"env,omitempty" toml:"env,omitempty"` // KEY=VALUE entries
}

// ToolInfo is the metadata MCP returns for a tool.
type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Content is an MCP content block.
type Content struct {
	Type string `json:"type"` // text | image | resource
	Text string `json:"text,omitempty"`
}

// CallResult is the result of tools/call.
type CallResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError"`
}

// Client is a single MCP server connection.
type Client struct {
	server  Server
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	scanner *bufio.Scanner
	pending map[uint64]chan json.RawMessage
	mu      sync.Mutex
	nextID  uint64
	done    chan struct{}
}

// rpcRequest is a JSON-RPC 2.0 request.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcResponse is a JSON-RPC 2.0 response or notification.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *uint64         `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// NewClient creates a client for an MCP server definition.
func NewClient(srv Server) *Client {
	return &Client{server: srv}
}

// Start spawns the MCP server and performs the initialize handshake.
func (c *Client) Start(ctx context.Context) error {
	if c.server.Command == "" {
		return fmt.Errorf("mcp server %q has no command", c.server.Name)
	}
	cmd := exec.CommandContext(ctx, c.server.Command, c.server.Args...)
	// Inherit the parent environment (so PATH etc. work) minus credential
	// variables, plus the server's configured overrides. Without os.Environ()
	// the child would get an empty environment and could not find its own
	// executables; with it unfiltered, the OmniRoute key would leak to the
	// server process.
	cmd.Env = append(envguard.Filter(), c.server.Env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// Stderr is forwarded to the process's stderr so server errors are visible.
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start mcp server %q: %w", c.server.Name, err)
	}
	c.cmd = cmd
	c.stdin = stdin
	c.pending = map[uint64]chan json.RawMessage{}
	c.done = make(chan struct{})
	c.scanner = bufio.NewScanner(stdout)
	c.scanner.Buffer(make([]byte, 0, 1<<20), 8<<20)
	go c.readLoop()

	// Handshake: initialize → initialized notification.
	var initResult json.RawMessage
	if err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "omniharness", "version": "1.0"},
	}, &initResult); err != nil {
		c.Close()
		return fmt.Errorf("mcp initialize %q: %w", c.server.Name, err)
	}
	if err := c.notify("notifications/initialized", map[string]any{}); err != nil {
		c.Close()
		return err
	}
	return nil
}

func (c *Client) readLoop() {
	defer close(c.done)
	for c.scanner.Scan() {
		var msg rpcResponse
		if err := json.Unmarshal(c.scanner.Bytes(), &msg); err != nil {
			continue // ignore malformed lines
		}
		if msg.ID == nil {
			continue // notification
		}
		c.mu.Lock()
		ch := c.pending[*msg.ID]
		delete(c.pending, *msg.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- c.scanner.Bytes()
		}
	}
	// Reader ended: fail all pending calls.
	c.mu.Lock()
	for id, ch := range c.pending {
		delete(c.pending, id)
		close(ch)
	}
	c.mu.Unlock()
}

// call sends a request and waits for its response.
func (c *Client) call(ctx context.Context, method string, params any, out *json.RawMessage) error {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan json.RawMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	req := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := c.stdin.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("mcp write: %w", err)
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	case <-c.done:
		return fmt.Errorf("mcp server %q closed its stdout", c.server.Name)
	case raw, ok := <-ch:
		if !ok {
			return fmt.Errorf("mcp server %q closed connection", c.server.Name)
		}
		var resp rpcResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			return err
		}
		if resp.Error != nil {
			return fmt.Errorf("mcp %s: %s", method, resp.Error.Message)
		}
		*out = resp.Result
		return nil
	}
}

// notify sends a fire-and-forget notification.
func (c *Client) notify(method string, params any) error {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()
	_ = id
	b, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	_, err = c.stdin.Write(append(b, '\n'))
	return err
}

// ListTools returns the tools exposed by the server.
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	var result struct {
		Tools []ToolInfo `json:"tools"`
	}
	var raw json.RawMessage
	if err := c.call(ctx, "tools/list", map[string]any{}, &raw); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("decode tools/list: %w", err)
	}
	return result.Tools, nil
}

// CallTool invokes a tool on the server.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (CallResult, error) {
	var raw json.RawMessage
	if err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, &raw); err != nil {
		return CallResult{}, err
	}
	var out CallResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return CallResult{}, fmt.Errorf("decode tools/call: %w", err)
	}
	return out, nil
}

// Close terminates the server process.
func (c *Client) Close() error {
	if c.stdin != nil {
		c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		done := make(chan struct{})
		go func() {
			c.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			c.cmd.Process.Kill()
			<-done
		}
	}
	return nil
}

// ToolName prefixes a tool name with the server name for registry uniqueness.
func ToolName(server, tool string) string {
	return "mcp:" + server + ":" + tool
}

// ParseToolName reverses ToolName.
func ParseToolName(full string) (server, tool string, ok bool) {
	parts := strings.SplitN(full, ":", 3)
	if len(parts) != 3 || parts[0] != "mcp" {
		return "", "", false
	}
	return parts[1], parts[2], true
}
