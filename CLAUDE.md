# OpenCode-Go

## What This Is
CLI tool in Go. Two runtime modes (local, Docker) behind a unified `Runtime` interface.
Local mode: host filesystem + os/exec. Docker mode: Docker SDK, bind-mounted container.

## Architecture
- `internal/runtime/` — Runtime interface, types, factory
- `internal/localrt/` — Local implementation
- `internal/dockerrt/` — Docker implementation (Docker SDK, not CLI shelling)
- `internal/config/` — Config loading (flags > env > file > defaults)
- `internal/pathutil/` — Path sandboxing (prevent traversal attacks)
- `internal/logger/` — slog wrapper
- `internal/cli/` — Cobra commands + Runner lifecycle
- `cmd/opencode/` — main.go entrypoint

## Key Design Rules
- Single `Runtime` interface owns BOTH file ops and command execution
- No `core/`, no `utils/`, no `pkg/` packages — everything has a named home
- Path sandboxing via pathutil.Resolve on every file operation
- Docker: one container per Runtime instance, reused for all ops, `sleep infinity`
- Docker file I/O: base64 encode/decode for binary safety
- Docker streams: use stdcopy.StdCopy to demux docker exec output
- Exec: non-zero exit code is NOT an error — it goes in ExecResult.ExitCode
- Atomic writes: temp file + rename
- All errors wrapped with context: fmt.Errorf("pkg.Func(%s): %w", arg, err)
- Sentinel errors: ErrPathEscape, ErrContainerNotRunning, ErrRuntimeNotReady

## Dependencies
- cobra (CLI), docker/docker (SDK), yaml.v3 (config), testify (tests)
- Standard lib: log/slog, os, os/exec, context, path/filepath, errors

## Current State
Read `progress/progress.md` for what's done and what's next.
Read `specs/system-spec.md` for full architecture and interface contracts.
Read `specs/function-design.md` for function-level design with implementation notes.

## Code Style
- Go 1.22+, gofumpt formatting
- No interface{} — use any or generics
- Tests: testify require/assert, t.TempDir() for fs tests
- Test files: *_test.go next to implementation
- Docker integration tests: //go:build integration && docker
- Table-driven tests for multi-case functions

## Workflow
1. Before starting work, ALWAYS read `progress/progress.md` first
2. Implement ONE module at a time, fully, with tests
3. Run `go test ./...` after every change
4. After completing a module, update `progress/progress.md` (check items, add session log entry)
5. Commit with conventional commit message
6. Do NOT move to the next module until current module passes all tests

## Commit Messages
feat: add <module> <description>
test: add <module> tests
docs: update <what>
refactor: <what changed>
