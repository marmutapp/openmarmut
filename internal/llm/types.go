package llm

import (
	"context"
	"encoding/json"
	"errors"
)

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
	Messages         []Message
	Tools            []ToolDef
	Temperature      *float64 // nil = provider entry default
	MaxTokens        *int     // nil = provider entry default
	ExtendedThinking bool     // Enable extended thinking / reasoning tokens.
	ThinkingBudget   int      // Max thinking tokens (0 = provider default).
}

// Response is the result of a completion.
type Response struct {
	Content        string     // Text content (may be empty if only tool calls)
	Thinking       string     // Reasoning/thinking content (if extended thinking enabled)
	ToolCalls      []ToolCall // Zero or more tool invocations requested by the model
	Usage          Usage      // Token counts
	ThinkingTokens int        // Tokens used for thinking/reasoning
	StopReason     string     // "end", "tool_use", "max_tokens"
}

// Message is a single message in the conversation.
type Message struct {
	Role       Role
	Content    string         // Text content
	Images     []ImageContent // Attached images (for vision-capable models)
	ToolCalls  []ToolCall     // Only for assistant messages that invoke tools
	ToolCallID string         // Only for tool result messages — matches ToolCall.ID
}

// ImageContent holds a base64-encoded image for multimodal messages.
type ImageContent struct {
	Data     string // Base64-encoded image data (no data URI prefix)
	MimeType string // MIME type: image/png, image/jpeg, image/gif, image/webp
	Path     string // Original file path (for display only)
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

// ProviderEntry is a single named provider configuration.
// Users define one or more of these in their config file.
type ProviderEntry struct {
	Name          string            `yaml:"name"`           // User-chosen identifier (e.g., "work-claude", "local-llama")
	Type          string            `yaml:"type"`           // Wire format: "openai", "anthropic", "gemini", "ollama", "custom"
	EndpointURL   string            `yaml:"endpoint_url"`   // Full base URL (e.g., "https://api.openai.com")
	ModelName     string            `yaml:"model"`          // Model identifier sent in the request
	APIKey        string            `yaml:"api_key"`        // Env var reference: "$OPENAI_API_KEY" or "env:MY_KEY", or literal
	Headers       map[string]string `yaml:"headers"`        // Extra HTTP headers (e.g., "api-version": "2024-02-01")
	Auth          AuthConfig        `yaml:"auth"`           // How to authenticate requests
	PayloadConfig json.RawMessage   `yaml:"payload_config"` // Type-specific overrides (custom type: full template)
	ResponsePath  string            `yaml:"response_path"`  // JSONPath-like for custom type response extraction
	Temperature      *float64          `yaml:"temperature"`        // Default temperature (nil = provider default)
	MaxTokens        *int              `yaml:"max_tokens"`         // Default max tokens (nil = provider default)
	ContextWindow    int               `yaml:"context_window"`     // Model context window in tokens (default: 128000)
	ExtendedThinking bool              `yaml:"extended_thinking"`  // Enable extended thinking / reasoning tokens
	ThinkingBudget   int               `yaml:"thinking_budget"`    // Max thinking tokens (0 = provider default)
}

// AuthConfig describes how to authenticate with an endpoint.
type AuthConfig struct {
	Type        string `yaml:"type"`         // "bearer", "header", "query", "none"
	HeaderName  string `yaml:"header_name"`  // For "header" type: custom header name (e.g., "x-api-key")
	TokenPrefix string `yaml:"token_prefix"` // For "bearer"/"header": prefix before the key
	QueryParam  string `yaml:"query_param"`  // For "query" type: query parameter name (e.g., "key")
}

// Sentinel errors.
var (
	ErrAuthFailed     = errors.New("authentication failed — check API key")
	ErrRateLimited    = errors.New("rate limited by provider")
	ErrServerError    = errors.New("server error from provider")
	ErrModelNotFound  = errors.New("model not found")
	ErrContextTooLong = errors.New("input exceeds model context window")
	ErrStreamAborted  = errors.New("stream aborted by callback")
)
