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
