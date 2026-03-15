package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/marmutapp/openmarmut/internal/llm"
	"github.com/marmutapp/openmarmut/internal/runtime"
)

// Rule represents a modular rule loaded from .openmarmut/rules/.
type Rule struct {
	Globs   []string // File patterns that activate this rule.
	Content string   // The rule text (after frontmatter).
	Source  string   // File path for debugging.
}

// LoadRules scans .openmarmut/rules/ for .md files and parses them into Rules.
// Returns empty slice (not an error) if the directory doesn't exist.
func LoadRules(ctx context.Context, rt runtime.Runtime) ([]Rule, error) {
	entries, err := rt.ListDir(ctx, ".openmarmut/rules")
	if err != nil {
		// Directory doesn't exist — that's fine.
		return nil, nil
	}

	var rules []Rule
	for _, e := range entries {
		if e.IsDir || !strings.HasSuffix(e.Name, ".md") {
			continue
		}
		relPath := filepath.Join(".openmarmut/rules", e.Name)
		data, readErr := rt.ReadFile(ctx, relPath)
		if readErr != nil {
			continue
		}

		rule, parseErr := parseRule(string(data), relPath)
		if parseErr != nil {
			continue
		}
		rules = append(rules, rule)
	}

	return rules, nil
}

// parseRule extracts frontmatter globs and content from a rule file.
func parseRule(content, source string) (Rule, error) {
	rule := Rule{Source: source}

	// Parse frontmatter.
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "---") {
		// No frontmatter — treat entire content as the rule, no globs (always active).
		rule.Content = content
		return rule, nil
	}

	// Find end of frontmatter.
	endIdx := strings.Index(content[3:], "---")
	if endIdx < 0 {
		return Rule{}, fmt.Errorf("rules.parseRule(%s): unterminated frontmatter", source)
	}
	frontmatter := content[3 : endIdx+3]
	rule.Content = strings.TrimSpace(content[endIdx+6:])

	// Parse globs from frontmatter — simple line-based parsing.
	// Supports: globs: ["pattern1", "pattern2"]
	// Or multi-line:
	//   globs:
	//     - "pattern1"
	//     - "pattern2"
	rule.Globs = parseGlobs(frontmatter)

	return rule, nil
}

// parseGlobs extracts glob patterns from frontmatter text.
func parseGlobs(frontmatter string) []string {
	var globs []string

	lines := strings.Split(frontmatter, "\n")
	inGlobs := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "globs:") {
			inGlobs = true
			// Check for inline array: globs: ["a", "b"]
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "globs:"))
			if strings.HasPrefix(rest, "[") {
				globs = append(globs, parseInlineArray(rest)...)
				inGlobs = false
			}
			continue
		}

		if inGlobs {
			if strings.HasPrefix(trimmed, "-") {
				// List item: - "pattern" or - pattern
				val := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
				val = strings.Trim(val, "\"'")
				if val != "" {
					globs = append(globs, val)
				}
			} else if trimmed != "" {
				// Hit a new key — stop.
				inGlobs = false
			}
		}
	}

	return globs
}

// parseInlineArray parses ["a", "b", "c"] into a slice.
func parseInlineArray(s string) []string {
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, "\"'")
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// MatchRules returns the combined content of all rules whose globs match any
// of the given file paths. Rules without globs are always included.
func MatchRules(rules []Rule, filePaths []string) string {
	if len(rules) == 0 {
		return ""
	}

	var matched []string
	seen := make(map[string]bool)

	for _, rule := range rules {
		if seen[rule.Source] {
			continue
		}

		if len(rule.Globs) == 0 {
			// No globs — always active.
			seen[rule.Source] = true
			matched = append(matched, rule.Content)
			continue
		}

		if matchesAny(rule.Globs, filePaths) {
			seen[rule.Source] = true
			matched = append(matched, rule.Content)
		}
	}

	if len(matched) == 0 {
		return ""
	}

	return strings.Join(matched, "\n\n")
}

// matchesAny checks if any file path matches any of the glob patterns.
func matchesAny(globs []string, filePaths []string) bool {
	for _, fp := range filePaths {
		for _, g := range globs {
			if matchGlob(g, fp) {
				return true
			}
		}
	}
	return false
}

// matchGlob matches a file path against a glob pattern.
// Supports ** for recursive directory matching.
func matchGlob(pattern, path string) bool {
	// Handle ** patterns by splitting into segments.
	if strings.Contains(pattern, "**") {
		return matchDoublestar(pattern, path)
	}
	matched, _ := filepath.Match(pattern, path)
	return matched
}

// matchDoublestar handles ** glob patterns.
func matchDoublestar(pattern, path string) bool {
	// Split pattern on **
	parts := strings.SplitN(pattern, "**", 2)
	if len(parts) != 2 {
		matched, _ := filepath.Match(pattern, path)
		return matched
	}

	prefix := parts[0]
	suffix := strings.TrimPrefix(parts[1], "/")

	// Check prefix.
	if prefix != "" {
		prefix = strings.TrimSuffix(prefix, "/")
		if !strings.HasPrefix(path, prefix+"/") && path != prefix {
			return false
		}
		// Strip prefix from path for suffix matching.
		path = strings.TrimPrefix(path, prefix+"/")
	}

	// If no suffix, ** matches everything.
	if suffix == "" {
		return true
	}

	// Try matching suffix against every possible subpath.
	segments := strings.Split(path, "/")
	for i := range segments {
		subpath := strings.Join(segments[i:], "/")
		matched, _ := filepath.Match(suffix, subpath)
		if matched {
			return true
		}
		// Also try matching just the filename.
		if i == len(segments)-1 {
			matched, _ = filepath.Match(suffix, segments[i])
			if matched {
				return true
			}
		}
	}

	return false
}

// ExtractRecentFilePaths extracts file paths from recent tool call arguments
// in the conversation history (last N messages).
func ExtractRecentFilePaths(history []llm.Message, lastN int) []string {
	var paths []string
	seen := make(map[string]bool)

	start := len(history) - lastN
	if start < 0 {
		start = 0
	}

	for _, msg := range history[start:] {
		for _, tc := range msg.ToolCalls {
			// Extract path from common tool call arguments.
			path := extractPathFromArgs(tc.Arguments)
			if path != "" && !seen[path] {
				seen[path] = true
				paths = append(paths, path)
			}
		}
	}

	return paths
}

// extractPathFromArgs pulls a "path" field from JSON tool call arguments.
func extractPathFromArgs(args string) string {
	// Simple extraction — look for "path":"value" pattern.
	idx := strings.Index(args, `"path"`)
	if idx < 0 {
		return ""
	}
	rest := args[idx+6:]
	// Skip : and whitespace.
	rest = strings.TrimLeft(rest, ": \t\n")
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	rest = rest[1:]
	endQuote := strings.Index(rest, `"`)
	if endQuote < 0 {
		return ""
	}
	return rest[:endQuote]
}

// FormatActiveRules wraps matched rule content for inclusion in the system prompt.
func FormatActiveRules(content string) string {
	if content == "" {
		return ""
	}
	return "\n\n## Active Rules\n\n" + content
}

// LoadRulesFromOS loads rules from .openmarmut/rules/ using the OS filesystem
// directly (fallback for when Runtime doesn't have the directory).
func LoadRulesFromOS(targetDir string) ([]Rule, error) {
	rulesDir := filepath.Join(targetDir, ".openmarmut", "rules")
	entries, err := os.ReadDir(rulesDir)
	if err != nil {
		return nil, nil // Directory doesn't exist — fine.
	}

	var rules []Rule
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(rulesDir, e.Name())
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		rule, parseErr := parseRule(string(data), filepath.Join(".openmarmut/rules", e.Name()))
		if parseErr != nil {
			continue
		}
		rules = append(rules, rule)
	}

	return rules, nil
}
