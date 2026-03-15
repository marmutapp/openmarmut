package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/marmutapp/openmarmut/internal/agent"
	"github.com/marmutapp/openmarmut/internal/llm"
	"github.com/marmutapp/openmarmut/internal/runtime"
)

// fileRefPattern matches @<path> patterns in user input.
// Matches @ followed by a non-whitespace path. Stops at common punctuation
// that wouldn't be part of a file path.
var fileRefPattern = regexp.MustCompile(`@([\w./_\-\\]+[\w./_\-\\]*)`)

// resolveFileRefs finds @<path> patterns in the input and replaces them with
// file contents (or directory listings) read from the runtime.
// Image files (.png, .jpg, .jpeg, .gif, .webp) are loaded as ImageContent
// and returned separately instead of being inlined as text.
// Returns the resolved string, loaded images, and any warnings for missing files.
func resolveFileRefs(ctx context.Context, input string, rt runtime.Runtime) (string, []llm.ImageContent, []string) {
	matches := fileRefPattern.FindAllStringSubmatchIndex(input, -1)
	if len(matches) == 0 {
		return input, nil, nil
	}

	var warnings []string
	var images []llm.ImageContent
	var result strings.Builder
	lastEnd := 0

	// Track seen paths to avoid duplicate reads.
	seen := make(map[string]bool)

	for _, match := range matches {
		// match[0:1] is the full match, match[2:3] is the captured path.
		fullStart, fullEnd := match[0], match[1]
		pathStr := input[match[2]:match[3]]

		result.WriteString(input[lastEnd:fullStart])
		lastEnd = fullEnd

		if seen[pathStr] {
			// Already resolved — keep the reference as-is for duplicate.
			result.WriteString("@" + pathStr)
			continue
		}
		seen[pathStr] = true

		// Check if this is an image file by extension.
		ext := strings.ToLower(filepath.Ext(pathStr))
		if agent.IsImageExtension(ext) {
			img, err := agent.LoadImage(ctx, rt, pathStr)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("@%s: %s", pathStr, err.Error()))
				result.WriteString("@" + pathStr)
			} else {
				images = append(images, *img)
				// Replace the @ref with a note that the image is attached.
				result.WriteString(fmt.Sprintf("[Image: %s]", pathStr))
			}
			continue
		}

		// Try reading as a file first.
		data, err := rt.ReadFile(ctx, pathStr)
		if err == nil {
			// File found — inject contents in a code block.
			lang := lookupLang(ext)
			result.WriteString(fmt.Sprintf("Contents of %s:\n```%s\n%s\n```", pathStr, lang, string(data)))
			continue
		}

		// Try as a directory.
		entries, dirErr := rt.ListDir(ctx, pathStr)
		if dirErr == nil {
			var listing strings.Builder
			listing.WriteString(fmt.Sprintf("Directory listing of %s:\n", pathStr))
			for _, e := range entries {
				if e.IsDir {
					listing.WriteString(fmt.Sprintf("  %s/\n", e.Name))
				} else {
					listing.WriteString(fmt.Sprintf("  %s\n", e.Name))
				}
			}
			result.WriteString(listing.String())
			continue
		}

		// Neither file nor directory — warn and keep reference.
		warnings = append(warnings, fmt.Sprintf("@%s: file not found", pathStr))
		result.WriteString("@" + pathStr)
	}

	result.WriteString(input[lastEnd:])
	return result.String(), images, warnings
}

// lookupLang returns the language identifier for a file extension,
// using the extToLang map from read.go with additional extensions.
func lookupLang(ext string) string {
	ext = strings.ToLower(ext)
	if lang, ok := extToLang[ext]; ok {
		return lang
	}
	// Additional extensions not in the shared map.
	switch ext {
	case ".tsx":
		return "tsx"
	case ".jsx":
		return "jsx"
	case ".rs":
		return "rust"
	case ".rb":
		return "ruby"
	case ".java":
		return "java"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".cxx", ".hpp":
		return "cpp"
	case ".toml":
		return "toml"
	case ".sql":
		return "sql"
	case ".html":
		return "html"
	case ".css":
		return "css"
	case ".xml":
		return "xml"
	default:
		return ""
	}
}
