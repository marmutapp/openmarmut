package agent

import (
	"strings"
	"testing"

	"github.com/gajaai/openmarmut-go/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{"empty", "", 0},
		{"short", "hi", 1},
		{"4 chars = 1 token", "abcd", 1},
		{"5 chars = 2 tokens", "abcde", 2},
		{"100 chars", strings.Repeat("x", 100), 25},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, EstimateTokens(tt.input))
		})
	}
}

func TestEstimateMessagesTokens(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: strings.Repeat("x", 100)},   // 25 + 4 overhead
		{Role: llm.RoleUser, Content: strings.Repeat("y", 40)},      // 10 + 4 overhead
		{Role: llm.RoleAssistant, Content: strings.Repeat("z", 80)}, // 20 + 4 overhead
	}
	tokens := EstimateMessagesTokens(msgs)
	// 25+4 + 10+4 + 20+4 = 67
	assert.Equal(t, 67, tokens)
}

func TestEstimateMessagesTokens_WithToolCalls(t *testing.T) {
	msgs := []llm.Message{
		{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{
				{Name: "read_file", Arguments: `{"path":"main.go"}`},
			},
		},
	}
	tokens := EstimateMessagesTokens(msgs)
	// Name: "read_file" = 9/4 ~3, Args: 18/4 ~5, + 4 overhead = ~12
	assert.Greater(t, tokens, 0)
}

func TestTruncateHistory_BelowThreshold(t *testing.T) {
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "hi"},
		{Role: llm.RoleAssistant, Content: "hello"},
	}

	cfg := ContextConfig{ContextWindow: 1000, TruncationRatio: 0.80}
	result := TruncateHistory(history, cfg)

	assert.Equal(t, history, result, "should not truncate when below threshold")
}

func TestTruncateHistory_AboveThreshold(t *testing.T) {
	// Create a history that exceeds the threshold.
	longContent := strings.Repeat("x", 4000) // ~1000 tokens
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: "system prompt"},
		{Role: llm.RoleUser, Content: longContent},
		{Role: llm.RoleAssistant, Content: longContent},
		{Role: llm.RoleUser, Content: longContent},
		{Role: llm.RoleAssistant, Content: longContent},
		{Role: llm.RoleUser, Content: longContent},
		{Role: llm.RoleAssistant, Content: longContent},
		{Role: llm.RoleUser, Content: longContent},
		{Role: llm.RoleAssistant, Content: longContent},
		{Role: llm.RoleUser, Content: "recent question"},
		{Role: llm.RoleAssistant, Content: "recent answer"},
	}

	// Small context window to force truncation.
	cfg := ContextConfig{ContextWindow: 500, TruncationRatio: 0.80}
	result := TruncateHistory(history, cfg)

	// Should be shorter than original.
	require.Less(t, len(result), len(history))

	// System prompt preserved.
	assert.Equal(t, llm.RoleSystem, result[0].Role)
	assert.Equal(t, "system prompt", result[0].Content)

	// Summary message present.
	assert.Equal(t, llm.RoleUser, result[1].Role)
	assert.Contains(t, result[1].Content, "[Earlier conversation summary]")

	// Recent messages preserved.
	last := result[len(result)-1]
	assert.Equal(t, llm.RoleAssistant, last.Role)
	assert.Equal(t, "recent answer", last.Content)
}

func TestTruncateHistory_PreservesSystemPrompt(t *testing.T) {
	longContent := strings.Repeat("a", 8000)
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: "important system instructions"},
		{Role: llm.RoleUser, Content: longContent},
		{Role: llm.RoleAssistant, Content: longContent},
		{Role: llm.RoleUser, Content: longContent},
		{Role: llm.RoleAssistant, Content: longContent},
		{Role: llm.RoleUser, Content: "latest"},
		{Role: llm.RoleAssistant, Content: "response"},
	}

	cfg := ContextConfig{ContextWindow: 200, TruncationRatio: 0.80}
	result := TruncateHistory(history, cfg)

	assert.Equal(t, "important system instructions", result[0].Content)
}

func TestTruncateHistory_TooShortToTruncate(t *testing.T) {
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: strings.Repeat("x", 10000)},
		{Role: llm.RoleUser, Content: "hi"},
	}

	cfg := ContextConfig{ContextWindow: 10, TruncationRatio: 0.80}
	result := TruncateHistory(history, cfg)

	// Can't truncate a 2-message history.
	assert.Equal(t, history, result)
}

func TestTruncateHistory_DefaultValues(t *testing.T) {
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "hi"},
		{Role: llm.RoleAssistant, Content: "hello"},
	}

	// Zero values should use defaults.
	cfg := ContextConfig{}
	result := TruncateHistory(history, cfg)
	assert.Equal(t, history, result)
}

func TestTruncateHistory_WithToolMessages(t *testing.T) {
	longContent := strings.Repeat("x", 4000)
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		// Old turn with tool call.
		{Role: llm.RoleUser, Content: longContent},
		{Role: llm.RoleAssistant, Content: "", ToolCalls: []llm.ToolCall{{ID: "1", Name: "read_file", Arguments: `{"path":"x"}`}}},
		{Role: llm.RoleTool, ToolCallID: "1", Content: longContent},
		{Role: llm.RoleAssistant, Content: longContent},
		// More old turns.
		{Role: llm.RoleUser, Content: longContent},
		{Role: llm.RoleAssistant, Content: longContent},
		{Role: llm.RoleUser, Content: longContent},
		{Role: llm.RoleAssistant, Content: longContent},
		// Recent turns (need at least minKeepTurns=4 user messages).
		{Role: llm.RoleUser, Content: "q1"},
		{Role: llm.RoleAssistant, Content: "a1"},
		{Role: llm.RoleUser, Content: "q2"},
		{Role: llm.RoleAssistant, Content: "a2"},
		{Role: llm.RoleUser, Content: "q3"},
		{Role: llm.RoleAssistant, Content: "a3"},
		{Role: llm.RoleUser, Content: "recent"},
		{Role: llm.RoleAssistant, Content: "answer"},
	}

	cfg := ContextConfig{ContextWindow: 200, TruncationRatio: 0.80}
	result := TruncateHistory(history, cfg)

	require.Less(t, len(result), len(history))
	// System preserved.
	assert.Equal(t, llm.RoleSystem, result[0].Role)
	// Summary present.
	assert.Contains(t, result[1].Content, "[Earlier conversation summary]")
	// Summary mentions tool calls.
	assert.Contains(t, result[1].Content, "tool calls")
}

func TestSummarizeMessages(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: "first question"},
		{Role: llm.RoleAssistant, Content: "first answer"},
		{Role: llm.RoleUser, Content: "second question"},
		{Role: llm.RoleAssistant, Content: "", ToolCalls: []llm.ToolCall{{ID: "1", Name: "read_file"}}},
		{Role: llm.RoleTool, ToolCallID: "1", Content: "file contents"},
		{Role: llm.RoleAssistant, Content: "second answer after reading file"},
	}

	summary := summarizeMessages(msgs)
	assert.Contains(t, summary, "[Earlier conversation summary]")
	assert.Contains(t, summary, "2 user messages")
	assert.Contains(t, summary, "1 tool calls")
	assert.Contains(t, summary, "first answer")
}

func TestSummarizeMessages_LongSnippetsTruncated(t *testing.T) {
	longMsg := strings.Repeat("y", 500)
	msgs := []llm.Message{
		{Role: llm.RoleAssistant, Content: longMsg},
	}

	summary := summarizeMessages(msgs)
	assert.Contains(t, summary, "...")
	assert.Less(t, len(summary), 400)
}

func TestCountTailMessages(t *testing.T) {
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "q1"},
		{Role: llm.RoleAssistant, Content: "a1"},
		{Role: llm.RoleUser, Content: "q2"},
		{Role: llm.RoleAssistant, Content: "", ToolCalls: []llm.ToolCall{{ID: "1"}}},
		{Role: llm.RoleTool, ToolCallID: "1", Content: "result"},
		{Role: llm.RoleAssistant, Content: "a2"},
		{Role: llm.RoleUser, Content: "q3"},
		{Role: llm.RoleAssistant, Content: "a3"},
	}

	// Last 2 turns = q2(+tool+assistant) + q3+a3
	count := countTailMessages(history, 2)
	// Should include: q2, assistant(tool), tool, assistant, q3, a3 = 6
	assert.Equal(t, 6, count)

	// Last 1 turn
	count = countTailMessages(history, 1)
	// q3, a3 = 2
	assert.Equal(t, 2, count)
}
