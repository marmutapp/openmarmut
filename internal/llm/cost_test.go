package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEstimateCost_KnownModel(t *testing.T) {
	usage := Usage{PromptTokens: 1000, CompletionTokens: 500}
	cost, ok := EstimateCost(usage, "gpt-4o")

	assert.True(t, ok)
	// 1000/1M * 2.50 + 500/1M * 10.00 = 0.0025 + 0.005 = 0.0075
	assert.InDelta(t, 0.0075, cost, 0.0001)
}

func TestEstimateCost_UnknownModel(t *testing.T) {
	usage := Usage{PromptTokens: 1000, CompletionTokens: 500}
	cost, ok := EstimateCost(usage, "unknown-model-xyz")

	assert.False(t, ok)
	assert.Equal(t, 0.0, cost)
}

func TestEstimateCost_ZeroUsage(t *testing.T) {
	cost, ok := EstimateCost(Usage{}, "gpt-4o")

	assert.True(t, ok)
	assert.Equal(t, 0.0, cost)
}

func TestEstimateCost_PrefixMatch(t *testing.T) {
	// "gpt-4o-2024-08-06" should match "gpt-4o" prefix.
	usage := Usage{PromptTokens: 1_000_000, CompletionTokens: 0}
	cost, ok := EstimateCost(usage, "gpt-4o-2024-08-06")

	assert.True(t, ok)
	assert.InDelta(t, 2.50, cost, 0.01)
}

func TestEstimateCost_LongestPrefixWins(t *testing.T) {
	// "gpt-4o-mini-2024" should match "gpt-4o-mini" not "gpt-4o".
	usage := Usage{PromptTokens: 1_000_000, CompletionTokens: 0}
	cost, ok := EstimateCost(usage, "gpt-4o-mini-2024")

	assert.True(t, ok)
	// gpt-4o-mini prompt = 0.15 per 1M.
	assert.InDelta(t, 0.15, cost, 0.01)
}

func TestEstimateCost_Anthropic(t *testing.T) {
	usage := Usage{PromptTokens: 10_000, CompletionTokens: 2_000}
	cost, ok := EstimateCost(usage, "claude-sonnet-4-20260514")

	assert.True(t, ok)
	// 10000/1M * 3.00 + 2000/1M * 15.00 = 0.03 + 0.03 = 0.06
	assert.InDelta(t, 0.06, cost, 0.001)
}

func TestEstimateCost_Gemini(t *testing.T) {
	usage := Usage{PromptTokens: 100_000, CompletionTokens: 10_000}
	cost, ok := EstimateCost(usage, "gemini-2.5-pro")

	assert.True(t, ok)
	// 100000/1M * 1.25 + 10000/1M * 10.00 = 0.125 + 0.1 = 0.225
	assert.InDelta(t, 0.225, cost, 0.001)
}

func TestFormatCost_SmallCost(t *testing.T) {
	usage := Usage{PromptTokens: 100, CompletionTokens: 50}
	s := FormatCost(usage, "gpt-4o")
	assert.Equal(t, "$0.0008", s)
}

func TestFormatCost_LargerCost(t *testing.T) {
	usage := Usage{PromptTokens: 1_000_000, CompletionTokens: 500_000}
	s := FormatCost(usage, "gpt-4")
	// 1M/1M * 30 + 0.5M/1M * 60 = 30 + 30 = 60
	assert.Equal(t, "$60.00", s)
}

func TestFormatCost_UnknownModel(t *testing.T) {
	usage := Usage{PromptTokens: 1000, CompletionTokens: 500}
	s := FormatCost(usage, "unknown-model")
	assert.Equal(t, "", s)
}

func TestLookupPricing_ExactMatch(t *testing.T) {
	p, ok := lookupPricing("gpt-4o")
	assert.True(t, ok)
	assert.Equal(t, 2.50, p.PromptCostPer1M)
}

func TestLookupPricing_NoMatch(t *testing.T) {
	_, ok := lookupPricing("llama-3")
	assert.False(t, ok)
}
