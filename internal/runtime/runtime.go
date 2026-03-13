package runtime

import (
	"context"
	"errors"
	"os"
	"time"
)

// Runtime abstracts the execution environment.
// Implementations must be safe for sequential use (not concurrent).
// Each Runtime instance is bound to a single target directory.
type Runtime interface {
	// Init prepares the runtime for use.
	// For local: validates the target directory exists.
	// For docker: creates and starts the container with the target dir mounted.
	Init(ctx context.Context) error

	// Close releases all resources.
	// For local: no-op.
	// For docker: stops and removes the container.
	Close(ctx context.Context) error

	// ReadFile reads the entire contents of a file relative to the target directory.
	// Returns os.ErrNotExist if the file does not exist.
	ReadFile(ctx context.Context, relPath string) ([]byte, error)

	// WriteFile writes data to a file relative to the target directory.
	// Creates parent directories as needed. Overwrites existing files.
	WriteFile(ctx context.Context, relPath string, data []byte, perm os.FileMode) error

	// DeleteFile removes a file relative to the target directory.
	// Returns os.ErrNotExist if the file does not exist.
	DeleteFile(ctx context.Context, relPath string) error

	// ListDir lists entries in a directory relative to the target directory.
	ListDir(ctx context.Context, relPath string) ([]FileEntry, error)

	// MkDir creates a directory (and parents) relative to the target directory.
	MkDir(ctx context.Context, relPath string, perm os.FileMode) error

	// Exec runs a shell command via sh -c.
	// Non-zero exit code is NOT an error — it's in ExecResult.ExitCode.
	// Only infrastructure failures (can't find sh, can't fork) return error.
	Exec(ctx context.Context, command string, opts ExecOpts) (*ExecResult, error)

	// TargetDir returns the absolute path of the target directory on the HOST.
	TargetDir() string
}

// FileEntry represents a single file or directory entry.
type FileEntry struct {
	Name    string
	IsDir   bool
	Size    int64
	ModTime time.Time
	Perm    os.FileMode
}

// ExecResult holds the outcome of a command execution.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
}

// ExecOpts configures a single command execution.
type ExecOpts struct {
	RelDir  string        // subdirectory as working dir (relative to target)
	Timeout time.Duration // overrides default timeout; zero = use default
	Env     []string      // additional KEY=VALUE env vars for this command
}

// Sentinel errors.
var (
	ErrPathEscape          = errors.New("path escapes target directory")
	ErrContainerNotRunning = errors.New("docker container is not running")
	ErrRuntimeNotReady     = errors.New("runtime has not been initialized")
)
