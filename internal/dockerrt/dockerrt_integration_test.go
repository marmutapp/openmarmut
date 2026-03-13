//go:build integration && docker

package dockerrt

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/gajaai/opencode-go/internal/config"
	"github.com/gajaai/opencode-go/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newIntegrationRuntime(t *testing.T) *DockerRuntime {
	t.Helper()
	dir := t.TempDir()

	cfg := config.DockerConfig{
		Image:       "ubuntu:20.04",
		MountPath:   "/workspace",
		Shell:       "sh",
		NetworkMode: "none",
	}

	rt := New(dir, cfg, 30*time.Second, testLogger)
	ctx := context.Background()

	err := rt.Init(ctx)
	if err != nil {
		t.Skipf("skipping integration test — Docker not available: %v", err)
	}

	t.Cleanup(func() {
		_ = rt.Close(context.Background())
	})

	return rt
}

// --- Init/Close ---

func TestIntegration_InitClose(t *testing.T) {
	rt := newIntegrationRuntime(t)
	assert.True(t, rt.initialized)
	assert.NotEmpty(t, rt.containerID)
}

// --- Exec ---

func TestIntegration_Exec_Echo(t *testing.T) {
	rt := newIntegrationRuntime(t)

	result, err := rt.Exec(context.Background(), "echo hello", runtime.ExecOpts{})
	require.NoError(t, err)
	assert.Equal(t, "hello\n", result.Stdout)
	assert.Equal(t, 0, result.ExitCode)
}

func TestIntegration_Exec_NonZeroExit(t *testing.T) {
	rt := newIntegrationRuntime(t)

	result, err := rt.Exec(context.Background(), "exit 42", runtime.ExecOpts{})
	require.NoError(t, err)
	assert.Equal(t, 42, result.ExitCode)
}

func TestIntegration_Exec_Stderr(t *testing.T) {
	rt := newIntegrationRuntime(t)

	result, err := rt.Exec(context.Background(), "echo err >&2", runtime.ExecOpts{})
	require.NoError(t, err)
	assert.Equal(t, "err\n", result.Stderr)
	assert.Empty(t, result.Stdout)
}

func TestIntegration_Exec_EnvVars(t *testing.T) {
	rt := newIntegrationRuntime(t)

	result, err := rt.Exec(context.Background(), "echo $MY_VAR", runtime.ExecOpts{
		Env: []string{"MY_VAR=hello"},
	})
	require.NoError(t, err)
	assert.Equal(t, "hello\n", result.Stdout)
}

func TestIntegration_Exec_WorkDir(t *testing.T) {
	rt := newIntegrationRuntime(t)

	// Create a subdirectory first.
	err := rt.MkDir(context.Background(), "subdir", 0o755)
	require.NoError(t, err)

	result, err := rt.Exec(context.Background(), "pwd", runtime.ExecOpts{
		RelDir: "subdir",
	})
	require.NoError(t, err)
	assert.Equal(t, "/workspace/subdir\n", result.Stdout)
}

// --- File Operations ---

func TestIntegration_WriteReadDelete(t *testing.T) {
	rt := newIntegrationRuntime(t)
	ctx := context.Background()

	content := []byte("hello integration test")

	// Write.
	err := rt.WriteFile(ctx, "test.txt", content, 0o644)
	require.NoError(t, err)

	// Read.
	data, err := rt.ReadFile(ctx, "test.txt")
	require.NoError(t, err)
	assert.Equal(t, content, data)

	// Delete.
	err = rt.DeleteFile(ctx, "test.txt")
	require.NoError(t, err)

	// Verify deleted.
	_, err = rt.ReadFile(ctx, "test.txt")
	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func TestIntegration_WriteFile_Binary(t *testing.T) {
	rt := newIntegrationRuntime(t)
	ctx := context.Background()

	binaryData := make([]byte, 256)
	for i := range binaryData {
		binaryData[i] = byte(i)
	}

	err := rt.WriteFile(ctx, "binary.bin", binaryData, 0o644)
	require.NoError(t, err)

	data, err := rt.ReadFile(ctx, "binary.bin")
	require.NoError(t, err)
	assert.Equal(t, binaryData, data)
}

func TestIntegration_WriteFile_NestedDirs(t *testing.T) {
	rt := newIntegrationRuntime(t)
	ctx := context.Background()

	err := rt.WriteFile(ctx, "a/b/c/deep.txt", []byte("deep"), 0o644)
	require.NoError(t, err)

	data, err := rt.ReadFile(ctx, "a/b/c/deep.txt")
	require.NoError(t, err)
	assert.Equal(t, []byte("deep"), data)
}

func TestIntegration_ReadFile_NotExist(t *testing.T) {
	rt := newIntegrationRuntime(t)

	_, err := rt.ReadFile(context.Background(), "nonexistent.txt")
	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func TestIntegration_DeleteFile_NotExist(t *testing.T) {
	rt := newIntegrationRuntime(t)

	err := rt.DeleteFile(context.Background(), "nonexistent.txt")
	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

// --- MkDir/ListDir ---

func TestIntegration_MkDirListDir(t *testing.T) {
	rt := newIntegrationRuntime(t)
	ctx := context.Background()

	err := rt.MkDir(ctx, "testdir", 0o755)
	require.NoError(t, err)

	// Write a file inside.
	err = rt.WriteFile(ctx, "testdir/file.txt", []byte("content"), 0o644)
	require.NoError(t, err)

	// Create a subdir.
	err = rt.MkDir(ctx, "testdir/sub", 0o755)
	require.NoError(t, err)

	entries, err := rt.ListDir(ctx, "testdir")
	require.NoError(t, err)
	require.Len(t, entries, 2)

	// Find the file and dir entries (order not guaranteed).
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
		if e.Name == "file.txt" {
			assert.False(t, e.IsDir)
			assert.Equal(t, int64(7), e.Size) // "content" is 7 bytes
		}
		if e.Name == "sub" {
			assert.True(t, e.IsDir)
		}
	}
	assert.True(t, names["file.txt"])
	assert.True(t, names["sub"])
}

func TestIntegration_ListDir_Empty(t *testing.T) {
	rt := newIntegrationRuntime(t)
	ctx := context.Background()

	err := rt.MkDir(ctx, "emptydir", 0o755)
	require.NoError(t, err)

	entries, err := rt.ListDir(ctx, "emptydir")
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestIntegration_ListDir_NotExist(t *testing.T) {
	rt := newIntegrationRuntime(t)

	_, err := rt.ListDir(context.Background(), "nonexistent")
	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

// --- Path Escape ---

func TestIntegration_PathEscape(t *testing.T) {
	rt := newIntegrationRuntime(t)
	ctx := context.Background()

	_, err := rt.ReadFile(ctx, "../../../etc/passwd")
	require.ErrorIs(t, err, runtime.ErrPathEscape)

	err = rt.WriteFile(ctx, "/etc/passwd", []byte("bad"), 0o644)
	require.ErrorIs(t, err, runtime.ErrPathEscape)

	_, err = rt.Exec(ctx, "ls", runtime.ExecOpts{RelDir: "../../"})
	require.ErrorIs(t, err, runtime.ErrPathEscape)
}

// --- TargetDir ---

func TestIntegration_TargetDir(t *testing.T) {
	rt := newIntegrationRuntime(t)
	assert.NotEmpty(t, rt.TargetDir())
}
