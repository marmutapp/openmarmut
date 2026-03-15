package agent

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/marmutapp/openmarmut/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ignoreTestRT is a runtime stub for testing ignore list loading.
type ignoreTestRT struct {
	targetDir string
	files     map[string][]byte
}

func (r *ignoreTestRT) Init(context.Context) error   { return nil }
func (r *ignoreTestRT) Close(context.Context) error   { return nil }
func (r *ignoreTestRT) TargetDir() string              { return r.targetDir }
func (r *ignoreTestRT) WriteFile(_ context.Context, path string, data []byte, _ os.FileMode) error {
	r.files[path] = data
	return nil
}
func (r *ignoreTestRT) DeleteFile(_ context.Context, _ string) error { return nil }
func (r *ignoreTestRT) MkDir(_ context.Context, _ string, _ os.FileMode) error { return nil }
func (r *ignoreTestRT) Exec(_ context.Context, _ string, _ runtime.ExecOpts) (*runtime.ExecResult, error) {
	return &runtime.ExecResult{}, nil
}
func (r *ignoreTestRT) ListDir(_ context.Context, relPath string) ([]runtime.FileEntry, error) {
	return nil, fmt.Errorf("not implemented")
}
func (r *ignoreTestRT) ReadFile(_ context.Context, relPath string) ([]byte, error) {
	if data, ok := r.files[relPath]; ok {
		return data, nil
	}
	return nil, os.ErrNotExist
}

func TestLoadIgnoreList_NoFile(t *testing.T) {
	rt := &ignoreTestRT{
		targetDir: t.TempDir(),
		files:     map[string][]byte{},
	}
	il := LoadIgnoreList(context.Background(), rt)
	// Should still have default patterns.
	require.NotNil(t, il)
	assert.Equal(t, len(defaultIgnorePatterns), len(il.Patterns()))
}

func TestLoadIgnoreList_DefaultPatternsAlwaysIncluded(t *testing.T) {
	rt := &ignoreTestRT{
		targetDir: t.TempDir(),
		files:     map[string][]byte{},
	}
	il := LoadIgnoreList(context.Background(), rt)
	for _, dp := range defaultIgnorePatterns {
		assert.Contains(t, il.Patterns(), dp, "default pattern %q should be present", dp)
	}
}

func TestLoadIgnoreList_WithPatterns(t *testing.T) {
	rt := &ignoreTestRT{
		targetDir: t.TempDir(),
		files: map[string][]byte{
			".openmarmutignore": []byte(`# Build output
node_modules/
dist/
*.log
.env
`),
		},
	}
	il := LoadIgnoreList(context.Background(), rt)
	require.NotNil(t, il)
	// Default patterns + 4 from .openmarmutignore (node_modules/ is in both, but appears twice).
	assert.Equal(t, len(defaultIgnorePatterns)+4, len(il.Patterns()))
	assert.Equal(t, ".openmarmutignore", il.Source())
}

func TestLoadIgnoreList_GitignoreLoaded(t *testing.T) {
	rt := &ignoreTestRT{
		targetDir: t.TempDir(),
		files: map[string][]byte{
			".gitignore": []byte("vendor/\n*.o\n"),
		},
	}
	il := LoadIgnoreList(context.Background(), rt)
	require.NotNil(t, il)
	assert.Contains(t, il.Patterns(), "vendor/")
	assert.Contains(t, il.Patterns(), "*.o")
}

func TestLoadIgnoreList_GitignoreAndOpenmarmutignore(t *testing.T) {
	rt := &ignoreTestRT{
		targetDir: t.TempDir(),
		files: map[string][]byte{
			".gitignore":        []byte("vendor/\n"),
			".openmarmutignore": []byte("secrets/\n"),
		},
	}
	il := LoadIgnoreList(context.Background(), rt)
	require.NotNil(t, il)
	// Should have defaults + vendor/ from .gitignore + secrets/ from .openmarmutignore.
	assert.Contains(t, il.Patterns(), "vendor/")
	assert.Contains(t, il.Patterns(), "secrets/")
	// .gitignore patterns come before .openmarmutignore patterns.
	vendorIdx := -1
	secretsIdx := -1
	for i, p := range il.Patterns() {
		if p == "vendor/" {
			vendorIdx = i
		}
		if p == "secrets/" {
			secretsIdx = i
		}
	}
	assert.Greater(t, secretsIdx, vendorIdx, ".openmarmutignore patterns should come after .gitignore")
}

func TestShouldIgnore_Nil(t *testing.T) {
	var il *IgnoreList
	assert.False(t, il.ShouldIgnore("anything"))
}

func TestShouldIgnore_SimpleFilename(t *testing.T) {
	il := &IgnoreList{patterns: []string{".env", "*.log"}}
	assert.True(t, il.ShouldIgnore(".env"))
	assert.True(t, il.ShouldIgnore("config/.env"))
	assert.True(t, il.ShouldIgnore("app.log"))
	assert.True(t, il.ShouldIgnore("logs/error.log"))
	assert.False(t, il.ShouldIgnore("main.go"))
}

func TestShouldIgnore_Directory(t *testing.T) {
	il := &IgnoreList{patterns: []string{"node_modules/"}}
	assert.True(t, il.ShouldIgnore("node_modules/package.json"))
	assert.True(t, il.ShouldIgnore("frontend/node_modules/lib/index.js"))
}

func TestShouldIgnore_PathPattern(t *testing.T) {
	il := &IgnoreList{patterns: []string{"internal/dockerrt/**"}}
	assert.True(t, il.ShouldIgnore("internal/dockerrt/dockerrt.go"))
	assert.False(t, il.ShouldIgnore("internal/config/config.go"))
}

func TestShouldIgnore_WildcardPattern(t *testing.T) {
	il := &IgnoreList{patterns: []string{"*.secret", "*.key"}}
	assert.True(t, il.ShouldIgnore("api.secret"))
	assert.True(t, il.ShouldIgnore("tls.key"))
	assert.False(t, il.ShouldIgnore("main.go"))
}

func TestShouldIgnore_Comments(t *testing.T) {
	patterns := parseIgnorePatterns(`# This is a comment
.env
# Another comment

*.log
`)
	assert.Equal(t, []string{".env", "*.log"}, patterns)
}

func TestShouldIgnore_EmptyLines(t *testing.T) {
	patterns := parseIgnorePatterns(`

.env

*.log

`)
	assert.Equal(t, []string{".env", "*.log"}, patterns)
}

func TestFormatIgnorePrompt_Empty(t *testing.T) {
	il := &IgnoreList{}
	assert.Empty(t, FormatIgnorePrompt(il))
}

func TestFormatIgnorePrompt_Nil(t *testing.T) {
	assert.Empty(t, FormatIgnorePrompt(nil))
}

func TestFormatIgnorePrompt_WithPatterns(t *testing.T) {
	il := &IgnoreList{patterns: []string{".env", "*.log"}}
	result := FormatIgnorePrompt(il)
	assert.Contains(t, result, "## Ignored Paths")
	assert.Contains(t, result, "- .env")
	assert.Contains(t, result, "- *.log")
}

func TestDirPatterns(t *testing.T) {
	il := &IgnoreList{patterns: []string{".git/", "node_modules/", "*.pyc", ".DS_Store"}}
	dirs := il.DirPatterns()
	assert.Equal(t, []string{".git", "node_modules"}, dirs)
}

func TestFilePatterns(t *testing.T) {
	il := &IgnoreList{patterns: []string{".git/", "node_modules/", "*.pyc", ".DS_Store", "internal/foo/**"}}
	files := il.FilePatterns()
	assert.Equal(t, []string{"*.pyc", ".DS_Store"}, files)
}

func TestShouldIgnoreEntry(t *testing.T) {
	il := &IgnoreList{patterns: []string{".git/", "node_modules/", "*.pyc", ".DS_Store"}}

	// Directory patterns match directories only.
	assert.True(t, il.ShouldIgnoreEntry(".git", true))
	assert.True(t, il.ShouldIgnoreEntry("node_modules", true))
	assert.False(t, il.ShouldIgnoreEntry(".git", false)) // file named .git won't match dir-only pattern
	assert.False(t, il.ShouldIgnoreEntry("node_modules", false))

	// File patterns match files.
	assert.True(t, il.ShouldIgnoreEntry("foo.pyc", false))
	assert.True(t, il.ShouldIgnoreEntry(".DS_Store", false))
	assert.False(t, il.ShouldIgnoreEntry("main.go", false))
}

func TestShouldIgnoreEntry_Nil(t *testing.T) {
	var il *IgnoreList
	assert.False(t, il.ShouldIgnoreEntry("anything", true))
}

func TestFormatIgnoreDisplay(t *testing.T) {
	il := &IgnoreList{
		patterns: []string{".git/", "*.pyc", "secrets/"},
		sources: []patternSource{
			{Pattern: ".git/", Source: "default"},
			{Pattern: "*.pyc", Source: ".gitignore"},
			{Pattern: "secrets/", Source: ".openmarmutignore"},
		},
	}
	result := FormatIgnoreDisplay(il)
	assert.Contains(t, result, "[default]")
	assert.Contains(t, result, "[.gitignore]")
	assert.Contains(t, result, "[.openmarmutignore]")
	assert.Contains(t, result, ".git/")
	assert.Contains(t, result, "*.pyc")
	assert.Contains(t, result, "secrets/")
}

func TestFormatIgnoreDisplay_Empty(t *testing.T) {
	il := &IgnoreList{}
	result := FormatIgnoreDisplay(il)
	assert.Contains(t, result, "No ignore patterns")
}

func TestAddPatternToFile(t *testing.T) {
	rt := &ignoreTestRT{
		targetDir: t.TempDir(),
		files:     map[string][]byte{},
	}
	ctx := context.Background()

	// Add to non-existent file.
	err := AddPatternToFile(ctx, rt, "*.log")
	require.NoError(t, err)
	data, ok := rt.files[".openmarmutignore"]
	require.True(t, ok)
	assert.Equal(t, "*.log\n", string(data))

	// Add another pattern.
	err = AddPatternToFile(ctx, rt, "dist/")
	require.NoError(t, err)
	data = rt.files[".openmarmutignore"]
	assert.Equal(t, "*.log\ndist/\n", string(data))
}

func TestRemovePatternFromFile(t *testing.T) {
	rt := &ignoreTestRT{
		targetDir: t.TempDir(),
		files: map[string][]byte{
			".openmarmutignore": []byte("*.log\ndist/\n.env\n"),
		},
	}
	ctx := context.Background()

	err := RemovePatternFromFile(ctx, rt, "dist/")
	require.NoError(t, err)
	data := rt.files[".openmarmutignore"]
	assert.NotContains(t, string(data), "dist/")
	assert.Contains(t, string(data), "*.log")
	assert.Contains(t, string(data), ".env")
}

func TestRemovePatternFromFile_NotFound(t *testing.T) {
	rt := &ignoreTestRT{
		targetDir: t.TempDir(),
		files: map[string][]byte{
			".openmarmutignore": []byte("*.log\n"),
		},
	}
	ctx := context.Background()

	err := RemovePatternFromFile(ctx, rt, "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pattern not found")
}

func TestDefaultTools_WithIgnoreList(t *testing.T) {
	il := &IgnoreList{patterns: []string{".git/", "node_modules/", "*.pyc"}}
	tools := DefaultTools(il)

	// Verify that tools are created without error.
	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Def.Name] = true
	}
	assert.True(t, names["grep_files"])
	assert.True(t, names["find_files"])
	assert.True(t, names["list_dir"])
}

func TestDefaultTools_WithoutIgnoreList(t *testing.T) {
	tools := DefaultTools()

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Def.Name] = true
	}
	assert.True(t, names["grep_files"])
	assert.True(t, names["find_files"])
	assert.True(t, names["list_dir"])
}

func TestLoadIgnoreListFromOS_DefaultPatterns(t *testing.T) {
	dir := t.TempDir()
	il := LoadIgnoreListFromOS(dir)
	require.NotNil(t, il)
	assert.Equal(t, len(defaultIgnorePatterns), len(il.Patterns()))
	for _, dp := range defaultIgnorePatterns {
		assert.Contains(t, il.Patterns(), dp)
	}
}
