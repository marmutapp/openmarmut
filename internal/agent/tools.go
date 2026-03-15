package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/marmutapp/openmarmut/internal/llm"
	"github.com/marmutapp/openmarmut/internal/runtime"
)

// Tool defines a callable action backed by a Runtime method.
type Tool struct {
	Def     llm.ToolDef
	Execute func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error)
}

// DefaultTools returns the standard set of tools backed by a Runtime.
// An optional IgnoreList filters results for grep_files, find_files, and list_dir.
func DefaultTools(il ...*IgnoreList) []Tool {
	var ignoreList *IgnoreList
	if len(il) > 0 {
		ignoreList = il[0]
	}
	return []Tool{
		readFileTool(),
		readFileLinesTool(),
		writeFileTool(),
		patchFileTool(),
		deleteFileTool(),
		listDirTool(ignoreList),
		mkdirTool(),
		execTool(),
		grepFilesTool(ignoreList),
		findFilesTool(ignoreList),
		gitStatusTool(),
		gitDiffTool(),
		gitDiffStagedTool(),
		gitLogTool(),
		gitCommitTool(),
		gitBranchTool(),
		gitCheckoutTool(),
	}
}

// ToolDefs returns just the llm.ToolDef slice for passing to Provider.Complete.
func ToolDefs(tools []Tool) []llm.ToolDef {
	defs := make([]llm.ToolDef, len(tools))
	for i, t := range tools {
		defs[i] = t.Def
	}
	return defs
}

func readFileTool() Tool {
	return Tool{
		Def: llm.ToolDef{
			Name:        "read_file",
			Description: "Read the contents of a file relative to the project root.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path relative to project root",
					},
				},
				"required": []string{"path"},
			},
		},
		Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
			var p struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("read_file: %w", err)
			}
			data, err := rt.ReadFile(ctx, p.Path)
			if err != nil {
				return "", err
			}
			const maxSize = 100 * 1024
			if len(data) > maxSize {
				return string(data[:maxSize]) + "\n\n[truncated — file exceeds 100KB]", nil
			}
			return string(data), nil
		},
	}
}

func writeFileTool() Tool {
	return Tool{
		Def: llm.ToolDef{
			Name:        "write_file",
			Description: "Write content to a file relative to the project root. Creates parent directories as needed. Overwrites existing files.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path relative to project root",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "Complete file content to write",
					},
				},
				"required": []string{"path", "content"},
			},
		},
		Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
			var p struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("write_file: %w", err)
			}
			if err := rt.WriteFile(ctx, p.Path, []byte(p.Content), 0644); err != nil {
				return "", err
			}
			return fmt.Sprintf("wrote %d bytes to %s", len(p.Content), p.Path), nil
		},
	}
}

func deleteFileTool() Tool {
	return Tool{
		Def: llm.ToolDef{
			Name:        "delete_file",
			Description: "Delete a file relative to the project root.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path relative to project root",
					},
				},
				"required": []string{"path"},
			},
		},
		Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
			var p struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("delete_file: %w", err)
			}
			if err := rt.DeleteFile(ctx, p.Path); err != nil {
				return "", err
			}
			return fmt.Sprintf("deleted %s", p.Path), nil
		},
	}
}

func listDirTool(il *IgnoreList) Tool {
	return Tool{
		Def: llm.ToolDef{
			Name:        "list_dir",
			Description: "List entries in a directory relative to the project root. Use '.' for the root directory.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Directory path relative to project root",
					},
				},
				"required": []string{"path"},
			},
		},
		Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
			var p struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("list_dir: %w", err)
			}
			entries, err := rt.ListDir(ctx, p.Path)
			if err != nil {
				return "", err
			}
			type entry struct {
				Name  string `json:"name"`
				IsDir bool   `json:"is_dir"`
				Size  int64  `json:"size"`
			}

			var out []entry
			hidden := 0
			for _, e := range entries {
				if il != nil && il.ShouldIgnoreEntry(e.Name, e.IsDir) {
					hidden++
					continue
				}
				out = append(out, entry{Name: e.Name, IsDir: e.IsDir, Size: e.Size})
			}
			if out == nil {
				out = []entry{}
			}
			b, _ := json.Marshal(out)
			result := string(b)
			if hidden > 0 {
				result += fmt.Sprintf("\n[+%d hidden by .openmarmutignore]", hidden)
			}
			return result, nil
		},
	}
}

func mkdirTool() Tool {
	return Tool{
		Def: llm.ToolDef{
			Name:        "mkdir",
			Description: "Create a directory (and parents) relative to the project root.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Directory path to create",
					},
				},
				"required": []string{"path"},
			},
		},
		Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
			var p struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("mkdir: %w", err)
			}
			if err := rt.MkDir(ctx, p.Path, os.FileMode(0755)); err != nil {
				return "", err
			}
			return fmt.Sprintf("created directory %s", p.Path), nil
		},
	}
}

func readFileLinesTool() Tool {
	return Tool{
		Def: llm.ToolDef{
			Name:        "read_file_lines",
			Description: "Read a range of lines from a file. Useful for large files where reading the whole thing wastes tokens.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path relative to project root",
					},
					"start_line": map[string]any{
						"type":        "integer",
						"description": "First line to read (1-based, inclusive)",
					},
					"end_line": map[string]any{
						"type":        "integer",
						"description": "Last line to read (1-based, inclusive)",
					},
				},
				"required": []string{"path", "start_line", "end_line"},
			},
		},
		Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
			var p struct {
				Path      string `json:"path"`
				StartLine int    `json:"start_line"`
				EndLine   int    `json:"end_line"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("read_file_lines: %w", err)
			}
			if p.StartLine < 1 {
				p.StartLine = 1
			}
			if p.EndLine < p.StartLine {
				return "", fmt.Errorf("read_file_lines: end_line (%d) must be >= start_line (%d)", p.EndLine, p.StartLine)
			}

			data, err := rt.ReadFile(ctx, p.Path)
			if err != nil {
				return "", err
			}

			lines := bytes.Split(data, []byte("\n"))
			// Clamp to actual line count.
			if p.StartLine > len(lines) {
				return "", fmt.Errorf("read_file_lines: start_line %d exceeds file length (%d lines)", p.StartLine, len(lines))
			}
			if p.EndLine > len(lines) {
				p.EndLine = len(lines)
			}

			// 1-based to 0-based indexing.
			selected := lines[p.StartLine-1 : p.EndLine]
			var buf strings.Builder
			for i, line := range selected {
				fmt.Fprintf(&buf, "%d\t%s", p.StartLine+i, string(line))
				if i < len(selected)-1 {
					buf.WriteByte('\n')
				}
			}
			return buf.String(), nil
		},
	}
}

func patchFileTool() Tool {
	return Tool{
		Def: llm.ToolDef{
			Name:        "patch_file",
			Description: "Apply surgical text replacements to a file without rewriting the whole thing. Each edit replaces one occurrence of old_text with new_text, applied in order.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path relative to project root",
					},
					"edits": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"old_text": map[string]any{
									"type":        "string",
									"description": "Exact text to find (must match uniquely)",
								},
								"new_text": map[string]any{
									"type":        "string",
									"description": "Replacement text",
								},
							},
							"required": []string{"old_text", "new_text"},
						},
						"description": "List of {old_text, new_text} replacements to apply in order",
					},
				},
				"required": []string{"path", "edits"},
			},
		},
		Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
			var p struct {
				Path  string `json:"path"`
				Edits []struct {
					OldText string `json:"old_text"`
					NewText string `json:"new_text"`
				} `json:"edits"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("patch_file: %w", err)
			}
			if len(p.Edits) == 0 {
				return "", fmt.Errorf("patch_file: no edits provided")
			}

			data, err := rt.ReadFile(ctx, p.Path)
			if err != nil {
				return "", err
			}

			content := string(data)
			for i, edit := range p.Edits {
				count := strings.Count(content, edit.OldText)
				if count == 0 {
					return "", fmt.Errorf("patch_file: edit %d: old_text not found in file", i+1)
				}
				if count > 1 {
					return "", fmt.Errorf("patch_file: edit %d: old_text matches %d times (must be unique)", i+1, count)
				}
				content = strings.Replace(content, edit.OldText, edit.NewText, 1)
			}

			if err := rt.WriteFile(ctx, p.Path, []byte(content), 0644); err != nil {
				return "", err
			}
			return fmt.Sprintf("applied %d edit(s) to %s", len(p.Edits), p.Path), nil
		},
	}
}

func grepFilesTool(il *IgnoreList) Tool {
	return Tool{
		Def: llm.ToolDef{
			Name:        "grep_files",
			Description: "Search for a regex pattern across files in a directory. Returns matching lines in file:line:content format.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "Regex pattern to search for",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Directory to search in, relative to project root (default: '.')",
					},
					"include_glob": map[string]any{
						"type":        "string",
						"description": "File pattern to include (e.g., '*.go', '*.ts')",
					},
					"max_results": map[string]any{
						"type":        "integer",
						"description": "Maximum number of matching lines to return (default: 50)",
					},
				},
				"required": []string{"pattern"},
			},
		},
		Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
			var p struct {
				Pattern     string `json:"pattern"`
				Path        string `json:"path"`
				IncludeGlob string `json:"include_glob"`
				MaxResults  int    `json:"max_results"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("grep_files: %w", err)
			}
			if p.Path == "" {
				p.Path = "."
			}
			if p.MaxResults <= 0 {
				p.MaxResults = 50
			}

			// Build grep command.
			cmd := fmt.Sprintf("grep -rn -E %s", shellQuote(p.Pattern))
			if p.IncludeGlob != "" {
				cmd += fmt.Sprintf(" --include=%s", shellQuote(p.IncludeGlob))
			}
			// Add exclusions from ignore list.
			if il != nil {
				for _, dir := range il.DirPatterns() {
					cmd += fmt.Sprintf(" --exclude-dir=%s", shellQuote(dir))
				}
				for _, fp := range il.FilePatterns() {
					cmd += fmt.Sprintf(" --exclude=%s", shellQuote(fp))
				}
			}
			cmd += " " + shellQuote(p.Path)
			cmd += fmt.Sprintf(" | head -n %d", p.MaxResults)

			result, err := rt.Exec(ctx, cmd, runtime.ExecOpts{})
			if err != nil {
				return "", err
			}
			// grep returns exit code 1 for no matches — not an error.
			if result.ExitCode == 1 && result.Stdout == "" {
				return "no matches found", nil
			}
			if result.ExitCode > 1 {
				return "", fmt.Errorf("grep_files: grep error: %s", result.Stderr)
			}
			output := strings.TrimRight(result.Stdout, "\n")
			lines := strings.Split(output, "\n")
			if len(lines) >= p.MaxResults {
				output += fmt.Sprintf("\n\n[results limited to %d matches]", p.MaxResults)
			}
			return output, nil
		},
	}
}

func findFilesTool(il *IgnoreList) Tool {
	return Tool{
		Def: llm.ToolDef{
			Name:        "find_files",
			Description: "Find files by name pattern in a directory. Returns a list of matching file paths.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "File name pattern (e.g., '*.go', 'test_*.py', 'Makefile')",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Directory to search in, relative to project root (default: '.')",
					},
				},
				"required": []string{"pattern"},
			},
		},
		Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
			var p struct {
				Pattern string `json:"pattern"`
				Path    string `json:"path"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("find_files: %w", err)
			}
			if p.Path == "" {
				p.Path = "."
			}

			cmd := fmt.Sprintf("find %s -name %s -type f",
				shellQuote(p.Path), shellQuote(p.Pattern))

			// Add exclusions from ignore list.
			if il != nil {
				for _, dir := range il.DirPatterns() {
					cmd += fmt.Sprintf(" -not -path '*/%s/*'", dir)
				}
				for _, fp := range il.FilePatterns() {
					cmd += fmt.Sprintf(" -not -name %s", shellQuote(fp))
				}
			}

			cmd += " 2>/dev/null | head -n 100"

			result, err := rt.Exec(ctx, cmd, runtime.ExecOpts{})
			if err != nil {
				return "", err
			}
			output := strings.TrimRight(result.Stdout, "\n")
			if output == "" {
				return "no files found", nil
			}
			return output, nil
		},
	}
}

// shellQuote wraps a string in single quotes for safe shell interpolation.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// --- Git Tools ---

func gitStatusTool() Tool {
	return Tool{
		Def: llm.ToolDef{
			Name:        "git_status",
			Description: "Show the working tree status (modified, staged, untracked files).",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
			result, err := rt.Exec(ctx, "git status --short", runtime.ExecOpts{})
			if err != nil {
				return "", err
			}
			if result.ExitCode != 0 {
				return "", fmt.Errorf("git_status: %s", result.Stderr)
			}
			output := strings.TrimRight(result.Stdout, "\n")
			if output == "" {
				return "working tree clean", nil
			}
			return output, nil
		},
	}
}

func gitDiffTool() Tool {
	return Tool{
		Def: llm.ToolDef{
			Name:        "git_diff",
			Description: "Show unstaged changes. Optionally for a specific file.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path to diff (optional, omit for all changes)",
					},
				},
			},
		},
		Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
			var p struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("git_diff: %w", err)
			}

			cmd := "git diff"
			if p.Path != "" {
				cmd += " -- " + shellQuote(p.Path)
			}

			result, err := rt.Exec(ctx, cmd, runtime.ExecOpts{})
			if err != nil {
				return "", err
			}
			if result.ExitCode != 0 {
				return "", fmt.Errorf("git_diff: %s", result.Stderr)
			}
			output := strings.TrimRight(result.Stdout, "\n")
			if output == "" {
				return "no unstaged changes", nil
			}
			return output, nil
		},
	}
}

func gitDiffStagedTool() Tool {
	return Tool{
		Def: llm.ToolDef{
			Name:        "git_diff_staged",
			Description: "Show staged (cached) changes that will be included in the next commit.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
			result, err := rt.Exec(ctx, "git diff --cached", runtime.ExecOpts{})
			if err != nil {
				return "", err
			}
			if result.ExitCode != 0 {
				return "", fmt.Errorf("git_diff_staged: %s", result.Stderr)
			}
			output := strings.TrimRight(result.Stdout, "\n")
			if output == "" {
				return "no staged changes", nil
			}
			return output, nil
		},
	}
}

func gitLogTool() Tool {
	return Tool{
		Def: llm.ToolDef{
			Name:        "git_log",
			Description: "Show recent commit history with hash, author, date, and message.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"n": map[string]any{
						"type":        "integer",
						"description": "Number of commits to show (default: 10)",
					},
				},
			},
		},
		Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
			var p struct {
				N int `json:"n"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("git_log: %w", err)
			}
			if p.N <= 0 {
				p.N = 10
			}
			if p.N > 50 {
				p.N = 50
			}

			cmd := fmt.Sprintf("git log --oneline --no-decorate -n %d", p.N)
			result, err := rt.Exec(ctx, cmd, runtime.ExecOpts{})
			if err != nil {
				return "", err
			}
			if result.ExitCode != 0 {
				return "", fmt.Errorf("git_log: %s", result.Stderr)
			}
			output := strings.TrimRight(result.Stdout, "\n")
			if output == "" {
				return "no commits found", nil
			}
			return output, nil
		},
	}
}

func gitCommitTool() Tool {
	return Tool{
		Def: llm.ToolDef{
			Name:        "git_commit",
			Description: "Stage all changes and create a git commit with the given message.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{
						"type":        "string",
						"description": "Commit message",
					},
				},
				"required": []string{"message"},
			},
		},
		Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
			var p struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("git_commit: %w", err)
			}
			if p.Message == "" {
				return "", fmt.Errorf("git_commit: message is required")
			}

			// Stage all changes.
			addResult, err := rt.Exec(ctx, "git add -A", runtime.ExecOpts{})
			if err != nil {
				return "", err
			}
			if addResult.ExitCode != 0 {
				return "", fmt.Errorf("git_commit: git add failed: %s", addResult.Stderr)
			}

			// Commit.
			cmd := fmt.Sprintf("git commit -m %s", shellQuote(p.Message))
			result, err := rt.Exec(ctx, cmd, runtime.ExecOpts{})
			if err != nil {
				return "", err
			}
			if result.ExitCode != 0 {
				return "", fmt.Errorf("git_commit: %s", result.Stderr)
			}
			return strings.TrimRight(result.Stdout, "\n"), nil
		},
	}
}

func gitBranchTool() Tool {
	return Tool{
		Def: llm.ToolDef{
			Name:        "git_branch",
			Description: "List branches or create a new branch. Omit name to list existing branches.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "Name of new branch to create (omit to list branches)",
					},
				},
			},
		},
		Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
			var p struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("git_branch: %w", err)
			}

			var cmd string
			if p.Name == "" {
				cmd = "git branch -a"
			} else {
				cmd = fmt.Sprintf("git branch %s", shellQuote(p.Name))
			}

			result, err := rt.Exec(ctx, cmd, runtime.ExecOpts{})
			if err != nil {
				return "", err
			}
			if result.ExitCode != 0 {
				return "", fmt.Errorf("git_branch: %s", result.Stderr)
			}
			output := strings.TrimRight(result.Stdout, "\n")
			if output == "" && p.Name != "" {
				return fmt.Sprintf("created branch %s", p.Name), nil
			}
			return output, nil
		},
	}
}

func gitCheckoutTool() Tool {
	return Tool{
		Def: llm.ToolDef{
			Name:        "git_checkout",
			Description: "Switch to a different branch.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"branch": map[string]any{
						"type":        "string",
						"description": "Branch name to switch to",
					},
				},
				"required": []string{"branch"},
			},
		},
		Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
			var p struct {
				Branch string `json:"branch"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("git_checkout: %w", err)
			}
			if p.Branch == "" {
				return "", fmt.Errorf("git_checkout: branch is required")
			}

			cmd := fmt.Sprintf("git checkout %s", shellQuote(p.Branch))
			result, err := rt.Exec(ctx, cmd, runtime.ExecOpts{})
			if err != nil {
				return "", err
			}
			if result.ExitCode != 0 {
				return "", fmt.Errorf("git_checkout: %s", result.Stderr)
			}
			// git checkout outputs to stderr on success.
			output := strings.TrimRight(result.Stderr, "\n")
			if output == "" {
				output = fmt.Sprintf("switched to branch %s", p.Branch)
			}
			return output, nil
		},
	}
}

func execTool() Tool {
	return Tool{
		Def: llm.ToolDef{
			Name:        "execute_command",
			Description: "Execute a shell command via sh -c. Returns stdout, stderr, and exit code.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "Shell command to execute",
					},
					"workdir": map[string]any{
						"type":        "string",
						"description": "Working directory relative to project root (optional)",
					},
				},
				"required": []string{"command"},
			},
		},
		Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
			var p struct {
				Command string `json:"command"`
				Workdir string `json:"workdir"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("execute_command: %w", err)
			}
			result, err := rt.Exec(ctx, p.Command, runtime.ExecOpts{
				RelDir: p.Workdir,
			})
			if err != nil {
				return "", err
			}
			out := map[string]any{
				"stdout":    result.Stdout,
				"stderr":    result.Stderr,
				"exit_code": result.ExitCode,
			}
			b, _ := json.Marshal(out)
			return string(b), nil
		},
	}
}
