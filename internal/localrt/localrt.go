package localrt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/marmutapp/openmarmut/internal/pathutil"
	"github.com/marmutapp/openmarmut/internal/runtime"
)

// LocalRuntime implements runtime.Runtime using the host filesystem and os/exec.
type LocalRuntime struct {
	targetDir      string
	defaultTimeout time.Duration
	logger         *slog.Logger
	initialized    bool
}

// Compile-time interface check.
var _ runtime.Runtime = (*LocalRuntime)(nil)

// New creates an uninitialized LocalRuntime. Call Init() before use.
func New(targetDir string, defaultTimeout time.Duration, logger *slog.Logger) *LocalRuntime {
	if defaultTimeout <= 0 {
		defaultTimeout = 30 * time.Second
	}
	return &LocalRuntime{
		targetDir:      targetDir,
		defaultTimeout: defaultTimeout,
		logger:         logger,
	}
}

// Init validates that the target directory exists and is a directory.
func (r *LocalRuntime) Init(ctx context.Context) error {
	info, err := os.Stat(r.targetDir)
	if err != nil {
		return fmt.Errorf("localrt.Init(%s): %w", r.targetDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("localrt.Init(%s): target is not a directory", r.targetDir)
	}
	r.initialized = true
	r.logger.Debug("local runtime initialized", "target_dir", r.targetDir)
	return nil
}

// Close is a no-op for LocalRuntime.
func (r *LocalRuntime) Close(_ context.Context) error {
	r.initialized = false
	return nil
}

// TargetDir returns the absolute path of the target directory.
func (r *LocalRuntime) TargetDir() string {
	return r.targetDir
}

// ReadFile reads a file relative to the target directory.
func (r *LocalRuntime) ReadFile(ctx context.Context, relPath string) ([]byte, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("localrt.ReadFile(%s): %w", relPath, err)
	}

	absPath, err := pathutil.Resolve(r.targetDir, relPath)
	if err != nil {
		return nil, fmt.Errorf("localrt.ReadFile(%s): %w", relPath, err)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("localrt.ReadFile(%s): %w", relPath, err)
	}
	return data, nil
}

// WriteFile writes data atomically to a file relative to the target directory.
// Creates parent directories as needed.
func (r *LocalRuntime) WriteFile(ctx context.Context, relPath string, data []byte, perm os.FileMode) error {
	if err := r.ready(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("localrt.WriteFile(%s): %w", relPath, err)
	}

	absPath, err := pathutil.Resolve(r.targetDir, relPath)
	if err != nil {
		return fmt.Errorf("localrt.WriteFile(%s): %w", relPath, err)
	}

	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("localrt.WriteFile(%s): create parent dirs: %w", relPath, err)
	}

	// Atomic write: temp file + rename.
	tmpFile, err := os.CreateTemp(dir, ".openmarmut-tmp-*")
	if err != nil {
		return fmt.Errorf("localrt.WriteFile(%s): create temp file: %w", relPath, err)
	}
	tmpPath := tmpFile.Name()

	// Clean up temp file on any failure.
	success := false
	defer func() {
		if !success {
			os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return fmt.Errorf("localrt.WriteFile(%s): write temp file: %w", relPath, err)
	}
	if err := tmpFile.Chmod(perm); err != nil {
		tmpFile.Close()
		return fmt.Errorf("localrt.WriteFile(%s): chmod temp file: %w", relPath, err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("localrt.WriteFile(%s): close temp file: %w", relPath, err)
	}

	if err := os.Rename(tmpPath, absPath); err != nil {
		return fmt.Errorf("localrt.WriteFile(%s): rename temp file: %w", relPath, err)
	}

	success = true
	return nil
}

// DeleteFile removes a file relative to the target directory.
func (r *LocalRuntime) DeleteFile(ctx context.Context, relPath string) error {
	if err := r.ready(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("localrt.DeleteFile(%s): %w", relPath, err)
	}

	absPath, err := pathutil.Resolve(r.targetDir, relPath)
	if err != nil {
		return fmt.Errorf("localrt.DeleteFile(%s): %w", relPath, err)
	}

	if err := os.Remove(absPath); err != nil {
		return fmt.Errorf("localrt.DeleteFile(%s): %w", relPath, err)
	}
	return nil
}

// ListDir lists entries in a directory relative to the target directory.
func (r *LocalRuntime) ListDir(ctx context.Context, relPath string) ([]runtime.FileEntry, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("localrt.ListDir(%s): %w", relPath, err)
	}

	absPath, err := pathutil.Resolve(r.targetDir, relPath)
	if err != nil {
		return nil, fmt.Errorf("localrt.ListDir(%s): %w", relPath, err)
	}

	dirEntries, err := os.ReadDir(absPath)
	if err != nil {
		return nil, fmt.Errorf("localrt.ListDir(%s): %w", relPath, err)
	}

	entries := make([]runtime.FileEntry, 0, len(dirEntries))
	for _, de := range dirEntries {
		info, err := de.Info()
		if err != nil {
			return nil, fmt.Errorf("localrt.ListDir(%s): stat %s: %w", relPath, de.Name(), err)
		}
		entries = append(entries, runtime.FileEntry{
			Name:    de.Name(),
			IsDir:   de.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
			Perm:    info.Mode().Perm(),
		})
	}
	return entries, nil
}

// MkDir creates a directory (and parents) relative to the target directory.
func (r *LocalRuntime) MkDir(ctx context.Context, relPath string, perm os.FileMode) error {
	if err := r.ready(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("localrt.MkDir(%s): %w", relPath, err)
	}

	absPath, err := pathutil.Resolve(r.targetDir, relPath)
	if err != nil {
		return fmt.Errorf("localrt.MkDir(%s): %w", relPath, err)
	}

	if err := os.MkdirAll(absPath, perm); err != nil {
		return fmt.Errorf("localrt.MkDir(%s): %w", relPath, err)
	}
	return nil
}

// Exec runs a shell command via sh -c in the target directory.
// Non-zero exit codes are returned in ExecResult.ExitCode, not as errors.
func (r *LocalRuntime) Exec(ctx context.Context, command string, opts runtime.ExecOpts) (*runtime.ExecResult, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}

	// Determine working directory.
	workDir := r.targetDir
	if opts.RelDir != "" {
		resolved, err := pathutil.Resolve(r.targetDir, opts.RelDir)
		if err != nil {
			return nil, fmt.Errorf("localrt.Exec: resolve work dir: %w", err)
		}
		workDir = resolved
	}

	// Determine timeout.
	timeout := r.defaultTimeout
	if opts.Timeout > 0 {
		timeout = opts.Timeout
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, "sh", "-c", command)
	cmd.Dir = workDir
	// Create a new process group so we can kill the entire group on timeout,
	// preventing orphaned child processes from keeping stdout/stderr pipes open.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if len(opts.Env) > 0 {
		cmd.Env = append(os.Environ(), opts.Env...)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start)

	exitCode := 0
	if err != nil {
		// Check timeout/cancellation BEFORE ExitError, because CommandContext
		// kills the process on deadline which also produces an ExitError.
		if timeoutCtx.Err() != nil {
			return nil, fmt.Errorf("localrt.Exec(%s): %w", command, timeoutCtx.Err())
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Non-zero exit code is NOT an error.
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("localrt.Exec(%s): %w", command, err)
		}
	}

	return &runtime.ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		Duration: duration,
	}, nil
}

func (r *LocalRuntime) ready() error {
	if !r.initialized {
		return runtime.ErrRuntimeNotReady
	}
	return nil
}
