package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marmutapp/openmarmut/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewID(t *testing.T) {
	id1 := NewID()
	id2 := NewID()
	assert.Len(t, id1, 8)
	assert.Len(t, id2, 8)
	assert.NotEqual(t, id1, id2)
}

func TestSessionSummary(t *testing.T) {
	s := &Session{
		ID:        "abc12345",
		Name:      "test-session",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Mode:      "local",
		TargetDir: "/tmp/project",
		Provider:  "test-provider",
		Model:     "test-model",
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "system"},
			{Role: llm.RoleUser, Content: "hello"},
			{Role: llm.RoleAssistant, Content: "hi"},
		},
		ToolCalls:   5,
		TotalTokens: 1000,
	}

	sum := s.Summary()
	assert.Equal(t, "abc12345", sum.ID)
	assert.Equal(t, "test-session", sum.Name)
	assert.Equal(t, "test-provider", sum.Provider)
	assert.Equal(t, 3, sum.Messages)
	assert.Equal(t, 5, sum.ToolCalls)
}

func TestSessionUserTurns(t *testing.T) {
	s := &Session{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "system"},
			{Role: llm.RoleUser, Content: "q1"},
			{Role: llm.RoleAssistant, Content: "a1"},
			{Role: llm.RoleUser, Content: "q2"},
			{Role: llm.RoleAssistant, Content: "a2"},
		},
	}
	assert.Equal(t, 2, s.UserTurns())
}

func TestSessionDisplayName(t *testing.T) {
	s := &Session{Name: "my-session"}
	assert.Equal(t, "my-session", s.DisplayName())

	s.Name = ""
	assert.Equal(t, "(unnamed)", s.DisplayName())
}

// Override sessionsDir for tests by using a temp directory.
func setupTestDir(t *testing.T) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	sessDir := filepath.Join(dir, ".openmarmut", "sessions")
	require.NoError(t, os.MkdirAll(sessDir, 0o755))

	// Override HOME so sessionsDir() uses the temp dir.
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)

	return dir, func() {
		os.Setenv("HOME", origHome)
	}
}

func makeSession(id, name, targetDir string) *Session {
	return &Session{
		ID:        id,
		Name:      name,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Mode:      "local",
		TargetDir: targetDir,
		Provider:  "test",
		Model:     "test-model",
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "system"},
			{Role: llm.RoleUser, Content: "hello"},
			{Role: llm.RoleAssistant, Content: "hi"},
		},
		Metadata: map[string]string{},
	}
}

func TestSaveAndLoad(t *testing.T) {
	_, cleanup := setupTestDir(t)
	defer cleanup()

	s := makeSession("test0001", "my-session", "/tmp/project")
	require.NoError(t, Save(s))

	loaded, err := Load("test0001")
	require.NoError(t, err)
	assert.Equal(t, "test0001", loaded.ID)
	assert.Equal(t, "my-session", loaded.Name)
	assert.Equal(t, "local", loaded.Mode)
	assert.Equal(t, "/tmp/project", loaded.TargetDir)
	assert.Len(t, loaded.Messages, 3)
}

func TestLoadNotFound(t *testing.T) {
	_, cleanup := setupTestDir(t)
	defer cleanup()

	_, err := Load("nonexistent")
	require.Error(t, err)
}

func TestDelete(t *testing.T) {
	_, cleanup := setupTestDir(t)
	defer cleanup()

	s := makeSession("del00001", "delete-me", "/tmp")
	require.NoError(t, Save(s))

	require.NoError(t, Delete("del00001"))

	_, err := Load("del00001")
	require.Error(t, err)
}

func TestDeleteNotFound(t *testing.T) {
	_, cleanup := setupTestDir(t)
	defer cleanup()

	err := Delete("nonexistent")
	require.Error(t, err)
}

func TestList(t *testing.T) {
	_, cleanup := setupTestDir(t)
	defer cleanup()

	s1 := makeSession("list0001", "first", "/tmp/a")
	s1.UpdatedAt = time.Now().Add(-2 * time.Hour)
	require.NoError(t, Save(s1))

	s2 := makeSession("list0002", "second", "/tmp/b")
	s2.UpdatedAt = time.Now().Add(-1 * time.Hour)
	require.NoError(t, Save(s2))

	summaries, err := List()
	require.NoError(t, err)
	require.Len(t, summaries, 2)
	// Most recent first.
	assert.Equal(t, "list0002", summaries[0].ID)
	assert.Equal(t, "list0001", summaries[1].ID)
}

func TestListEmpty(t *testing.T) {
	_, cleanup := setupTestDir(t)
	defer cleanup()

	summaries, err := List()
	require.NoError(t, err)
	assert.Empty(t, summaries)
}

func TestFindRecent(t *testing.T) {
	_, cleanup := setupTestDir(t)
	defer cleanup()

	for i := 0; i < 5; i++ {
		s := makeSession(NewID(), "", "/tmp")
		s.UpdatedAt = time.Now().Add(-time.Duration(i) * time.Hour)
		require.NoError(t, Save(s))
	}

	recent, err := FindRecent(3)
	require.NoError(t, err)
	assert.Len(t, recent, 3)
}

func TestFindByTarget(t *testing.T) {
	_, cleanup := setupTestDir(t)
	defer cleanup()

	require.NoError(t, Save(makeSession("tgt00001", "a", "/home/user/project")))
	require.NoError(t, Save(makeSession("tgt00002", "b", "/home/user/other")))
	require.NoError(t, Save(makeSession("tgt00003", "c", "/home/user/project")))

	matched, err := FindByTarget("/home/user/project")
	require.NoError(t, err)
	assert.Len(t, matched, 2)

	matched, err = FindByTarget("/nonexistent")
	require.NoError(t, err)
	assert.Empty(t, matched)
}

func TestCleanup(t *testing.T) {
	_, cleanup := setupTestDir(t)
	defer cleanup()

	// Old session (40 days ago) — save first, then overwrite JSON with old timestamp.
	old := makeSession("old00001", "old", "/tmp")
	require.NoError(t, Save(old))
	// Re-load, set old timestamp, and re-write directly.
	old.UpdatedAt = time.Now().AddDate(0, 0, -40)
	saveWithoutTimestampUpdate(t, old)

	// Recent session (1 day ago).
	recent := makeSession("new00001", "new", "/tmp")
	require.NoError(t, Save(recent))

	deleted, err := Cleanup(30)
	require.NoError(t, err)
	assert.Equal(t, 1, deleted)

	// Old session should be gone, recent should remain.
	_, err = Load("old00001")
	require.Error(t, err)

	_, err = Load("new00001")
	require.NoError(t, err)
}

func TestSessionPathTraversal(t *testing.T) {
	_, err := sessionPath("../../../etc/passwd")
	require.Error(t, err)

	_, err = sessionPath("foo/bar")
	require.Error(t, err)
}

// saveWithoutTimestampUpdate writes the session JSON directly without updating UpdatedAt.
func saveWithoutTimestampUpdate(t *testing.T, s *Session) {
	t.Helper()
	dir, err := sessionsDir()
	require.NoError(t, err)
	data, err := json.MarshalIndent(s, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, s.ID+".json"), data, 0o644))
}

func TestSaveUpdatesTimestamp(t *testing.T) {
	_, cleanup := setupTestDir(t)
	defer cleanup()

	s := makeSession("ts000001", "timestamp", "/tmp")
	before := time.Now().Add(-time.Second)
	require.NoError(t, Save(s))

	loaded, err := Load("ts000001")
	require.NoError(t, err)
	assert.True(t, loaded.UpdatedAt.After(before))
}
