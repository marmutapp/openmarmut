package dockerrt

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/gajaai/opencode-go/internal/config"
	"github.com/gajaai/opencode-go/internal/runtime"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock Docker Client ---

type mockDockerClient struct {
	imageInspectFn     func(ctx context.Context, imageID string) (image.InspectResponse, []byte, error)
	imagePullFn        func(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error)
	containerCreateFn  func(ctx context.Context, cfg *container.Config, hostCfg *container.HostConfig, networkCfg *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	containerStartFn   func(ctx context.Context, containerID string, options container.StartOptions) error
	containerStopFn    func(ctx context.Context, containerID string, options container.StopOptions) error
	containerRemoveFn  func(ctx context.Context, containerID string, options container.RemoveOptions) error
	containerInspectFn func(ctx context.Context, containerID string) (container.InspectResponse, error)
	execCreateFn       func(ctx context.Context, containerID string, options container.ExecOptions) (container.ExecCreateResponse, error)
	execAttachFn       func(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error)
	execInspectFn      func(ctx context.Context, execID string) (container.ExecInspect, error)
}

func (m *mockDockerClient) ImageInspectWithRaw(ctx context.Context, imageID string) (image.InspectResponse, []byte, error) {
	if m.imageInspectFn != nil {
		return m.imageInspectFn(ctx, imageID)
	}
	return image.InspectResponse{}, nil, nil
}

func (m *mockDockerClient) ImagePull(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error) {
	if m.imagePullFn != nil {
		return m.imagePullFn(ctx, refStr, options)
	}
	return io.NopCloser(strings.NewReader("")), nil
}

func (m *mockDockerClient) ContainerCreate(ctx context.Context, cfg *container.Config, hostCfg *container.HostConfig, networkCfg *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
	if m.containerCreateFn != nil {
		return m.containerCreateFn(ctx, cfg, hostCfg, networkCfg, platform, containerName)
	}
	return container.CreateResponse{ID: "test-container-id"}, nil
}

func (m *mockDockerClient) ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error {
	if m.containerStartFn != nil {
		return m.containerStartFn(ctx, containerID, options)
	}
	return nil
}

func (m *mockDockerClient) ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error {
	if m.containerStopFn != nil {
		return m.containerStopFn(ctx, containerID, options)
	}
	return nil
}

func (m *mockDockerClient) ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error {
	if m.containerRemoveFn != nil {
		return m.containerRemoveFn(ctx, containerID, options)
	}
	return nil
}

func (m *mockDockerClient) ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error) {
	if m.containerInspectFn != nil {
		return m.containerInspectFn(ctx, containerID)
	}
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			State: &container.State{Running: true},
		},
	}, nil
}

func (m *mockDockerClient) ContainerExecCreate(ctx context.Context, containerID string, options container.ExecOptions) (container.ExecCreateResponse, error) {
	if m.execCreateFn != nil {
		return m.execCreateFn(ctx, containerID, options)
	}
	return container.ExecCreateResponse{ID: "test-exec-id"}, nil
}

func (m *mockDockerClient) ContainerExecAttach(ctx context.Context, execID string, cfg container.ExecAttachOptions) (types.HijackedResponse, error) {
	if m.execAttachFn != nil {
		return m.execAttachFn(ctx, execID, cfg)
	}
	return newMockHijackedResponse("", ""), nil
}

func (m *mockDockerClient) ContainerExecInspect(ctx context.Context, execID string) (container.ExecInspect, error) {
	if m.execInspectFn != nil {
		return m.execInspectFn(ctx, execID)
	}
	return container.ExecInspect{ExitCode: 0}, nil
}

// --- Test Helpers ---

var testLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

func testConfig() config.DockerConfig {
	return config.DockerConfig{
		Image:       "ubuntu:24.04",
		MountPath:   "/workspace",
		Shell:       "sh",
		NetworkMode: "none",
	}
}

// newMockHijackedResponse creates a HijackedResponse with docker-multiplexed stdout/stderr.
func newMockHijackedResponse(stdout, stderr string) types.HijackedResponse {
	var buf bytes.Buffer
	if stdout != "" {
		writeDockerFrame(&buf, 1, []byte(stdout)) // stdout stream type = 1
	}
	if stderr != "" {
		writeDockerFrame(&buf, 2, []byte(stderr)) // stderr stream type = 2
	}

	pr, pw := net.Pipe()
	go func() {
		_, _ = pw.Write(buf.Bytes())
		pw.Close()
	}()

	return types.HijackedResponse{
		Conn:   pr,
		Reader: bufio.NewReader(pr),
	}
}

// writeDockerFrame writes a Docker multiplexed stream frame.
// Header: [stream_type(1byte), 0, 0, 0, size(4bytes big-endian)]
func writeDockerFrame(w *bytes.Buffer, streamType byte, data []byte) {
	header := make([]byte, 8)
	header[0] = streamType
	binary.BigEndian.PutUint32(header[4:], uint32(len(data)))
	w.Write(header)
	w.Write(data)
}

func initRuntime(t *testing.T, mock *mockDockerClient) *DockerRuntime {
	t.Helper()
	rt := newWithClient("/tmp/test", testConfig(), 30*time.Second, testLogger, mock)
	err := rt.Init(context.Background())
	require.NoError(t, err)
	return rt
}

// mockExecPipeline sets up the mock to handle a single exec call returning given stdout/stderr/exitCode.
func mockExecPipeline(mock *mockDockerClient, stdout, stderr string, exitCode int) {
	mock.execCreateFn = func(ctx context.Context, containerID string, options container.ExecOptions) (container.ExecCreateResponse, error) {
		return container.ExecCreateResponse{ID: "exec-id"}, nil
	}
	mock.execAttachFn = func(ctx context.Context, execID string, cfg container.ExecAttachOptions) (types.HijackedResponse, error) {
		return newMockHijackedResponse(stdout, stderr), nil
	}
	mock.execInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
		return container.ExecInspect{ExitCode: exitCode}, nil
	}
}

// --- Init Tests ---

func TestDockerRuntime_Init_Success(t *testing.T) {
	mock := &mockDockerClient{}
	rt := newWithClient("/tmp/test", testConfig(), 30*time.Second, testLogger, mock)
	err := rt.Init(context.Background())

	require.NoError(t, err)
	assert.True(t, rt.initialized)
	assert.NotEmpty(t, rt.containerID)
}

func TestDockerRuntime_Init_ImagePull(t *testing.T) {
	pullCalled := false
	mock := &mockDockerClient{
		imageInspectFn: func(ctx context.Context, imageID string) (image.InspectResponse, []byte, error) {
			return image.InspectResponse{}, nil, errors.New("image not found")
		},
		imagePullFn: func(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error) {
			pullCalled = true
			assert.Equal(t, "ubuntu:24.04", refStr)
			return io.NopCloser(strings.NewReader("{}")), nil
		},
	}

	rt := newWithClient("/tmp/test", testConfig(), 30*time.Second, testLogger, mock)
	err := rt.Init(context.Background())

	require.NoError(t, err)
	assert.True(t, pullCalled)
}

func TestDockerRuntime_Init_PullFails(t *testing.T) {
	mock := &mockDockerClient{
		imageInspectFn: func(ctx context.Context, imageID string) (image.InspectResponse, []byte, error) {
			return image.InspectResponse{}, nil, errors.New("not found")
		},
		imagePullFn: func(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error) {
			return nil, errors.New("network error")
		},
	}

	rt := newWithClient("/tmp/test", testConfig(), 30*time.Second, testLogger, mock)
	err := rt.Init(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "pull image")
	assert.False(t, rt.initialized)
}

func TestDockerRuntime_Init_CreateFails(t *testing.T) {
	mock := &mockDockerClient{
		containerCreateFn: func(ctx context.Context, cfg *container.Config, hostCfg *container.HostConfig, networkCfg *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			return container.CreateResponse{}, errors.New("create failed")
		},
	}

	rt := newWithClient("/tmp/test", testConfig(), 30*time.Second, testLogger, mock)
	err := rt.Init(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "create container")
}

func TestDockerRuntime_Init_StartFails_CleansUp(t *testing.T) {
	removeCalled := false
	mock := &mockDockerClient{
		containerStartFn: func(ctx context.Context, containerID string, options container.StartOptions) error {
			return errors.New("start failed")
		},
		containerRemoveFn: func(ctx context.Context, containerID string, options container.RemoveOptions) error {
			removeCalled = true
			assert.True(t, options.Force)
			return nil
		},
	}

	rt := newWithClient("/tmp/test", testConfig(), 30*time.Second, testLogger, mock)
	err := rt.Init(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "start container")
	assert.True(t, removeCalled)
}

func TestDockerRuntime_Init_ContainerConfig(t *testing.T) {
	var capturedCfg *container.Config
	var capturedHostCfg *container.HostConfig

	mock := &mockDockerClient{
		containerCreateFn: func(ctx context.Context, cfg *container.Config, hostCfg *container.HostConfig, networkCfg *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
			capturedCfg = cfg
			capturedHostCfg = hostCfg
			return container.CreateResponse{ID: "test-id"}, nil
		},
	}

	rt := newWithClient("/tmp/test", testConfig(), 30*time.Second, testLogger, mock)
	err := rt.Init(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "ubuntu:24.04", capturedCfg.Image)
	assert.Equal(t, []string{"sleep", "infinity"}, []string(capturedCfg.Cmd))
	assert.Equal(t, "/workspace", capturedCfg.WorkingDir)
	assert.Contains(t, capturedHostCfg.Binds[0], "/tmp/test:/workspace")
	assert.Equal(t, container.NetworkMode("none"), capturedHostCfg.NetworkMode)
}

// --- Close Tests ---

func TestDockerRuntime_Close_Success(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)

	stopCalled := false
	removeCalled := false
	mock.containerStopFn = func(ctx context.Context, containerID string, options container.StopOptions) error {
		stopCalled = true
		require.NotNil(t, options.Timeout)
		assert.Equal(t, 5, *options.Timeout)
		return nil
	}
	mock.containerRemoveFn = func(ctx context.Context, containerID string, options container.RemoveOptions) error {
		removeCalled = true
		assert.True(t, options.Force)
		return nil
	}

	err := rt.Close(context.Background())

	require.NoError(t, err)
	assert.True(t, stopCalled)
	assert.True(t, removeCalled)
	assert.False(t, rt.initialized)
	assert.Empty(t, rt.containerID)
}

func TestDockerRuntime_Close_StopFails_StillRemoves(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)

	removeCalled := false
	mock.containerStopFn = func(ctx context.Context, containerID string, options container.StopOptions) error {
		return errors.New("stop error")
	}
	mock.containerRemoveFn = func(ctx context.Context, containerID string, options container.RemoveOptions) error {
		removeCalled = true
		return nil
	}

	err := rt.Close(context.Background())

	require.NoError(t, err)
	assert.True(t, removeCalled)
}

func TestDockerRuntime_Close_NoContainer(t *testing.T) {
	rt := newWithClient("/tmp/test", testConfig(), 30*time.Second, testLogger, &mockDockerClient{})
	err := rt.Close(context.Background())
	require.NoError(t, err)
}

// --- Guard Tests ---

func TestDockerRuntime_Guard_NotInitialized(t *testing.T) {
	rt := newWithClient("/tmp/test", testConfig(), 30*time.Second, testLogger, &mockDockerClient{})

	_, err := rt.ReadFile(context.Background(), "file.txt")
	require.ErrorIs(t, err, runtime.ErrRuntimeNotReady)

	err = rt.WriteFile(context.Background(), "file.txt", []byte("data"), 0o644)
	require.ErrorIs(t, err, runtime.ErrRuntimeNotReady)

	err = rt.DeleteFile(context.Background(), "file.txt")
	require.ErrorIs(t, err, runtime.ErrRuntimeNotReady)

	_, err = rt.ListDir(context.Background(), ".")
	require.ErrorIs(t, err, runtime.ErrRuntimeNotReady)

	err = rt.MkDir(context.Background(), "dir", 0o755)
	require.ErrorIs(t, err, runtime.ErrRuntimeNotReady)

	_, err = rt.Exec(context.Background(), "echo hi", runtime.ExecOpts{})
	require.ErrorIs(t, err, runtime.ErrRuntimeNotReady)
}

func TestDockerRuntime_Guard_CancelledContext(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := rt.ReadFile(ctx, "file.txt")
	require.ErrorIs(t, err, context.Canceled)
}

// --- TargetDir Test ---

func TestDockerRuntime_TargetDir(t *testing.T) {
	rt := newWithClient("/my/target", testConfig(), 30*time.Second, testLogger, &mockDockerClient{})
	assert.Equal(t, "/my/target", rt.TargetDir())
}

// --- Path Escape Tests ---

func TestDockerRuntime_PathEscape_AbsolutePath(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)

	_, err := rt.ReadFile(context.Background(), "/etc/passwd")
	require.ErrorIs(t, err, runtime.ErrPathEscape)
}

func TestDockerRuntime_PathEscape_Traversal(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)

	_, err := rt.ReadFile(context.Background(), "../../../etc/passwd")
	require.ErrorIs(t, err, runtime.ErrPathEscape)
}

// --- ReadFile Tests ---

func TestDockerRuntime_ReadFile_Success(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)

	content := []byte("hello world")
	encoded := base64.StdEncoding.EncodeToString(content)
	mockExecPipeline(mock, encoded+"\n", "", 0)

	data, err := rt.ReadFile(context.Background(), "test.txt")

	require.NoError(t, err)
	assert.Equal(t, content, data)
}

func TestDockerRuntime_ReadFile_NotExist(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)
	mockExecPipeline(mock, "", "", 1)

	_, err := rt.ReadFile(context.Background(), "nonexistent.txt")

	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func TestDockerRuntime_ReadFile_BinaryData(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)

	binaryData := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD}
	encoded := base64.StdEncoding.EncodeToString(binaryData)
	mockExecPipeline(mock, encoded+"\n", "", 0)

	data, err := rt.ReadFile(context.Background(), "binary.bin")

	require.NoError(t, err)
	assert.Equal(t, binaryData, data)
}

// --- WriteFile Tests ---

func TestDockerRuntime_WriteFile_Success(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)

	var capturedCmd []string
	mock.execCreateFn = func(ctx context.Context, containerID string, options container.ExecOptions) (container.ExecCreateResponse, error) {
		capturedCmd = options.Cmd
		return container.ExecCreateResponse{ID: "exec-id"}, nil
	}
	mock.execAttachFn = func(ctx context.Context, execID string, cfg container.ExecAttachOptions) (types.HijackedResponse, error) {
		return newMockHijackedResponse("", ""), nil
	}
	mock.execInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
		return container.ExecInspect{ExitCode: 0}, nil
	}

	err := rt.WriteFile(context.Background(), "sub/file.txt", []byte("data"), 0o644)

	require.NoError(t, err)
	require.Len(t, capturedCmd, 3)
	cmd := capturedCmd[2]
	assert.Contains(t, cmd, "mkdir -p")
	assert.Contains(t, cmd, "base64 -d")
	assert.Contains(t, cmd, "chmod 0644")
	assert.Contains(t, cmd, "/workspace/sub/file.txt")
}

func TestDockerRuntime_WriteFile_PathEscape(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)

	err := rt.WriteFile(context.Background(), "../../escape.txt", []byte("bad"), 0o644)
	require.ErrorIs(t, err, runtime.ErrPathEscape)
}

// --- DeleteFile Tests ---

func TestDockerRuntime_DeleteFile_Success(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)
	mockExecPipeline(mock, "", "", 0)

	err := rt.DeleteFile(context.Background(), "file.txt")
	require.NoError(t, err)
}

func TestDockerRuntime_DeleteFile_NotExist(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)
	mockExecPipeline(mock, "", "rm: cannot remove '/workspace/file.txt': No such file or directory\n", 1)

	err := rt.DeleteFile(context.Background(), "file.txt")
	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func TestDockerRuntime_DeleteFile_PathEscape(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)

	err := rt.DeleteFile(context.Background(), "../escape.txt")
	require.ErrorIs(t, err, runtime.ErrPathEscape)
}

// --- ListDir Tests ---

func TestDockerRuntime_ListDir_Success(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)

	statOutput := "regular file 644 100 1700000000 /workspace/hello.txt\n" +
		"directory 755 4096 1700000000 /workspace/subdir\n"
	mockExecPipeline(mock, statOutput, "", 0)

	entries, err := rt.ListDir(context.Background(), ".")

	require.NoError(t, err)
	require.Len(t, entries, 2)

	assert.Equal(t, "hello.txt", entries[0].Name)
	assert.False(t, entries[0].IsDir)
	assert.Equal(t, int64(100), entries[0].Size)
	assert.Equal(t, os.FileMode(0o644), entries[0].Perm)

	assert.Equal(t, "subdir", entries[1].Name)
	assert.True(t, entries[1].IsDir)
}

func TestDockerRuntime_ListDir_Empty(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)
	mockExecPipeline(mock, "", "", 0)

	entries, err := rt.ListDir(context.Background(), ".")

	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestDockerRuntime_ListDir_NotExist(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)
	mockExecPipeline(mock, "", "find: '/workspace/nonexistent': No such file or directory\n", 1)

	_, err := rt.ListDir(context.Background(), "nonexistent")
	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func TestDockerRuntime_ListDir_PathEscape(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)

	_, err := rt.ListDir(context.Background(), "../../")
	require.ErrorIs(t, err, runtime.ErrPathEscape)
}

// --- MkDir Tests ---

func TestDockerRuntime_MkDir_Success(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)

	var capturedCmd string
	mock.execCreateFn = func(ctx context.Context, containerID string, options container.ExecOptions) (container.ExecCreateResponse, error) {
		capturedCmd = options.Cmd[2]
		return container.ExecCreateResponse{ID: "exec-id"}, nil
	}
	mock.execAttachFn = func(ctx context.Context, execID string, cfg container.ExecAttachOptions) (types.HijackedResponse, error) {
		return newMockHijackedResponse("", ""), nil
	}
	mock.execInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
		return container.ExecInspect{ExitCode: 0}, nil
	}

	err := rt.MkDir(context.Background(), "a/b/c", 0o755)

	require.NoError(t, err)
	assert.Contains(t, capturedCmd, "mkdir -p")
	assert.Contains(t, capturedCmd, "/workspace/a/b/c")
	assert.Contains(t, capturedCmd, "chmod 0755")
}

func TestDockerRuntime_MkDir_PathEscape(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)

	err := rt.MkDir(context.Background(), "../../escape", 0o755)
	require.ErrorIs(t, err, runtime.ErrPathEscape)
}

// --- Exec Tests ---

func TestDockerRuntime_Exec_Success(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)
	mockExecPipeline(mock, "hello\n", "", 0)

	result, err := rt.Exec(context.Background(), "echo hello", runtime.ExecOpts{})

	require.NoError(t, err)
	assert.Equal(t, "hello\n", result.Stdout)
	assert.Empty(t, result.Stderr)
	assert.Equal(t, 0, result.ExitCode)
	assert.Greater(t, result.Duration, time.Duration(0))
}

func TestDockerRuntime_Exec_NonZeroExit(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)
	mockExecPipeline(mock, "", "error\n", 42)

	result, err := rt.Exec(context.Background(), "false", runtime.ExecOpts{})

	require.NoError(t, err)
	assert.Equal(t, 42, result.ExitCode)
	assert.Equal(t, "error\n", result.Stderr)
}

func TestDockerRuntime_Exec_Stderr(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)
	mockExecPipeline(mock, "out\n", "err\n", 0)

	result, err := rt.Exec(context.Background(), "cmd", runtime.ExecOpts{})

	require.NoError(t, err)
	assert.Equal(t, "out\n", result.Stdout)
	assert.Equal(t, "err\n", result.Stderr)
}

func TestDockerRuntime_Exec_WorkDir(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)

	var capturedWorkDir string
	mock.execCreateFn = func(ctx context.Context, containerID string, options container.ExecOptions) (container.ExecCreateResponse, error) {
		capturedWorkDir = options.WorkingDir
		return container.ExecCreateResponse{ID: "exec-id"}, nil
	}
	mock.execAttachFn = func(ctx context.Context, execID string, cfg container.ExecAttachOptions) (types.HijackedResponse, error) {
		return newMockHijackedResponse("", ""), nil
	}
	mock.execInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
		return container.ExecInspect{ExitCode: 0}, nil
	}

	_, err := rt.Exec(context.Background(), "ls", runtime.ExecOpts{RelDir: "subdir"})

	require.NoError(t, err)
	assert.Equal(t, "/workspace/subdir", capturedWorkDir)
}

func TestDockerRuntime_Exec_WorkDirEscape(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)

	_, err := rt.Exec(context.Background(), "ls", runtime.ExecOpts{RelDir: "../../"})
	require.ErrorIs(t, err, runtime.ErrPathEscape)
}

func TestDockerRuntime_Exec_EnvVars(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)

	var capturedEnv []string
	mock.execCreateFn = func(ctx context.Context, containerID string, options container.ExecOptions) (container.ExecCreateResponse, error) {
		capturedEnv = options.Env
		return container.ExecCreateResponse{ID: "exec-id"}, nil
	}
	mock.execAttachFn = func(ctx context.Context, execID string, cfg container.ExecAttachOptions) (types.HijackedResponse, error) {
		return newMockHijackedResponse("", ""), nil
	}
	mock.execInspectFn = func(ctx context.Context, execID string) (container.ExecInspect, error) {
		return container.ExecInspect{ExitCode: 0}, nil
	}

	_, err := rt.Exec(context.Background(), "env", runtime.ExecOpts{Env: []string{"FOO=bar"}})

	require.NoError(t, err)
	assert.Equal(t, []string{"FOO=bar"}, capturedEnv)
}

func TestDockerRuntime_Exec_ContainerNotRunning(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)

	mock.execCreateFn = func(ctx context.Context, containerID string, options container.ExecOptions) (container.ExecCreateResponse, error) {
		return container.ExecCreateResponse{}, errors.New("container abc is not running")
	}

	_, err := rt.Exec(context.Background(), "echo hi", runtime.ExecOpts{})

	require.Error(t, err)
	assert.True(t, errors.Is(err, runtime.ErrContainerNotRunning))
}

func TestDockerRuntime_Exec_Timeout(t *testing.T) {
	mock := &mockDockerClient{}
	rt := initRuntime(t, mock)

	mock.execCreateFn = func(ctx context.Context, containerID string, options container.ExecOptions) (container.ExecCreateResponse, error) {
		// Simulate context deadline exceeded.
		<-ctx.Done()
		return container.ExecCreateResponse{}, ctx.Err()
	}

	_, err := rt.Exec(context.Background(), "sleep 100", runtime.ExecOpts{
		Timeout: 50 * time.Millisecond,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

// --- containerPath Tests ---

func TestContainerPath_Valid(t *testing.T) {
	rt := newWithClient("/tmp/test", testConfig(), 30*time.Second, testLogger, &mockDockerClient{})

	tests := []struct {
		relPath string
		want    string
	}{
		{"file.txt", "/workspace/file.txt"},
		{"sub/dir/file.txt", "/workspace/sub/dir/file.txt"},
		{".", "/workspace"},
		{"a/../b", "/workspace/b"},
	}

	for _, tc := range tests {
		got, err := rt.containerPath(tc.relPath)
		require.NoError(t, err, "relPath=%s", tc.relPath)
		assert.Equal(t, tc.want, got, "relPath=%s", tc.relPath)
	}
}

func TestContainerPath_Escape(t *testing.T) {
	rt := newWithClient("/tmp/test", testConfig(), 30*time.Second, testLogger, &mockDockerClient{})

	tests := []string{
		"..",
		"../../../etc/passwd",
		"/etc/passwd",
		"a/../../etc",
	}

	for _, relPath := range tests {
		_, err := rt.containerPath(relPath)
		require.Error(t, err, "relPath=%s", relPath)
		assert.ErrorIs(t, err, runtime.ErrPathEscape, "relPath=%s", relPath)
	}
}

// --- shellQuote Tests ---

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "'simple'"},
		{"with space", "'with space'"},
		{"it's", "'it'\"'\"'s'"},
		{"", "''"},
	}

	for _, tc := range tests {
		assert.Equal(t, tc.want, shellQuote(tc.input))
	}
}

// --- parseStatLine Tests ---

func TestParseStatLine(t *testing.T) {
	tests := []struct {
		line    string
		wantOK  bool
		name    string
		isDir   bool
		size    int64
		perm    os.FileMode
	}{
		{"regular file 644 100 1700000000 /workspace/hello.txt", true, "hello.txt", false, 100, 0o644},
		{"regular empty file 644 0 1700000000 /workspace/empty.txt", true, "empty.txt", false, 0, 0o644},
		{"directory 755 4096 1700000000 /workspace/subdir", true, "subdir", true, 4096, 0o755},
		{"symbolic link 777 10 1700000000 /workspace/link", false, "", false, 0, 0},
		{"", false, "", false, 0, 0},
	}

	for _, tc := range tests {
		entry, ok := parseStatLine(tc.line, "/workspace")
		assert.Equal(t, tc.wantOK, ok, "line=%q", tc.line)
		if ok {
			assert.Equal(t, tc.name, entry.Name)
			assert.Equal(t, tc.isDir, entry.IsDir)
			assert.Equal(t, tc.size, entry.Size)
			assert.Equal(t, tc.perm, entry.Perm)
		}
	}
}

// --- New defaults Tests ---

func TestNew_Defaults(t *testing.T) {
	rt := New("/tmp/test", config.DockerConfig{Image: "test"}, 0, testLogger)
	assert.Equal(t, "/workspace", rt.cfg.MountPath)
	assert.Equal(t, "sh", rt.cfg.Shell)
	assert.Equal(t, "none", rt.cfg.NetworkMode)
	assert.Equal(t, 30*time.Second, rt.defaultTimeout)
}
