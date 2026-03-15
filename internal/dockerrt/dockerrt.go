package dockerrt

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/marmutapp/openmarmut/internal/config"
	"github.com/marmutapp/openmarmut/internal/runtime"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// dockerClient abstracts the Docker SDK methods we use, enabling unit testing with mocks.
type dockerClient interface {
	ImageInspectWithRaw(ctx context.Context, imageID string) (image.InspectResponse, []byte, error)
	ImagePull(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error)
	ContainerCreate(ctx context.Context, cfg *container.Config, hostCfg *container.HostConfig, networkCfg *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
	ContainerExecCreate(ctx context.Context, containerID string, options container.ExecOptions) (container.ExecCreateResponse, error)
	ContainerExecAttach(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error)
	ContainerExecInspect(ctx context.Context, execID string) (container.ExecInspect, error)
}

// DockerRuntime implements runtime.Runtime using Docker containers.
type DockerRuntime struct {
	targetDir      string
	cfg            config.DockerConfig
	defaultTimeout time.Duration
	logger         *slog.Logger
	client         dockerClient
	containerID    string
	initialized    bool
}

// Compile-time interface check.
var _ runtime.Runtime = (*DockerRuntime)(nil)

// New creates an uninitialized DockerRuntime. Call Init() before use.
func New(targetDir string, cfg config.DockerConfig, defaultTimeout time.Duration, logger *slog.Logger) *DockerRuntime {
	if cfg.MountPath == "" {
		cfg.MountPath = "/workspace"
	}
	if cfg.Shell == "" {
		cfg.Shell = "sh"
	}
	if cfg.NetworkMode == "" {
		cfg.NetworkMode = "none"
	}
	if defaultTimeout <= 0 {
		defaultTimeout = 30 * time.Second
	}
	return &DockerRuntime{
		targetDir:      targetDir,
		cfg:            cfg,
		defaultTimeout: defaultTimeout,
		logger:         logger,
	}
}

// newWithClient creates a DockerRuntime with a pre-configured client (for testing).
func newWithClient(targetDir string, cfg config.DockerConfig, defaultTimeout time.Duration, logger *slog.Logger, client dockerClient) *DockerRuntime {
	rt := New(targetDir, cfg, defaultTimeout, logger)
	rt.client = client
	return rt
}

// Init creates and starts a Docker container with the target directory mounted.
func (r *DockerRuntime) Init(ctx context.Context) error {
	if r.client == nil {
		cli, err := newDockerClient()
		if err != nil {
			return fmt.Errorf("dockerrt.Init: create docker client: %w", err)
		}
		r.client = cli
	}

	// Check if image exists locally; pull if not.
	_, _, err := r.client.ImageInspectWithRaw(ctx, r.cfg.Image)
	if err != nil {
		r.logger.Info("pulling docker image", "image", r.cfg.Image)
		reader, pullErr := r.client.ImagePull(ctx, r.cfg.Image, image.PullOptions{})
		if pullErr != nil {
			return fmt.Errorf("dockerrt.Init: pull image %s: %w", r.cfg.Image, pullErr)
		}
		// Must read the pull output to completion.
		_, _ = io.Copy(io.Discard, reader)
		reader.Close()
	}

	// Build container config.
	user := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())

	containerCfg := &container.Config{
		Image:      r.cfg.Image,
		Cmd:        []string{"sleep", "infinity"},
		WorkingDir: r.cfg.MountPath,
		User:       user,
		Env:        r.cfg.EnvVars,
	}

	binds := []string{r.targetDir + ":" + r.cfg.MountPath}
	binds = append(binds, r.cfg.ExtraVolumes...)

	hostCfg := &container.HostConfig{
		Binds:       binds,
		NetworkMode: container.NetworkMode(r.cfg.NetworkMode),
	}

	resp, err := r.client.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, "")
	if err != nil {
		return fmt.Errorf("dockerrt.Init: create container: %w", err)
	}

	if err := r.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		// Best-effort cleanup of created container.
		_ = r.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return fmt.Errorf("dockerrt.Init: start container: %w", err)
	}

	// Verify container is running.
	inspect, err := r.client.ContainerInspect(ctx, resp.ID)
	if err != nil {
		_ = r.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return fmt.Errorf("dockerrt.Init: inspect container: %w", err)
	}
	if inspect.State == nil || !inspect.State.Running {
		_ = r.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return fmt.Errorf("dockerrt.Init: container not running after start")
	}

	r.containerID = resp.ID
	r.initialized = true
	shortID := resp.ID
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	r.logger.Info("docker runtime initialized", "container", shortID, "image", r.cfg.Image)
	return nil
}

// Close stops and removes the container.
func (r *DockerRuntime) Close(ctx context.Context) error {
	if r.containerID == "" {
		r.initialized = false
		return nil
	}

	timeout := 5
	stopErr := r.client.ContainerStop(ctx, r.containerID, container.StopOptions{Timeout: &timeout})
	if stopErr != nil {
		r.logger.Warn("container stop failed, forcing removal", "error", stopErr)
	}

	rmErr := r.client.ContainerRemove(ctx, r.containerID, container.RemoveOptions{Force: true})

	r.containerID = ""
	r.initialized = false

	if rmErr != nil {
		return fmt.Errorf("dockerrt.Close: remove container: %w", rmErr)
	}
	return nil
}

// TargetDir returns the host-side target directory.
func (r *DockerRuntime) TargetDir() string {
	return r.targetDir
}

// ReadFile reads a file from the container via base64-encoded docker exec.
func (r *DockerRuntime) ReadFile(ctx context.Context, relPath string) ([]byte, error) {
	if err := r.guard(ctx); err != nil {
		return nil, fmt.Errorf("dockerrt.ReadFile(%s): %w", relPath, err)
	}

	containerPath, err := r.containerPath(relPath)
	if err != nil {
		return nil, fmt.Errorf("dockerrt.ReadFile(%s): %w", relPath, err)
	}

	// Use test -f to check existence, then base64 encode.
	cmd := fmt.Sprintf("test -f %s && base64 %s", shellQuote(containerPath), shellQuote(containerPath))
	stdout, stderr, exitCode, err := r.execInContainer(ctx, cmd, r.cfg.MountPath, nil)
	if err != nil {
		return nil, fmt.Errorf("dockerrt.ReadFile(%s): %w", relPath, err)
	}
	if exitCode != 0 {
		if strings.Contains(stderr, "No such file") || strings.Contains(stderr, "not found") || exitCode == 1 {
			return nil, fmt.Errorf("dockerrt.ReadFile(%s): %w", relPath, os.ErrNotExist)
		}
		return nil, fmt.Errorf("dockerrt.ReadFile(%s): container command failed (exit %d): %s", relPath, exitCode, stderr)
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(stdout))
	if err != nil {
		return nil, fmt.Errorf("dockerrt.ReadFile(%s): decode base64: %w", relPath, err)
	}
	return decoded, nil
}

// WriteFile writes data to a file in the container via base64-encoded stdin pipe.
func (r *DockerRuntime) WriteFile(ctx context.Context, relPath string, data []byte, perm os.FileMode) error {
	if err := r.guard(ctx); err != nil {
		return fmt.Errorf("dockerrt.WriteFile(%s): %w", relPath, err)
	}

	containerPath, err := r.containerPath(relPath)
	if err != nil {
		return fmt.Errorf("dockerrt.WriteFile(%s): %w", relPath, err)
	}

	dir := path.Dir(containerPath)

	// Create parent directories, decode base64 from stdin, write to file, set permissions.
	encoded := base64.StdEncoding.EncodeToString(data)
	cmd := fmt.Sprintf("mkdir -p %s && echo %s | base64 -d > %s && chmod %s %s",
		shellQuote(dir),
		shellQuote(encoded),
		shellQuote(containerPath),
		fmt.Sprintf("%04o", perm),
		shellQuote(containerPath),
	)

	_, stderr, exitCode, err := r.execInContainer(ctx, cmd, r.cfg.MountPath, nil)
	if err != nil {
		return fmt.Errorf("dockerrt.WriteFile(%s): %w", relPath, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("dockerrt.WriteFile(%s): container command failed (exit %d): %s", relPath, exitCode, stderr)
	}
	return nil
}

// DeleteFile removes a file in the container.
func (r *DockerRuntime) DeleteFile(ctx context.Context, relPath string) error {
	if err := r.guard(ctx); err != nil {
		return fmt.Errorf("dockerrt.DeleteFile(%s): %w", relPath, err)
	}

	containerPath, err := r.containerPath(relPath)
	if err != nil {
		return fmt.Errorf("dockerrt.DeleteFile(%s): %w", relPath, err)
	}

	cmd := fmt.Sprintf("rm %s", shellQuote(containerPath))
	_, stderr, exitCode, err := r.execInContainer(ctx, cmd, r.cfg.MountPath, nil)
	if err != nil {
		return fmt.Errorf("dockerrt.DeleteFile(%s): %w", relPath, err)
	}
	if exitCode != 0 {
		if strings.Contains(stderr, "No such file") {
			return fmt.Errorf("dockerrt.DeleteFile(%s): %w", relPath, os.ErrNotExist)
		}
		return fmt.Errorf("dockerrt.DeleteFile(%s): container command failed (exit %d): %s", relPath, exitCode, stderr)
	}
	return nil
}

// ListDir lists entries in a container directory.
func (r *DockerRuntime) ListDir(ctx context.Context, relPath string) ([]runtime.FileEntry, error) {
	if err := r.guard(ctx); err != nil {
		return nil, fmt.Errorf("dockerrt.ListDir(%s): %w", relPath, err)
	}

	containerPath, err := r.containerPath(relPath)
	if err != nil {
		return nil, fmt.Errorf("dockerrt.ListDir(%s): %w", relPath, err)
	}

	// Use stat-based output for richer info: type, permissions, size, mtime, name.
	// Format: %F %a %s %Y %n where %F=type, %a=octal perm, %s=size, %Y=mtime epoch, %n=name
	cmd := fmt.Sprintf(
		`find %s -maxdepth 1 -mindepth 1 -exec stat -c '%%F %%a %%s %%Y %%n' {} \;`,
		shellQuote(containerPath),
	)

	stdout, stderr, exitCode, err := r.execInContainer(ctx, cmd, r.cfg.MountPath, nil)
	if err != nil {
		return nil, fmt.Errorf("dockerrt.ListDir(%s): %w", relPath, err)
	}
	if exitCode != 0 {
		if strings.Contains(stderr, "No such file") || strings.Contains(stderr, "Not a directory") {
			return nil, fmt.Errorf("dockerrt.ListDir(%s): %w", relPath, os.ErrNotExist)
		}
		return nil, fmt.Errorf("dockerrt.ListDir(%s): container command failed (exit %d): %s", relPath, exitCode, stderr)
	}

	return parseStatOutput(stdout, containerPath), nil
}

// MkDir creates a directory (and parents) in the container.
func (r *DockerRuntime) MkDir(ctx context.Context, relPath string, perm os.FileMode) error {
	if err := r.guard(ctx); err != nil {
		return fmt.Errorf("dockerrt.MkDir(%s): %w", relPath, err)
	}

	containerPath, err := r.containerPath(relPath)
	if err != nil {
		return fmt.Errorf("dockerrt.MkDir(%s): %w", relPath, err)
	}

	cmd := fmt.Sprintf("mkdir -p %s && chmod %s %s",
		shellQuote(containerPath),
		fmt.Sprintf("%04o", perm),
		shellQuote(containerPath),
	)

	_, stderr, exitCode, err := r.execInContainer(ctx, cmd, r.cfg.MountPath, nil)
	if err != nil {
		return fmt.Errorf("dockerrt.MkDir(%s): %w", relPath, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("dockerrt.MkDir(%s): container command failed (exit %d): %s", relPath, exitCode, stderr)
	}
	return nil
}

// Exec runs a shell command inside the container.
// Non-zero exit code is NOT an error — it goes in ExecResult.ExitCode.
func (r *DockerRuntime) Exec(ctx context.Context, command string, opts runtime.ExecOpts) (*runtime.ExecResult, error) {
	if err := r.guard(ctx); err != nil {
		return nil, fmt.Errorf("dockerrt.Exec: %w", err)
	}

	workDir := r.cfg.MountPath
	if opts.RelDir != "" {
		resolved, err := r.containerPath(opts.RelDir)
		if err != nil {
			return nil, fmt.Errorf("dockerrt.Exec: %w", err)
		}
		workDir = resolved
	}

	timeout := r.defaultTimeout
	if opts.Timeout > 0 {
		timeout = opts.Timeout
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	stdout, stderr, exitCode, err := r.execInContainer(execCtx, command, workDir, opts.Env)
	duration := time.Since(start)

	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("dockerrt.Exec: command timed out after %s", timeout)
		}
		return nil, fmt.Errorf("dockerrt.Exec: %w", err)
	}

	return &runtime.ExecResult{
		Stdout:   stdout,
		Stderr:   stderr,
		ExitCode: exitCode,
		Duration: duration,
	}, nil
}

// guard checks that the runtime is initialized, the container is accessible, and the context is valid.
func (r *DockerRuntime) guard(ctx context.Context) error {
	if !r.initialized {
		return runtime.ErrRuntimeNotReady
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// containerPath resolves a relative path to an absolute container path,
// preventing path escape beyond the mount point.
func (r *DockerRuntime) containerPath(relPath string) (string, error) {
	if path.IsAbs(relPath) {
		return "", fmt.Errorf("%w: absolute path %q not allowed", runtime.ErrPathEscape, relPath)
	}

	joined := path.Join(r.cfg.MountPath, relPath)
	cleaned := path.Clean(joined)
	base := path.Clean(r.cfg.MountPath)

	if cleaned == base {
		return cleaned, nil
	}
	if !strings.HasPrefix(cleaned, base+"/") {
		return "", fmt.Errorf("%w: %q resolves outside mount path", runtime.ErrPathEscape, relPath)
	}
	return cleaned, nil
}

// execInContainer runs a command inside the container and returns stdout, stderr, exit code.
func (r *DockerRuntime) execInContainer(ctx context.Context, command string, workDir string, env []string) (string, string, int, error) {
	execCfg := container.ExecOptions{
		Cmd:          []string{r.cfg.Shell, "-c", command},
		WorkingDir:   workDir,
		AttachStdout: true,
		AttachStderr: true,
		Env:          env,
	}

	createResp, err := r.client.ContainerExecCreate(ctx, r.containerID, execCfg)
	if err != nil {
		if isContainerNotRunning(err) {
			return "", "", 0, runtime.ErrContainerNotRunning
		}
		return "", "", 0, fmt.Errorf("exec create: %w", err)
	}

	attachResp, err := r.client.ContainerExecAttach(ctx, createResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", "", 0, fmt.Errorf("exec attach: %w", err)
	}
	defer attachResp.Close()

	var stdout, stderr bytes.Buffer
	_, err = stdcopy.StdCopy(&stdout, &stderr, attachResp.Reader)
	if err != nil {
		return "", "", 0, fmt.Errorf("read exec output: %w", err)
	}

	inspectResp, err := r.client.ContainerExecInspect(ctx, createResp.ID)
	if err != nil {
		return "", "", 0, fmt.Errorf("exec inspect: %w", err)
	}

	return stdout.String(), stderr.String(), inspectResp.ExitCode, nil
}

// shellQuote wraps a string in single quotes, escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// isContainerNotRunning checks if a Docker error indicates a dead container.
func isContainerNotRunning(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "is not running") || strings.Contains(msg, "No such container")
}

// parseStatOutput parses the output of find + stat into FileEntry slices.
func parseStatOutput(output string, dirPath string) []runtime.FileEntry {
	var entries []runtime.FileEntry
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		entry, ok := parseStatLine(line, dirPath)
		if ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

// parseStatLine parses a single line from stat -c '%F %a %s %Y %n'.
// Format: "regular file 644 1234 1678901234 /workspace/foo.txt"
// or:     "directory 755 4096 1678901234 /workspace/subdir"
func parseStatLine(line string, dirPath string) (runtime.FileEntry, bool) {
	var entry runtime.FileEntry

	// File type is variable-length ("regular file", "directory", etc.).
	// Parse from the end: name is everything after the last space-preceded field.
	// Strategy: find the file type prefix, then parse the remaining 4 fixed fields + name.
	isDir := strings.HasPrefix(line, "directory ")
	isRegular := strings.HasPrefix(line, "regular file ") || strings.HasPrefix(line, "regular empty file ")

	var rest string
	switch {
	case strings.HasPrefix(line, "directory "):
		rest = strings.TrimPrefix(line, "directory ")
	case strings.HasPrefix(line, "regular file "):
		rest = strings.TrimPrefix(line, "regular file ")
	case strings.HasPrefix(line, "regular empty file "):
		rest = strings.TrimPrefix(line, "regular empty file ")
	case strings.HasPrefix(line, "symbolic link "):
		rest = strings.TrimPrefix(line, "symbolic link ")
	default:
		// Other types (block, char, pipe, socket) — skip first word(s).
		// Try to find the numeric perm field.
		parts := strings.SplitN(line, " ", 2)
		if len(parts) < 2 {
			return entry, false
		}
		rest = parts[1]
	}

	// rest should be: "<perm> <size> <mtime_epoch> <full_path>"
	// Split into at most 4 parts (path may contain spaces).
	parts := strings.SplitN(rest, " ", 4)
	if len(parts) < 4 {
		return entry, false
	}

	permBits, err := strconv.ParseUint(parts[0], 8, 32)
	if err != nil {
		return entry, false
	}

	size, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return entry, false
	}

	mtimeEpoch, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return entry, false
	}

	fullPath := parts[3]
	name := path.Base(fullPath)

	entry.Name = name
	entry.IsDir = isDir
	entry.Size = size
	entry.ModTime = time.Unix(mtimeEpoch, 0)
	entry.Perm = os.FileMode(permBits)

	// Only include regular files and directories.
	if !isDir && !isRegular {
		return entry, false
	}

	return entry, true
}
