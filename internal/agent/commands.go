package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/marmutapp/openmarmut/internal/runtime"
)

// CustomCommand represents a user-defined slash command loaded from .openmarmut/commands/.
type CustomCommand struct {
	Name        string // Command name (derived from filename, without extension).
	Description string // Short description from frontmatter.
	Content     string // Prompt content to send as user message.
	Source      string // File path relative to target dir.
}

// LoadCustomCommands scans .openmarmut/commands/ for .md files and returns parsed commands.
func LoadCustomCommands(ctx context.Context, rt runtime.Runtime) ([]CustomCommand, error) {
	dir := ".openmarmut/commands"
	entries, err := rt.ListDir(ctx, dir)
	if err != nil {
		return nil, nil // Directory doesn't exist — not an error.
	}

	var commands []CustomCommand
	for _, e := range entries {
		if e.IsDir || !strings.HasSuffix(e.Name, ".md") {
			continue
		}

		path := filepath.Join(dir, e.Name)
		data, err := rt.ReadFile(ctx, path)
		if err != nil {
			continue
		}

		cmd := parseCustomCommand(e.Name, path, string(data))
		commands = append(commands, cmd)
	}

	return commands, nil
}

// parseCustomCommand parses a markdown file into a CustomCommand.
// Supports optional YAML frontmatter with a "description" field.
func parseCustomCommand(filename, source, content string) CustomCommand {
	name := strings.TrimSuffix(filename, ".md")

	cmd := CustomCommand{
		Name:   name,
		Source: source,
	}

	// Parse frontmatter if present.
	if strings.HasPrefix(content, "---\n") {
		endIdx := strings.Index(content[4:], "\n---")
		if endIdx >= 0 {
			frontmatter := content[4 : 4+endIdx]
			cmd.Content = strings.TrimSpace(content[4+endIdx+4:])

			// Parse description from frontmatter.
			for _, line := range strings.Split(frontmatter, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "description:") {
					desc := strings.TrimSpace(strings.TrimPrefix(line, "description:"))
					desc = strings.Trim(desc, "\"'")
					cmd.Description = desc
				}
			}
		} else {
			cmd.Content = content
		}
	} else {
		cmd.Content = content
	}

	if cmd.Description == "" {
		// Use first line as description if no frontmatter.
		if idx := strings.Index(cmd.Content, "\n"); idx > 0 {
			cmd.Description = strings.TrimSpace(cmd.Content[:idx])
			if len(cmd.Description) > 80 {
				cmd.Description = cmd.Description[:80] + "..."
			}
		} else {
			cmd.Description = cmd.Content
		}
	}

	return cmd
}

// FindCustomCommand returns the command matching the given name, or nil.
func FindCustomCommand(commands []CustomCommand, name string) *CustomCommand {
	for i := range commands {
		if commands[i].Name == name {
			return &commands[i]
		}
	}
	return nil
}

// FormatCustomCommandsList returns a formatted string listing all custom commands.
func FormatCustomCommandsList(commands []CustomCommand) string {
	if len(commands) == 0 {
		return "No custom commands found. Add .md files to .openmarmut/commands/"
	}

	var sb strings.Builder
	for _, cmd := range commands {
		sb.WriteString(fmt.Sprintf("  /%s — %s\n", cmd.Name, cmd.Description))
	}
	return strings.TrimRight(sb.String(), "\n")
}
