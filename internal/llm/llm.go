package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
)

// Provider abstracts an LLM API. Implementations must be safe for sequential use.
type Provider interface {
	// Complete sends a conversation to the model and streams the response.
	// The callback is invoked for each text chunk. The final Response is returned
	// after the stream completes. Pass nil callback to skip streaming.
	Complete(ctx context.Context, req Request, cb StreamCallback) (*Response, error)

	// Name returns the provider identifier (e.g., "openai", "anthropic").
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
	Temperature *float64 // nil = provider default
	MaxTokens   *int     // nil = provider default
}

// Response is the result of a completion.
type Response struct {
	Content    string     // Text content (may be empty if only tool calls)
	ToolCalls  []ToolCall // Tool invocations requested by the model
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

// ProviderConfig holds everything needed to create a provider.
type ProviderConfig struct {
	Name        string   // "openai", "anthropic", "gemini", "ollama"
	Model       string   // e.g., "gpt-4o", "claude-sonnet-4-20250514"
	APIKey      string   // Resolved key (never logged)
	BaseURL     string   // Override for Ollama or proxies
	Temperature *float64 // Default temperature (overridden per-request)
	MaxTokens   *int     // Default max tokens (overridden per-request)
}

// Sentinel errors.
var (
	ErrAuthFailed     = errors.New("authentication failed — check API key")
	ErrRateLimited    = errors.New("rate limited by provider")
	ErrModelNotFound  = errors.New("model not found")
	ErrContextTooLong = errors.New("input exceeds model context window")
	ErrStreamAborted  = errors.New("stream aborted by callback")
)

// ProviderConstructor creates a Provider from config.
type ProviderConstructor func(cfg ProviderConfig, logger *slog.Logger) (Provider, error)

var constructors = map[string]ProviderConstructor{}

// Register adds a provider constructor for a given name.
func Register(name string, ctor ProviderConstructor) {
	constructors[name] = ctor
}

// NewProvider creates a Provider from config using the registered constructor.
func NewProvider(cfg ProviderConfig, logger *slog.Logger) (Provider, error) {
	ctor, ok := constructors[cfg.Name]
	if !ok {
		return nil, fmt.Errorf("llm.NewProvider: unknown provider %q", cfg.Name)
	}
	return ctor(cfg, logger)
}

// ResolveAPIKey returns the API key for a provider.
// Returns empty string for providers that don't need auth (ollama).
// Returns error if key is required but not found.
func ResolveAPIKey(provider, apiKeyEnv string) (string, error) {
	// Standard env var names per provider.
	defaultEnvVars := map[string]string{
		"openai":    "OPENAI_API_KEY",
		"anthropic": "ANTHROPIC_API_KEY",
		"gemini":    "GOOGLE_API_KEY",
	}

	// Ollama needs no key.
	if provider == "ollama" {
		return "", nil
	}

	// Try explicit env var name first.
	if apiKeyEnv != "" {
		if key := os.Getenv(apiKeyEnv); key != "" {
			return key, nil
		}
	}

	// Try standard env var for the provider.
	if envName, ok := defaultEnvVars[provider]; ok {
		if key := os.Getenv(envName); key != "" {
			return key, nil
		}
	}

	return "", fmt.Errorf("llm.ResolveAPIKey: %w: set %s or configure llm.api_key_env",
		ErrAuthFailed, defaultEnvVars[provider])
}
