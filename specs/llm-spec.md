# OpenMarmut-Go: LLM Integration Specification

**Version:** 2.0
**Last Updated:** 2026-03-13
**Status:** Design Complete — Implementation In Progress
**Depends on:** specs/system-spec.md (Phases 1–6 complete)

---

## 1. Overview

Phase 7 adds AI model support to OpenMarmut-Go. An LLM reads the project via the
existing `Runtime` interface, decides what operations to perform, and executes
them through the same Runtime. The tool becomes an agentic coding assistant that
works identically whether the underlying runtime is local or Docker.

### Core Principle: There Are No Special Providers

Every LLM connection is a **user-configured provider entry** with:

- A **name** — an arbitrary identifier the user chooses (e.g., `"work-claude"`, `"local-llama"`, `"azure-gpt4"`)
- A **type** — the wire format this endpoint speaks (`"openai"`, `"anthropic"`, `"gemini"`, `"ollama"`, `"custom"`)
- An **endpoint URL** — where to send requests
- A **model name** — what model to request
- **Auth credentials** — how to authenticate
- Optional **headers**, **payload config**, and **response path** for custom endpoints

**Provider type means "what request/response format does this endpoint speak"**, NOT "who hosts it." Azure OpenAI, Groq, Together, Fireworks, vLLM, and any OpenAI-compatible endpoint all use `type: openai` with different URLs. A corporate proxy that wraps Anthropic uses `type: anthropic` with a custom URL.

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
│  Existing commands + new: chat, ask, providers      │
├─────────────────────────────────────────────────────┤
│               Agent Loop (agent/)                   │
│  observe → plan → act → verify                      │
│  Owns the conversation, dispatches tool calls       │
├──────────────┬──────────────────────────────────────┤
│  LLM Client  │         Runtime                      │
│  (llm/)      │  (localrt/ or dockerrt/)             │
│  Talks to    │  Executes file ops                   │
│  any endpoint│  and shell commands                  │
├──────────────┴──────────────────────────────────────┤
│              Shared Packages                        │
│  config/ | logger/ | pathutil/ | runtime/           │
└─────────────────────────────────────────────────────┘
```

### 2.2 Key Design Decisions

**Decision 1: Provider type = wire format, not hosting provider.**

The `type` field on a provider entry controls which request/response codec is
used. `type: openai` means "speak the OpenAI chat completions API." It says
nothing about who runs the endpoint. Azure, Groq, Together, Fireworks, vLLM,
LiteLLM, and localhost all use the same OpenAI wire format.

**Decision 2: Multiple named provider entries, one active.**

Users define a list of providers in config. One is selected as active via
`active_provider`. This allows switching between providers without editing
config. The CLI flag `--provider` overrides `active_provider`.

**Decision 3: Auth is explicit and configurable.**

Each provider entry has an `AuthConfig` that specifies how credentials are
sent: bearer token, custom header, query parameter, or none. No implicit
auth guessing based on URL patterns.

**Decision 4: Credential values are always env var references.**

API keys in config are never literal values. They use `$VAR` or `env:VAR`
syntax, resolved at validation time. This prevents accidental exposure in
committed config files.

**Decision 5: The agent loop is separate from the LLM client.**

`internal/llm/` handles HTTP-level API communication.
`internal/agent/` handles the observe→plan→act→verify loop, conversation
history, and tool dispatch. This separation means the agent loop can be
tested with a mock provider.

**Decision 6: Tool calls map 1:1 to Runtime methods.**

The LLM sees tools named `read_file`, `write_file`, `list_dir`, `exec`, etc.
Each tool call maps directly to a `Runtime` method. No intermediate abstraction.

**Decision 7: Streaming by default.**

All providers stream responses token-by-token. This gives the user immediate
feedback. Non-streaming is a degenerate case (single chunk).

**Decision 8: Custom type for unsupported wire formats.**

The `custom` type provides a JSON template system for endpoints that don't
speak any standard format. The user provides a request template and a
response path to extract text.

---

## 3. Provider Configuration

### 3.1 Core Types

```go
package llm

// ProviderEntry is a single named provider configuration.
// Users define one or more of these in their config file.
type ProviderEntry struct {
    Name          string            `yaml:"name"`           // User-chosen identifier (e.g., "work-claude", "local-llama")
    Type          string            `yaml:"type"`           // Wire format: "openai", "anthropic", "gemini", "ollama", "custom"
    EndpointURL   string            `yaml:"endpoint_url"`   // Full base URL (e.g., "https://api.openai.com", "http://localhost:11434")
    ModelName     string            `yaml:"model"`          // Model identifier sent in the request
    APIKey        string            `yaml:"api_key"`        // Env var reference: "$OPENAI_API_KEY" or "env:MY_KEY"
    Headers       map[string]string `yaml:"headers"`        // Extra HTTP headers (e.g., "api-version": "2024-02-01")
    Auth          AuthConfig        `yaml:"auth"`           // How to authenticate requests
    PayloadConfig json.RawMessage   `yaml:"payload_config"` // Type-specific overrides (custom type: full template)
    ResponsePath  string            `yaml:"response_path"`  // JSONPath-like for custom type response extraction
    Temperature   *float64          `yaml:"temperature"`    // Default temperature (nil = provider default)
    MaxTokens     *int              `yaml:"max_tokens"`     // Default max tokens (nil = provider default)
}

// AuthConfig describes how to authenticate with an endpoint.
type AuthConfig struct {
    Type        string `yaml:"type"`         // "bearer", "header", "query", "none"
    HeaderName  string `yaml:"header_name"`  // For "header" type: custom header name (e.g., "x-api-key")
    TokenPrefix string `yaml:"token_prefix"` // For "bearer"/"header": prefix before the key (default: "Bearer " for bearer)
    QueryParam  string `yaml:"query_param"`  // For "query" type: query parameter name (e.g., "key")
}
```

### 3.2 Auth Type Semantics

| Auth Type | Behavior | Example |
|-----------|----------|---------|
| `bearer` | `Authorization: {TokenPrefix} {resolved_key}` | OpenAI, Groq, Together, Azure |
| `header` | `{HeaderName}: {TokenPrefix}{resolved_key}` | Anthropic (`x-api-key`) |
| `query` | URL param `?{QueryParam}={resolved_key}` | Gemini (`key`) |
| `none` | No auth sent | Ollama, local endpoints |

**Defaults by provider type** (applied if `auth` is not explicitly set):

| Type | Default Auth |
|------|-------------|
| `openai` | `{type: "bearer", token_prefix: "Bearer "}` |
| `anthropic` | `{type: "header", header_name: "x-api-key"}` |
| `gemini` | `{type: "query", query_param: "key"}` |
| `ollama` | `{type: "none"}` |
| `custom` | `{type: "none"}` |

### 3.3 Credential Resolution

API key values in config use env var references. Supported syntaxes:

- `$VAR_NAME` — resolves to `os.Getenv("VAR_NAME")`
- `env:VAR_NAME` — same behavior, explicit prefix
- Empty string — no key (valid only for `auth.type: "none"`)

Resolution happens at `Validate()` time. If the referenced env var is empty
and auth type requires a key, validation fails with a clear error.

```go
// ResolveCredential resolves an env var reference to its value.
// "$VAR" and "env:VAR" both resolve to os.Getenv("VAR").
// Empty string returns empty string (valid for auth type "none").
// Returns error if reference is set but env var is empty.
func ResolveCredential(ref string) (string, error)
```

### 3.4 Type-Specific Defaults

When `endpoint_url` is omitted, the following defaults apply:

| Type | Default Endpoint URL |
|------|---------------------|
| `openai` | `https://api.openai.com` |
| `anthropic` | `https://api.anthropic.com` |
| `gemini` | `https://generativelanguage.googleapis.com` |
| `ollama` | `http://localhost:11434` |
| `custom` | *(required — no default)* |

---

## 4. Provider Interface

### 4.1 Package: `internal/llm`

```go
package llm

import "context"

// Provider abstracts an LLM API. Implementations must be safe for sequential use.
type Provider interface {
    // Complete sends a conversation to the model and streams the response.
    // The callback is invoked for each text chunk. The final Response is returned
    // after the stream completes. Pass nil callback to skip streaming.
    Complete(ctx context.Context, req Request, cb StreamCallback) (*Response, error)

    // Name returns the user-assigned provider name (e.g., "work-claude").
    Name() string

    // Model returns the model identifier being used.
    Model() string
}

// StreamCallback is called for each token chunk during streaming.
// Return a non-nil error to abort the stream.
type StreamCallback func(text string) error

// Request is a single completion request.
type Request struct {
    Messages    []Message
    Tools       []ToolDef
    Temperature *float64 // nil = provider entry default
    MaxTokens   *int     // nil = provider entry default
}

// Response is the result of a completion.
type Response struct {
    Content    string     // Text content (may be empty if only tool calls)
    ToolCalls  []ToolCall // Zero or more tool invocations requested by the model
    Usage      Usage      // Token counts
    StopReason string     // "end", "tool_use", "max_tokens"
}

// Message is a single message in the conversation.
type Message struct {
    Role       Role
    Content    string     // Text content
    ToolCalls  []ToolCall // Only for assistant messages that invoke tools
    ToolCallID string     // Only for tool result messages — matches ToolCall.ID
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
    ID        string // Unique ID (provider-assigned)
    Name      string // Tool function name (e.g., "read_file")
    Arguments string // JSON-encoded arguments
}

// ToolDef describes a tool the model can invoke.
type ToolDef struct {
    Name        string // Function name
    Description string // What the tool does
    Parameters  any    // JSON Schema object for the parameters
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

### 4.2 Provider Factory

```go
package llm

import "log/slog"

// TypeConstructor creates a Provider from a ProviderEntry.
// Each type ("openai", "anthropic", etc.) registers one of these.
type TypeConstructor func(entry ProviderEntry, logger *slog.Logger) (Provider, error)

var constructors = map[string]TypeConstructor{}

// RegisterType adds a wire format constructor.
func RegisterType(typeName string, ctor TypeConstructor)

// NewProvider creates a Provider from a ProviderEntry using the registered constructor
// for its type. Unknown types return an error.
func NewProvider(entry ProviderEntry, logger *slog.Logger) (Provider, error) {
    ctor, ok := constructors[entry.Type]
    if !ok {
        return nil, fmt.Errorf("llm.NewProvider: unknown provider type %q", entry.Type)
    }
    return ctor(entry, logger)
}
```

Each wire format package registers itself via `init()`:

```go
// In internal/llm/openai/openai.go
func init() {
    llm.RegisterType("openai", func(entry llm.ProviderEntry, logger *slog.Logger) (llm.Provider, error) {
        return New(entry, logger)
    })
}
```

---

## 5. Wire Format Implementations

### 5.1 Package Structure

```
internal/llm/
  llm.go              — Provider interface, types, factory, credential resolution, sentinel errors
  llm_test.go         — Factory tests, credential resolution tests
  openai/
    openai.go         — OpenAI wire format (also: Azure, Groq, Together, Fireworks, vLLM)
    openai_test.go
  anthropic/
    anthropic.go      — Anthropic Messages API wire format
    anthropic_test.go
  gemini/
    gemini.go         — Google Gemini wire format
    gemini_test.go
  ollama/
    ollama.go         — Ollama native API wire format
    ollama_test.go
  custom/
    custom.go         — Custom JSON template wire format
    custom_test.go
```

### 5.2 OpenAI Wire Format (`internal/llm/openai`)

Speaks the OpenAI Chat Completions API. Used by:
- OpenAI direct
- Azure OpenAI (different URL, extra header `api-version`)
- Groq, Together, Fireworks (different URLs)
- vLLM, LiteLLM, text-generation-inference (different URLs)
- Any OpenAI-compatible endpoint

**Request path:** `{endpoint_url}/v1/chat/completions`

**Request mapping:**

| ProviderEntry field | OpenAI API field |
|---------------------|-----------------|
| `ModelName` | `model` |
| `Temperature` | `temperature` |
| `MaxTokens` | `max_tokens` |
| Request.Messages | `messages` (role mapping: system→system, user→user, assistant→assistant, tool→tool) |
| Request.Tools | `tools` (type: "function", function: {name, description, parameters}) |

**Auth applied:** Per AuthConfig (default: `Authorization: Bearer <key>`)

**Streaming:** SSE via `stream: true`, parse `data: {...}` lines, `data: [DONE]` terminates.

**Tool calls:** Parse `tool_calls` array in assistant delta chunks. Accumulate `function.arguments`
across multiple deltas (they arrive in fragments).

### 5.3 Anthropic Wire Format (`internal/llm/anthropic`)

Speaks the Anthropic Messages API.

**Request path:** `{endpoint_url}/v1/messages`

**Extra required header:** `anthropic-version: 2023-06-01`

**Key differences from OpenAI:**
- System prompt is a top-level `system` field, not in the messages array
- Tool results are `tool_result` content blocks inside user messages
- Tool calls arrive as `tool_use` content blocks
- Input arguments arrive as `input_json_delta` events (accumulated incrementally)
- Auth default: `x-api-key: <key>` header (not Bearer)

**Streaming:** SSE with event types: `message_start`, `content_block_start`,
`content_block_delta`, `content_block_stop`, `message_delta`, `message_stop`.

### 5.4 Gemini Wire Format (`internal/llm/gemini`)

Speaks the Google Generative Language API.

**Request path:** `{endpoint_url}/v1beta/models/{model}:streamGenerateContent`

**Key differences:**
- Auth default: query parameter `?key=<key>`
- Uses `contents` array with `parts` (not `messages`)
- Tool definitions use `functionDeclarations` in `tools`
- Tool calls arrive as `functionCall` parts
- Streaming: chunked JSON responses (not SSE)

### 5.5 Ollama Wire Format (`internal/llm/ollama`)

Speaks the Ollama native chat API.

**Request path:** `{endpoint_url}/api/chat`

**Key differences:**
- Auth default: none (local server)
- Streaming: NDJSON (newline-delimited JSON), each line is a response chunk
- Tool support via `tools` field (same schema as OpenAI)
- Model must be pre-pulled

### 5.6 Custom Wire Format (`internal/llm/custom`)

For endpoints that don't speak any standard format. Requires:
- `endpoint_url` (mandatory)
- `payload_config` — JSON template with `{{model}}`, `{{messages}}`, `{{temperature}}` placeholders
- `response_path` — dot-notation path to extract text from response (e.g., `"result.text"`, `"choices.0.message.content"`)

**Limitations:**
- No streaming (single request/response)
- No tool call support (text-only responses)
- Primarily for simple completion endpoints

```yaml
providers:
  - name: corp-internal
    type: custom
    endpoint_url: https://internal.corp.com/v1/complete
    model: internal-7b
    api_key: "$CORP_API_KEY"
    auth:
      type: bearer
    payload_config: |
      {
        "model": "{{model}}",
        "prompt": "{{messages_text}}",
        "max_tokens": {{max_tokens}},
        "temperature": {{temperature}}
      }
    response_path: "output.text"
```

---

## 6. Configuration

### 6.1 Config Additions

```go
// Added to config.Config
type Config struct {
    // ... existing fields (Mode, TargetDir, Docker, Log, DefaultTimeout) ...
    LLM LLMConfig `yaml:"llm"`
}

// LLMConfig holds all LLM provider configuration.
type LLMConfig struct {
    Providers      []ProviderEntry `yaml:"providers"`       // All configured provider entries
    ActiveProvider string          `yaml:"active_provider"` // Name of the provider to use
    SystemPrompt   string          `yaml:"system_prompt"`   // Custom system prompt (optional override)
}
```

Note: `ProviderEntry` and `AuthConfig` are defined in `internal/llm` but
embedded in config via YAML tags. The config package imports `llm` types
for deserialization. Alternatively, config can define its own mirror structs
and convert to `llm.ProviderEntry` during `Load()` — this avoids the
circular dependency if `llm` ever needs to import `config`.

### 6.2 Environment Variables

| Variable | Purpose |
|----------|---------|
| `OPENMARMUT_LLM_PROVIDER` | Override active_provider |
| `OPENAI_API_KEY` | OpenAI API key (standard) |
| `ANTHROPIC_API_KEY` | Anthropic API key (standard) |
| `GOOGLE_API_KEY` | Google Gemini API key (standard) |

API key env vars use the standard names established by each provider.
Provider entries reference them via `$VAR` syntax.

### 6.3 FlagOverrides Additions

```go
// Added to config.FlagOverrides
type FlagOverrides struct {
    // ... existing fields ...
    LLMProvider    *string  // --provider flag → overrides active_provider
    LLMModel       *string  // --model flag → overrides active provider's model
    LLMTemperature *float64 // --temperature flag
}
```

### 6.4 Config File Examples

**Standard OpenAI:**

```yaml
mode: local
llm:
  active_provider: openai
  providers:
    - name: openai
      type: openai
      model: gpt-4o
      api_key: "$OPENAI_API_KEY"
      temperature: 0.2
      max_tokens: 4096
```

**Azure OpenAI:**

```yaml
mode: local
llm:
  active_provider: azure
  providers:
    - name: azure
      type: openai
      endpoint_url: https://my-resource.openai.azure.com
      model: gpt-4o
      api_key: "$AZURE_OPENAI_API_KEY"
      headers:
        api-version: "2024-08-01-preview"
      auth:
        type: bearer
```

**Groq (OpenAI-compatible):**

```yaml
mode: local
llm:
  active_provider: groq
  providers:
    - name: groq
      type: openai
      endpoint_url: https://api.groq.com/openai
      model: llama-3.3-70b-versatile
      api_key: "$GROQ_API_KEY"
```

**Anthropic:**

```yaml
mode: local
llm:
  active_provider: claude
  providers:
    - name: claude
      type: anthropic
      model: claude-sonnet-4-20250514
      api_key: "$ANTHROPIC_API_KEY"
      temperature: 0.3
```

**Local Ollama:**

```yaml
mode: local
llm:
  active_provider: local
  providers:
    - name: local
      type: ollama
      model: llama3.1
```

**Self-hosted vLLM:**

```yaml
mode: local
llm:
  active_provider: vllm
  providers:
    - name: vllm
      type: openai
      endpoint_url: http://gpu-box.internal:8000
      model: codellama-34b
      auth:
        type: none
```

**Corporate custom endpoint:**

```yaml
mode: local
llm:
  active_provider: corp
  providers:
    - name: corp
      type: custom
      endpoint_url: https://llm.internal.corp.com/v1/complete
      model: internal-7b
      api_key: "$CORP_API_KEY"
      auth:
        type: bearer
      payload_config: |
        {
          "model": "{{model}}",
          "prompt": "{{messages_text}}",
          "max_tokens": {{max_tokens}}
        }
      response_path: "output.text"
```

**Multi-provider setup (switch between providers):**

```yaml
mode: local
llm:
  active_provider: claude
  providers:
    - name: claude
      type: anthropic
      model: claude-sonnet-4-20250514
      api_key: "$ANTHROPIC_API_KEY"
    - name: gpt
      type: openai
      model: gpt-4o
      api_key: "$OPENAI_API_KEY"
    - name: local
      type: ollama
      model: llama3.1
```

Usage: `openmarmut ask --provider gpt "explain this code"` overrides active_provider.

### 6.5 Validation

When `chat`, `ask`, or `providers` commands are used:

1. `llm.providers` must have at least one entry
2. `llm.active_provider` must match the `name` of one entry (or be overridden by `--provider`)
3. Each provider entry:
   - `name` must be non-empty and unique within the list
   - `type` must be one of: `openai`, `anthropic`, `gemini`, `ollama`, `custom`
   - `model` must be non-empty
   - `api_key` env var reference must resolve if `auth.type` requires a key
   - `temperature`, if set, must be in `[0.0, 2.0]`
   - `max_tokens`, if set, must be positive
   - If `type` is `custom`: `endpoint_url`, `payload_config`, and `response_path` are required
4. `endpoint_url`, if set, must be a valid URL
5. Non-localhost endpoints must use HTTPS (except for `auth.type: "none"`)

LLM validation is **skipped** for non-LLM commands (`read`, `write`, `exec`,
etc.) so the tool continues to work without any LLM configuration.

### 6.6 Active Provider Resolution

```
1. If --provider flag set → find entry by name → error if not found
2. Else if OPENMARMUT_LLM_PROVIDER env var set → find entry by name
3. Else use llm.active_provider from config
4. If still empty and providers list has exactly 1 entry → use it
5. If still empty → error: "no active provider configured"
```

---

## 7. Tool Definitions

The agent exposes Runtime methods as tools the LLM can call.

### 7.1 Tool Registry

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

### 7.2 Tool Definitions

| Tool Name | Parameters | Maps To | Returns |
|-----------|-----------|---------|---------|
| `read_file` | `{"path": "string"}` | `rt.ReadFile(ctx, path)` | File contents (truncated at 100KB with warning) |
| `write_file` | `{"path": "string", "content": "string"}` | `rt.WriteFile(ctx, path, []byte(content), 0644)` | `"wrote N bytes to <path>"` |
| `delete_file` | `{"path": "string"}` | `rt.DeleteFile(ctx, path)` | `"deleted <path>"` |
| `list_dir` | `{"path": "string"}` | `rt.ListDir(ctx, path)` | JSON array of entries |
| `mkdir` | `{"path": "string"}` | `rt.MkDir(ctx, path, 0755)` | `"created directory <path>"` |
| `exec` | `{"command": "string", "workdir": "string?"}` | `rt.Exec(ctx, command, opts)` | JSON with stdout, stderr, exit_code |

### 7.3 Tool Argument Schemas (JSON Schema)

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

## 8. Agent Loop

### 8.1 Package: `internal/agent`

```go
package agent

import (
    "context"
    "log/slog"

    "github.com/gajaai/openmarmut-go/internal/llm"
    "github.com/gajaai/openmarmut-go/internal/runtime"
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

### 8.2 Agentic Loop Flow

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

### 8.3 System Prompt

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
- Never include API keys, secrets, or credentials in tool call arguments.
```

The system prompt can be overridden via `llm.system_prompt` in config.

### 8.4 Conversation History Management

History grows unboundedly during a session. For long conversations:

- The agent tracks total token usage via `Response.Usage`
- When total tokens approach the model's context limit (provider-specific),
  the agent summarizes older messages into a condensed system message
- This is a Phase 7c optimization — initial implementation keeps full history

---

## 9. CLI Commands

### 9.1 `openmarmut ask "question"`

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

### 9.2 `openmarmut chat`

Interactive REPL mode. Reads user input, runs the agent loop for each input,
prints streaming output. Conversation persists across turns.

```
1. Print prompt (e.g., "you> ")
2. Read line from stdin (bufio.Scanner)
3. If line is "/quit" or EOF → exit
4. agent.Run(ctx, line, streamCallback)
5. Print newline, go to 1
```

### 9.3 `openmarmut providers`

Lists all configured providers and indicates which is active.

```
$ openmarmut providers
  NAME          TYPE        MODEL                     ENDPOINT
* claude        anthropic   claude-sonnet-4-20250514  https://api.anthropic.com
  gpt           openai      gpt-4o                    https://api.openai.com
  local         ollama      llama3.1                  http://localhost:11434
```

The `*` marks the active provider. This command validates all provider
entries and reports configuration errors.

### 9.4 New Global Flags

```
--provider, -p   Select provider by name (overrides active_provider)
--model          Override active provider's model
--temperature    Sampling temperature (0.0–2.0)
```

### 9.5 Runner Extension

The existing `Runner.Run` stays unchanged for non-LLM commands. A new method
handles the LLM lifecycle:

```go
// RunWithLLM extends Run with LLM provider initialization.
// Lifecycle: config → logger → runtime.Init → resolve active provider →
//            ResolveCredential → NewProvider → agent.New → fn → runtime.Close
func (r *Runner) RunWithLLM(ctx context.Context, fn func(ctx context.Context, rt runtime.Runtime, a *agent.Agent) error) error
```

---

## 10. Credential Security

### 10.1 Rules

1. **Keys from env var references only.** No `--api-key` flag. No hardcoded
   strings. Config stores `"$VAR_NAME"`, not the key itself.

2. **Keys never logged.** Provider implementations must not include keys in
   `fmt.Errorf` messages or slog fields. Use `"[REDACTED]"` if key presence
   needs to be indicated.

3. **Keys redacted in exec commands.** Before executing any `exec` tool call
   from the LLM, the agent scans the command string for patterns matching
   known API key formats and refuses execution if found:

```go
var redactedKeyPatterns = []*regexp.Regexp{
    regexp.MustCompile(`sk-[a-zA-Z0-9]{20,}`),           // OpenAI
    regexp.MustCompile(`sk-ant-[a-zA-Z0-9]{20,}`),       // Anthropic
    regexp.MustCompile(`AIza[a-zA-Z0-9_-]{35}`),         // Google
}

// ContainsAPIKey returns true if the string contains a known API key pattern.
func ContainsAPIKey(s string) bool
```

4. **HTTPS required.** All non-localhost endpoints must use HTTPS. Provider
   constructors validate the URL scheme.

### 10.2 Implementation Checklist

- [ ] `ResolveCredential` never returns the key in error messages
- [ ] Logger calls in provider code never include the key
- [ ] `Agent.Run` checks `ContainsAPIKey` on exec tool call arguments
- [ ] Provider constructors validate HTTPS for non-localhost URLs
- [ ] Config file examples show `$VAR` references, never raw keys

---

## 11. Dependencies

### 11.1 New Dependencies

| Dependency | Purpose |
|-----------|---------|
| `net/http` (stdlib) | HTTP client for all providers |
| `encoding/json` (stdlib) | JSON marshal/unmarshal for API payloads |
| `bufio` (stdlib) | SSE stream parsing, chat REPL input |
| `regexp` (stdlib) | API key pattern detection |

### 11.2 No SDK Dependencies

Provider implementations use raw HTTP, not vendor SDKs. Rationale:

- Avoids five separate SDK dependencies with their own transitive trees
- All APIs are simple REST+JSON+SSE — no complex protocol negotiation
- Gives full control over streaming, retries, and error handling
- Keeps the binary small

---

## 12. Testing Strategy

### 12.1 Unit Tests

Each wire format gets a test file with `httptest.Server` replaying canned responses:

```go
func TestOpenAI_Complete_Success(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Verify request structure, return canned SSE response
    }))
    defer srv.Close()

    entry := llm.ProviderEntry{
        Name:        "test",
        Type:        "openai",
        EndpointURL: srv.URL,
        ModelName:   "gpt-4o",
        APIKey:      "",  // No auth needed for test server
        Auth:        llm.AuthConfig{Type: "none"},
    }
    p, _ := openai.New(entry, testLogger)

    resp, err := p.Complete(ctx, req, nil)
    // Assert response fields
}
```

### 12.2 Agent Loop Tests

Tested with a mock `Provider` that returns scripted responses:

Test cases:
- Single-turn: user asks, model responds with text only
- Multi-turn: model calls read_file, then responds with analysis
- Tool error: model calls read_file on nonexistent path, gets error, adapts
- Multiple tool calls in one turn: model calls list_dir + read_file
- Credential leak prevention: model tries to exec a command with an API key

### 12.3 Integration Tests

Gated by `//go:build integration && llm`:

- Round-trip with each provider (requires real API keys in env)
- Agent loop with real provider against a t.TempDir() project

---

## 13. Implementation Order

```
Phase 7a: Foundation
  1. internal/llm/llm.go           — Provider interface, types, factory (RegisterType/NewProvider),
                                      ProviderEntry, AuthConfig, ResolveCredential, sentinel errors
  2. Config additions              — LLMConfig with []ProviderEntry, active_provider, validation,
                                      env vars, CLI flags (--provider, --model, --temperature)
  3. internal/llm/openai/          — OpenAI wire format (streaming, tool calls)
  4. internal/llm/anthropic/       — Anthropic wire format (already started — update to ProviderEntry)
  5. internal/agent/agent.go       — Agent loop (observe→plan→act→verify)
  6. internal/agent/tools.go       — Tool registry mapping to Runtime methods
  7. internal/agent/security.go    — ContainsAPIKey, credential redaction
  8. internal/cli/ask.go           — Single-shot command
  9. internal/cli/chat.go          — Interactive REPL
 10. internal/cli/providers.go     — List configured providers
 11. Runner extension              — RunWithLLM lifecycle method

Phase 7b: Remaining Wire Formats
 12. internal/llm/gemini/          — Gemini wire format
 13. internal/llm/ollama/          — Ollama wire format
 14. internal/llm/custom/          — Custom JSON template wire format

Phase 7c: Polish
 15. Context window management     — token counting, history summarization
 16. Retry logic                   — exponential backoff for rate limits
 17. Cost tracking                 — token usage display after each turn
```

Each step is independently testable and committable.
