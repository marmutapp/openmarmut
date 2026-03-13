# Handoff: LocalRuntime (Phase 3)

## What was built

`internal/localrt/localrt.go` — full `runtime.Runtime` implementation for host-local execution.

## Files

| File | Lines | Purpose |
|------|-------|---------|
| `internal/localrt/localrt.go` | 294 | All 9 Runtime methods |
| `internal/localrt/localrt_test.go` | ~500 | 44 tests |

## Constructor

```go
localrt.New(targetDir string, defaultTimeout time.Duration, logger *slog.Logger) *LocalRuntime
```

- `defaultTimeout <= 0` defaults to 30s.
- Must call `Init(ctx)` before any operation; all methods return `ErrRuntimeNotReady` otherwise.

## Key implementation patterns

### Atomic writes (`WriteFile`)

Every write goes through temp-file + rename to prevent partial writes on crash:

1. `os.CreateTemp` in same directory as target (same filesystem = atomic rename)
2. Write data, `Chmod`, close
3. `os.Rename(tmp, target)`
4. Deferred cleanup removes temp file if any step fails (`success` flag pattern)

### Exit code is not an error (`Exec`)

`cmd.Run()` returns `*exec.ExitError` for non-zero exits. We extract `ExitCode()` and return it in `ExecResult` — the Go-level `error` return is `nil`. Only infrastructure failures (can't find `sh`, timeout) produce errors.

### Timeout with process group kill

Problem discovered during implementation: `exec.CommandContext` kills the shell process on timeout, but child processes (e.g., `sleep`) inherit stdout/stderr pipes and keep `cmd.Wait()` blocked until they die.

Fix applied:
- `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}` — new process group
- `cmd.Cancel` — kills entire group via `syscall.Kill(-pid, SIGKILL)`
- Context deadline check comes BEFORE `ExitError` check, because the kill signal also produces an `ExitError`

### Path sandboxing

Every file method calls `pathutil.Resolve(r.targetDir, relPath)` which:
1. Rejects absolute paths
2. Joins + cleans
3. Verifies result stays within `targetDir`

### Context cancellation

All file methods check `ctx.Err()` at the top. `Exec` uses `context.WithTimeout` wrapping the caller's context.

## Test coverage

| Category | Count | What's tested |
|----------|-------|---------------|
| Init/Close | 5 | Valid dir, nonexistent, file-not-dir, close resets state, TargetDir() |
| Guard | 1 | All 6 methods reject when uninitialized |
| ReadFile | 6 | Success, nested, not-exist, path escape, absolute path, cancelled ctx |
| WriteFile | 8 | Create, overwrite, parent dirs, atomic cleanup, permissions, binary data, empty, path escape, cancelled ctx |
| DeleteFile | 3 | Success, not-exist, path escape |
| ListDir | 4 | Populated, empty, not-exist, path escape |
| MkDir | 4 | Simple, nested, idempotent, path escape |
| Exec | 10 | Simple, non-zero exit, stderr, separate streams, workdir, workdir escape, env vars, timeout, default timeout, timeout override, cancelled ctx |
| Round-trips | 3 | write→read→delete, mkdir→listdir, exec→readfile |

## Gotchas for future work

1. **`New` takes `defaultTimeout`** — this is not in the `Runtime` interface. The CLI/factory layer needs to pass `config.DefaultTimeout` when constructing.
2. **Process group kill uses `syscall`** — Linux-only. If cross-platform support is needed, this needs build tags.
3. **`ListDir` calls `Info()` per entry** — one extra syscall per entry. Acceptable for typical directory sizes.
4. **`WriteFile` temp files** use `.opencode-tmp-*` prefix — visible briefly during writes. Cleaned up on failure via deferred remove.

## What's next

Phase 4: CLI layer (`internal/cli/` + `cmd/opencode/main.go`). The CLI will construct `LocalRuntime` via the Runner lifecycle pattern.
