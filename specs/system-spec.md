# OpenCode-Go: System Specification

**Version:** 1.0  
**Last Updated:** 2026-03-13  
**Status:** Design Complete — Implementation Not Started

---

## 1. System Overview

OpenCode-Go is a CLI tool written in Go that provides an AI-assisted development environment capable of operating on a target project directory. It supports two mutually exclusive runtime modes:

- **Local Mode** — operates directly on the host filesystem and executes commands via the host shell.
- **Docker Mode** — mounts the target directory into a Docker container and executes all operations within that container, providing isolation from the host.

The tool acts as a runtime-agnostic bridge: the caller selects a mode, and all downstream operations (file I/O, command execution) are dispatched through a unified `Runtime` interface. This makes the runtime interchangeable without changing any business logic.

### What This Is NOT

This is not an IDE, not a web server, and not a daemon. It is a CLI tool that receives instructions, executes them against a target directory via a chosen runtime, and returns structured results.

---

## 2. Architecture Design

### 2.1 Layered Architecture

```
┌─────────────────────────────────────────────┐
│              CLI Layer (cmd/)                │
│  Parses args, selects runtime, invokes ops  │
├─────────────────────────────────────────────┤
│           Runner (cli/runner.go)            │
│  Config → Logger → Runtime → Init → fn →   │
│  Close lifecycle for every command           │
├──────────────┬──────────────────────────────┤
│  Local Runtime│   Docker Runtime            │
│  (localrt/)   │   (dockerrt/)               │
│  Implements   │   Implements                │
│  Runtime iface│   Runtime iface             │
├──────────────┴──────────────────────────────┤
│           Shared Packages                   │
│  config/ | logger/ | pathutil/ | runtime/   │
└─────────────────────────────────────────────┘
```

### 2.2 Key Design Decisions

**Decision 1: Single `Runtime` interface, not separate `Filesystem` + `Executor`.**

Both file ops and command execution need access to the same underlying context (host dir for local, container reference for Docker). Splitting them creates shared-state problems. A single interface keeps it clean.

**Decision 2: No `core/` package.**

A package named `core` is a Go anti-pattern. It becomes a dumping ground. Every piece of logic belongs to a specific domain.

**Decision 3: Operations are methods on the Runtime, not separate functions.**

The Runtime interface IS the operations layer. There's no separate "operations" package that wraps Runtime calls — that would just be indirection.

**Decision 4: Docker container lifecycle is explicit.**

A Docker runtime instance maps to exactly one container. Created on `Init()`, reused for all operations, destroyed on `Close()`. No implicit container creation.

**Decision 5: Structured errors with wrapping, not custom error types.**

Go 1.13+ error wrapping (`fmt.Errorf("...: %w", err)`) with sentinel errors for known failure modes.

---

## 3. Runtime Interface Contract

This is the most critical type in the system. Every operation goes through it.

```go
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
```

### Shared Types

```go
type FileEntry struct {
    Name    string
    IsDir   bool
    Size    int64
    ModTime time.Time
    Perm    os.FileMode
}

type ExecResult struct {
    Stdout   string
    Stderr   string
    ExitCode int
    Duration time.Duration
}

type ExecOpts struct {
    RelDir  string        // subdirectory as working dir (relative to target)
    Timeout time.Duration // overrides default timeout; zero = use default
    Env     []string      // additional KEY=VALUE env vars for this command
}

// Sentinel errors
var (
    ErrPathEscape          = errors.New("path escapes target directory")
    ErrContainerNotRunning = errors.New("docker container is not running")
    ErrRuntimeNotReady     = errors.New("runtime has not been initialized")
)
```

---

## 4. Runtime Mode Details

### 4.1 Local Runtime (`internal/localrt`)

Thin wrapper around `os` and `os/exec` with path sandboxing.

| Method | Implementation | Key Concern |
|--------|---------------|-------------|
| `Init` | `os.Stat(targetDir)` — verify exists and is a directory | Clear error if missing |
| `Close` | No-op, returns nil | |
| `ReadFile` | `os.ReadFile(resolved)` | Path traversal prevention |
| `WriteFile` | `os.MkdirAll` parent + temp file + `os.Rename` (atomic) | Atomic write |
| `DeleteFile` | `os.Remove(resolved)` | |
| `ListDir` | `os.ReadDir(resolved)` → `[]FileEntry` | |
| `MkDir` | `os.MkdirAll(resolved, perm)` | |
| `Exec` | `exec.CommandContext(ctx, "sh", "-c", cmd)` | Timeout via context, capture stdout+stderr separately |

**Path sandboxing** (every method):
1. `filepath.Join(targetDir, relPath)`
2. `filepath.Clean` the result
3. Verify result starts with `targetDir + separator`
4. If not → `ErrPathEscape`

### 4.2 Docker Runtime (`internal/dockerrt`)

Uses the official Docker SDK for Go. Manages a single container.

**Container Lifecycle:**
```
Init()                          Close()
  │                                │
  ▼                                ▼
docker create                  docker stop (5s timeout)
  │                              │
  ▼                              ▼
docker start                   docker rm (force)
  │
  ▼
Container running with bind mount
(all ops via docker exec)
```

**Container configuration:**
```go
type DockerConfig struct {
    Image       string   // Required. e.g., "ubuntu:24.04"
    MountPath   string   // Container mount point. Default: "/workspace"
    Shell       string   // Default: "sh"
    ExtraVolumes []string
    EnvVars     []string // KEY=VALUE pairs
    Memory      string   // e.g., "512m"
    CPUs        string   // e.g., "1.0"
    NetworkMode string   // Default: "none"
}
```

| Method | Implementation | Key Concern |
|--------|---------------|-------------|
| `Init` | SDK: ImageInspect (pull if missing), ContainerCreate, ContainerStart | Image availability |
| `Close` | SDK: ContainerStop + ContainerRemove(force) | Best-effort cleanup |
| `ReadFile` | `docker exec base64 <path>` → decode | Binary safety |
| `WriteFile` | Pipe base64-encoded data to `docker exec base64 -d > <path>` | Binary safety |
| `DeleteFile` | `docker exec rm <path>` | |
| `ListDir` | `docker exec find <path> -maxdepth 1` → parse | Richer than `ls` |
| `MkDir` | `docker exec mkdir -p <path>` | |
| `Exec` | SDK: ContainerExecCreate + ContainerExecAttach | Use stdcopy.StdCopy to demux |

**Critical issues addressed:**
1. **File permissions:** Run with `--user $(id -u):$(id -g)` to match host
2. **Binary files:** base64 encode/decode prevents corruption
3. **Stream demuxing:** Docker multiplexes stdout/stderr — must use `stdcopy.StdCopy`
4. **Container death:** Return `ErrContainerNotRunning`, caller can `Close()` + `Init()`

---

## 5. Module Definitions

### 5.1 `internal/pathutil`

Pure functions, zero external dependencies.

```go
func Resolve(baseDir, relPath string) (string, error)  // Safe path resolution
func MustBeRelative(path string) error                  // Rejects absolute paths
```

### 5.2 `internal/runtime`

Interface, types, sentinel errors, factory.

```go
// runtime.go: Runtime interface, FileEntry, ExecResult, ExecOpts, sentinel errors
// factory.go: NewRuntime(cfg, logger) → Runtime
```

### 5.3 `internal/config`

```go
type Config struct {
    Mode           string        // "local" or "docker"
    TargetDir      string        // Absolute path to operate on
    Docker         DockerConfig  // Docker-specific settings
    Log            LogConfig     // Level + format
    DefaultTimeout time.Duration // Default command timeout (30s)
}

type LogConfig struct {
    Level  string // debug, info, warn, error
    Format string // text, json
}

func Load(flags *FlagOverrides) (*Config, error)  // Merge: flags > env > file > defaults
func Validate(cfg *Config) error                    // Returns all violations, not just first
```

Config sources (priority order):
1. CLI flags (highest)
2. Environment variables (prefix `OPENCODE_`)
3. Config file (`.opencode.yaml` in target dir or `~/.config/opencode/config.yaml`)
4. Hardcoded defaults (lowest)

### 5.4 `internal/logger`

```go
func New(cfg LogConfig) *slog.Logger  // Returns configured slog.Logger
```

No wrapper types. Pass `*slog.Logger` directly everywhere.

### 5.5 `internal/cli`

Cobra-based CLI.

```
opencode [global flags] <command> [command flags] [args]

Global flags:
  --mode, -m      "local" or "docker" (default: "local")
  --target, -t    Target directory (default: cwd)
  --config, -c    Config file path
  --log-level     debug/info/warn/error (default: info)
  --log-format    text/json (default: text)

Commands:
  read <path>     Read file to stdout
  write <path>    Write stdin to file
  delete <path>   Delete file
  ls [path]       List directory
  mkdir <path>    Create directory
  exec <command>  Execute shell command
  info            Show runtime info
```

**Runner pattern:** All commands share a lifecycle:
```
Parse flags → Load config → Create logger → Create runtime → Init() → fn() → Close()
```

This is handled by `Runner.Run(ctx, fn)` — each command only implements `fn`.

---

## 6. Error Strategy

1. **Wrap, don't create:** `fmt.Errorf("localrt.ReadFile(%s): %w", relPath, err)`
2. **Sentinel errors:** `ErrPathEscape`, `ErrContainerNotRunning`, `ErrRuntimeNotReady`
3. **Check with `errors.Is`:** e.g., `errors.Is(err, os.ErrNotExist)`
4. **No panics:** All error paths return errors. `log.Fatal` only in `main()`.
5. **CLI exit codes:** 0 = success, 1 = runtime error, 2 = usage error

---

## 7. Dependencies

| Dependency | Purpose |
|-----------|---------|
| `github.com/spf13/cobra` | CLI framework |
| `github.com/docker/docker` | Docker SDK |
| `gopkg.in/yaml.v3` | Config file parsing |
| `github.com/stretchr/testify` | Test assertions |

Standard library: `log/slog`, `os`, `os/exec`, `context`, `io`, `path/filepath`, `errors`, `testing`

---

## 8. Implementation Order

Each step is independently testable. No step depends on a later step.

1. `internal/pathutil` — zero dependencies, pure logic
2. `internal/runtime` — interface + types only, no implementation
3. `internal/config` — config loading and validation
4. `internal/logger` — trivial slog wrapper
5. `internal/localrt` — first runtime, testable with t.TempDir()
6. `internal/cli` — wire cobra commands, working CLI in local mode
7. `internal/dockerrt` — second runtime, drop-in replacement
8. `internal/runtime/factory.go` — factory creates right runtime from config
9. End-to-end tests, Makefile, Dockerfile, README
