package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/marmutapp/openmarmut/internal/runtime"
)

// ignoreFileNames lists the filenames to search for ignore patterns.
var ignoreFileNames = []string{
	".gitignore",
	".openmarmutignore",
}

// defaultIgnorePatterns are always included even without any ignore files.
var defaultIgnorePatterns = []string{
	".git/",
	"node_modules/",
	"__pycache__/",
	".openmarmut/sessions/",
	"*.pyc",
	".DS_Store",
}

// IgnoreList holds patterns of files the agent should not read or modify.
type IgnoreList struct {
	patterns []string
	sources  []patternSource // tracks which file each pattern came from
}

// patternSource associates a pattern with its origin.
type patternSource struct {
	Pattern string
	Source  string // "default", ".gitignore", ".openmarmutignore"
}

// LoadIgnoreList searches for .gitignore and .openmarmutignore in the target
// directory and parses them. Default patterns are always included first.
// Returns an IgnoreList with at least the default patterns.
func LoadIgnoreList(ctx context.Context, rt runtime.Runtime) *IgnoreList {
	il := &IgnoreList{}

	// Always start with default patterns.
	for _, p := range defaultIgnorePatterns {
		il.patterns = append(il.patterns, p)
		il.sources = append(il.sources, patternSource{Pattern: p, Source: "default"})
	}

	// Load patterns from each ignore file in order (.gitignore first, then .openmarmutignore).
	for _, name := range ignoreFileNames {
		data, err := rt.ReadFile(ctx, name)
		if err != nil {
			continue
		}
		patterns := parseIgnorePatterns(string(data))
		for _, p := range patterns {
			il.patterns = append(il.patterns, p)
			il.sources = append(il.sources, patternSource{Pattern: p, Source: name})
		}
	}

	return il
}

// LoadIgnoreListFromOS loads the ignore list using the OS filesystem.
func LoadIgnoreListFromOS(targetDir string) *IgnoreList {
	il := &IgnoreList{}

	// Always start with default patterns.
	for _, p := range defaultIgnorePatterns {
		il.patterns = append(il.patterns, p)
		il.sources = append(il.sources, patternSource{Pattern: p, Source: "default"})
	}

	for _, name := range ignoreFileNames {
		path := filepath.Join(targetDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		patterns := parseIgnorePatterns(string(data))
		for _, p := range patterns {
			il.patterns = append(il.patterns, p)
			il.sources = append(il.sources, patternSource{Pattern: p, Source: name})
		}
	}

	return il
}

// NewIgnoreListForTest creates an IgnoreList from a set of patterns (for testing).
func NewIgnoreListForTest(patterns []string) *IgnoreList {
	return &IgnoreList{patterns: patterns}
}

// parseIgnorePatterns extracts patterns from a gitignore-style file.
func parseIgnorePatterns(content string) []string {
	var patterns []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns
}

// ShouldIgnore checks if a file path matches any ignore pattern.
func (il *IgnoreList) ShouldIgnore(path string) bool {
	if il == nil || len(il.patterns) == 0 {
		return false
	}
	for _, pattern := range il.patterns {
		if matchIgnorePattern(pattern, path) {
			return true
		}
	}
	return false
}

// Patterns returns the loaded ignore patterns.
func (il *IgnoreList) Patterns() []string {
	if il == nil {
		return nil
	}
	return il.patterns
}

// Sources returns pattern source information for display.
func (il *IgnoreList) Sources() []patternSource {
	if il == nil {
		return nil
	}
	return il.sources
}

// Source returns the source file path (for backward compatibility, returns
// the last non-default source or empty).
func (il *IgnoreList) Source() string {
	if il == nil {
		return ""
	}
	for i := len(il.sources) - 1; i >= 0; i-- {
		if il.sources[i].Source != "default" {
			return il.sources[i].Source
		}
	}
	return ""
}

// DirPatterns returns patterns that match directories (ending with /).
// The trailing / is stripped from the returned values.
func (il *IgnoreList) DirPatterns() []string {
	if il == nil {
		return nil
	}
	var dirs []string
	for _, p := range il.patterns {
		if strings.HasSuffix(p, "/") {
			dirs = append(dirs, strings.TrimSuffix(p, "/"))
		}
	}
	return dirs
}

// FilePatterns returns patterns that match files (not ending with /).
func (il *IgnoreList) FilePatterns() []string {
	if il == nil {
		return nil
	}
	var files []string
	for _, p := range il.patterns {
		if !strings.HasSuffix(p, "/") && !strings.Contains(p, "/") {
			files = append(files, p)
		}
	}
	return files
}

// ShouldIgnoreEntry checks if a directory entry name matches any ignore pattern.
// For directories, the name is checked against both file and directory patterns.
func (il *IgnoreList) ShouldIgnoreEntry(name string, isDir bool) bool {
	if il == nil || len(il.patterns) == 0 {
		return false
	}
	for _, pattern := range il.patterns {
		dirOnly := strings.HasSuffix(pattern, "/")
		cleanPattern := strings.TrimSuffix(pattern, "/")

		// Skip path patterns (containing /) for entry-level matching.
		if strings.Contains(cleanPattern, "/") {
			continue
		}

		if dirOnly && !isDir {
			continue
		}

		if matchGlob(cleanPattern, name) {
			return true
		}
	}
	return false
}

// matchIgnorePattern checks if a path matches a gitignore-style pattern.
func matchIgnorePattern(pattern, path string) bool {
	// Directory pattern: ends with /
	dirOnly := false
	if strings.HasSuffix(pattern, "/") {
		dirOnly = true
		pattern = strings.TrimSuffix(pattern, "/")
	}

	// Exact file/directory name match (no path separator in pattern).
	if !strings.Contains(pattern, "/") {
		// Match against any path component.
		segments := strings.Split(path, "/")
		for i, seg := range segments {
			if matchGlob(pattern, seg) {
				if dirOnly && i == len(segments)-1 {
					// Pattern requires directory but this is the final component
					// — we don't know if it's a dir, so match anyway (conservative).
				}
				return true
			}
		}
		// Also try matching the full path.
		return matchGlob(pattern, filepath.Base(path))
	}

	// Path pattern: contains /
	// Match against the full relative path.
	pattern = strings.TrimPrefix(pattern, "/")

	if strings.Contains(pattern, "**") {
		return matchDoublestar(pattern, path)
	}

	matched, _ := filepath.Match(pattern, path)
	return matched
}

// FormatIgnorePrompt returns a system prompt section listing ignored paths.
func FormatIgnorePrompt(il *IgnoreList) string {
	if il == nil || len(il.patterns) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n## Ignored Paths (from .openmarmutignore)\n")
	sb.WriteString("Do NOT read, modify, or search within these paths:\n")
	for _, p := range il.patterns {
		sb.WriteString("- " + p + "\n")
	}
	return sb.String()
}

// FormatIgnoreDisplay returns a human-readable display of all ignore patterns
// grouped by source, for the /ignore command.
func FormatIgnoreDisplay(il *IgnoreList) string {
	if il == nil || len(il.patterns) == 0 {
		return "No ignore patterns loaded."
	}

	var sb strings.Builder
	sb.WriteString("Ignore patterns:\n")

	currentSource := ""
	for _, s := range il.sources {
		if s.Source != currentSource {
			currentSource = s.Source
			sb.WriteString(fmt.Sprintf("\n  [%s]\n", currentSource))
		}
		sb.WriteString(fmt.Sprintf("    %s\n", s.Pattern))
	}
	return sb.String()
}

// AddPatternToFile appends a pattern to the .openmarmutignore file.
func AddPatternToFile(ctx context.Context, rt runtime.Runtime, pattern string) error {
	content := ""
	data, err := rt.ReadFile(ctx, ".openmarmutignore")
	if err == nil {
		content = string(data)
	}

	// Ensure trailing newline before appending.
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += pattern + "\n"

	if err := rt.WriteFile(ctx, ".openmarmutignore", []byte(content), 0644); err != nil {
		return fmt.Errorf("AddPatternToFile(%s): %w", pattern, err)
	}
	return nil
}

// RemovePatternFromFile removes a pattern from the .openmarmutignore file.
func RemovePatternFromFile(ctx context.Context, rt runtime.Runtime, pattern string) error {
	data, err := rt.ReadFile(ctx, ".openmarmutignore")
	if err != nil {
		return fmt.Errorf("RemovePatternFromFile(%s): %w", pattern, err)
	}

	lines := strings.Split(string(data), "\n")
	var newLines []string
	found := false
	for _, line := range lines {
		if strings.TrimSpace(line) == pattern {
			found = true
			continue
		}
		newLines = append(newLines, line)
	}

	if !found {
		return fmt.Errorf("RemovePatternFromFile(%s): pattern not found in .openmarmutignore", pattern)
	}

	content := strings.Join(newLines, "\n")
	if err := rt.WriteFile(ctx, ".openmarmutignore", []byte(content), 0644); err != nil {
		return fmt.Errorf("RemovePatternFromFile(%s): %w", pattern, err)
	}
	return nil
}
