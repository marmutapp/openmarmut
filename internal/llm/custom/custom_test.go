package custom

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marmutapp/openmarmut/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

func sseData(chunks ...string) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString("data: ")
		b.WriteString(c)
		b.WriteString("\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// --- Constructor Tests ---

func TestNew_Success(t *testing.T) {
	p, err := New(llm.ProviderEntry{
		Name:        "my-custom",
		ModelName:   "my-model",
		EndpointURL: "https://custom.example.com",
		Auth:        llm.AuthConfig{Type: "bearer"},
	}, testLogger)

	require.NoError(t, err)
	assert.Equal(t, "my-custom", p.Name())
	assert.Equal(t, "my-model", p.Model())
}

func TestNew_MissingModel(t *testing.T) {
	_, err := New(llm.ProviderEntry{
		Name:        "test",
		EndpointURL: "https://example.com",
	}, testLogger)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model is required")
}

func TestNew_MissingEndpoint(t *testing.T) {
	_, err := New(llm.ProviderEntry{
		Name:      "test",
		ModelName: "my-model",
	}, testLogger)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "endpoint_url is required")
}

func TestNew_PayloadConfig(t *testing.T) {
	p, err := New(llm.ProviderEntry{
		Name:          "custom",
		ModelName:     "my-model",
		EndpointURL:   "https://example.com",
		PayloadConfig: json.RawMessage(`{"api_path":"/api/generate","extra":{"top_p":0.9}}`),
	}, testLogger)

	require.NoError(t, err)
	assert.Equal(t, "/api/generate", p.apiPath)
	assert.Equal(t, 0.9, p.extra["top_p"])
}

func TestNew_InvalidPayloadConfig(t *testing.T) {
	_, err := New(llm.ProviderEntry{
		Name:          "test",
		ModelName:     "model",
		EndpointURL:   "https://example.com",
		PayloadConfig: json.RawMessage(`{invalid`),
	}, testLogger)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid payload_config")
}

// --- Complete: Text Response ---

func TestComplete_TextResponse(t *testing.T) {
	sse := sseData(
		`{"choices":[{"delta":{"content":"Hello"},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"content":" world"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse)
	}))
	defer srv.Close()

	p, err := New(llm.ProviderEntry{
		Name:        "test",
		ModelName:   "test-model",
		EndpointURL: srv.URL,
		Auth:        llm.AuthConfig{Type: "none"},
	}, testLogger)
	require.NoError(t, err)

	var streamed strings.Builder
	resp, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, func(text string) error {
		streamed.WriteString(text)
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, "Hello world", resp.Content)
	assert.Equal(t, "Hello world", streamed.String())
	assert.Equal(t, "end", resp.StopReason)
	assert.Equal(t, 5, resp.Usage.PromptTokens)
	assert.Equal(t, 2, resp.Usage.CompletionTokens)
	assert.Equal(t, 7, resp.Usage.TotalTokens)
}

// --- Complete: Custom API Path ---

func TestComplete_CustomAPIPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v2/generate", r.URL.Path)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseData(
			`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
		))
	}))
	defer srv.Close()

	p, err := New(llm.ProviderEntry{
		Name:          "test",
		ModelName:     "model",
		EndpointURL:   srv.URL,
		Auth:          llm.AuthConfig{Type: "none"},
		PayloadConfig: json.RawMessage(`{"api_path":"/api/v2/generate"}`),
	}, testLogger)
	require.NoError(t, err)

	resp, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)

	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Content)
}

// --- Complete: Extra Payload Fields ---

func TestComplete_ExtraPayloadFields(t *testing.T) {
	var capturedBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseData(
			`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
		))
	}))
	defer srv.Close()

	p, err := New(llm.ProviderEntry{
		Name:          "test",
		ModelName:     "model",
		EndpointURL:   srv.URL,
		Auth:          llm.AuthConfig{Type: "none"},
		PayloadConfig: json.RawMessage(`{"extra":{"top_p":0.9,"presence_penalty":0.5}}`),
	}, testLogger)
	require.NoError(t, err)

	_, err = p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)
	require.NoError(t, err)

	assert.InDelta(t, 0.9, capturedBody["top_p"].(float64), 0.001)
	assert.InDelta(t, 0.5, capturedBody["presence_penalty"].(float64), 0.001)
	// Built-in fields should not be overridden.
	assert.Equal(t, "model", capturedBody["model"])
}

// --- Complete: Tool Call ---

func TestComplete_ToolCall(t *testing.T) {
	sse := sseData(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":""}}]},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]},"finish_reason":null}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"main.go\"}"}}]},"finish_reason":"tool_calls"}]}`,
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse)
	}))
	defer srv.Close()

	p, err := New(llm.ProviderEntry{
		Name:        "test",
		ModelName:   "model",
		EndpointURL: srv.URL,
		Auth:        llm.AuthConfig{Type: "none"},
	}, testLogger)
	require.NoError(t, err)

	resp, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "read main.go"}},
		Tools: []llm.ToolDef{{
			Name:        "read_file",
			Description: "Read a file",
			Parameters:  map[string]any{"type": "object"},
		}},
	}, nil)

	require.NoError(t, err)
	assert.Equal(t, "tool_use", resp.StopReason)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "call_1", resp.ToolCalls[0].ID)
	assert.Equal(t, "read_file", resp.ToolCalls[0].Name)
	assert.JSONEq(t, `{"path":"main.go"}`, resp.ToolCalls[0].Arguments)
}

// --- Complete: Nil Callback ---

func TestComplete_NilCallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseData(
			`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
		))
	}))
	defer srv.Close()

	p, err := New(llm.ProviderEntry{
		Name:        "test",
		ModelName:   "model",
		EndpointURL: srv.URL,
		Auth:        llm.AuthConfig{Type: "none"},
	}, testLogger)
	require.NoError(t, err)

	resp, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)

	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Content)
}

// --- Complete: Stream Abort ---

func TestComplete_StreamAbort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseData(
			`{"choices":[{"delta":{"content":"hello"},"finish_reason":null}]}`,
		))
	}))
	defer srv.Close()

	p, err := New(llm.ProviderEntry{
		Name:        "test",
		ModelName:   "model",
		EndpointURL: srv.URL,
		Auth:        llm.AuthConfig{Type: "none"},
	}, testLogger)
	require.NoError(t, err)

	_, err = p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, func(text string) error {
		return fmt.Errorf("cancelled")
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, llm.ErrStreamAborted)
}

// --- Error Responses ---

func TestComplete_AuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":{"message":"Invalid API key"}}`)
	}))
	defer srv.Close()

	p, err := New(llm.ProviderEntry{
		Name:        "test",
		ModelName:   "model",
		EndpointURL: srv.URL,
		Auth:        llm.AuthConfig{Type: "none"},
	}, testLogger)
	require.NoError(t, err)

	_, err = p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, llm.ErrAuthFailed)
}

func TestComplete_RateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":{"message":"Rate limit exceeded"}}`)
	}))
	defer srv.Close()

	p, err := New(llm.ProviderEntry{
		Name:        "test",
		ModelName:   "model",
		EndpointURL: srv.URL,
		Auth:        llm.AuthConfig{Type: "none"},
	}, testLogger)
	require.NoError(t, err)

	_, err = p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, llm.ErrRateLimited)
}

func TestComplete_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":{"message":"Model not found"}}`)
	}))
	defer srv.Close()

	p, err := New(llm.ProviderEntry{
		Name:        "test",
		ModelName:   "model",
		EndpointURL: srv.URL,
		Auth:        llm.AuthConfig{Type: "none"},
	}, testLogger)
	require.NoError(t, err)

	_, err = p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, llm.ErrModelNotFound)
}

// --- Request Structure ---

func TestComplete_RequestStructure(t *testing.T) {
	var capturedBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseData(
			`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
		))
	}))
	defer srv.Close()

	temp := 0.7
	maxTok := 4096

	p, err := New(llm.ProviderEntry{
		Name:        "test",
		ModelName:   "test-model",
		EndpointURL: srv.URL,
		Auth:        llm.AuthConfig{Type: "none"},
	}, testLogger)
	require.NoError(t, err)

	_, err = p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "You are helpful."},
			{Role: llm.RoleUser, Content: "hello"},
		},
		Temperature: &temp,
		MaxTokens:   &maxTok,
		Tools: []llm.ToolDef{{
			Name:        "read_file",
			Description: "Read a file",
			Parameters:  map[string]any{"type": "object"},
		}},
	}, nil)
	require.NoError(t, err)

	assert.Equal(t, "test-model", capturedBody["model"])
	assert.Equal(t, true, capturedBody["stream"])
	assert.InDelta(t, 0.7, capturedBody["temperature"].(float64), 0.001)
	assert.InDelta(t, 4096, capturedBody["max_tokens"].(float64), 0.001)

	messages := capturedBody["messages"].([]any)
	require.Len(t, messages, 2)

	sysMsg := messages[0].(map[string]any)
	assert.Equal(t, "system", sysMsg["role"])
	assert.Equal(t, "You are helpful.", sysMsg["content"])

	userMsg := messages[1].(map[string]any)
	assert.Equal(t, "user", userMsg["role"])
	assert.Equal(t, "hello", userMsg["content"])

	tools := capturedBody["tools"].([]any)
	require.Len(t, tools, 1)
	tool := tools[0].(map[string]any)
	assert.Equal(t, "function", tool["type"])
	fn := tool["function"].(map[string]any)
	assert.Equal(t, "read_file", fn["name"])
}

// --- Tool Result Messages ---

func TestComplete_ToolResultMessages(t *testing.T) {
	var capturedBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseData(
			`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`,
		))
	}))
	defer srv.Close()

	p, err := New(llm.ProviderEntry{
		Name:        "test",
		ModelName:   "model",
		EndpointURL: srv.URL,
		Auth:        llm.AuthConfig{Type: "none"},
	}, testLogger)
	require.NoError(t, err)

	_, err = p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "read file"},
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
				{ID: "call_1", Name: "read_file", Arguments: `{"path":"main.go"}`},
			}},
			{Role: llm.RoleTool, ToolCallID: "call_1", Content: `package main`},
		},
	}, nil)
	require.NoError(t, err)

	messages := capturedBody["messages"].([]any)
	require.Len(t, messages, 3)

	// Assistant message with tool_calls.
	assistantMsg := messages[1].(map[string]any)
	assert.Equal(t, "assistant", assistantMsg["role"])
	tcs := assistantMsg["tool_calls"].([]any)
	require.Len(t, tcs, 1)
	tc := tcs[0].(map[string]any)
	assert.Equal(t, "call_1", tc["id"])

	// Tool result message.
	toolMsg := messages[2].(map[string]any)
	assert.Equal(t, "tool", toolMsg["role"])
	assert.Equal(t, "call_1", toolMsg["tool_call_id"])
	assert.Equal(t, "package main", toolMsg["content"])
}

// --- Max Tokens Finish Reason ---

func TestComplete_MaxTokensFinishReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseData(
			`{"choices":[{"delta":{"content":"truncated"},"finish_reason":"length"}]}`,
		))
	}))
	defer srv.Close()

	p, err := New(llm.ProviderEntry{
		Name:        "test",
		ModelName:   "model",
		EndpointURL: srv.URL,
		Auth:        llm.AuthConfig{Type: "none"},
	}, testLogger)
	require.NoError(t, err)

	resp, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)

	require.NoError(t, err)
	assert.Equal(t, "max_tokens", resp.StopReason)
}

// --- Registration ---

func TestRegistration(t *testing.T) {
	p, err := llm.NewProvider(llm.ProviderEntry{
		Name:        "my-custom",
		Type:        "custom",
		ModelName:   "my-model",
		EndpointURL: "https://example.com",
	}, testLogger)

	require.NoError(t, err)
	assert.Equal(t, "my-custom", p.Name())
	assert.Equal(t, "my-model", p.Model())
}

// --- Bearer Auth ---

func TestComplete_BearerAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseData(
			`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
		))
	}))
	defer srv.Close()

	p, err := New(llm.ProviderEntry{
		Name:        "test",
		ModelName:   "model",
		EndpointURL: srv.URL,
		APIKey:      "test-key",
		Auth:        llm.AuthConfig{Type: "bearer", TokenPrefix: "Bearer "},
	}, testLogger)
	require.NoError(t, err)

	_, err = p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)
	require.NoError(t, err)
}
