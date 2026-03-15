package agent

import (
	"encoding/json"
	"testing"

	"github.com/gajaai/openmarmut-go/internal/llm"
	"github.com/gajaai/openmarmut-go/internal/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPToolsFromManager_Nil(t *testing.T) {
	tools := MCPToolsFromManager(nil)
	assert.Nil(t, tools)
}

func TestMCPToolPermissions_Nil(t *testing.T) {
	perms := MCPToolPermissions(nil)
	assert.Nil(t, perms)
}

func TestFormatMCPToolsPrompt_Nil(t *testing.T) {
	prompt := FormatMCPToolsPrompt(nil)
	assert.Empty(t, prompt)
}

func TestFormatMCPToolsPrompt_Empty(t *testing.T) {
	mgr := mcp.NewManager()
	prompt := FormatMCPToolsPrompt(mgr)
	assert.Empty(t, prompt)
}

func TestFormatMCPToolPreview_Basic(t *testing.T) {
	tc := llm.ToolCall{
		ID:        "tc1",
		Name:      "mcp_github_create_issue",
		Arguments: `{"title":"bug report","body":"found a bug"}`,
	}
	preview := FormatMCPToolPreview(tc)
	assert.Contains(t, preview, "mcp_github_create_issue")
	assert.Contains(t, preview, "title")
	assert.Contains(t, preview, "bug report")
}

func TestFormatMCPToolPreview_InvalidJSON(t *testing.T) {
	tc := llm.ToolCall{
		ID:        "tc1",
		Name:      "mcp_test_tool",
		Arguments: `invalid json`,
	}
	preview := FormatMCPToolPreview(tc)
	assert.Contains(t, preview, "mcp_test_tool")
}

func TestFormatMCPToolPreview_LongValue(t *testing.T) {
	longVal := ""
	for i := 0; i < 200; i++ {
		longVal += "x"
	}
	tc := llm.ToolCall{
		ID:        "tc1",
		Name:      "mcp_test_tool",
		Arguments: `{"content":"` + longVal + `"}`,
	}
	preview := FormatMCPToolPreview(tc)
	assert.Contains(t, preview, "...")
}

func TestMCPToolPermissions_AllConfirm(t *testing.T) {
	// Create a mock manager and manually inject a client with tools.
	// Since we can't easily create a connected manager in unit tests,
	// we test the function's behavior with an empty manager.
	mgr := mcp.NewManager()
	perms := MCPToolPermissions(mgr)
	// Empty manager should return empty perms.
	assert.Empty(t, perms)
}

func TestMCPTool_InputSchema_Parsing(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)

	tool := mcp.MCPTool{
		Name:        "search",
		Description: "Search for things",
		InputSchema: schema,
	}

	var params any
	err := json.Unmarshal(tool.InputSchema, &params)
	require.NoError(t, err)

	paramsMap, ok := params.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "object", paramsMap["type"])
}
