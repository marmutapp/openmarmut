# OpenCode-Go: LLM Integration Specification

**Version:** 1.0
**Last Updated:** 2026-03-13
**Status:** Design Complete — Implementation Not Started
**Depends on:** specs/system-spec.md (Phases 1–6 complete)

---

## 1. Overview

Phase 7 adds AI model support to OpenCode-Go. An LLM reads the project via the
existing `Runtime` interface, decides what operations to perform, and executes
them through the same Runtime. The tool becomes an agentic coding assistant that
works identically whether the underlying runtime is local or Docker.

### What Changes

```
Before (Phase 1–6):
  User → CLI command → Runtime → result

After (Phase 7):
  User → "ask" or "chat" → LLM → [tool calls] → Runtime → [observations] → LLM → answer
```

### What Does NOT Change

- The `Runtime` interface is untouched. The LLM is a consumer of Runtime, not a replacement.
- Existing CLI commands (`read`, `write`, `exec`, etc.) continue to work as before.
- Config loading, logger, path sandboxing — all unchanged.

---

## 2. Architecture

### 2.1 Updated Layer Diagram

```
┌─────────────────────────────────────────────────────┐
│                  CLI Layer (cmd/)                    │
│  Existing commands + new: chat, ask                 │
├─────────────────────────────────────────────────────┤
│               Agent Loop (agent/)                   │
│  observe → plan → act → verify                      │
│  Owns the conversation, dispatches tool calls       │
├──────────────┬──────────────────────────────────────┤
│  LLM Client  │         Runtime                      │
│  (llm/)      │  (localrt/ or dockerrt/)             │
│  Talks to    │  Executes file ops                   │
│  AI provider │  and shell commands                  │
├──────────────┴──────────────────────────────────────┤
│              Shared Packages                        │
│  config/ | logger/ | pathutil/ | runtime/           │
└─────────────────────────────────────────────────────┘
```

### 2.2 Key Design Decisions

**Decision 1: LLM client is provider-agnostic via interface.**

A `Provider` interface abstracts all LLM APIs. Implementations are isolated in
sub-packages. The agent loop never imports a provider directly.

**Decision 2: The agent loop is separate from the LLM client.**

`internal/llm/` handles HTTP-level API communication.
`internal/agent/` handles the observe→plan→act→verify loop, conversation
history, and tool dispatch. This separation means the agent loop can be
tested with a mock provider.

**Decision 3: Tool calls map 1:1 to Runtime methods.**

The LLM sees tools named `read_file`, `write_file`, `list_dir`, `exec`, etc.
Each tool call maps directly to a `Runtime` method. No intermediate abstraction.

**Decision 4: Streaming by default.**

All providers stream responses token-by-token. This gives the user immediate
feedback. Non-streaming is a degenerate case of streaming (single chunk).

**Decision 5: No agent autonomy limits in the spec.**

The agent loop runs until the LLM emits a final text response with no tool
calls. Iteration limits, cost caps, and confirmation prompts are
implementation details for Phase 7b or later.

---

## 3. Provider Interface

### 3.1 Package: `internal/llm`

```go
package llm

import (
    "context"
)

// Provider abstracts an LLM API. Implementations must be safe for sequential use.
type Provider interface {
    // Complete sends a conversation to the model and streams the response.
    // The callback is invoked for each chunk. The final Response is returned
    // after the stream completes.
    //
    // If the response contains tool calls, they appear in Response.ToolCalls.
    // The caller is responsible for executing tools and appending results
    // before calling Complete again.
    Complete(ctx context.Context, req Request, cb StreamCallback) (*Response, error)

    // Name returns the provider identifier (e.g., "openai", "anthropic").
    Name() string

    // Model returns the model identifier being used (e.g., "gpt-4o", "claude-sonnet-4-20250514").
    Model() string
}

// StreamCallback is called for each token chunk during streaming.
// text is the incremental text delta. Return a non-nil error to abort the stream.
type StreamCallback func(text string) error

// Request is a single completion request.
type Request struct {
    Messages   []Message
    Tools      []ToolDef
    Temperature *float64 // nil = provider default
    MaxTokens  *int      // nil = provider default
}

// Response is the result of a completion.
type Response struct {
    Content    string      // The text content of the response (may be empty if only tool calls)
    ToolCalls  []ToolCall  // Zero or more tool invocations requested by the model
    Usage      Usage       // Token counts
    StopReason string      // "end", "tool_use", "max_tokens", "error"
}

// Message is a single message in the conversation.
type Message struct {
    Role       Role
    Content    string      // Text content (for user, assistant, system messages)
    ToolCalls  []ToolCall  // Only for assistant messages that invoke tools
    ToolCallID string      // Only for tool result messages — matches ToolCall.ID
}

// Role is the message sender.
type Role string

const (
    RoleSystem    Role = "system"
    RoleUser      Role = "user"
    RoleAssistant Role = "assistant"
    RoleTool      Role = "tool"
)

// ToolCall is a request from the model to execute a tool.
type ToolCall struct {
    ID        string // Unique ID for this call (provider-assigned)
    Name      string // Tool function name (e.g., "read_file")
    Arguments string // JSON-encoded arguments
}

// ToolDef describes a tool the model can invoke.
type ToolDef struct {
    Name        string // Function name
    Description string // What the tool does (shown to the model)
    Parameters  any    // JSON Schema object describing the parameters
}

// Usage tracks token consumption.
type Usage struct {
    PromptTokens     int
    CompletionTokens int
    TotalTokens      int
}

// Sentinel errors.
var (
    ErrAuthFailed     = errors.New("authentication failed — check API key")
    ErrRateLimited    = errors.New("rate limited by provider")
    ErrModelNotFound  = errors.New("model not found")
    ErrContextTooLong = errors.New("input exceeds model context window")
    ErrStreamAborted  = errors.New("stream aborted by callback")
)
```

### 3.2 Provider Factory

```go
package llm

import "log/slog"

// ProviderConfig holds everything needed to create a provider.
type ProviderConfig struct {
    Name        string  // "openai", "anthropic", "gemini", "ollama"
    Model       string  // e.g., "gpt-4o", "claude-sonnet-4-20250514"
    APIKey      string  // Resolved key (never from a flag, never logged)
    BaseURL     string  // Override for Ollama or proxies
    Temperature *float64
    MaxTokens   *int
}

// NewProvider creates a Provider from config.
func NewProvider(cfg ProviderConfig, logger *slog.Logger) (Provider, error)
```

The factory switches on `cfg.Name` and returns the correct implementation.
Unknown provider names return an error.

---

## 4. Provider Implementations

### 4.1 Package Structure

```
internal/llm/
  llm.go              — Provider interface, types, factory, sentinel errors
  openai/
    openai.go         — OpenAI implementation (GPT-4o, GPT-4o-mini, o1, etc.)
    openai_test.go
  anthropic/
    anthropic.go      — Anthropic implementation (Claude 4 Sonnet, Claude 4 Opus, etc.)
    anthropic_test.go
  gemini/
    gemini.go         — Google Gemini implementation
    gemini_test.go
  ollama/
    ollama.go         — Ollama local model implementation
    ollama_test.go
```

### 4.2 OpenAI (`internal/llm/openai`)

- **API:** `POST https://api.openai.com/v1/chat/completions`
- **Auth:** `Authorization: Bearer $OPENAI_API_KEY`
- **Streaming:** SSE via `stream: true`, parse `data: {...}` lines
- **Tool calls:** Use `tools` field with `type: "function"`, parse `tool_calls` in response
- **Models:** `gpt-4o`, `gpt-4o-mini`, `o1`, `o1-mini`
- **Base URL override:** Configurable for Azure OpenAI or proxies

**Request mapping:**

| llm.Request field | OpenAI field |
|-------------------|-------------|
| Messages | messages (role mapping: system→system, user→user, assistant→assistant, tool→tool) |
| Tools | tools (type: "function", function: {name, description, parameters}) |
| Temperature | temperature |
| MaxTokens | max_tokens |

### 4.3 Anthropic (`internal/llm/anthropic`)

- **API:** `POST https://api.anthropic.com/v1/messages`
- **Auth:** `x-api-key: $ANTHROPIC_API_KEY` header
- **Streaming:** SSE via `stream: true`, parse event types: `content_block_delta`, `message_stop`, etc.
- **Tool calls:** Use `tools` field, model returns `tool_use` content blocks
- **Models:** `claude-sonnet-4-20250514`, `claude-opus-4-20250514`, `claude-haiku-4-5-20251001`
- **System message:** Separate `system` field (not in messages array)

**Key differences from OpenAI:**

- System prompt is a top-level field, not a message
- Tool results are sent as `tool_result` content blocks in user messages
- Each tool call has an `id`; tool results must reference it
- Anthropic-specific header: `anthropic-version: 2023-06-01`

### 4.4 Gemini (`internal/llm/gemini`)

- **API:** `POST https://generativelanguage.googleapis.com/v1beta/models/{model}:streamGenerateContent`
- **Auth:** `?key=$GOOGLE_API_KEY` query parameter
- **Streaming:** JSON streaming (chunked responses)
- **Tool calls:** `functionDeclarations` in `tools`, model returns `functionCall` parts
- **Models:** `gemini-2.5-pro`, `gemini-2.5-flash`

**Key differences:**

- Auth via query parameter, not header
- Tool definitions use `functionDeclarations` schema (similar to but not identical to JSON Schema)
- Content blocks use `parts` array with different part types

### 4.5 Ollama (`internal/llm/ollama`)

- **API:** `POST http://localhost:11434/api/chat` (OpenAI-compatible endpoint also available)
- **Auth:** None (local server)
- **Streaming:** NDJSON (newline-delimited JSON), each line is a response chunk
- **Tool calls:** Supported via `tools` field (same schema as OpenAI)
- **Models:** Any model installed locally (e.g., `llama3.1`, `codellama`, `deepseek-coder`)
- **Base URL:** Configurable (default `http://localhost:11434`)

**Key differences:**

- No authentication required
- Base URL must be configurable (user may run Ollama on a different host/port)
- Model must be pre-pulled (`ollama pull <model>`)
- Tool support varies by model — some models don't support function calling

---

## 5. Configuration

### 5.1 Config Additions

```go
// Added to config.Config
type Config struct {
    // ... existing fields ...
    LLM LLMConfig `yaml:"llm"`
}

// LLMConfig holds LLM provider settings.
type LLMConfig struct {
    Provider    string   `yaml:"provider"`     // "openai", "anthropic", "gemini", "ollama"
    Model       string   `yaml:"model"`        // Provider-specific model ID
    APIKeyEnv   string   `yaml:"api_key_env"`  // Env var name holding the key (default: auto)
    BaseURL     string   `yaml:"base_url"`     // Override API endpoint (for Ollama, proxies)
    Temperature *float64 `yaml:"temperature"`  // nil = provider default
    MaxTokens   *int     `yaml:"max_tokens"`   // nil = provider default
    SystemPrompt string  `yaml:"system_prompt"` // Custom system prompt (optional override)
}
```

### 5.2 API Key Resolution

Keys are **never** stored directly in config. The resolution order is:

1. Environment variable named in `LLMConfig.APIKeyEnv` (explicit override)
2. Standard environment variable for the provider:
   - OpenAI: `OPENAI_API_KEY`
   - Anthropic: `ANTHROPIC_API_KEY`
   - Gemini: `GOOGLE_API_KEY`
   - Ollama: (no key needed)
3. If neither is set and provider requires auth → return `ErrAuthFailed`

```go
// ResolveAPIKey returns the API key for the configured provider.
// Returns empty string for providers that don't need auth (ollama).
// Returns error if key is required but not found.
func ResolveAPIKey(cfg LLMConfig) (string, error)
```

### 5.3 Environment Variables

| Variable | Purpose |
|----------|---------|
| `OPENCODE_LLM_PROVIDER` | Provider name |
| `OPENCODE_LLM_MODEL` | Model identifier |
| `OPENCODE_LLM_BASE_URL` | Base URL override |
| `OPENAI_API_KEY` | OpenAI API key (standard) |
| `ANTHROPIC_API_KEY` | Anthropic API key (standard) |
| `GOOGLE_API_KEY` | Google Gemini API key (standard) |

Note: API key env vars use the standard names established by each provider,
not `OPENCODE_`-prefixed names. This matches ecosystem conventions and avoids
requiring users to duplicate keys.

### 5.4 FlagOverrides Additions

```go
// Added to config.FlagOverrides
type FlagOverrides struct {
    // ... existing fields ...
    LLMProvider    *string
    LLMModel       *string
    LLMTemperature *float64
}
```

### 5.5 Config File Examples

**OpenAI:**

```yaml
mode: local
llm:
  provider: openai
  model: gpt-4o
  temperature: 0.2
  max_tokens: 4096
```

**Anthropic:**

```yaml
mode: local
llm:
  provider: anthropic
  model: claude-sonnet-4-20250514
  temperature: 0.3
```

**Ollama (local):**

```yaml
mode: local
llm:
  provider: ollama
  model: llama3.1
  base_url: http://localhost:11434
```

**Docker + Gemini:**

```yaml
mode: docker
docker:
  image: opencode-sandbox
llm:
  provider: gemini
  model: gemini-2.5-pro
```

### 5.6 Validation

The following rules apply when `chat` or `ask` commands are used:

- `llm.provider` must be one of: `openai`, `anthropic`, `gemini`, `ollama`
- `llm.model` must be non-empty
- API key must resolve for providers that require one
- `llm.temperature`, if set, must be in `[0.0, 2.0]`
- `llm.max_tokens`, if set, must be positive

LLM validation is **skipped** for non-LLM commands (`read`, `write`, `exec`,
etc.) so the tool continues to work without any LLM configuration.

---

## 6. Tool Definitions

The agent exposes Runtime methods as tools the LLM can call.

### 6.1 Tool Registry

```go
package agent

// Tool defines a callable action backed by a Runtime method.
type Tool struct {
    Def     llm.ToolDef
    Execute func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error)
}

// DefaultTools returns the standard set of tools backed by a Runtime.
func DefaultTools() []Tool
```

### 6.2 Tool Definitions

| Tool Name | Parameters | Maps To | Returns |
|-----------|-----------|---------|---------|
| `read_file` | `{"path": "string"}` | `rt.ReadFile(ctx, path)` | File contents as string (truncated at 100KB with warning) |
| `write_file` | `{"path": "string", "content": "string"}` | `rt.WriteFile(ctx, path, []byte(content), 0644)` | `"wrote N bytes to <path>"` |
| `delete_file` | `{"path": "string"}` | `rt.DeleteFile(ctx, path)` | `"deleted <path>"` |
| `list_dir` | `{"path": "string"}` | `rt.ListDir(ctx, path)` | JSON array of entries |
| `mkdir` | `{"path": "string"}` | `rt.MkDir(ctx, path, 0755)` | `"created directory <path>"` |
| `exec` | `{"command": "string", "workdir": "string?"}` | `rt.Exec(ctx, command, opts)` | JSON with stdout, stderr, exit_code |

### 6.3 Tool Argument Schemas (JSON Schema)

```json
{
  "read_file": {
    "type": "object",
    "properties": {
      "path": {"type": "string", "description": "File path relative to project root"}
    },
    "required": ["path"]
  },
  "write_file": {
    "type": "object",
    "properties": {
      "path": {"type": "string", "description": "File path relative to project root"},
      "content": {"type": "string", "description": "Complete file content to write"}
    },
    "required": ["path", "content"]
  },
  "delete_file": {
    "type": "object",
    "properties": {
      "path": {"type": "string", "description": "File path relative to project root"}
    },
    "required": ["path"]
  },
  "list_dir": {
    "type": "object",
    "properties": {
      "path": {"type": "string", "description": "Directory path relative to project root. Use '.' for root."}
    },
    "required": ["path"]
  },
  "mkdir": {
    "type": "object",
    "properties": {
      "path": {"type": "string", "description": "Directory path to create (parents created automatically)"}
    },
    "required": ["path"]
  },
  "exec": {
    "type": "object",
    "properties": {
      "command": {"type": "string", "description": "Shell command to execute via sh -c"},
      "workdir": {"type": "string", "description": "Working directory relative to project root (optional)"}
    },
    "required": ["command"]
  }
}
```

---

## 7. Agent Loop

### 7.1 Package: `internal/agent`

```go
package agent

import (
    "context"
    "log/slog"

    "github.com/gajaai/opencode-go/internal/llm"
    "github.com/gajaai/opencode-go/internal/runtime"
)

// Agent orchestrates the observe→plan→act→verify loop.
type Agent struct {
    provider llm.Provider
    rt       runtime.Runtime
    tools    []Tool
    logger   *slog.Logger
    history  []llm.Message
}

// New creates an Agent with the given provider and runtime.
func New(provider llm.Provider, rt runtime.Runtime, logger *slog.Logger) *Agent

// Run sends a user message and runs the agentic loop until the model
// produces a final text response (no more tool calls).
// The stream callback receives incremental text tokens for display.
func (a *Agent) Run(ctx context.Context, userMessage string, stream llm.StreamCallback) (*Result, error)

// Result holds the outcome of an agent run.
type Result struct {
    Response string    // Final text response from the model
    Steps    []Step    // Executed tool calls and their results
    Usage    llm.Usage // Aggregated token usage across all turns
}

// Step records one tool invocation within an agent run.
type Step struct {
    ToolCall llm.ToolCall // What the model requested
    Output   string       // What the tool returned
    Error    string       // Non-empty if the tool failed
}
```

### 7.2 Agentic Loop Flow

```
┌──────────────────────────────────────────────────────┐
│                     Agent.Run()                      │
├──────────────────────────────────────────────────────┤
│                                                      │
│  1. Append user message to history                   │
│  2. Build Request{Messages: history, Tools: tools}   │
│                                                      │
│  ┌─────────── LOOP ──────────────────────────────┐   │
│  │                                                │   │
│  │  3. provider.Complete(ctx, request, streamCB)  │   │
│  │       ↓                                        │   │
│  │  4. Append assistant message to history         │   │
│  │       ↓                                        │   │
│  │  5. If response.ToolCalls is empty → BREAK     │   │
│  │       ↓                                        │   │
│  │  6. For each ToolCall:                         │   │
│  │     a. Find matching Tool by name              │   │
│  │     b. Execute Tool(ctx, rt, args)             │   │
│  │     c. Record Step                             │   │
│  │     d. Append tool result message to history   │   │
│  │       ↓                                        │   │
│  │  7. Go to step 3 (next turn)                   │   │
│  │                                                │   │
│  └────────────────────────────────────────────────┘   │
│                                                      │
│  8. Return Result{Response, Steps, Usage}            │
│                                                      │
└──────────────────────────────────────────────────────┘
```

### 7.3 System Prompt

The default system prompt is injected as the first message:

```
You are an AI coding assistant operating on a project directory.
You have access to tools for reading files, writing files, listing directories,
creating directories, deleting files, and executing shell commands.

The project is located at: {target_dir}

Rules:
- Always read a file before modifying it.
- Use list_dir to understand the project structure before making changes.
- After writing files, verify your changes by reading them back or running tests.
- Use exec to run build commands, tests, and linters.
- Explain what you are doing and why before each action.
- If a command fails, analyze the error and try a different approach.
```

The system prompt can be overridden via `llm.system_prompt` in config.

### 7.4 Conversation History Management

History grows unboundedly during a session. For long conversations:

- The agent tracks total token usage via `Response.Usage`
- When total tokens approach the model's context limit (provider-specific),
  the agent summarizes older messages into a condensed system message
- This is a Phase 7b optimization — initial implementation keeps full history

---

## 8. CLI Commands

### 8.1 `opencode ask "question"`

Single-shot mode. Sends one message, runs the agent loop, prints the result, exits.

```go
func newAskCmd(runner *Runner) *cobra.Command {
    return &cobra.Command{
        Use:   "ask <question>",
        Short: "Ask the AI a question about the project",
        Args:  cobra.MinimumNArgs(1),
        RunE: func(cmd *cobra.Command, args []string) error {
            return runner.RunWithLLM(cmd.Context(), func(ctx context.Context, rt runtime.Runtime, agent *agent.Agent) error {
                question := strings.Join(args, " ")
                result, err := agent.Run(ctx, question, func(text string) error {
                    fmt.Fprint(os.Stdout, text)
                    return nil
                })
                if err != nil {
                    return err
                }
                fmt.Fprintln(os.Stdout)
                return nil
            })
        },
    }
}
```

### 8.2 `opencode chat`

Interactive REPL mode. Reads user input line-by-line, runs the agent loop for
each input, prints streaming output. The conversation persists across turns.

```go
func newChatCmd(runner *Runner) *cobra.Command {
    return &cobra.Command{
        Use:   "chat",
        Short: "Start an interactive AI chat session",
        Args:  cobra.NoArgs,
        RunE: func(cmd *cobra.Command, args []string) error {
            return runner.RunWithLLM(cmd.Context(), func(ctx context.Context, rt runtime.Runtime, agent *agent.Agent) error {
                return runChatLoop(ctx, agent)
            })
        },
    }
}
```

The chat loop:

```
1. Print prompt (e.g., "you> ")
2. Read line from stdin (bufio.Scanner)
3. If line is "/quit" or EOF → exit
4. agent.Run(ctx, line, streamCallback)
5. Print newline, go to 1
```

### 8.3 New Global Flags

```
--provider, -p   LLM provider: openai/anthropic/gemini/ollama
--model          Model identifier (e.g., gpt-4o, claude-sonnet-4-20250514)
--temperature    Sampling temperature (0.0–2.0)
```

### 8.4 Runner Extension

The existing `Runner.Run` stays unchanged for non-LLM commands. A new method
handles the LLM lifecycle:

```go
// RunWithLLM extends Run with LLM provider initialization.
// Lifecycle: config → logger → runtime.Init → provider.New → agent.New → fn → runtime.Close
func (r *Runner) RunWithLLM(ctx context.Context, fn func(ctx context.Context, rt runtime.Runtime, a *agent.Agent) error) error
```

---

## 9. Credential Security

### 9.1 Rules

1. **Keys from env vars or config `api_key_env` only.** No `--api-key` flag. No
   hardcoded strings. The config file stores the *name* of the env var, not the key.

2. **Keys never logged.** The logger must never receive an API key as a value.
   Provider implementations must not include keys in `fmt.Errorf` messages.
   Use `"[REDACTED]"` if key presence needs to be indicated.

3. **Keys never in command output.** If a user accidentally passes an API key
   as part of a command (e.g., `opencode exec "curl -H 'Authorization: Bearer sk-...'"`,
   the runtime does not prevent this — but the agent's system prompt must
   instruct the model to never include secrets in tool call arguments.

4. **Keys redacted in exec commands.** Before executing any `exec` tool call
   from the LLM, the agent scans the command string for patterns matching known
   API key formats and refuses execution if found:

```go
// redactedKeyPatterns are regexes that match known API key formats.
var redactedKeyPatterns = []*regexp.Regexp{
    regexp.MustCompile(`sk-[a-zA-Z0-9]{20,}`),           // OpenAI
    regexp.MustCompile(`sk-ant-[a-zA-Z0-9]{20,}`),       // Anthropic
    regexp.MustCompile(`AIza[a-zA-Z0-9_-]{35}`),         // Google
}

// ContainsAPIKey returns true if the string contains a known API key pattern.
func ContainsAPIKey(s string) bool
```

5. **HTTP transport.** All providers (except Ollama on localhost) must use HTTPS.
   The provider must validate the base URL scheme.

### 9.2 Implementation Checklist

- [ ] `ResolveAPIKey` never returns the key in error messages
- [ ] Logger calls in provider code never include the key
- [ ] `Agent.Run` checks `ContainsAPIKey` on exec tool call arguments
- [ ] Provider constructors validate HTTPS for non-localhost URLs
- [ ] Config file example shows `api_key_env`, never a raw key

---

## 10. Dependencies

### 10.1 New Dependencies

| Dependency | Purpose |
|-----------|---------|
| `net/http` (stdlib) | HTTP client for all providers |
| `encoding/json` (stdlib) | JSON marshal/unmarshal for API payloads |
| `bufio` (stdlib) | SSE stream parsing, chat REPL input |
| `regexp` (stdlib) | API key pattern detection |

No external HTTP client library. The standard `net/http` is sufficient for
all four providers. SSE parsing is straightforward (read lines, strip `data: `
prefix, unmarshal JSON).

### 10.2 No SDK Dependencies

Provider implementations use raw HTTP, not vendor SDKs. Rationale:

- Avoids four separate SDK dependencies with their own transitive trees
- All four APIs are simple REST+JSON+SSE — no complex protocol negotiation
- Gives full control over streaming, retries, and error handling
- Keeps the binary small

---

## 11. Testing Strategy

### 11.1 Unit Tests

Each provider gets a test file with an `httptest.Server` that replays
canned responses:

```go
func TestOpenAI_Complete_Success(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Verify request structure, return canned SSE response
    }))
    defer srv.Close()

    p, _ := openai.New(llm.ProviderConfig{
        Model:   "gpt-4o",
        APIKey:  "test-key",
        BaseURL: srv.URL,
    }, testLogger)

    resp, err := p.Complete(ctx, req, nil)
    // Assert response fields
}
```

### 11.2 Agent Loop Tests

The agent loop is tested with a mock `Provider` that returns scripted
sequences of tool calls and final responses:

```go
type mockProvider struct {
    responses []llm.Response
    callIndex int
}

func (m *mockProvider) Complete(ctx context.Context, req llm.Request, cb llm.StreamCallback) (*llm.Response, error) {
    resp := m.responses[m.callIndex]
    m.callIndex++
    return &resp, nil
}
```

Test cases:
- Single-turn: user asks, model responds with text only
- Multi-turn: model calls read_file, then responds with analysis
- Tool error: model calls read_file on nonexistent path, gets error, adapts
- Multiple tool calls in one turn: model calls list_dir + read_file
- Credential leak prevention: model tries to exec a command with an API key

### 11.3 Integration Tests

Gated by `//go:build integration && llm`:

- Round-trip with each provider (requires real API keys in env)
- Agent loop with real provider against a t.TempDir() project

---

## 12. Implementation Order

```
Phase 7a: Foundation
  1. internal/llm/llm.go           — interfaces, types, errors, factory stub
  2. config additions              — LLMConfig, validation, env vars, flags
  3. internal/llm/openai/          — first provider (most common)
  4. internal/agent/               — agent loop, tool registry, system prompt
  5. internal/cli/ask.go           — single-shot command
  6. internal/cli/chat.go          — interactive REPL
  7. Credential security module    — ContainsAPIKey, ResolveAPIKey

Phase 7b: Remaining Providers
  8. internal/llm/anthropic/       — Anthropic provider
  9. internal/llm/gemini/          — Gemini provider
 10. internal/llm/ollama/          — Ollama provider

Phase 7c: Polish
 11. Context window management     — token counting, history summarization
 12. Retry logic                   — exponential backoff for rate limits
 13. Cost tracking                 — token usage display after each turn
```

Each step is independently testable and committable.
