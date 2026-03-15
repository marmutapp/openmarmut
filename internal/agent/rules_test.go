package agent

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/gajaai/openmarmut-go/internal/llm"
	"github.com/gajaai/openmarmut-go/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rulesTestRT is a runtime stub for testing rule loading.
type rulesTestRT struct {
	targetDir string
	files     map[string][]byte
	dirs      map[string][]runtime.FileEntry
}

func (r *rulesTestRT) Init(context.Context) error   { return nil }
func (r *rulesTestRT) Close(context.Context) error   { return nil }
func (r *rulesTestRT) TargetDir() string              { return r.targetDir }
func (r *rulesTestRT) WriteFile(_ context.Context, _ string, _ []byte, _ os.FileMode) error {
	return nil
}
func (r *rulesTestRT) DeleteFile(_ context.Context, _ string) error { return nil }
func (r *rulesTestRT) MkDir(_ context.Context, _ string, _ os.FileMode) error { return nil }
func (r *rulesTestRT) Exec(_ context.Context, _ string, _ runtime.ExecOpts) (*runtime.ExecResult, error) {
	return &runtime.ExecResult{}, nil
}
func (r *rulesTestRT) ListDir(_ context.Context, relPath string) ([]runtime.FileEntry, error) {
	if entries, ok := r.dirs[relPath]; ok {
		return entries, nil
	}
	return nil, fmt.Errorf("directory not found: %s", relPath)
}
func (r *rulesTestRT) ReadFile(_ context.Context, relPath string) ([]byte, error) {
	if data, ok := r.files[relPath]; ok {
		return data, nil
	}
	return nil, os.ErrNotExist
}

func TestLoadRules_NoDirctory(t *testing.T) {
	rt := &rulesTestRT{
		targetDir: t.TempDir(),
		files:     map[string][]byte{},
		dirs:      map[string][]runtime.FileEntry{},
	}
	rules, err := LoadRules(context.Background(), rt)
	require.NoError(t, err)
	assert.Empty(t, rules)
}

func TestLoadRules_WithRules(t *testing.T) {
	rt := &rulesTestRT{
		targetDir: t.TempDir(),
		files: map[string][]byte{
			".openmarmut/rules/docker.md": []byte(`---
globs: ["internal/dockerrt/**", "**/*docker*"]
---
# Docker Rules
- Use Docker SDK
- Never shell out`),
			".openmarmut/rules/testing.md": []byte(`---
globs: ["**/*_test.go"]
---
# Testing Rules
- Use testify`),
		},
		dirs: map[string][]runtime.FileEntry{
			".openmarmut/rules": {
				{Name: "docker.md"},
				{Name: "testing.md"},
			},
		},
	}
	rules, err := LoadRules(context.Background(), rt)
	require.NoError(t, err)
	require.Len(t, rules, 2)

	// Check docker rule.
	dockerRule := findRule(rules, ".openmarmut/rules/docker.md")
	require.NotNil(t, dockerRule)
	assert.Equal(t, []string{"internal/dockerrt/**", "**/*docker*"}, dockerRule.Globs)
	assert.Contains(t, dockerRule.Content, "Docker Rules")
	assert.Contains(t, dockerRule.Content, "Use Docker SDK")
}

func TestLoadRules_NoFrontmatter(t *testing.T) {
	rt := &rulesTestRT{
		targetDir: t.TempDir(),
		files: map[string][]byte{
			".openmarmut/rules/general.md": []byte("# General\nAlways be nice."),
		},
		dirs: map[string][]runtime.FileEntry{
			".openmarmut/rules": {
				{Name: "general.md"},
			},
		},
	}
	rules, err := LoadRules(context.Background(), rt)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	assert.Empty(t, rules[0].Globs)
	assert.Contains(t, rules[0].Content, "Always be nice")
}

func TestLoadRules_SkipsNonMd(t *testing.T) {
	rt := &rulesTestRT{
		targetDir: t.TempDir(),
		files: map[string][]byte{
			".openmarmut/rules/notes.txt": []byte("not a rule"),
		},
		dirs: map[string][]runtime.FileEntry{
			".openmarmut/rules": {
				{Name: "notes.txt"},
				{Name: "subdir", IsDir: true},
			},
		},
	}
	rules, err := LoadRules(context.Background(), rt)
	require.NoError(t, err)
	assert.Empty(t, rules)
}

func TestMatchRules_NoGlobs(t *testing.T) {
	rules := []Rule{
		{Content: "always active", Source: "a.md"},
	}
	result := MatchRules(rules, []string{"anything.go"})
	assert.Contains(t, result, "always active")
}

func TestMatchRules_GlobMatch(t *testing.T) {
	rules := []Rule{
		{Globs: []string{"internal/dockerrt/**"}, Content: "docker rule", Source: "docker.md"},
		{Globs: []string{"**/*_test.go"}, Content: "test rule", Source: "test.md"},
	}

	// Match docker file.
	result := MatchRules(rules, []string{"internal/dockerrt/dockerrt.go"})
	assert.Contains(t, result, "docker rule")
	assert.NotContains(t, result, "test rule")

	// Match test file.
	result = MatchRules(rules, []string{"internal/agent/agent_test.go"})
	assert.Contains(t, result, "test rule")
	assert.NotContains(t, result, "docker rule")
}

func TestMatchRules_NoMatch(t *testing.T) {
	rules := []Rule{
		{Globs: []string{"internal/dockerrt/**"}, Content: "docker rule", Source: "docker.md"},
	}
	result := MatchRules(rules, []string{"internal/config/config.go"})
	assert.Empty(t, result)
}

func TestMatchRules_MultipleMatches(t *testing.T) {
	rules := []Rule{
		{Globs: []string{"**/*.go"}, Content: "go rule", Source: "go.md"},
		{Globs: []string{"**/*_test.go"}, Content: "test rule", Source: "test.md"},
	}
	result := MatchRules(rules, []string{"internal/agent/agent_test.go"})
	assert.Contains(t, result, "go rule")
	assert.Contains(t, result, "test rule")
}

func TestMatchRules_EmptyRules(t *testing.T) {
	result := MatchRules(nil, []string{"file.go"})
	assert.Empty(t, result)
}

func TestMatchRules_EmptyPaths(t *testing.T) {
	rules := []Rule{
		{Globs: []string{"**/*.go"}, Content: "go rule", Source: "go.md"},
	}
	result := MatchRules(rules, nil)
	assert.Empty(t, result)
}

func TestMatchGlob_DoublestarPrefix(t *testing.T) {
	assert.True(t, matchGlob("internal/dockerrt/**", "internal/dockerrt/dockerrt.go"))
	assert.True(t, matchGlob("internal/dockerrt/**", "internal/dockerrt/sub/deep.go"))
	assert.False(t, matchGlob("internal/dockerrt/**", "internal/config/config.go"))
}

func TestMatchGlob_DoublestarSuffix(t *testing.T) {
	assert.True(t, matchGlob("**/*.go", "internal/agent/agent.go"))
	assert.True(t, matchGlob("**/*_test.go", "internal/agent/agent_test.go"))
	assert.False(t, matchGlob("**/*_test.go", "internal/agent/agent.go"))
}

func TestMatchGlob_SimplePattern(t *testing.T) {
	assert.True(t, matchGlob("*.go", "main.go"))
	assert.False(t, matchGlob("*.go", "main.py"))
}

func TestMatchGlob_DoublestarAnywhere(t *testing.T) {
	assert.True(t, matchGlob("**/*docker*", "internal/dockerrt/dockerrt.go"))
	assert.True(t, matchGlob("**/*docker*", "docker-compose.yml"))
	assert.False(t, matchGlob("**/*docker*", "internal/config/config.go"))
}

func TestExtractRecentFilePaths(t *testing.T) {
	history := []llm.Message{
		{Role: llm.RoleUser, Content: "read the config"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{Name: "read_file", Arguments: `{"path":"internal/config/config.go"}`},
		}},
		{Role: llm.RoleTool, Content: "file contents"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{Name: "read_file", Arguments: `{"path":"internal/agent/agent.go"}`},
		}},
	}

	paths := ExtractRecentFilePaths(history, 10)
	assert.Contains(t, paths, "internal/config/config.go")
	assert.Contains(t, paths, "internal/agent/agent.go")
	assert.Len(t, paths, 2)
}

func TestExtractRecentFilePaths_NoDuplicates(t *testing.T) {
	history := []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{Name: "read_file", Arguments: `{"path":"a.go"}`},
			{Name: "write_file", Arguments: `{"path":"a.go","content":"..."}`},
		}},
	}
	paths := ExtractRecentFilePaths(history, 10)
	assert.Len(t, paths, 1)
}

func TestExtractRecentFilePaths_LastN(t *testing.T) {
	history := []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{Name: "read_file", Arguments: `{"path":"old.go"}`},
		}},
		{Role: llm.RoleUser, Content: "now something else"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{Name: "read_file", Arguments: `{"path":"new.go"}`},
		}},
	}

	paths := ExtractRecentFilePaths(history, 2)
	assert.Contains(t, paths, "new.go")
	assert.NotContains(t, paths, "old.go")
}

func TestParseRule_InlineArray(t *testing.T) {
	content := `---
globs: ["*.go", "*.py"]
---
Rule content.`
	rule, err := parseRule(content, "test.md")
	require.NoError(t, err)
	assert.Equal(t, []string{"*.go", "*.py"}, rule.Globs)
	assert.Equal(t, "Rule content.", rule.Content)
}

func TestParseRule_MultiLineGlobs(t *testing.T) {
	content := `---
globs:
  - "internal/**"
  - "*.go"
---
Multi-line rule.`
	rule, err := parseRule(content, "test.md")
	require.NoError(t, err)
	assert.Equal(t, []string{"internal/**", "*.go"}, rule.Globs)
	assert.Equal(t, "Multi-line rule.", rule.Content)
}

func TestFormatActiveRules_Empty(t *testing.T) {
	assert.Empty(t, FormatActiveRules(""))
}

func TestFormatActiveRules_NonEmpty(t *testing.T) {
	result := FormatActiveRules("some rules")
	assert.Contains(t, result, "## Active Rules")
	assert.Contains(t, result, "some rules")
}

func findRule(rules []Rule, source string) *Rule {
	for i := range rules {
		if rules[i].Source == source {
			return &rules[i]
		}
	}
	return nil
}
