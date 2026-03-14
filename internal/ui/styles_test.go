package ui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatError(t *testing.T) {
	overrideTTY(false)
	defer overrideTTY(false)

	result := FormatError("something broke")
	assert.Equal(t, "✗ Error: something broke", result)
}

func TestFormatSuccess(t *testing.T) {
	overrideTTY(false)
	defer overrideTTY(false)

	result := FormatSuccess("done")
	assert.Equal(t, "✓ done", result)
}

func TestFormatWarning(t *testing.T) {
	overrideTTY(false)
	defer overrideTTY(false)

	result := FormatWarning("careful")
	assert.Equal(t, "⚠ careful", result)
}

func TestFormatToolCall(t *testing.T) {
	overrideTTY(false)
	defer overrideTTY(false)

	result := FormatToolCall("read_file", "main.go")
	assert.Equal(t, "  → read_file(main.go)", result)
}

func TestFormatToolCall_Styled(t *testing.T) {
	overrideTTY(true)
	defer overrideTTY(false)

	result := FormatToolCall("read_file", "main.go")
	assert.Contains(t, result, "read_file")
	assert.Contains(t, result, "main.go")
	assert.Contains(t, result, "→")
}

func TestFormatSummary_WithToolCalls(t *testing.T) {
	overrideTTY(false)
	defer overrideTTY(false)

	result := FormatSummary(3, 100, 50, "$0.01", 2300*time.Millisecond)
	assert.Contains(t, result, "3 tool calls")
	assert.Contains(t, result, "100 + 50 = 150 tokens")
	assert.Contains(t, result, "~$0.01")
	assert.Contains(t, result, "2.3s")
}

func TestFormatSummary_NoToolCalls(t *testing.T) {
	overrideTTY(false)
	defer overrideTTY(false)

	result := FormatSummary(0, 200, 100, "", 1*time.Second)
	assert.NotContains(t, result, "tool calls")
	assert.Contains(t, result, "200 + 100 = 300 tokens")
	assert.Contains(t, result, "1.0s")
	assert.NotContains(t, result, "~")
}

func TestFormatKeyValue(t *testing.T) {
	overrideTTY(false)
	defer overrideTTY(false)

	result := FormatKeyValue("Model", "gpt-4o")
	assert.Equal(t, "Model: gpt-4o", result)
}

func TestRenderBox_NoColor(t *testing.T) {
	overrideTTY(false)
	defer overrideTTY(false)

	result := RenderBox("Title", "body text")
	assert.Contains(t, result, "Title")
	assert.Contains(t, result, "body text")
}

func TestRenderBox_WithColor(t *testing.T) {
	overrideTTY(true)
	defer overrideTTY(false)

	result := RenderBox("Title", "body text")
	assert.Contains(t, result, "Title")
	assert.Contains(t, result, "body text")
}

func TestRenderTable(t *testing.T) {
	overrideTTY(false)
	defer overrideTTY(false)

	headers := []string{"NAME", "TYPE", "MODEL"}
	rows := [][]string{
		{"gpt", "openai", "gpt-4o"},
		{"claude", "anthropic", "claude-3.5"},
	}

	result := RenderTable(headers, rows, -1)
	assert.Contains(t, result, "NAME")
	assert.Contains(t, result, "gpt-4o")
	assert.Contains(t, result, "claude-3.5")
}

func TestRenderTable_ActiveRow(t *testing.T) {
	overrideTTY(true)
	defer overrideTTY(false)

	headers := []string{"NAME", "MODEL"}
	rows := [][]string{
		{"a", "model-a"},
		{"b", "model-b"},
	}

	result := RenderTable(headers, rows, 0)
	require.Contains(t, result, "model-a")
	require.Contains(t, result, "model-b")
}

func TestRenderTable_Empty(t *testing.T) {
	result := RenderTable(nil, nil, -1)
	assert.Equal(t, "", result)
}

func TestHumanizeBytes(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1572864, "1.5 MB"},
		{1073741824, "1.0 GB"},
		{1610612736, "1.5 GB"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, HumanizeBytes(tt.input))
		})
	}
}

func TestColorEnabled_RespectedByHelpers(t *testing.T) {
	// With color disabled, all Format* functions return plain text.
	overrideTTY(false)
	defer overrideTTY(false)

	// None of these should contain ANSI escape sequences.
	for _, s := range []string{
		FormatError("err"),
		FormatSuccess("ok"),
		FormatWarning("warn"),
		FormatToolCall("tool", "arg"),
		FormatSummary(1, 10, 5, "$0.01", time.Second),
		FormatKeyValue("k", "v"),
	} {
		assert.NotContains(t, s, "\033[", "should not contain ANSI escapes when color disabled")
	}
}

func TestColorEnabled_ProducesANSI(t *testing.T) {
	overrideTTY(true)
	defer overrideTTY(false)

	// With color enabled, styled output should contain ANSI sequences.
	result := FormatError("test")
	assert.Contains(t, result, "\033[", "should contain ANSI escapes when color enabled")
}

func TestRenderWelcomeBanner_NoColor(t *testing.T) {
	overrideTTY(false)
	defer overrideTTY(false)

	result := RenderWelcomeBanner("azure-codex", "gpt-5.1", "/tmp/project", "local")
	assert.Contains(t, result, "OpenMarmut")
	assert.Contains(t, result, "azure-codex")
	assert.Contains(t, result, "gpt-5.1")
	assert.Contains(t, result, "/tmp/project")
	assert.Contains(t, result, "local")
	assert.Contains(t, result, "/help")
}

func TestRenderWelcomeBanner_WithColor(t *testing.T) {
	overrideTTY(true)
	defer overrideTTY(false)

	result := RenderWelcomeBanner("claude", "opus", "/home/user", "docker")
	assert.Contains(t, result, "claude")
	assert.Contains(t, result, "opus")
	assert.Contains(t, result, "/home/user")
	assert.Contains(t, result, "docker")
}

func TestRenderConfirmBox_NoColor(t *testing.T) {
	overrideTTY(false)
	defer overrideTTY(false)

	result := RenderConfirmBox("→ write_file(main.go)")
	assert.Contains(t, result, "Permission Required")
	assert.Contains(t, result, "write_file")
	assert.Contains(t, result, "[y]es")
	assert.Contains(t, result, "[n]o")
	assert.Contains(t, result, "[a]lways")
}

func TestRenderConfirmBox_WithColor(t *testing.T) {
	overrideTTY(true)
	defer overrideTTY(false)

	result := RenderConfirmBox("→ execute_command\n  $ rm -rf /tmp/test")
	assert.Contains(t, result, "Permission Required")
	assert.Contains(t, result, "execute_command")
}

func TestRenderMarkdown_NoColor(t *testing.T) {
	overrideTTY(false)
	defer overrideTTY(false)

	result := RenderMarkdown("# Hello\n\nSome **bold** text")
	assert.Contains(t, result, "Hello")
	assert.Contains(t, result, "bold")
}

func TestRenderMarkdown_WithColor(t *testing.T) {
	overrideTTY(true)
	defer overrideTTY(false)

	result := RenderMarkdown("# Hello\n\nSome `code` here")
	assert.Contains(t, result, "Hello")
	assert.Contains(t, result, "code")
}

func TestFormatContextPercent(t *testing.T) {
	overrideTTY(false)
	defer overrideTTY(false)

	tests := []struct {
		pct      int
		expected string
	}{
		{0, "ctx: 0%"},
		{45, "ctx: 45%"},
		{65, "ctx: 65%"},
		{85, "ctx: 85%"},
		{100, "ctx: 100%"},
	}
	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, FormatContextPercent(tt.pct))
		})
	}
}

func TestFormatContextPercent_ColorCoded(t *testing.T) {
	overrideTTY(true)
	defer overrideTTY(false)

	// Green for low usage.
	low := FormatContextPercent(30)
	assert.Contains(t, low, "ctx: 30%")
	assert.Contains(t, low, "\033[") // has ANSI

	// Yellow for medium.
	mid := FormatContextPercent(65)
	assert.Contains(t, mid, "ctx: 65%")

	// Red for high.
	high := FormatContextPercent(85)
	assert.Contains(t, high, "ctx: 85%")
}

func TestRenderProgressBar(t *testing.T) {
	overrideTTY(false)
	defer overrideTTY(false)

	bar := RenderProgressBar(50, 20)
	assert.Contains(t, bar, "50%")
	// 50% of 20 = 10 filled blocks.
	assert.Contains(t, bar, "██████████")
	assert.Contains(t, bar, "░░░░░░░░░░")
}

func TestRenderProgressBar_Bounds(t *testing.T) {
	overrideTTY(false)
	defer overrideTTY(false)

	// 0% should be all empty.
	bar0 := RenderProgressBar(0, 10)
	assert.Contains(t, bar0, "░░░░░░░░░░")
	assert.Contains(t, bar0, "0%")

	// 100% should be all filled.
	bar100 := RenderProgressBar(100, 10)
	assert.Contains(t, bar100, "██████████")
	assert.Contains(t, bar100, "100%")
}

func TestFormatSummary_WithContextPct(t *testing.T) {
	overrideTTY(false)
	defer overrideTTY(false)

	result := FormatSummary(2, 100, 50, "$0.01", 2*time.Second, 14)
	assert.Contains(t, result, "ctx: 14%")
	assert.Contains(t, result, "2 tool calls")
}

func TestFormatSummary_WithoutContextPct(t *testing.T) {
	overrideTTY(false)
	defer overrideTTY(false)

	result := FormatSummary(0, 100, 50, "", time.Second)
	assert.NotContains(t, result, "ctx:")
}
