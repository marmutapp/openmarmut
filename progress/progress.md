# Project Progress Tracker

**Project:** OpenCode-Go  
**Started:** 2026-03-13  
**Last Updated:** 2026-03-14

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
- [x] `internal/localrt/localrt.go` — struct, New, Init, Close
- [x] `internal/localrt/localrt.go` — ReadFile (path sandboxing)
- [x] `internal/localrt/localrt.go` — WriteFile (atomic write)
- [x] `internal/localrt/localrt.go` — DeleteFile
- [x] `internal/localrt/localrt.go` — ListDir
- [x] `internal/localrt/localrt.go` — MkDir
- [x] `internal/localrt/localrt.go` — Exec (timeout, exit code handling)
- [x] `internal/localrt/localrt_test.go` — full unit test suite
- [x] Runtime compliance test (local)

## Phase 4: CLI
- [x] `internal/cli/root.go` — root cobra command + global flags
- [x] `internal/cli/runner.go` — Runner lifecycle helper
- [x] `internal/cli/read.go`
- [x] `internal/cli/write.go`
- [x] `internal/cli/delete.go`
- [x] `internal/cli/ls.go`
- [x] `internal/cli/mkdir.go`
- [x] `internal/cli/exec.go`
- [x] `internal/cli/info.go`
- [x] `cmd/opencode/main.go` — entrypoint
- [x] Smoke test: `go run ./cmd/opencode read README.md`

## Phase 5: Docker Runtime
- [x] `internal/dockerrt/dockerrt.go` — struct, New, Init (container create/start)
- [x] `internal/dockerrt/dockerrt.go` — Close (stop + remove)
- [x] `internal/dockerrt/dockerrt.go` — ReadFile (base64 via docker exec)
- [x] `internal/dockerrt/dockerrt.go` — WriteFile (base64 encode via docker exec)
- [x] `internal/dockerrt/dockerrt.go` — DeleteFile
- [x] `internal/dockerrt/dockerrt.go` — ListDir
- [x] `internal/dockerrt/dockerrt.go` — MkDir
- [x] `internal/dockerrt/dockerrt.go` — Exec (multiplexed stream handling via stdcopy)
- [x] `internal/dockerrt/dockerrt_test.go` — unit tests (mocked Docker client, 40 tests)
- [x] `internal/dockerrt/dockerrt_integration_test.go` — real Docker tests (gated by build tags)
- [x] Runtime compliance test (docker)

## Phase 6: Integration & Polish
- [x] `internal/runtime/factory.go` — NewRuntime factory with registration pattern
- [x] `internal/localrt/register.go` — registers local constructor
- [x] `internal/dockerrt/register.go` — registers docker constructor
- [x] CLI runner updated to use factory (both modes supported)
- [x] `internal/runtime/factory_test.go` — unknown mode test
- [x] Makefile (build, test, lint, integration-test)
- [x] Dockerfile (minimal ubuntu:20.04 for docker mode)
- [x] README.md

## Phase 7: LLM Integration (Provider Type System)

### Phase 7a: Foundation
- [x] `specs/llm-spec.md` — LLM integration specification v2 (provider type system, custom endpoints)
- [x] `internal/llm/types.go` — Provider interface, Request/Response, ProviderEntry, AuthConfig, sentinel errors
- [x] `internal/llm/factory.go` — RegisterType/NewProvider with ProviderEntry (defaults + credential resolution)
- [x] `internal/llm/credentials.go` — ResolveCredential, ApplyAuth, DefaultAuthForType, DefaultEndpointURL
- [x] `internal/llm/llm_test.go` — 20 tests: factory, credentials, auth, defaults
- [x] Config additions — `LLMConfig` with `[]ProviderEntry`, `active_provider`, validation
- [x] Config additions — FlagOverrides: `--provider`, `--model`, `--temperature`
- [x] Config additions — Active provider resolution logic (flag > env > config > single-entry)
- [x] `internal/llm/openai/openai.go` — OpenAI wire format (streaming, tool calls, custom headers)
- [x] `internal/llm/openai/openai_test.go` — 21 httptest-based unit tests
- [x] `internal/llm/anthropic/anthropic.go` — Anthropic wire format (updated to ProviderEntry + ApplyAuth)
- [x] `internal/llm/anthropic/anthropic_test.go` — 16 httptest-based unit tests (updated for ProviderEntry)
- [x] `internal/agent/agent.go` — Agent loop (observe→plan→act→verify), 21 tests
- [x] `internal/agent/tools.go` — 6 tools mapped to Runtime methods (read_file, write_file, delete_file, list_dir, mkdir, execute_command)
- [x] `internal/agent/agent_test.go` — Agent loop tests with mock provider and runtime (streaming after tool calls verified)
- [ ] `internal/agent/security.go` — ContainsAPIKey, credential redaction
- [x] `internal/cli/ask.go` — `opencode ask` with agent loop + `--no-tools` flag for simple questions
- [x] `internal/cli/chat.go` — `opencode chat` interactive REPL with multi-turn agent loop
- [x] `internal/cli/providers.go` — `opencode providers` list command
- [x] Root command flags: `--provider`, `--model`, `--temperature`
- [x] `initRuntime` helper in runner.go for ask/chat commands
- [x] Tested end-to-end with Azure OpenAI (gpt-5.1-codex-mini via openai-responses type)

### Phase 7b: Remaining Wire Formats
- [x] `internal/llm/responses/responses.go` — OpenAI Responses API wire format (o3, o4-mini, Codex, Azure)
- [x] `internal/llm/responses/responses_test.go` — 22 httptest-based unit tests (incl. multi-turn agent flow, full URL support)
- [x] `internal/llm/gemini/gemini.go` — Gemini wire format (streaming, functionCall/functionResponse)
- [x] `internal/llm/gemini/gemini_test.go` — 15 httptest-based unit tests
- [x] `internal/llm/ollama/ollama.go` — Ollama wire format (NDJSON streaming, tool calls)
- [x] `internal/llm/ollama/ollama_test.go` — 15 httptest-based unit tests
- [x] `internal/llm/custom/custom.go` — Custom provider (configurable endpoint, extra payload fields)
- [x] `internal/llm/custom/custom_test.go` — 19 httptest-based unit tests
- [x] `internal/cli/ask.go` — Updated imports to register all 6 provider types

### Phase 7c: Polish
- [ ] Context window management — token counting, history summarization
- [ ] Retry logic — exponential backoff for rate limits
- [ ] Cost tracking — token usage display

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
2026-03-13 | Phase 3 | LocalRuntime fully implemented: all 8 methods + 44 tests. Atomic writes, path sandboxing, exit-code-not-error, process group kill on timeout. | Start Phase 4: CLI
2026-03-13 | Phase 4 | CLI complete: root+runner+7 commands+main.go. Cobra-based with global flags, Runner lifecycle pattern. All smoke tests pass (read, write, delete, ls, mkdir, exec, info). | Start Phase 5: Docker runtime
2026-03-13 | Phase 5 | DockerRuntime fully implemented: all 9 Runtime methods, dockerClient interface for testability, 40 unit tests (mocked), 16 integration tests (build-tagged). Base64 file I/O, stdcopy stream demux, container lifecycle, path sandboxing, shell quoting. | Start Phase 6: Integration & polish
2026-03-13 | Phase 6 | Factory with init-registration pattern, CLI wired to both runtimes, Makefile, Dockerfile, README. All tests pass. | Start Phase 7: LLM integration
2026-03-13 | Phase 7 | LLM integration spec complete (specs/llm-spec.md): Provider interface, 4 providers (OpenAI/Anthropic/Gemini/Ollama), agent loop, tool definitions, config, CLI commands, credential security. | Start Phase 7a implementation
2026-03-13 | Phase 7a | Provider interface + types + factory + ResolveAPIKey (internal/llm/llm.go). Anthropic provider fully implemented: SSE streaming, tool_use blocks, input_json_delta accumulation, system prompt extraction, tool result merging. 22 unit tests. | Continue Phase 7a: remaining providers + agent
2026-03-13 | Phase 7 | Rewrote specs/llm-spec.md v2: provider type system (wire format abstraction), ProviderEntry/AuthConfig structs, multi-provider config with active_provider, credential env var references, custom endpoint support, `opencode providers` command. Updated progress.md Phase 7 checkboxes. | Continue Phase 7a: update llm.go to ProviderEntry, implement OpenAI wire format
2026-03-13 | Phase 7a | Rewrote LLM package: types.go + factory.go + credentials.go replacing llm.go. ProviderEntry/AuthConfig, RegisterType/NewProvider factory with defaults+credential resolution, ApplyAuth helper. Updated Anthropic to ProviderEntry. Implemented OpenAI wire format: SSE streaming, tool call accumulation, custom headers. 57 total tests (20 llm + 16 anthropic + 21 openai). | Continue Phase 7a: config additions, agent loop, CLI commands
2026-03-13 | Phase 7a | LLM config wired into internal/config: LLMConfig struct (Providers, ActiveProvider, DefaultTemperature, DefaultMaxTokens, DefaultTimeout), FlagOverrides (--provider, --model, --temperature), env vars (OPENCODE_LLM_PROVIDER/MODEL/API_KEY), validation, ResolveActiveProvider with override chain. 29 new tests (42 total config tests). | Continue Phase 7a: agent loop, CLI commands
2026-03-13 | Phase 7a | CLI commands: `opencode providers` (tabwriter, active marker), `opencode ask` (single-turn streaming via provider.Complete), root flags --provider/--model/--temperature wired to FlagOverrides. Smoke tested with multi-provider config. | Continue Phase 7a: agent loop, chat REPL
2026-03-14 | Phase 7b | All 4 remaining wire formats implemented: openai-responses (18 tests), gemini (15 tests), ollama (15 tests), custom (19 tests). Updated ask.go imports to register all 6 provider types. 67 new tests across 4 packages. | Continue Phase 7a: agent loop, chat REPL
2026-03-14 | Phase 7a | Agent loop implemented: tools.go (6 tools → Runtime), agent.go (loop with max iterations, usage aggregation, history), 21 tests. CLI wired: ask uses agent loop with --no-tools flag, chat REPL added. | Phase 7a nearly complete, remaining: security.go
2026-03-14 | Phase 7a+7b | Bug fixes: responses provider tool call serialization (call_id, empty assistant msg), endpoint URL path detection, streaming after tool calls. Tested end-to-end with Azure OpenAI gpt-5.1-codex-mini. Phase 7a+7b complete except security.go. | Start Phase 7c or security.go

<!-- Claude: append a new line here after each working session -->
