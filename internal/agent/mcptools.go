package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gajaai/openmarmut-go/internal/llm"
	"github.com/gajaai/openmarmut-go/internal/mcp"
	"github.com/gajaai/openmarmut-go/internal/runtime"
)

// MCPToolsFromManager creates agent Tools from all connected MCP servers.
// Each tool is prefixed with "mcp_<server>_" to avoid name collisions.
// All MCP tools get PermConfirm by default.
func MCPToolsFromManager(mgr *mcp.Manager) []Tool {
	if mgr == nil {
		return nil
	}

	allTools := mgr.AllTools()
	var tools []Tool

	for prefixedName, entry := range allTools {
		// Capture for closure.
		client := entry.Client
		mcpTool := entry.Tool
		toolName := prefixedName

		// Convert MCP input schema to parameters map.
		var params any
		if mcpTool.InputSchema != nil {
			if err := json.Unmarshal(mcpTool.InputSchema, &params); err != nil {
				params = map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				}
			}
		} else {
			params = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
		}

		desc := mcpTool.Description
		if desc == "" {
			desc = fmt.Sprintf("MCP tool from %s server", client.Name)
		}

		tools = append(tools, Tool{
			Def: llm.ToolDef{
				Name:        toolName,
				Description: fmt.Sprintf("[MCP:%s] %s", client.Name, desc),
				Parameters:  params,
			},
			Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
				result, err := client.CallTool(ctx, mcpTool.Name, args)
				if err != nil {
					return "", fmt.Errorf("%s: %w", toolName, err)
				}
				return result, nil
			},
		})
	}

	return tools
}

// MCPToolPermissions returns permissions for all MCP tools (all set to confirm).
func MCPToolPermissions(mgr *mcp.Manager) map[string]PermissionLevel {
	if mgr == nil {
		return nil
	}

	perms := make(map[string]PermissionLevel)
	for name := range mgr.AllTools() {
		perms[name] = PermConfirm
	}
	return perms
}

// FormatMCPToolPreview creates a human-readable preview for an MCP tool call.
func FormatMCPToolPreview(tc llm.ToolCall) string {
	var b strings.Builder
	fmt.Fprintf(&b, "-> %s", tc.Name)

	// Try to show args.
	var args map[string]any
	if err := json.Unmarshal([]byte(tc.Arguments), &args); err == nil {
		for k, v := range args {
			s := fmt.Sprintf("%v", v)
			if len(s) > 80 {
				s = s[:80] + "..."
			}
			fmt.Fprintf(&b, "\n  %s: %s", k, s)
		}
	}

	return b.String()
}

// WithMCPManager sets the MCP manager on the agent, registering all MCP tools.
func WithMCPManager(mgr *mcp.Manager) Option {
	return func(a *Agent) {
		a.mcpManager = mgr
	}
}

// FormatMCPToolsPrompt returns a system prompt section listing MCP tools.
func FormatMCPToolsPrompt(mgr *mcp.Manager) string {
	if mgr == nil {
		return ""
	}
	allTools := mgr.AllTools()
	if len(allTools) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\nExternal MCP tools (require confirmation before each call):")
	for name, entry := range allTools {
		desc := entry.Tool.Description
		if len(desc) > 120 {
			desc = desc[:120] + "..."
		}
		fmt.Fprintf(&sb, "\n- %s: %s", name, desc)
	}
	return sb.String()
}
