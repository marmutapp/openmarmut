package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marmutapp/openmarmut/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memoryTestRT is a minimal runtime for testing project instruction loading.
type memoryTestRT struct {
	targetDir string
	files     map[string][]byte
	dirs      map[string][]runtime.FileEntry
}

func (r *memoryTestRT) Init(context.Context) error   { return nil }
func (r *memoryTestRT) Close(context.Context) error   { return nil }
func (r *memoryTestRT) TargetDir() string              { return r.targetDir }
func (r *memoryTestRT) WriteFile(_ context.Context, _ string, _ []byte, _ os.FileMode) error {
	return nil
}
func (r *memoryTestRT) DeleteFile(_ context.Context, _ string) error { return nil }
func (r *memoryTestRT) MkDir(_ context.Context, _ string, _ os.FileMode) error { return nil }
func (r *memoryTestRT) Exec(_ context.Context, _ string, _ runtime.ExecOpts) (*runtime.ExecResult, error) {
	return &runtime.ExecResult{}, nil
}
func (r *memoryTestRT) ListDir(_ context.Context, relPath string) ([]runtime.FileEntry, error) {
	if entries, ok := r.dirs[relPath]; ok {
		return entries, nil
	}
	return nil, fmt.Errorf("directory not found: %s", relPath)
}
func (r *memoryTestRT) ReadFile(_ context.Context, relPath string) ([]byte, error) {
	if data, ok := r.files[relPath]; ok {
		return data, nil
	}
	return nil, os.ErrNotExist
}

func TestLoadProjectInstructions_NoFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // Avoid picking up real global file.
	rt := &memoryTestRT{
		targetDir: t.TempDir(),
		files:     map[string][]byte{},
	}
	info, err := LoadProjectInstructions(context.Background(), rt)
	require.NoError(t, err)
	assert.Empty(t, info.Content)
	assert.Empty(t, info.Source)
	assert.Equal(t, 0, info.Lines)
}

func TestLoadProjectInstructions_TargetDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // Avoid picking up real global file.
	rt := &memoryTestRT{
		targetDir: t.TempDir(),
		files: map[string][]byte{
			"OPENMARMUT.md": []byte("# My Project\nSome instructions here."),
		},
	}
	info, err := LoadProjectInstructions(context.Background(), rt)
	require.NoError(t, err)
	assert.Contains(t, info.Content, "# My Project")
	assert.Contains(t, info.Content, "Some instructions here.")
	assert.Equal(t, "OPENMARMUT.md", info.Source)
	assert.Equal(t, 2, info.Lines)
	assert.False(t, info.Truncated)
}

func TestLoadProjectInstructions_CaseInsensitive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Should find openmarmut.md (lowercase).
	rt := &memoryTestRT{
		targetDir: t.TempDir(),
		files: map[string][]byte{
			"openmarmut.md": []byte("lowercase instructions"),
		},
	}
	info, err := LoadProjectInstructions(context.Background(), rt)
	require.NoError(t, err)
	assert.Contains(t, info.Content, "lowercase instructions")
	assert.Equal(t, "openmarmut.md", info.Source)
}

func TestLoadProjectInstructions_DotPrefix(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rt := &memoryTestRT{
		targetDir: t.TempDir(),
		files: map[string][]byte{
			".openmarmut.md": []byte("hidden instructions"),
		},
	}
	info, err := LoadProjectInstructions(context.Background(), rt)
	require.NoError(t, err)
	assert.Contains(t, info.Content, "hidden instructions")
}

func TestLoadProjectInstructions_PriorityOrder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// OPENMARMUT.md should be preferred over openmarmut.md.
	rt := &memoryTestRT{
		targetDir: t.TempDir(),
		files: map[string][]byte{
			"OPENMARMUT.md":  []byte("uppercase wins"),
			"openmarmut.md":  []byte("lowercase loses"),
			".openmarmut.md": []byte("dot loses"),
		},
	}
	info, err := LoadProjectInstructions(context.Background(), rt)
	require.NoError(t, err)
	assert.Contains(t, info.Content, "uppercase wins")
	assert.NotContains(t, info.Content, "lowercase loses")
}

func TestLoadProjectInstructions_Truncation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Create content larger than maxProjectInstructionChars.
	bigContent := strings.Repeat("x", maxProjectInstructionChars+500)
	rt := &memoryTestRT{
		targetDir: t.TempDir(),
		files: map[string][]byte{
			"OPENMARMUT.md": []byte(bigContent),
		},
	}
	info, err := LoadProjectInstructions(context.Background(), rt)
	require.NoError(t, err)
	assert.True(t, info.Truncated)
	assert.Contains(t, info.Content, "[WARNING: Project instructions truncated")
	// Content should be capped near maxProjectInstructionChars.
	assert.Less(t, len(info.Content), maxProjectInstructionChars+200)
}

func TestLoadProjectInstructions_AncestorLoading(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Create a nested directory structure with OPENMARMUT.md at different levels.
	root := t.TempDir()
	child := filepath.Join(root, "projects", "myapp")
	require.NoError(t, os.MkdirAll(child, 0o755))

	// Write ancestor OPENMARMUT.md in root.
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "OPENMARMUT.md"),
		[]byte("root-level instructions"),
		0o644,
	))

	// Write project OPENMARMUT.md in child (via Runtime).
	rt := &memoryTestRT{
		targetDir: child,
		files: map[string][]byte{
			"OPENMARMUT.md": []byte("project-level instructions"),
		},
	}

	info, err := LoadProjectInstructions(context.Background(), rt)
	require.NoError(t, err)
	// Both should be present: root first, then project.
	assert.Contains(t, info.Content, "root-level instructions")
	assert.Contains(t, info.Content, "project-level instructions")
	// Root should come before project.
	rootIdx := strings.Index(info.Content, "root-level")
	projIdx := strings.Index(info.Content, "project-level")
	assert.Less(t, rootIdx, projIdx, "ancestor (root) should appear before project")
}

func TestLoadProjectInstructions_ImportResolution(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rt := &memoryTestRT{
		targetDir: t.TempDir(),
		files: map[string][]byte{
			"OPENMARMUT.md":       []byte("# Project\n@docs/arch.md\nEnd."),
			"docs/arch.md":        []byte("Architecture content here"),
		},
	}
	info, err := LoadProjectInstructions(context.Background(), rt)
	require.NoError(t, err)
	assert.Contains(t, info.Content, "Architecture content here")
	assert.Contains(t, info.Content, "<!-- from docs/arch.md -->")
	assert.NotContains(t, info.Content, "@docs/arch.md")
}

func TestLoadProjectInstructions_ImportMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rt := &memoryTestRT{
		targetDir: t.TempDir(),
		files: map[string][]byte{
			"OPENMARMUT.md": []byte("# Project\n@nonexistent.md\nEnd."),
		},
	}
	info, err := LoadProjectInstructions(context.Background(), rt)
	require.NoError(t, err)
	assert.Contains(t, info.Content, "[import not found: nonexistent.md]")
}

func TestLoadProjectInstructions_ImportDeduplicate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rt := &memoryTestRT{
		targetDir: t.TempDir(),
		files: map[string][]byte{
			"OPENMARMUT.md": []byte("@docs/a.md\n@docs/a.md\n"),
			"docs/a.md":     []byte("content A"),
		},
	}
	info, err := LoadProjectInstructions(context.Background(), rt)
	require.NoError(t, err)
	// First occurrence resolved, second deduplicated.
	assert.Contains(t, info.Content, "content A")
	assert.Contains(t, info.Content, "[already included: docs/a.md]")
}

func TestLoadProjectInstructions_ImportRecursive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rt := &memoryTestRT{
		targetDir: t.TempDir(),
		files: map[string][]byte{
			"OPENMARMUT.md": []byte("@a.md"),
			"a.md":          []byte("from A\n@b.md"),
			"b.md":          []byte("from B"),
		},
	}
	info, err := LoadProjectInstructions(context.Background(), rt)
	require.NoError(t, err)
	assert.Contains(t, info.Content, "from A")
	assert.Contains(t, info.Content, "from B")
}

func TestLoadProjectInstructions_ImportMaxDepth(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Create a chain deeper than maxImportDepth.
	files := map[string][]byte{
		"OPENMARMUT.md": []byte("@level1.md"),
	}
	for i := 1; i <= maxImportDepth+2; i++ {
		files[fmt.Sprintf("level%d.md", i)] = []byte(fmt.Sprintf("@level%d.md", i+1))
	}
	rt := &memoryTestRT{
		targetDir: t.TempDir(),
		files:     files,
	}
	info, err := LoadProjectInstructions(context.Background(), rt)
	require.NoError(t, err)
	// Should not panic or infinite loop. Deep imports stay as @references.
	assert.NotEmpty(t, info.Content)
}

func TestFormatProjectInstructionsPrompt_Empty(t *testing.T) {
	assert.Empty(t, FormatProjectInstructionsPrompt(""))
}

func TestFormatProjectInstructionsPrompt_NonEmpty(t *testing.T) {
	result := FormatProjectInstructionsPrompt("hello world")
	assert.Contains(t, result, "## Project Instructions (from OPENMARMUT.md)")
	assert.Contains(t, result, "hello world")
}

func TestLoadProjectInstructions_GlobalFile(t *testing.T) {
	// Create a temporary home dir with global OPENMARMUT.md.
	tmpHome := t.TempDir()
	openmarmutDir := filepath.Join(tmpHome, ".openmarmut")
	require.NoError(t, os.MkdirAll(openmarmutDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(openmarmutDir, "OPENMARMUT.md"),
		[]byte("global instructions"),
		0o644,
	))

	// Override HOME for this test.
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	rt := &memoryTestRT{
		targetDir: t.TempDir(),
		files: map[string][]byte{
			"OPENMARMUT.md": []byte("project instructions"),
		},
	}
	info, err := LoadProjectInstructions(context.Background(), rt)
	require.NoError(t, err)
	assert.Contains(t, info.Content, "global instructions")
	assert.Contains(t, info.Content, "project instructions")
	// Global should come before project.
	globalIdx := strings.Index(info.Content, "global")
	projIdx := strings.Index(info.Content, "project")
	assert.Less(t, globalIdx, projIdx, "global should appear before project")
}

func TestLoadProjectInstructions_ImportNotInline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// @ references that are inline (not on their own line) should NOT be treated as imports.
	rt := &memoryTestRT{
		targetDir: t.TempDir(),
		files: map[string][]byte{
			"OPENMARMUT.md": []byte("Contact me at @user or @admin for help."),
		},
	}
	info, err := LoadProjectInstructions(context.Background(), rt)
	require.NoError(t, err)
	// Inline @refs should be left as-is.
	assert.Contains(t, info.Content, "Contact me at @user or @admin for help.")
}
