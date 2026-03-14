package agent

import (
	"strings"

	"github.com/gajaai/openmarmut-go/internal/config"
	"github.com/gajaai/openmarmut-go/internal/llm"
)

const redactedPlaceholder = "[REDACTED]"

// ErrCredentialLeak is returned when a command contains credential values.
var ErrCredentialLeak = errCredentialLeak("agent: command blocked — contains credential value")

type errCredentialLeak string

func (e errCredentialLeak) Error() string { return string(e) }

// RedactCredentials replaces any occurrence of the given credential values
// in input with "[REDACTED]". Empty keys are skipped.
func RedactCredentials(input string, keys []string) string {
	if input == "" {
		return input
	}
	for _, k := range keys {
		if k == "" {
			continue
		}
		input = strings.ReplaceAll(input, k, redactedPlaceholder)
	}
	return input
}

// DetectCredentialLeak returns true if any credential value appears in command.
// Empty keys are skipped.
func DetectCredentialLeak(command string, keys []string) bool {
	if command == "" {
		return false
	}
	for _, k := range keys {
		if k == "" {
			continue
		}
		if strings.Contains(command, k) {
			return true
		}
	}
	return false
}

// CollectCredentials extracts all resolved API key values from the LLM config.
// It resolves env var references (e.g., "$OPENAI_API_KEY") to their actual values.
// Keys that fail to resolve or are empty are skipped.
func CollectCredentials(cfg config.LLMConfig) []string {
	seen := make(map[string]bool)
	var keys []string

	// Collect from provider entries.
	for _, p := range cfg.Providers {
		addKey(&keys, seen, p.APIKey)
	}

	// Collect the API key override if set.
	addKey(&keys, seen, cfg.APIKeyOverride)

	return keys
}

// addKey resolves a credential reference and appends the value if non-empty and not seen.
func addKey(keys *[]string, seen map[string]bool, ref string) {
	if ref == "" {
		return
	}
	val, err := llm.ResolveCredential(ref)
	if err != nil || val == "" {
		return
	}
	if seen[val] {
		return
	}
	seen[val] = true
	*keys = append(*keys, val)
}
