# OpenCode-Go: Detailed Function Design

**Companion to:** specs/system-spec.md

---

## 1. Module: `internal/pathutil`

### 1.1 `Resolve(baseDir, relPath string) (string, error)`

**Purpose:** Safely resolve a relative path against a base directory. Security boundary for all file ops.

**Inputs:**
- `baseDir` — Absolute path to the target directory.
- `relPath` — Relative path from the user/caller.

**Outputs:**
- Absolute, cleaned resolved path.
- Error if path escapes baseDir, or if inputs are invalid.

**Error Cases:**
- `relPath` is absolute → error via MustBeRelative
- Resolved path escapes `baseDir` → `runtime.ErrPathEscape`
- `baseDir` is empty → error

**Implementation:**
```
1. Validate baseDir is non-empty
2. MustBeRelative(relPath) — reject absolute paths
3. joined := filepath.Join(baseDir, relPath)
4. resolved := filepath.Clean(joined)
5. absBase := filepath.Clean(baseDir)
6. If resolved == absBase → return resolved (root access is fine)
7. If !strings.HasPrefix(resolved, absBase + string(filepath.Separator)) → ErrPathEscape
8. Return resolved
```

**Edge cases to test:**
- `relPath = ""` → resolves to baseDir ✓
- `relPath = "."` → resolves to baseDir ✓
- `relPath = ".."` → ErrPathEscape ✓
- `relPath = "a/../b"` → baseDir/b ✓
- `relPath = "a/../../etc"` → ErrPathEscape ✓
- `relPath = "/etc/passwd"` → error (absolute) ✓
- `baseDir = "/home/user/project"`, `relPath = "../project/file.txt"` → ErrPathEscape ✓

### 1.2 `MustBeRelative(path string) error`

**Purpose:** Guard that rejects absolute paths.

**Implementation:**
```go
func MustBeRelative(path string) error {
    if filepath.IsAbs(path) {
        return fmt.Errorf("path must be relative, got absolute: %s", path)
    }
    return nil
}
```

---

## 2. Module: `internal/config`

### 2.1 `Load(flags *FlagOverrides) (*Config, error)`

**Purpose:** Load config from all sources, merge by priority, validate.

**Merge order (highest → lowest):**
1. CLI flags (from `flags`)
2. Environment variables (prefix `OPENCODE_`)
3. Config file (`.opencode.yaml` in CWD, then `~/.config/opencode/config.yaml`)
4. Hardcoded defaults

**Implementation:**
```
1. Start with defaults: Mode="local", Timeout=30s, Log.Level="info", Log.Format="text",
   Docker.MountPath="/workspace", Docker.Shell="sh", Docker.NetworkMode="none"
2. Try config file:
   a. If flags.ConfigPath set → use it (error if not found)
   b. Else try .opencode.yaml in CWD, then ~/.config/opencode/config.yaml
   c. If found → unmarshal YAML, overlay onto defaults
3. Read env vars, overlay non-empty values
4. Apply flags overrides for non-nil fields
5. If TargetDir is relative, resolve to absolute
6. Validate(cfg)
7. Return cfg
```

**`FlagOverrides` struct** — pointer fields so nil means "not set":
```go
type FlagOverrides struct {
    Mode        *string
    TargetDir   *string
    ConfigPath  *string
    LogLevel    *string
    LogFormat   *string
    DockerImage *string
}
```

### 2.2 `Validate(cfg *Config) error`

**Purpose:** Validate fully-merged config. Returns ALL violations, not just the first.

**Rules:**
1. `Mode` must be "local" or "docker"
2. `TargetDir` must be non-empty
3. If Mode=="docker": `Docker.Image` must be non-empty
4. If Mode=="docker": `Docker.MountPath` must be absolute
5. `DefaultTimeout` must be positive
6. `Log.Level` must be debug/info/warn/error
7. `Log.Format` must be text/json

---

## 3. Module: `internal/localrt`

### 3.1 `New(targetDir string, logger *slog.Logger) *LocalRuntime`

**Purpose:** Create uninitialized LocalRuntime. Directory validation happens in Init().

### 3.2 `Init(ctx context.Context) error`

**Purpose:** Validate target directory exists and is a directory.

**Error cases:**
- Does not exist → wrapped `os.ErrNotExist`
- Exists but is a file → "target is not a directory"
- Permission denied → wrapped

**Implementation:**
```
1. info, err := os.Stat(r.targetDir)
2. If err → return wrapped
3. If !info.IsDir() → return "not a directory" error
4. r.initialized = true
```

### 3.3 `ReadFile(ctx context.Context, relPath string) ([]byte, error)`

**Implementation:**
```
1. Guard: if !initialized → ErrRuntimeNotReady
2. Check ctx.Err() → return early if cancelled
3. absPath := pathutil.Resolve(r.targetDir, relPath)
4. data := os.ReadFile(absPath)
5. Return with wrapped errors
```

### 3.4 `WriteFile(ctx context.Context, relPath string, data []byte, perm os.FileMode) error`

**Implementation (atomic write):**
```
1. Guard: initialized, ctx
2. absPath := pathutil.Resolve(r.targetDir, relPath)
3. os.MkdirAll(filepath.Dir(absPath), 0755)
4. Write to temp file: absPath + ".tmp.<random>"
5. os.WriteFile(tmpPath, data, perm)
6. os.Rename(tmpPath, absPath) — atomic on same filesystem
7. If rename fails, clean up tmp file
```

### 3.5 `DeleteFile(ctx context.Context, relPath string) error`

**Implementation:**
```
1. Guard: initialized, ctx
2. absPath := pathutil.Resolve(r.targetDir, relPath)
3. os.Remove(absPath)
```

### 3.6 `ListDir(ctx context.Context, relPath string) ([]FileEntry, error)`

**Implementation:**
```
1. Guard: initialized, ctx
2. absPath := pathutil.Resolve(r.targetDir, relPath)
3. entries := os.ReadDir(absPath)
4. Convert each to FileEntry (call Info() for size/modtime/perm)
```

### 3.7 `MkDir(ctx context.Context, relPath string, perm os.FileMode) error`

**Implementation:**
```
1. Guard: initialized, ctx
2. absPath := pathutil.Resolve(r.targetDir, relPath)
3. os.MkdirAll(absPath, perm)
```

### 3.8 `Exec(ctx context.Context, command string, opts ExecOpts) (*ExecResult, error)`

**Purpose:** Execute shell command in target directory.

**Critical: non-zero exit code is NOT an error.**

**Implementation:**
```
1. Guard: initialized
2. Determine workDir:
   - If opts.RelDir empty → r.targetDir
   - Else → pathutil.Resolve(r.targetDir, opts.RelDir)
3. Determine timeout:
   - If opts.Timeout > 0 → use it
   - Else → use runtime's default
   - timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
4. cmd := exec.CommandContext(timeoutCtx, "sh", "-c", command)
5. cmd.Dir = workDir
6. cmd.Env = append(os.Environ(), opts.Env...)
7. var stdout, stderr bytes.Buffer
8. cmd.Stdout = &stdout; cmd.Stderr = &stderr
9. start := time.Now()
10. err := cmd.Run()
11. duration := time.Since(start)
12. exitCode := 0
13. If err != nil:
    a. ExitError → exitCode = exitErr.ExitCode() (NOT an error)
    b. context.DeadlineExceeded → return nil, timeout error
    c. Other → return nil, wrapped infrastructure error
14. Return &ExecResult{...}, nil
```

---

## 4. Module: `internal/dockerrt`

### 4.1 `Init(ctx context.Context) error`

```
1. Connect: client.NewClientWithOpts(client.FromEnv)
2. Check image: ImageInspect. If not found → ImagePull (with progress logging)
3. ContainerCreate:
   - Image: r.cfg.Image
   - Cmd: ["sleep", "infinity"]
   - WorkingDir: r.cfg.MountPath
   - HostConfig.Binds: [r.targetDir + ":" + r.cfg.MountPath]
   - HostConfig.NetworkMode: r.cfg.NetworkMode
   - HostConfig.Resources.Memory/CPUs if configured
4. ContainerStart
5. ContainerInspect → verify State.Running
6. r.containerID = id, r.initialized = true
```

### 4.2 `Close(ctx context.Context) error`

```
1. If containerID empty → return nil
2. ContainerStop(ctx, id, timeout=5s)
3. ContainerRemove(ctx, id, force=true)
4. r.containerID = "", r.initialized = false
```

Best-effort: if stop fails, force remove. Log errors but don't panic.

### 4.3 `ReadFile(ctx context.Context, relPath string) ([]byte, error)`

```
1. Guard: initialized, path validation against MountPath
2. containerPath := path.Join(r.cfg.MountPath, relPath)  // path.Join, not filepath
3. Exec inside container: "base64 < " + shellQuote(containerPath)
4. Decode base64 stdout
5. Return decoded bytes
```

### 4.4 `Exec(ctx context.Context, command string, opts ExecOpts) (*ExecResult, error)`

```
1. Guard: initialized, container running
2. workDir inside container:
   - Empty opts.RelDir → r.cfg.MountPath
   - Else → path.Join(r.cfg.MountPath, opts.RelDir)
3. ContainerExecCreate:
   - Cmd: ["sh", "-c", command]
   - WorkingDir: workDir
   - AttachStdout: true, AttachStderr: true
   - Env: opts.Env
4. ContainerExecAttach → multiplexed stream
5. stdcopy.StdCopy(&stdout, &stderr, resp.Reader) — MUST demux
6. ContainerExecInspect → get ExitCode
7. Return ExecResult
```

---

## 5. Module: `internal/cli`

### 5.1 `Runner.Run(ctx, fn func(ctx, Runtime) error) error`

Common lifecycle for all commands:
```
1. Load config
2. Create logger
3. NewRuntime(cfg, logger)
4. runtime.Init(ctx)
5. defer runtime.Close(ctx)
6. return fn(ctx, runtime)
```

### 5.2 Example command: `read`

```go
func newReadCmd(runner *Runner) *cobra.Command {
    return &cobra.Command{
        Use:   "read <path>",
        Short: "Read a file and print to stdout",
        Args:  cobra.ExactArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            return runner.Run(cmd.Context(), func(ctx context.Context, rt runtime.Runtime) error {
                data, err := rt.ReadFile(ctx, args[0])
                if err != nil {
                    return err
                }
                _, err = os.Stdout.Write(data)
                return err
            })
        },
    }
}
```

---

## 6. Module: `internal/runtime/factory.go`

### 6.1 `NewRuntime(cfg *config.Config, logger *slog.Logger) (Runtime, error)`

```go
func NewRuntime(cfg *config.Config, logger *slog.Logger) (Runtime, error) {
    switch cfg.Mode {
    case "local":
        return localrt.New(cfg.TargetDir, logger), nil
    case "docker":
        return dockerrt.New(cfg.TargetDir, cfg.Docker, logger), nil
    default:
        return nil, fmt.Errorf("unknown runtime mode: %s", cfg.Mode)
    }
}
```

Simple switch. Config already validated. No over-abstraction.
