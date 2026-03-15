package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInputHistory_AddAndNavigate(t *testing.T) {
	h := &inputHistory{index: -1}

	h.Add("hello")
	h.Add("world")

	// Previous (Up) returns newest first.
	msg, ok := h.Previous()
	assert.True(t, ok)
	assert.Equal(t, "world", msg)

	msg, ok = h.Previous()
	assert.True(t, ok)
	assert.Equal(t, "hello", msg)

	// At oldest, stays put.
	msg, ok = h.Previous()
	assert.True(t, ok)
	assert.Equal(t, "hello", msg)

	// Next (Down) moves forward.
	msg, ok = h.Next()
	assert.True(t, ok)
	assert.Equal(t, "world", msg)

	// Past newest returns empty.
	msg, ok = h.Next()
	assert.False(t, ok)
	assert.Equal(t, "", msg)
}

func TestInputHistory_SkipDuplicate(t *testing.T) {
	h := &inputHistory{index: -1}

	h.Add("hello")
	h.Add("hello")

	entries := h.Entries()
	assert.Len(t, entries, 1)
}

func TestInputHistory_SkipEmpty(t *testing.T) {
	h := &inputHistory{index: -1}

	h.Add("")
	h.Add("  ")

	entries := h.Entries()
	assert.Empty(t, entries)
}

func TestInputHistory_MaxEntries(t *testing.T) {
	h := &inputHistory{index: -1}

	for i := 0; i < maxHistoryEntries+100; i++ {
		h.Add(string(rune('a' + i%26)))
	}

	// Due to dedup, we won't have maxHistoryEntries+100, but let's just check it's bounded.
	entries := h.Entries()
	assert.LessOrEqual(t, len(entries), maxHistoryEntries)
}

func TestInputHistory_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history")

	h := &inputHistory{index: -1, path: path}
	h.Add("first")
	h.Add("second")
	h.Add("third")

	err := h.Save()
	require.NoError(t, err)

	// Verify file exists.
	_, err = os.Stat(path)
	require.NoError(t, err)

	// Load into new history.
	h2 := &inputHistory{index: -1, path: path}
	h2.load()

	assert.Equal(t, h.Entries(), h2.Entries())
}

func TestInputHistory_Reset(t *testing.T) {
	h := &inputHistory{index: -1}
	h.Add("one")
	h.Add("two")

	h.Previous()
	h.Reset()

	// After reset, Previous should start from the end again.
	msg, ok := h.Previous()
	assert.True(t, ok)
	assert.Equal(t, "two", msg)
}

func TestInputHistory_EmptyNavigation(t *testing.T) {
	h := &inputHistory{index: -1}

	_, ok := h.Previous()
	assert.False(t, ok)

	_, ok = h.Next()
	assert.False(t, ok)
}
