package agent

import (
	"fmt"
	"strings"

	"github.com/marmutapp/openmarmut/internal/llm"
)

const (
	// defaultContextWindow is the default model context window in tokens.
	defaultContextWindow = 128000
	// defaultTruncationRatio is the ratio of context window at which truncation triggers.
	defaultTruncationRatio = 0.80
	// minKeepTurns is the minimum number of recent user+assistant turn pairs to keep intact.
	minKeepTurns = 4
)

// EstimateTokens estimates the token count for a string using a simple
// chars/4 heuristic. Not perfect but good enough for context management.
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}

// EstimateMessagesTokens estimates total tokens across a slice of messages.
func EstimateMessagesTokens(msgs []llm.Message) int {
	total := 0
	for _, m := range msgs {
		total += EstimateTokens(m.Content)
		for _, tc := range m.ToolCalls {
			total += EstimateTokens(tc.Arguments)
			total += EstimateTokens(tc.Name)
		}
		// Per-message overhead for role, formatting.
		total += 4
	}
	return total
}

// ContextConfig controls context window management behavior.
type ContextConfig struct {
	ContextWindow    int     // Model's context window size in tokens.
	TruncationRatio  float64 // Fraction of window that triggers truncation (0.0–1.0).
	KeepRecentTurns  int     // Minimum recent turn pairs to preserve during truncation.
}

// DefaultContextConfig returns sensible defaults.
func DefaultContextConfig() ContextConfig {
	return ContextConfig{
		ContextWindow:   defaultContextWindow,
		TruncationRatio: defaultTruncationRatio,
		KeepRecentTurns: minKeepTurns,
	}
}

// TruncateHistory checks whether the message history exceeds the token budget
// and, if so, replaces older messages (between system prompt and the last N turns)
// with a summary message. Returns the (possibly truncated) history.
//
// The system prompt (first message) and the most recent minKeepTurns turn pairs
// are always preserved.
func TruncateHistory(history []llm.Message, cfg ContextConfig) []llm.Message {
	if cfg.ContextWindow <= 0 {
		cfg.ContextWindow = defaultContextWindow
	}
	if cfg.TruncationRatio <= 0 || cfg.TruncationRatio > 1 {
		cfg.TruncationRatio = defaultTruncationRatio
	}

	threshold := int(float64(cfg.ContextWindow) * cfg.TruncationRatio)
	tokens := EstimateMessagesTokens(history)

	if tokens <= threshold {
		return history
	}

	// Need at least system + 1 turn pair to truncate.
	if len(history) < 3 {
		return history
	}

	// Find split point: keep system prompt + last N turns.
	// Count turns from the end: a "turn" is user+assistant (possibly with tool messages between).
	keepTurns := cfg.KeepRecentTurns
	if keepTurns <= 0 {
		keepTurns = minKeepTurns
	}
	keepFromEnd := countTailMessages(history, keepTurns)
	splitIdx := len(history) - keepFromEnd

	// Must keep at least the system message.
	if splitIdx <= 1 {
		return history
	}

	// Summarize the middle section (indices 1..splitIdx-1).
	middle := history[1:splitIdx]
	summary := summarizeMessages(middle)

	result := make([]llm.Message, 0, 2+keepFromEnd)
	result = append(result, history[0]) // system prompt
	result = append(result, llm.Message{
		Role:    llm.RoleUser,
		Content: summary,
	})
	result = append(result, history[splitIdx:]...)

	return result
}

// countTailMessages counts how many messages from the end of history
// correspond to `turns` user-initiated turns (user + assistant + tool messages).
func countTailMessages(history []llm.Message, turns int) int {
	count := 0
	turnsSeen := 0
	for i := len(history) - 1; i >= 1; i-- { // skip system at [0]
		count++
		if history[i].Role == llm.RoleUser {
			turnsSeen++
			if turnsSeen >= turns {
				break
			}
		}
	}
	return count
}

// summarizeMessages produces a condensed summary of a sequence of messages.
func summarizeMessages(msgs []llm.Message) string {
	var b strings.Builder
	b.WriteString("[Earlier conversation summary]\n")

	toolCalls := 0
	userMsgs := 0
	assistantSnippets := make([]string, 0)

	for _, m := range msgs {
		switch m.Role {
		case llm.RoleUser:
			userMsgs++
		case llm.RoleAssistant:
			if m.Content != "" {
				snippet := m.Content
				if len(snippet) > 200 {
					snippet = snippet[:200] + "..."
				}
				assistantSnippets = append(assistantSnippets, snippet)
			}
			toolCalls += len(m.ToolCalls)
		case llm.RoleTool:
			// Already counted via ToolCalls.
		}
	}

	fmt.Fprintf(&b, "The conversation included %d user messages and %d tool calls.\n", userMsgs, toolCalls)

	if len(assistantSnippets) > 0 {
		b.WriteString("Key assistant responses:\n")
		limit := 5
		if len(assistantSnippets) < limit {
			limit = len(assistantSnippets)
		}
		for _, s := range assistantSnippets[:limit] {
			fmt.Fprintf(&b, "- %s\n", s)
		}
		if len(assistantSnippets) > 5 {
			fmt.Fprintf(&b, "- ...and %d more responses\n", len(assistantSnippets)-5)
		}
	}

	return b.String()
}

// ContextUsageInfo holds computed context window usage.
type ContextUsageInfo struct {
	EstimatedTokens int // Estimated tokens currently in history.
	ContextWindow   int // Total context window size.
	Percent         int // Usage as 0–100 percentage.
	Threshold       int // Truncation threshold in tokens.
	HistoryTurns    int // Number of user turns in history.
	SystemTokens    int // Estimated tokens in system prompt.
}

// ComputeContextUsage calculates context window usage from the current history.
func ComputeContextUsage(history []llm.Message, cfg ContextConfig) ContextUsageInfo {
	if cfg.ContextWindow <= 0 {
		cfg.ContextWindow = defaultContextWindow
	}
	if cfg.TruncationRatio <= 0 || cfg.TruncationRatio > 1 {
		cfg.TruncationRatio = defaultTruncationRatio
	}

	tokens := EstimateMessagesTokens(history)
	pct := 0
	if cfg.ContextWindow > 0 {
		pct = (tokens * 100) / cfg.ContextWindow
		if pct > 100 {
			pct = 100
		}
	}

	var sysTok int
	var turns int
	for _, m := range history {
		if m.Role == llm.RoleSystem {
			sysTok += EstimateTokens(m.Content) + 4
		}
		if m.Role == llm.RoleUser {
			turns++
		}
	}

	return ContextUsageInfo{
		EstimatedTokens: tokens,
		ContextWindow:   cfg.ContextWindow,
		Percent:         pct,
		Threshold:       int(float64(cfg.ContextWindow) * cfg.TruncationRatio),
		HistoryTurns:    turns,
		SystemTokens:    sysTok,
	}
}

// TruncateLargeToolResult truncates a tool result string if it exceeds
// maxTokens (estimated). Keeps the head and tail with a truncation marker.
// Returns the original string if it fits within the budget.
func TruncateLargeToolResult(output string, maxTokens int) string {
	if maxTokens <= 0 || EstimateTokens(output) <= maxTokens {
		return output
	}

	// Convert token budget to approximate char budget (4 chars per token).
	maxChars := maxTokens * 4
	if maxChars >= len(output) {
		return output
	}

	// Keep 60% from head, 20% from tail, 20% for marker.
	headChars := maxChars * 60 / 100
	tailChars := maxChars * 20 / 100

	origLen := len(output)

	marker := fmt.Sprintf("\n\n[output truncated to fit context window — %d of %d chars shown]\n\n",
		headChars+tailChars, origLen)

	return output[:headChars] + marker + output[origLen-tailChars:]
}
