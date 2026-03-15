package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryStore_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "MEMORY.md")

	ms := NewMemoryStoreAt(path)
	require.NoError(t, ms.Save("learning", "Go tests use testify"))
	require.NoError(t, ms.Save("preference", "User prefers table-driven tests"))
	assert.Equal(t, 2, ms.Count())

	// Load in a fresh store.
	ms2 := NewMemoryStoreAt(path)
	require.NoError(t, ms2.Load())
	assert.Equal(t, 2, ms2.Count())

	entries := ms2.Entries()
	assert.Equal(t, "learning", entries[0].Category)
	assert.Equal(t, "Go tests use testify", entries[0].Content)
	assert.Equal(t, "preference", entries[1].Category)
}

func TestMemoryStore_SaveWithProject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "MEMORY.md")

	ms := NewMemoryStoreAt(path)
	require.NoError(t, ms.SaveWithProject("/home/user/myapp", "learning", "Build command: go build ./cmd/app"))
	require.NoError(t, ms.SaveWithProject("", "preference", "User prefers table-driven tests"))
	assert.Equal(t, 2, ms.Count())

	// Verify project tags are persisted.
	ms2 := NewMemoryStoreAt(path)
	require.NoError(t, ms2.Load())
	require.Equal(t, 2, ms2.Count())

	entries := ms2.Entries()
	assert.Equal(t, "/home/user/myapp", entries[0].Project)
	assert.Equal(t, "learning", entries[0].Category)
	assert.Equal(t, "Build command: go build ./cmd/app", entries[0].Content)
	assert.Equal(t, "", entries[1].Project) // Global.
	assert.Equal(t, "preference", entries[1].Category)
}

func TestMemoryStore_LoadNoFile(t *testing.T) {
	ms := NewMemoryStoreAt(filepath.Join(t.TempDir(), "nonexistent.md"))
	require.NoError(t, ms.Load())
	assert.Equal(t, 0, ms.Count())
}

func TestMemoryStore_Clear(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "MEMORY.md")

	ms := NewMemoryStoreAt(path)
	require.NoError(t, ms.Save("test", "something"))
	assert.Equal(t, 1, ms.Count())

	require.NoError(t, ms.Clear())
	assert.Equal(t, 0, ms.Count())

	// File should be gone.
	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err))
}

func TestMemoryStore_ClearNoFile(t *testing.T) {
	ms := NewMemoryStoreAt(filepath.Join(t.TempDir(), "nonexistent.md"))
	require.NoError(t, ms.Clear())
}

func TestMemoryStore_FormatForPrompt_Empty(t *testing.T) {
	ms := NewMemoryStoreAt(filepath.Join(t.TempDir(), "m.md"))
	assert.Empty(t, ms.FormatForPrompt())
}

func TestMemoryStore_FormatForPrompt_WithEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "MEMORY.md")

	ms := NewMemoryStoreAt(path)
	require.NoError(t, ms.Save("learning", "use testify"))
	require.NoError(t, ms.Save("context", "project uses cobra"))

	result := ms.FormatForPrompt()
	assert.Contains(t, result, "## Auto-Memory")
	assert.Contains(t, result, "learning")
	assert.Contains(t, result, "use testify")
	assert.Contains(t, result, "context")
	assert.Contains(t, result, "project uses cobra")
	assert.Contains(t, result, "global") // Both entries are global.
}

func TestMemoryStore_FormatForPrompt_WithProjectTag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "MEMORY.md")

	ms := NewMemoryStoreAt(path)
	require.NoError(t, ms.SaveWithProject("/home/user/myapp", "learning", "Build with make"))
	require.NoError(t, ms.SaveWithProject("", "preference", "prefers gofumpt"))

	result := ms.FormatForPrompt()
	assert.Contains(t, result, "project:/home/user/myapp")
	assert.Contains(t, result, "global")
	assert.Contains(t, result, "Build with make")
	assert.Contains(t, result, "prefers gofumpt")
}

func TestMemoryStore_FormatForPrompt_Truncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "MEMORY.md")

	ms := NewMemoryStoreAt(path)
	// Add many entries to exceed maxMemoryChars.
	for i := 0; i < 200; i++ {
		require.NoError(t, ms.Save("test", strings.Repeat("x", 50)))
	}

	result := ms.FormatForPrompt()
	assert.Contains(t, result, "older memories truncated")
	assert.Less(t, len(result), maxMemoryChars+200)
}

func TestMemoryStore_FormatForPromptFiltered(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "MEMORY.md")

	ms := NewMemoryStoreAt(path)
	require.NoError(t, ms.SaveWithProject("/home/user/project-a", "learning", "Project A uses React"))
	require.NoError(t, ms.SaveWithProject("/home/user/project-b", "learning", "Project B uses Go"))
	require.NoError(t, ms.SaveWithProject("", "preference", "User prefers dark mode"))

	// Filter to project-a.
	result := ms.FormatForPromptFiltered("/home/user/project-a")
	assert.Contains(t, result, "Project A uses React")
	assert.Contains(t, result, "User prefers dark mode")   // Global always included.
	assert.NotContains(t, result, "Project B uses Go")      // Filtered out.

	// Filter to project-b.
	result = ms.FormatForPromptFiltered("/home/user/project-b")
	assert.NotContains(t, result, "Project A uses React")
	assert.Contains(t, result, "Project B uses Go")
	assert.Contains(t, result, "User prefers dark mode")
}

func TestMemoryStore_EntriesForProject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "MEMORY.md")

	ms := NewMemoryStoreAt(path)
	require.NoError(t, ms.SaveWithProject("/home/user/myapp", "learning", "uses cobra"))
	require.NoError(t, ms.SaveWithProject("/home/user/other", "learning", "uses gin"))
	require.NoError(t, ms.SaveWithProject("", "preference", "global pref"))

	// Exact match.
	entries := ms.EntriesForProject("/home/user/myapp")
	require.Len(t, entries, 2) // myapp + global
	assert.Equal(t, "uses cobra", entries[0].Content)
	assert.Equal(t, "global pref", entries[1].Content)

	// Subdirectory match.
	entries = ms.EntriesForProject("/home/user/myapp/cmd/server")
	require.Len(t, entries, 2) // myapp parent + global
	assert.Equal(t, "uses cobra", entries[0].Content)

	// No match for unrelated project.
	entries = ms.EntriesForProject("/home/user/unrelated")
	require.Len(t, entries, 1) // Only global.
	assert.Equal(t, "global pref", entries[0].Content)

	// Empty target = all entries.
	entries = ms.EntriesForProject("")
	require.Len(t, entries, 3)
}

func TestMemoryStore_CreatesDirOnSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "dir", "MEMORY.md")

	ms := NewMemoryStoreAt(path)
	require.NoError(t, ms.Save("test", "auto-creates directory"))

	// File should exist.
	_, err := os.Stat(path)
	assert.NoError(t, err)
}

func TestMemoryStore_AppendOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "MEMORY.md")

	ms := NewMemoryStoreAt(path)
	require.NoError(t, ms.Save("first", "entry one"))

	// Save again — should append, not overwrite.
	ms2 := NewMemoryStoreAt(path)
	require.NoError(t, ms2.Load())
	require.NoError(t, ms2.Save("second", "entry two"))

	// Read raw file to verify append.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "entry one")
	assert.Contains(t, content, "entry two")
	// Should have exactly 2 lines starting with "- ["
	lines := strings.Split(strings.TrimSpace(content), "\n")
	entryLines := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "- [") {
			entryLines++
		}
	}
	assert.Equal(t, 2, entryLines)
}

func TestParseMemoryEntries_NewFormat(t *testing.T) {
	content := `- [2026-01-15] project:/home/user/app | learning | Go tests use testify
- [2026-02-20] global | preference | table-driven tests preferred
`
	entries := parseMemoryEntries(content)
	require.Len(t, entries, 2)

	assert.Equal(t, "/home/user/app", entries[0].Project)
	assert.Equal(t, "learning", entries[0].Category)
	assert.Equal(t, "Go tests use testify", entries[0].Content)
	assert.Equal(t, 2026, entries[0].Timestamp.Year())

	assert.Equal(t, "", entries[1].Project) // "global" means no project.
	assert.Equal(t, "preference", entries[1].Category)
	assert.Equal(t, "table-driven tests preferred", entries[1].Content)
}

func TestParseMemoryEntries_OldFormat(t *testing.T) {
	content := `- [2026-01-15] learning | Go tests use testify
- [2026-02-20] preference | table-driven tests preferred
`
	entries := parseMemoryEntries(content)
	require.Len(t, entries, 2)
	assert.Equal(t, "", entries[0].Project) // Old format has no project.
	assert.Equal(t, "learning", entries[0].Category)
	assert.Equal(t, "Go tests use testify", entries[0].Content)
}

func TestParseMemoryEntries_SkipsInvalid(t *testing.T) {
	content := `# Header
Some random text
- invalid line
- [bad-date] learning | something
- [2026-01-15] global | learning | valid entry
`
	entries := parseMemoryEntries(content)
	require.Len(t, entries, 1)
	assert.Equal(t, "valid entry", entries[0].Content)
}

func TestNewMemoryStore(t *testing.T) {
	ms := NewMemoryStore()
	// Should not be nil on systems with HOME set.
	if os.Getenv("HOME") != "" {
		assert.NotNil(t, ms)
		assert.Contains(t, ms.Path(), memoryFileName)
	}
}

func TestNewMemoryStoreWithPath(t *testing.T) {
	// Custom path.
	ms := NewMemoryStoreWithPath("/custom/path/MEMORY.md")
	require.NotNil(t, ms)
	assert.Equal(t, "/custom/path/MEMORY.md", ms.Path())

	// Empty path falls back to default.
	ms2 := NewMemoryStoreWithPath("")
	if os.Getenv("HOME") != "" {
		assert.NotNil(t, ms2)
		assert.Contains(t, ms2.Path(), memoryFileName)
	}
}

func TestParseExtractedMemories(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "NONE response",
			input:    "NONE",
			expected: nil,
		},
		{
			name:     "none lowercase",
			input:    "none",
			expected: nil,
		},
		{
			name:     "empty response",
			input:    "",
			expected: nil,
		},
		{
			name:  "valid memories",
			input: "- Build command is go build ./cmd/app\n- Uses testify for testing\n- Prefers conventional commits",
			expected: []string{
				"Build command is go build ./cmd/app",
				"Uses testify for testing",
				"Prefers conventional commits",
			},
		},
		{
			name:     "mixed with non-bullet lines",
			input:    "Here are the memories:\n- Important fact one\nSome explanation\n- Important fact two\n",
			expected: []string{"Important fact one", "Important fact two"},
		},
		{
			name:     "empty bullets skipped",
			input:    "- \n- real memory\n-  \n",
			expected: []string{"real memory"},
		},
		{
			name:     "NONE bullet skipped",
			input:    "- NONE\n- real memory\n",
			expected: []string{"real memory"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseExtractedMemories(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEntryMatchesProject(t *testing.T) {
	tests := []struct {
		name      string
		entry     MemoryEntry
		targetDir string
		expected  bool
	}{
		{
			name:      "global entry matches anything",
			entry:     MemoryEntry{Project: ""},
			targetDir: "/home/user/app",
			expected:  true,
		},
		{
			name:      "exact project match",
			entry:     MemoryEntry{Project: "/home/user/app"},
			targetDir: "/home/user/app",
			expected:  true,
		},
		{
			name:      "subdirectory match",
			entry:     MemoryEntry{Project: "/home/user/app"},
			targetDir: "/home/user/app/cmd/server",
			expected:  true,
		},
		{
			name:      "no match for different project",
			entry:     MemoryEntry{Project: "/home/user/app"},
			targetDir: "/home/user/other",
			expected:  false,
		},
		{
			name:      "no match for partial prefix",
			entry:     MemoryEntry{Project: "/home/user/app"},
			targetDir: "/home/user/app-extra",
			expected:  false,
		},
		{
			name:      "empty target matches all",
			entry:     MemoryEntry{Project: "/home/user/app"},
			targetDir: "",
			expected:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := entryMatchesProject(tt.entry, tt.targetDir)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFormatMemoryEntry_WithProject(t *testing.T) {
	entry := MemoryEntry{
		Timestamp: mustParseDate("2026-03-15"),
		Project:   "/home/user/myapp",
		Category:  "learning",
		Content:   "Build command: go build ./cmd/app",
	}
	result := formatMemoryEntry(entry)
	assert.Equal(t, "- [2026-03-15] project:/home/user/myapp | learning | Build command: go build ./cmd/app\n", result)
}

func TestFormatMemoryEntry_Global(t *testing.T) {
	entry := MemoryEntry{
		Timestamp: mustParseDate("2026-03-15"),
		Project:   "",
		Category:  "preference",
		Content:   "User prefers table-driven tests",
	}
	result := formatMemoryEntry(entry)
	assert.Equal(t, "- [2026-03-15] global | preference | User prefers table-driven tests\n", result)
}

func mustParseDate(s string) (t time.Time) {
	t, _ = time.Parse("2006-01-02", s)
	return
}
