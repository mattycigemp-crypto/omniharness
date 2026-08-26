package mcp

import (
	"context"
	"fmt"
	"strings"

	"omniharness/internal/tools"
)

// ToolAdapter adapts an MCP tool to the tools.Tool interface so MCP tools
// flow through the same registry and policy engine as native tools.
type ToolAdapter struct {
	Client *Client
	Info   ToolInfo
}

// Spec returns the structured metadata. MCP tools run code in the server's
// context, so they are conservatively classified high risk.
func (a *ToolAdapter) Spec() tools.Spec {
	params := a.Info.InputSchema
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	risk := tools.RiskHigh
	return tools.Spec{
		Name:        ToolName(a.Client.server.Name, a.Info.Name),
		Description: a.Info.Description,
		Parameters:  params,
		Risk:        risk,
		ExecutesCode: true,
	}
}

// Run invokes the MCP tool.
func (a *ToolAdapter) Run(ctx context.Context, input map[string]any) (tools.Result, error) {
	result, err := a.Client.CallTool(ctx, a.Info.Name, input)
	if err != nil {
		return tools.Result{}, err
	}
	var b strings.Builder
	for _, c := range result.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
			b.WriteString("\n")
		}
	}
	output := strings.TrimSuffix(b.String(), "\n")
	if result.IsError {
		return tools.Result{Output: output}, fmt.Errorf("mcp tool %s failed: %s", a.Info.Name, output)
	}
	return tools.Result{Output: output}, nil
}
