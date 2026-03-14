package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/gajaai/opencode-go/internal/llm"
	"github.com/gajaai/opencode-go/internal/runtime"
)

// Tool defines a callable action backed by a Runtime method.
type Tool struct {
	Def     llm.ToolDef
	Execute func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error)
}

// DefaultTools returns the standard set of tools backed by a Runtime.
func DefaultTools() []Tool {
	return []Tool{
		readFileTool(),
		writeFileTool(),
		deleteFileTool(),
		listDirTool(),
		mkdirTool(),
		execTool(),
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

func listDirTool() Tool {
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
			out := make([]entry, len(entries))
			for i, e := range entries {
				out[i] = entry{Name: e.Name, IsDir: e.IsDir, Size: e.Size}
			}
			b, _ := json.Marshal(out)
			return string(b), nil
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
