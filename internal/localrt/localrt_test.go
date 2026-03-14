package localrt

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gajaai/openmarmut-go/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestRuntime(t *testing.T) (*LocalRuntime, string) {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	rt := New(dir, 30*time.Second, logger)
	require.NoError(t, rt.Init(context.Background()))
	return rt, dir
}

// --- Interface compliance ---

func TestLocalRuntime_ImplementsRuntime(t *testing.T) {
	var _ runtime.Runtime = (*LocalRuntime)(nil)
}

// --- Init / Close ---

func TestInit_ValidDirectory(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	rt := New(dir, 0, logger)

	err := rt.Init(context.Background())
	require.NoError(t, err)
	assert.True(t, rt.initialized)
}

func TestInit_NonExistentDirectory(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	rt := New("/nonexistent/path/xyz", 0, logger)

	err := rt.Init(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func TestInit_FileNotDirectory(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "notadir")
	require.NoError(t, os.WriteFile(filePath, []byte("hello"), 0644))

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	rt := New(filePath, 0, logger)

	err := rt.Init(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

func TestClose(t *testing.T) {
	rt, _ := newTestRuntime(t)
	assert.True(t, rt.initialized)

	err := rt.Close(context.Background())
	require.NoError(t, err)
	assert.False(t, rt.initialized)
}

func TestTargetDir(t *testing.T) {
	rt, dir := newTestRuntime(t)
	assert.Equal(t, dir, rt.TargetDir())
}

// --- Not initialized guard ---

func TestNotInitialized_AllMethods(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	rt := New(t.TempDir(), 0, logger)
	ctx := context.Background()

	_, err := rt.ReadFile(ctx, "file.txt")
	assert.ErrorIs(t, err, runtime.ErrRuntimeNotReady)

	err = rt.WriteFile(ctx, "file.txt", []byte("data"), 0644)
	assert.ErrorIs(t, err, runtime.ErrRuntimeNotReady)

	err = rt.DeleteFile(ctx, "file.txt")
	assert.ErrorIs(t, err, runtime.ErrRuntimeNotReady)

	_, err = rt.ListDir(ctx, ".")
	assert.ErrorIs(t, err, runtime.ErrRuntimeNotReady)

	err = rt.MkDir(ctx, "dir", 0755)
	assert.ErrorIs(t, err, runtime.ErrRuntimeNotReady)

	_, err = rt.Exec(ctx, "echo hi", runtime.ExecOpts{})
	assert.ErrorIs(t, err, runtime.ErrRuntimeNotReady)
}

// --- ReadFile ---

func TestReadFile_Success(t *testing.T) {
	rt, dir := newTestRuntime(t)
	content := []byte("hello world")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "test.txt"), content, 0644))

	data, err := rt.ReadFile(context.Background(), "test.txt")
	require.NoError(t, err)
	assert.Equal(t, content, data)
}

func TestReadFile_Nested(t *testing.T) {
	rt, dir := newTestRuntime(t)
	nested := filepath.Join(dir, "a", "b")
	require.NoError(t, os.MkdirAll(nested, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(nested, "deep.txt"), []byte("deep"), 0644))

	data, err := rt.ReadFile(context.Background(), "a/b/deep.txt")
	require.NoError(t, err)
	assert.Equal(t, []byte("deep"), data)
}

func TestReadFile_NotExist(t *testing.T) {
	rt, _ := newTestRuntime(t)
	_, err := rt.ReadFile(context.Background(), "missing.txt")
	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func TestReadFile_PathEscape(t *testing.T) {
	rt, _ := newTestRuntime(t)
	_, err := rt.ReadFile(context.Background(), "../../../etc/passwd")
	require.Error(t, err)
	assert.True(t, errors.Is(err, runtime.ErrPathEscape))
}

func TestReadFile_AbsolutePath(t *testing.T) {
	rt, _ := newTestRuntime(t)
	_, err := rt.ReadFile(context.Background(), "/etc/passwd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path must be relative")
}

func TestReadFile_CancelledContext(t *testing.T) {
	rt, _ := newTestRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := rt.ReadFile(ctx, "file.txt")
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
}

// --- WriteFile ---

func TestWriteFile_CreateNew(t *testing.T) {
	rt, dir := newTestRuntime(t)
	data := []byte("new content")

	err := rt.WriteFile(context.Background(), "new.txt", data, 0644)
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

func TestWriteFile_Overwrite(t *testing.T) {
	rt, dir := newTestRuntime(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "existing.txt"), []byte("old"), 0644))

	err := rt.WriteFile(context.Background(), "existing.txt", []byte("new"), 0644)
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(dir, "existing.txt"))
	require.NoError(t, err)
	assert.Equal(t, []byte("new"), got)
}

func TestWriteFile_CreatesParentDirs(t *testing.T) {
	rt, dir := newTestRuntime(t)

	err := rt.WriteFile(context.Background(), "a/b/c/file.txt", []byte("deep"), 0644)
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(dir, "a", "b", "c", "file.txt"))
	require.NoError(t, err)
	assert.Equal(t, []byte("deep"), got)
}

func TestWriteFile_AtomicNoPartialOnFailure(t *testing.T) {
	rt, dir := newTestRuntime(t)

	// Verify no temp files are left behind on path escape error.
	err := rt.WriteFile(context.Background(), "../../escape.txt", []byte("bad"), 0644)
	require.Error(t, err)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, filepath.Ext(e.Name()) == "" && len(e.Name()) > 10,
			"unexpected temp file left behind: %s", e.Name())
	}
}

func TestWriteFile_PermissionsPreserved(t *testing.T) {
	rt, dir := newTestRuntime(t)

	err := rt.WriteFile(context.Background(), "script.sh", []byte("#!/bin/sh"), 0755)
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(dir, "script.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm())
}

func TestWriteFile_PathEscape(t *testing.T) {
	rt, _ := newTestRuntime(t)
	err := rt.WriteFile(context.Background(), "../escape.txt", []byte("bad"), 0644)
	require.Error(t, err)
	assert.True(t, errors.Is(err, runtime.ErrPathEscape))
}

func TestWriteFile_BinaryData(t *testing.T) {
	rt, dir := newTestRuntime(t)
	data := make([]byte, 256)
	for i := range data {
		data[i] = byte(i)
	}

	err := rt.WriteFile(context.Background(), "binary.bin", data, 0644)
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(dir, "binary.bin"))
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

func TestWriteFile_EmptyFile(t *testing.T) {
	rt, dir := newTestRuntime(t)

	err := rt.WriteFile(context.Background(), "empty.txt", []byte{}, 0644)
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(dir, "empty.txt"))
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestWriteFile_CancelledContext(t *testing.T) {
	rt, _ := newTestRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := rt.WriteFile(ctx, "file.txt", []byte("data"), 0644)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
}

// --- DeleteFile ---

func TestDeleteFile_Success(t *testing.T) {
	rt, dir := newTestRuntime(t)
	filePath := filepath.Join(dir, "todelete.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("bye"), 0644))

	err := rt.DeleteFile(context.Background(), "todelete.txt")
	require.NoError(t, err)

	_, err = os.Stat(filePath)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func TestDeleteFile_NotExist(t *testing.T) {
	rt, _ := newTestRuntime(t)
	err := rt.DeleteFile(context.Background(), "nonexistent.txt")
	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func TestDeleteFile_PathEscape(t *testing.T) {
	rt, _ := newTestRuntime(t)
	err := rt.DeleteFile(context.Background(), "../../important")
	require.Error(t, err)
	assert.True(t, errors.Is(err, runtime.ErrPathEscape))
}

// --- ListDir ---

func TestListDir_Success(t *testing.T) {
	rt, dir := newTestRuntime(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file1.txt"), []byte("a"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file2.txt"), []byte("bb"), 0644))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "subdir"), 0755))

	entries, err := rt.ListDir(context.Background(), ".")
	require.NoError(t, err)
	assert.Len(t, entries, 3)

	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name] = true
		if e.Name == "subdir" {
			assert.True(t, e.IsDir)
		}
		if e.Name == "file1.txt" {
			assert.False(t, e.IsDir)
			assert.Equal(t, int64(1), e.Size)
		}
		if e.Name == "file2.txt" {
			assert.Equal(t, int64(2), e.Size)
		}
	}
	assert.True(t, names["file1.txt"])
	assert.True(t, names["file2.txt"])
	assert.True(t, names["subdir"])
}

func TestListDir_Empty(t *testing.T) {
	rt, _ := newTestRuntime(t)
	entries, err := rt.ListDir(context.Background(), ".")
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestListDir_NotExist(t *testing.T) {
	rt, _ := newTestRuntime(t)
	_, err := rt.ListDir(context.Background(), "nonexistent")
	require.Error(t, err)
}

func TestListDir_PathEscape(t *testing.T) {
	rt, _ := newTestRuntime(t)
	_, err := rt.ListDir(context.Background(), "../../..")
	require.Error(t, err)
	assert.True(t, errors.Is(err, runtime.ErrPathEscape))
}

// --- MkDir ---

func TestMkDir_Simple(t *testing.T) {
	rt, dir := newTestRuntime(t)

	err := rt.MkDir(context.Background(), "newdir", 0755)
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(dir, "newdir"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestMkDir_Nested(t *testing.T) {
	rt, dir := newTestRuntime(t)

	err := rt.MkDir(context.Background(), "a/b/c", 0755)
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(dir, "a", "b", "c"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestMkDir_AlreadyExists(t *testing.T) {
	rt, dir := newTestRuntime(t)
	require.NoError(t, os.Mkdir(filepath.Join(dir, "existing"), 0755))

	err := rt.MkDir(context.Background(), "existing", 0755)
	require.NoError(t, err)
}

func TestMkDir_PathEscape(t *testing.T) {
	rt, _ := newTestRuntime(t)
	err := rt.MkDir(context.Background(), "../../escape", 0755)
	require.Error(t, err)
	assert.True(t, errors.Is(err, runtime.ErrPathEscape))
}

// --- Exec ---

func TestExec_SimpleCommand(t *testing.T) {
	rt, _ := newTestRuntime(t)

	result, err := rt.Exec(context.Background(), "echo hello", runtime.ExecOpts{})
	require.NoError(t, err)
	assert.Equal(t, 0, result.ExitCode)
	assert.Equal(t, "hello\n", result.Stdout)
	assert.Empty(t, result.Stderr)
	assert.True(t, result.Duration > 0)
}

func TestExec_NonZeroExitIsNotError(t *testing.T) {
	rt, _ := newTestRuntime(t)

	result, err := rt.Exec(context.Background(), "exit 42", runtime.ExecOpts{})
	require.NoError(t, err, "non-zero exit must NOT be returned as error")
	assert.Equal(t, 42, result.ExitCode)
}

func TestExec_StderrCaptured(t *testing.T) {
	rt, _ := newTestRuntime(t)

	result, err := rt.Exec(context.Background(), "echo err >&2", runtime.ExecOpts{})
	require.NoError(t, err)
	assert.Equal(t, 0, result.ExitCode)
	assert.Equal(t, "err\n", result.Stderr)
}

func TestExec_StdoutAndStderrSeparate(t *testing.T) {
	rt, _ := newTestRuntime(t)

	result, err := rt.Exec(context.Background(), "echo out && echo err >&2", runtime.ExecOpts{})
	require.NoError(t, err)
	assert.Equal(t, "out\n", result.Stdout)
	assert.Equal(t, "err\n", result.Stderr)
}

func TestExec_WorkDir(t *testing.T) {
	rt, dir := newTestRuntime(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0755))

	result, err := rt.Exec(context.Background(), "pwd", runtime.ExecOpts{RelDir: "sub"})
	require.NoError(t, err)
	assert.Contains(t, result.Stdout, "sub")
}

func TestExec_WorkDirPathEscape(t *testing.T) {
	rt, _ := newTestRuntime(t)

	_, err := rt.Exec(context.Background(), "pwd", runtime.ExecOpts{RelDir: "../../"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, runtime.ErrPathEscape))
}

func TestExec_EnvVars(t *testing.T) {
	rt, _ := newTestRuntime(t)

	result, err := rt.Exec(context.Background(), "echo $MY_VAR", runtime.ExecOpts{
		Env: []string{"MY_VAR=test_value"},
	})
	require.NoError(t, err)
	assert.Equal(t, "test_value\n", result.Stdout)
}

func TestExec_Timeout(t *testing.T) {
	rt, _ := newTestRuntime(t)

	_, err := rt.Exec(context.Background(), "sleep 10", runtime.ExecOpts{
		Timeout: 100 * time.Millisecond,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
}

func TestExec_DefaultTimeout(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	rt := New(dir, 200*time.Millisecond, logger)
	require.NoError(t, rt.Init(context.Background()))

	_, err := rt.Exec(context.Background(), "sleep 10", runtime.ExecOpts{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
}

func TestExec_TimeoutOverridesDefault(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	// Short default timeout, but command-specific override is longer.
	rt := New(dir, 50*time.Millisecond, logger)
	require.NoError(t, rt.Init(context.Background()))

	result, err := rt.Exec(context.Background(), "echo fast", runtime.ExecOpts{
		Timeout: 5 * time.Second,
	})
	require.NoError(t, err)
	assert.Equal(t, "fast\n", result.Stdout)
}

func TestExec_CancelledContext(t *testing.T) {
	rt, _ := newTestRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := rt.Exec(ctx, "sleep 10", runtime.ExecOpts{})
	require.Error(t, err)
}

// --- Round-trip integration ---

func TestRoundTrip_WriteReadDelete(t *testing.T) {
	rt, _ := newTestRuntime(t)
	ctx := context.Background()

	data := []byte("round trip content")
	require.NoError(t, rt.WriteFile(ctx, "round.txt", data, 0644))

	got, err := rt.ReadFile(ctx, "round.txt")
	require.NoError(t, err)
	assert.Equal(t, data, got)

	require.NoError(t, rt.DeleteFile(ctx, "round.txt"))

	_, err = rt.ReadFile(ctx, "round.txt")
	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func TestRoundTrip_MkDirThenListDir(t *testing.T) {
	rt, _ := newTestRuntime(t)
	ctx := context.Background()

	require.NoError(t, rt.MkDir(ctx, "mydir", 0755))
	require.NoError(t, rt.WriteFile(ctx, "mydir/file.txt", []byte("in subdir"), 0644))

	entries, err := rt.ListDir(ctx, "mydir")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "file.txt", entries[0].Name)
	assert.False(t, entries[0].IsDir)
}

func TestRoundTrip_ExecCreatesFile(t *testing.T) {
	rt, _ := newTestRuntime(t)
	ctx := context.Background()

	result, err := rt.Exec(ctx, "echo created > created.txt", runtime.ExecOpts{})
	require.NoError(t, err)
	assert.Equal(t, 0, result.ExitCode)

	data, err := rt.ReadFile(ctx, "created.txt")
	require.NoError(t, err)
	assert.Equal(t, "created\n", string(data))
}
