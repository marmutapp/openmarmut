# Project Progress Tracker

**Project:** OpenCode-Go  
**Started:** 2026-03-13  
**Last Updated:** 2026-03-13

---

## Phase 1: Specifications & Design
- [x] System overview and architecture
- [x] Runtime interface contract
- [x] Local runtime design
- [x] Docker runtime design
- [x] All module definitions
- [x] CLI design
- [x] Error strategy
- [x] Testing strategy
- [x] Detailed function design
- [x] Folder structure
- [x] CLAUDE.md + rules setup

## Phase 2: Foundation Modules
- [x] `go.mod` init (`github.com/gajaai/opencode-go`)
- [x] `internal/runtime/runtime.go` — Runtime interface + shared types + sentinel errors
- [x] `internal/pathutil/pathutil.go` — Resolve + MustBeRelative
- [x] `internal/pathutil/pathutil_test.go` — all edge cases
- [x] `internal/config/config.go` — Config struct, Load, Validate, FlagOverrides
- [x] `internal/config/config_test.go` — merge order, validation
- [x] `internal/logger/logger.go` — New(LogConfig) → *slog.Logger
- [x] `internal/logger/logger_test.go`

## Phase 3: Local Runtime
- [ ] `internal/localrt/localrt.go` — struct, New, Init, Close
- [ ] `internal/localrt/localrt.go` — ReadFile (path sandboxing)
- [ ] `internal/localrt/localrt.go` — WriteFile (atomic write)
- [ ] `internal/localrt/localrt.go` — DeleteFile
- [ ] `internal/localrt/localrt.go` — ListDir
- [ ] `internal/localrt/localrt.go` — MkDir
- [ ] `internal/localrt/localrt.go` — Exec (timeout, exit code handling)
- [ ] `internal/localrt/localrt_test.go` — full unit test suite
- [ ] Runtime compliance test (local)

## Phase 4: CLI
- [ ] `internal/cli/root.go` — root cobra command + global flags
- [ ] `internal/cli/runner.go` — Runner lifecycle helper
- [ ] `internal/cli/read.go`
- [ ] `internal/cli/write.go`
- [ ] `internal/cli/delete.go`
- [ ] `internal/cli/ls.go`
- [ ] `internal/cli/mkdir.go`
- [ ] `internal/cli/exec.go`
- [ ] `internal/cli/info.go`
- [ ] `cmd/opencode/main.go` — entrypoint
- [ ] Smoke test: `go run ./cmd/opencode read README.md`

## Phase 5: Docker Runtime
- [ ] `internal/dockerrt/dockerrt.go` — struct, New, Init (container create/start)
- [ ] `internal/dockerrt/dockerrt.go` — Close (stop + remove)
- [ ] `internal/dockerrt/dockerrt.go` — ReadFile (base64 via docker exec)
- [ ] `internal/dockerrt/dockerrt.go` — WriteFile (stdin pipe)
- [ ] `internal/dockerrt/dockerrt.go` — DeleteFile
- [ ] `internal/dockerrt/dockerrt.go` — ListDir
- [ ] `internal/dockerrt/dockerrt.go` — MkDir
- [ ] `internal/dockerrt/dockerrt.go` — Exec (multiplexed stream handling)
- [ ] `internal/dockerrt/dockerrt_test.go` — unit tests (mocked Docker client)
- [ ] `internal/dockerrt/dockerrt_integration_test.go` — real Docker tests
- [ ] Runtime compliance test (docker)

## Phase 6: Integration & Polish
- [ ] `internal/runtime/factory.go` — NewRuntime factory
- [ ] End-to-end CLI tests (both modes)
- [ ] Makefile (build, test, lint, integration-test)
- [ ] Dockerfile (default image for docker mode)
- [ ] README.md
- [ ] `docs/architecture.md`

---

## Completion Criteria

A phase is complete when:
1. All items are checked
2. `go test ./...` passes
3. No TODO/FIXME in code
4. Code reviewed for DRY violations
5. Changes committed with conventional message

---

## Session Log

Format: YYYY-MM-DD | Phase | What was accomplished | What's next

2026-03-13 | Phase 1 | Design complete. Specs, CLAUDE.md, rules, progress tracker created. | Start Phase 2: go.mod + foundation modules
2026-03-13 | Phase 2 | All foundation modules implemented with tests: runtime interface, pathutil, config, logger. All tests pass. | Start Phase 3: local runtime

<!-- Claude: append a new line here after each working session -->
