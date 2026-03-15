package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gajaai/openmarmut-go/internal/runtime"
)

// projectFileNames lists the filenames to search for project instructions,
// in priority order (first match wins per directory).
var projectFileNames = []string{
	"OPENMARMUT.md",
	"openmarmut.md",
	".openmarmut.md",
}

// importLinePattern matches lines like "@docs/architecture.md" (standalone @ references).
var importLinePattern = regexp.MustCompile(`(?m)^@([\w./_\-\\]+[\w./_\-\\]*)\s*$`)

const (
	maxProjectInstructionChars = 10000
	maxImportDepth             = 5
)

// ProjectInstructionsInfo holds loaded project instructions and metadata.
type ProjectInstructionsInfo struct {
	Content  string // Merged, import-resolved content.
	Source   string // Primary source file path (for display).
	Lines    int    // Total line count.
	Truncated bool  // True if content was truncated to fit limit.
}

// LoadProjectInstructions searches for OPENMARMUT.md files and returns merged
// project instructions. Search order: global (~/.openmarmut/OPENMARMUT.md) →
// ancestor directories (root first) → project target directory.
// Returns empty info (not an error) if no files are found.
func LoadProjectInstructions(ctx context.Context, rt runtime.Runtime) (*ProjectInstructionsInfo, error) {
	var sections []string
	var primarySource string

	// 1. Global: ~/.openmarmut/OPENMARMUT.md
	if home, err := os.UserHomeDir(); err == nil {
		globalPath := filepath.Join(home, ".openmarmut", "OPENMARMUT.md")
		if data, err := os.ReadFile(globalPath); err == nil {
			content := strings.TrimSpace(string(data))
			if content != "" {
				sections = append(sections, content)
				primarySource = globalPath
			}
		}
	}

	// 2. Ancestors: walk up from parent of target dir toward root.
	targetDir := rt.TargetDir()
	var ancestors []string
	dir := filepath.Dir(targetDir)
	for dir != targetDir { // Don't re-read target dir itself.
		if content := tryReadProjectFileOS(dir); content != "" {
			ancestors = append(ancestors, content)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // Reached filesystem root.
		}
		dir = parent
	}
	// Reverse so root-level comes first (most general → most specific).
	for i, j := 0, len(ancestors)-1; i < j; i, j = i+1, j-1 {
		ancestors[i], ancestors[j] = ancestors[j], ancestors[i]
	}
	sections = append(sections, ancestors...)

	// 3. Project: OPENMARMUT.md in target directory (via Runtime).
	if content, source := tryReadProjectFileRT(ctx, rt); content != "" {
		sections = append(sections, content)
		primarySource = source
	}

	if len(sections) == 0 {
		return &ProjectInstructionsInfo{}, nil
	}

	merged := strings.Join(sections, "\n\n")

	// Resolve @import references within the content.
	seen := make(map[string]bool)
	merged = resolveProjectImports(ctx, rt, merged, 0, seen)

	// Count lines before potential truncation.
	lines := strings.Count(merged, "\n") + 1

	// Cap total content.
	info := &ProjectInstructionsInfo{
		Content: merged,
		Source:  primarySource,
		Lines:   lines,
	}

	if len(merged) > maxProjectInstructionChars {
		info.Content = merged[:maxProjectInstructionChars] +
			"\n\n[WARNING: Project instructions truncated at 10,000 characters]"
		info.Truncated = true
	}

	return info, nil
}

// tryReadProjectFileOS tries to read a project instruction file from a
// directory using the OS filesystem directly (for ancestor/global loading).
func tryReadProjectFileOS(dir string) string {
	for _, name := range projectFileNames {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err == nil {
			content := strings.TrimSpace(string(data))
			if content != "" {
				return content
			}
		}
	}
	return ""
}

// tryReadProjectFileRT tries to read a project instruction file from the
// target directory using the Runtime (works in both local and Docker mode).
func tryReadProjectFileRT(ctx context.Context, rt runtime.Runtime) (content, source string) {
	for _, name := range projectFileNames {
		data, err := rt.ReadFile(ctx, name)
		if err == nil {
			content := strings.TrimSpace(string(data))
			if content != "" {
				return content, name
			}
		}
	}
	return "", ""
}

// resolveProjectImports replaces @path lines in content with the referenced
// file contents, up to maxImportDepth levels of recursion.
func resolveProjectImports(ctx context.Context, rt runtime.Runtime, content string, depth int, seen map[string]bool) string {
	if depth >= maxImportDepth {
		return content
	}

	matches := importLinePattern.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return content
	}

	var result strings.Builder
	lastEnd := 0

	for _, match := range matches {
		fullStart, fullEnd := match[0], match[1]
		importPath := content[match[2]:match[3]]

		result.WriteString(content[lastEnd:fullStart])
		lastEnd = fullEnd

		// Deduplicate.
		if seen[importPath] {
			result.WriteString(fmt.Sprintf("[already included: %s]\n", importPath))
			continue
		}
		seen[importPath] = true

		// Read the referenced file via Runtime.
		data, err := rt.ReadFile(ctx, importPath)
		if err != nil {
			result.WriteString(fmt.Sprintf("[import not found: %s]\n", importPath))
			continue
		}

		imported := strings.TrimSpace(string(data))
		if imported != "" {
			// Recursively resolve imports in the imported content.
			imported = resolveProjectImports(ctx, rt, imported, depth+1, seen)
			result.WriteString(fmt.Sprintf("<!-- from %s -->\n%s\n", importPath, imported))
		}
	}

	result.WriteString(content[lastEnd:])
	return result.String()
}

// FormatProjectInstructionsPrompt wraps project instructions for inclusion
// in the system prompt.
func FormatProjectInstructionsPrompt(content string) string {
	if content == "" {
		return ""
	}
	return "## Project Instructions (from OPENMARMUT.md)\n\n" + content + "\n\n"
}
