package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/gajaai/openmarmut-go/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// commandsTestRT is a minimal runtime for testing custom command loading.
type commandsTestRT struct {
	targetDir string
	files     map[string][]byte
	dirs      map[string][]runtime.FileEntry
}

func (r *commandsTestRT) Init(context.Context) error   { return nil }
func (r *commandsTestRT) Close(context.Context) error   { return nil }
func (r *commandsTestRT) TargetDir() string              { return r.targetDir }
func (r *commandsTestRT) WriteFile(_ context.Context, _ string, _ []byte, _ os.FileMode) error {
	return nil
}
func (r *commandsTestRT) DeleteFile(_ context.Context, _ string) error { return nil }
func (r *commandsTestRT) MkDir(_ context.Context, _ string, _ os.FileMode) error { return nil }
func (r *commandsTestRT) Exec(_ context.Context, _ string, _ runtime.ExecOpts) (*runtime.ExecResult, error) {
	return &runtime.ExecResult{}, nil
}
func (r *commandsTestRT) ListDir(_ context.Context, relPath string) ([]runtime.FileEntry, error) {
	if entries, ok := r.dirs[relPath]; ok {
		return entries, nil
	}
	return nil, fmt.Errorf("directory not found: %s", relPath)
}
func (r *commandsTestRT) ReadFile(_ context.Context, relPath string) ([]byte, error) {
	if data, ok := r.files[relPath]; ok {
		return data, nil
	}
	return nil, os.ErrNotExist
}

func TestParseCustomCommand_WithFrontmatter(t *testing.T) {
	content := "---\ndescription: \"Run all tests\"\n---\nRun `go test ./...` and analyze the results."

	cmd := parseCustomCommand("test.md", ".openmarmut/commands/test.md", content)

	assert.Equal(t, "test", cmd.Name)
	assert.Equal(t, "Run all tests", cmd.Description)
	assert.Contains(t, cmd.Content, "go test ./...")
	assert.Equal(t, ".openmarmut/commands/test.md", cmd.Source)
}

func TestParseCustomCommand_WithoutFrontmatter(t *testing.T) {
	content := "Review the code for bugs and security issues."

	cmd := parseCustomCommand("review.md", ".openmarmut/commands/review.md", content)

	assert.Equal(t, "review", cmd.Name)
	assert.Equal(t, "Review the code for bugs and security issues.", cmd.Description)
	assert.Equal(t, content, cmd.Content)
}

func TestParseCustomCommand_MultilineNoFrontmatter(t *testing.T) {
	content := "First line summary\nSecond line with details.\nThird line."

	cmd := parseCustomCommand("multi.md", "src", content)

	assert.Equal(t, "multi", cmd.Name)
	assert.Equal(t, "First line summary", cmd.Description)
	assert.Equal(t, content, cmd.Content)
}

func TestParseCustomCommand_UnclosedFrontmatter(t *testing.T) {
	content := "---\ndescription: test\nNo closing fence."

	cmd := parseCustomCommand("broken.md", "src", content)

	assert.Equal(t, "broken", cmd.Name)
	assert.Equal(t, content, cmd.Content)
}

func TestParseCustomCommand_SingleQuotedDescription(t *testing.T) {
	content := "---\ndescription: 'Lint the code'\n---\nRun golangci-lint."

	cmd := parseCustomCommand("lint.md", "src", content)

	assert.Equal(t, "Lint the code", cmd.Description)
}

func TestFindCustomCommand(t *testing.T) {
	commands := []CustomCommand{
		{Name: "test", Description: "Run tests"},
		{Name: "review", Description: "Code review"},
	}

	found := FindCustomCommand(commands, "test")
	require.NotNil(t, found)
	assert.Equal(t, "test", found.Name)

	assert.Nil(t, FindCustomCommand(commands, "missing"))
}

func TestFormatCustomCommandsList(t *testing.T) {
	commands := []CustomCommand{
		{Name: "test", Description: "Run tests"},
		{Name: "review", Description: "Code review"},
	}

	result := FormatCustomCommandsList(commands)
	assert.Contains(t, result, "/test")
	assert.Contains(t, result, "Run tests")
	assert.Contains(t, result, "/review")
	assert.Contains(t, result, "Code review")
}

func TestFormatCustomCommandsList_Empty(t *testing.T) {
	result := FormatCustomCommandsList(nil)
	assert.Contains(t, result, "No custom commands")
}

func TestLoadCustomCommands_WithMockRT(t *testing.T) {
	rt := &commandsTestRT{
		targetDir: "/project",
		dirs: map[string][]runtime.FileEntry{
			".openmarmut/commands": {
				{Name: "test.md", IsDir: false},
				{Name: "lint.md", IsDir: false},
				{Name: "notes.txt", IsDir: false}, // Should be ignored.
				{Name: "subdir", IsDir: true},      // Should be ignored.
			},
		},
		files: map[string][]byte{
			".openmarmut/commands/test.md": []byte("---\ndescription: \"Run tests\"\n---\nRun go test ./..."),
			".openmarmut/commands/lint.md": []byte("Run golangci-lint on the project."),
		},
	}

	commands, err := LoadCustomCommands(context.Background(), rt)
	require.NoError(t, err)
	assert.Len(t, commands, 2)

	test := FindCustomCommand(commands, "test")
	require.NotNil(t, test)
	assert.Equal(t, "Run tests", test.Description)

	lint := FindCustomCommand(commands, "lint")
	require.NotNil(t, lint)
	assert.Contains(t, lint.Content, "golangci-lint")
}

func TestLoadCustomCommands_NoDirReturnsNil(t *testing.T) {
	rt := &commandsTestRT{
		targetDir: "/project",
		dirs:      map[string][]runtime.FileEntry{},
		files:     map[string][]byte{},
	}
	commands, err := LoadCustomCommands(context.Background(), rt)
	assert.NoError(t, err)
	assert.Nil(t, commands)
}

func TestParseCustomCommand_DescriptionTruncation(t *testing.T) {
	// First line is long, second line exists so description is truncated from first line.
	long := strings.Repeat("A", 200) + "\nSecond line."
	cmd := parseCustomCommand("long.md", "src", long)
	assert.LessOrEqual(t, len(cmd.Description), 83) // 80 + "..."
}
