package llm

import "fmt"

// ModelPricing holds per-token costs in USD.
type ModelPricing struct {
	PromptCostPer1M     float64 // Cost per 1M prompt tokens.
	CompletionCostPer1M float64 // Cost per 1M completion tokens.
}

// modelPrices maps model name prefixes to pricing.
// Prices are approximate and updated periodically.
var modelPrices = map[string]ModelPricing{
	// OpenAI
	"gpt-4o":            {PromptCostPer1M: 2.50, CompletionCostPer1M: 10.00},
	"gpt-4o-mini":       {PromptCostPer1M: 0.15, CompletionCostPer1M: 0.60},
	"gpt-4-turbo":       {PromptCostPer1M: 10.00, CompletionCostPer1M: 30.00},
	"gpt-4":             {PromptCostPer1M: 30.00, CompletionCostPer1M: 60.00},
	"gpt-3.5-turbo":     {PromptCostPer1M: 0.50, CompletionCostPer1M: 1.50},
	"gpt-5.1-codex":     {PromptCostPer1M: 2.50, CompletionCostPer1M: 10.00},
	"o3":                {PromptCostPer1M: 10.00, CompletionCostPer1M: 40.00},
	"o3-mini":            {PromptCostPer1M: 1.10, CompletionCostPer1M: 4.40},
	"o4-mini":            {PromptCostPer1M: 1.10, CompletionCostPer1M: 4.40},

	// Anthropic
	"claude-opus-4":     {PromptCostPer1M: 15.00, CompletionCostPer1M: 75.00},
	"claude-sonnet-4":   {PromptCostPer1M: 3.00, CompletionCostPer1M: 15.00},
	"claude-3.5-sonnet": {PromptCostPer1M: 3.00, CompletionCostPer1M: 15.00},
	"claude-3-haiku":    {PromptCostPer1M: 0.25, CompletionCostPer1M: 1.25},
	"claude-3-opus":     {PromptCostPer1M: 15.00, CompletionCostPer1M: 75.00},

	// Google Gemini
	"gemini-2.5-pro":   {PromptCostPer1M: 1.25, CompletionCostPer1M: 10.00},
	"gemini-2.5-flash": {PromptCostPer1M: 0.15, CompletionCostPer1M: 0.60},
	"gemini-2.0-flash": {PromptCostPer1M: 0.10, CompletionCostPer1M: 0.40},
	"gemini-1.5-pro":   {PromptCostPer1M: 1.25, CompletionCostPer1M: 5.00},
	"gemini-1.5-flash": {PromptCostPer1M: 0.075, CompletionCostPer1M: 0.30},
}

// EstimateCost calculates the estimated cost in USD for the given usage and model.
// Returns the cost and true if the model is found, or 0 and false otherwise.
func EstimateCost(usage Usage, model string) (float64, bool) {
	pricing, ok := lookupPricing(model)
	if !ok {
		return 0, false
	}

	promptCost := float64(usage.PromptTokens) / 1_000_000 * pricing.PromptCostPer1M
	completionCost := float64(usage.CompletionTokens) / 1_000_000 * pricing.CompletionCostPer1M
	return promptCost + completionCost, true
}

// FormatCost returns a human-readable cost string (e.g., "$0.0023").
// Returns empty string if the model is unknown.
func FormatCost(usage Usage, model string) string {
	cost, ok := EstimateCost(usage, model)
	if !ok {
		return ""
	}
	if cost < 0.01 {
		return fmt.Sprintf("$%.4f", cost)
	}
	return fmt.Sprintf("$%.2f", cost)
}

// lookupPricing finds pricing for a model, trying exact match first,
// then prefix match (longest prefix wins).
func lookupPricing(model string) (ModelPricing, bool) {
	// Exact match.
	if p, ok := modelPrices[model]; ok {
		return p, true
	}

	// Prefix match — find longest matching prefix.
	var best ModelPricing
	bestLen := 0
	for prefix, p := range modelPrices {
		if len(prefix) > bestLen && len(model) >= len(prefix) && model[:len(prefix)] == prefix {
			best = p
			bestLen = len(prefix)
		}
	}

	if bestLen > 0 {
		return best, true
	}
	return ModelPricing{}, false
}
