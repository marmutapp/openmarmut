package openai

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

	"github.com/gajaai/openmarmut-go/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

func testEntry(url string) llm.ProviderEntry {
	return llm.ProviderEntry{
		Name:        "test",
		Type:        "openai",
		ModelName:   "gpt-4o",
		APIKey:      "test-key",
		EndpointURL: url,
		Auth:        llm.AuthConfig{Type: "bearer", TokenPrefix: "Bearer "},
	}
}

func testProvider(t *testing.T, url string) *Provider {
	t.Helper()
	p, err := New(testEntry(url), testLogger)
	require.NoError(t, err)
	return p
}

// sseData formats lines as SSE data events.
func sseData(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("data: ")
		b.WriteString(l)
		b.WriteString("\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// --- Constructor Tests ---

func TestNew_Success(t *testing.T) {
	p, err := New(llm.ProviderEntry{
		Name:        "my-openai",
		Type:        "openai",
		ModelName:   "gpt-4o",
		APIKey:      "sk-test",
		EndpointURL: "https://api.openai.com",
		Auth:        llm.AuthConfig{Type: "bearer"},
	}, testLogger)

	require.NoError(t, err)
	assert.Equal(t, "my-openai", p.Name())
	assert.Equal(t, "gpt-4o", p.Model())
	assert.Equal(t, "https://api.openai.com", p.baseURL)
}

func TestNew_CustomEndpoint(t *testing.T) {
	p, err := New(llm.ProviderEntry{
		Name:        "groq",
		Type:        "openai",
		ModelName:   "llama-3.3-70b-versatile",
		EndpointURL: "https://api.groq.com/openai/",
		Auth:        llm.AuthConfig{Type: "none"},
	}, testLogger)

	require.NoError(t, err)
	assert.Equal(t, "https://api.groq.com/openai", p.baseURL)
}

func TestNew_MissingModel(t *testing.T) {
	_, err := New(llm.ProviderEntry{
		Name:   "test",
		APIKey: "sk-test",
	}, testLogger)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model is required")
}

func TestNew_NoAuthRequired(t *testing.T) {
	// vLLM or local endpoints may have no auth.
	p, err := New(llm.ProviderEntry{
		Name:        "vllm",
		ModelName:   "codellama-34b",
		EndpointURL: "http://localhost:8000",
		Auth:        llm.AuthConfig{Type: "none"},
	}, testLogger)

	require.NoError(t, err)
	assert.Equal(t, "vllm", p.Name())
}

// --- Complete: Text Response ---

func TestComplete_TextResponse(t *testing.T) {
	content1 := "Hello"
	content2 := " world"
	stop := "stop"

	sse := sseData(
		fmt.Sprintf(`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant","content":%q},"finish_reason":null}]}`, content1),
		fmt.Sprintf(`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":%q},"finish_reason":null}]}`, content2),
		fmt.Sprintf(`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":%q}]}`, stop),
		`{"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, chatCompletionsPath, r.URL.Path)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse)
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)

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
	assert.Empty(t, resp.ToolCalls)
	assert.Equal(t, "end", resp.StopReason)
	assert.Equal(t, 10, resp.Usage.PromptTokens)
	assert.Equal(t, 5, resp.Usage.CompletionTokens)
	assert.Equal(t, 15, resp.Usage.TotalTokens)
}

// --- Complete: Tool Call Response ---

func TestComplete_ToolCall(t *testing.T) {
	sse := sseData(
		`{"id":"chatcmpl-2","choices":[{"index":0,"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"read_file","arguments":""}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"pa"}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\""}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"main.go\"}"}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-2","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"id":"chatcmpl-2","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":10,"total_tokens":30}}`,
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse)
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)

	resp, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "read main.go"}},
		Tools: []llm.ToolDef{{
			Name:        "read_file",
			Description: "Read a file",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
		}},
	}, nil)

	require.NoError(t, err)
	assert.Equal(t, "tool_use", resp.StopReason)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "call_abc", resp.ToolCalls[0].ID)
	assert.Equal(t, "read_file", resp.ToolCalls[0].Name)
	assert.Equal(t, `{"path":"main.go"}`, resp.ToolCalls[0].Arguments)
}

// --- Complete: Multiple Tool Calls ---

func TestComplete_MultipleToolCalls(t *testing.T) {
	sse := sseData(
		`{"id":"chatcmpl-3","choices":[{"index":0,"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"list_dir","arguments":"{\"path\":\".\"}"}},{"index":1,"id":"call_2","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"go.mod\"}"}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-3","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse)
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)

	resp, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "explore"}},
	}, nil)

	require.NoError(t, err)
	require.Len(t, resp.ToolCalls, 2)
	assert.Equal(t, "list_dir", resp.ToolCalls[0].Name)
	assert.Equal(t, `{"path":"."}`, resp.ToolCalls[0].Arguments)
	assert.Equal(t, "read_file", resp.ToolCalls[1].Name)
	assert.Equal(t, `{"path":"go.mod"}`, resp.ToolCalls[1].Arguments)
}

// --- Complete: Text + Tool Call ---

func TestComplete_TextAndToolCall(t *testing.T) {
	content := "Let me read that file."

	sse := sseData(
		fmt.Sprintf(`{"id":"chatcmpl-4","choices":[{"index":0,"delta":{"role":"assistant","content":%q},"finish_reason":null}]}`, content),
		`{"id":"chatcmpl-4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"main.go\"}"}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-4","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse)
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)

	resp, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "read main.go"}},
	}, nil)

	require.NoError(t, err)
	assert.Equal(t, "Let me read that file.", resp.Content)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "read_file", resp.ToolCalls[0].Name)
}

// --- Complete: Nil Callback ---

func TestComplete_NilCallback(t *testing.T) {
	content := "ok"
	stop := "stop"

	sse := sseData(
		fmt.Sprintf(`{"id":"chatcmpl-5","choices":[{"index":0,"delta":{"content":%q},"finish_reason":null}]}`, content),
		fmt.Sprintf(`{"id":"chatcmpl-5","choices":[{"index":0,"delta":{},"finish_reason":%q}]}`, stop),
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse)
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)

	resp, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)

	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Content)
}

// --- Complete: Stream Callback Abort ---

func TestComplete_StreamAbort(t *testing.T) {
	sse := sseData(
		`{"id":"chatcmpl-6","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-6","choices":[{"index":0,"delta":{"content":" more"},"finish_reason":null}]}`,
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse)
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)

	_, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, func(text string) error {
		return fmt.Errorf("user cancelled")
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, llm.ErrStreamAborted)
}

// --- Error Responses ---

func TestComplete_AuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`)
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)
	_, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, llm.ErrAuthFailed)
}

func TestComplete_RateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":{"message":"Rate limit exceeded","type":"rate_limit_error"}}`)
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)
	_, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, llm.ErrRateLimited)
}

func TestComplete_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":{"message":"Model not found","type":"invalid_request_error"}}`)
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)
	_, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, llm.ErrModelNotFound)
}

func TestComplete_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, `{"error":{"message":"Internal server error"}}`)
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)
	_, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 500")
}

// --- Request Structure Verification ---

func TestComplete_RequestStructure(t *testing.T) {
	var capturedBody chatRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseData(
			`{"id":"chatcmpl-7","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
		))
	}))
	defer srv.Close()

	temp := 0.5
	maxTok := 2048

	p := testProvider(t, srv.URL)
	_, err := p.Complete(context.Background(), llm.Request{
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
	assert.Equal(t, "gpt-4o", capturedBody.Model)
	assert.True(t, capturedBody.Stream)
	require.NotNil(t, capturedBody.StreamOptions)
	assert.True(t, capturedBody.StreamOptions.IncludeUsage)
	require.NotNil(t, capturedBody.Temperature)
	assert.InDelta(t, 0.5, *capturedBody.Temperature, 0.001)
	require.NotNil(t, capturedBody.MaxTokens)
	assert.Equal(t, 2048, *capturedBody.MaxTokens)

	// Messages.
	require.Len(t, capturedBody.Messages, 2)
	assert.Equal(t, "system", capturedBody.Messages[0].Role)
	assert.Equal(t, "You are helpful.", capturedBody.Messages[0].Content)
	assert.Equal(t, "user", capturedBody.Messages[1].Role)
	assert.Equal(t, "hello", capturedBody.Messages[1].Content)

	// Tools.
	require.Len(t, capturedBody.Tools, 1)
	assert.Equal(t, "function", capturedBody.Tools[0].Type)
	assert.Equal(t, "read_file", capturedBody.Tools[0].Function.Name)
}

// --- Tool Result Messages ---

func TestComplete_ToolResultMessages(t *testing.T) {
	var capturedBody chatRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseData(
			`{"id":"chatcmpl-8","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`,
		))
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)
	_, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "read file"},
			{Role: llm.RoleAssistant, Content: "", ToolCalls: []llm.ToolCall{
				{ID: "call_abc", Name: "read_file", Arguments: `{"path":"main.go"}`},
			}},
			{Role: llm.RoleTool, ToolCallID: "call_abc", Content: "package main"},
		},
	}, nil)

	require.NoError(t, err)

	// Verify messages structure.
	require.Len(t, capturedBody.Messages, 3)

	// User message.
	assert.Equal(t, "user", capturedBody.Messages[0].Role)

	// Assistant message with tool calls.
	assert.Equal(t, "assistant", capturedBody.Messages[1].Role)
	require.Len(t, capturedBody.Messages[1].ToolCalls, 1)
	assert.Equal(t, "call_abc", capturedBody.Messages[1].ToolCalls[0].ID)
	assert.Equal(t, "function", capturedBody.Messages[1].ToolCalls[0].Type)
	assert.Equal(t, "read_file", capturedBody.Messages[1].ToolCalls[0].Function.Name)

	// Tool result message.
	assert.Equal(t, "tool", capturedBody.Messages[2].Role)
	assert.Equal(t, "call_abc", capturedBody.Messages[2].ToolCallID)
	assert.Equal(t, "package main", capturedBody.Messages[2].Content)
}

// --- Custom Headers ---

func TestComplete_CustomHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "2024-08-01-preview", r.Header.Get("api-version"))
		assert.Equal(t, "custom-value", r.Header.Get("X-Custom"))

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseData(
			`{"id":"chatcmpl-9","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
		))
	}))
	defer srv.Close()

	entry := testEntry(srv.URL)
	entry.Headers = map[string]string{
		"api-version": "2024-08-01-preview",
		"X-Custom":    "custom-value",
	}

	p, err := New(entry, testLogger)
	require.NoError(t, err)

	_, err = p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)
	require.NoError(t, err)
}

// --- Max Tokens Default ---

func TestComplete_DefaultMaxTokens(t *testing.T) {
	var capturedBody chatRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseData(
			`{"id":"chatcmpl-10","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
		))
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)
	_, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)

	require.NoError(t, err)
	// No default max_tokens is set — should be nil (omitted from JSON).
	assert.Nil(t, capturedBody.MaxTokens)
}

func TestComplete_ProviderDefaultMaxTokens(t *testing.T) {
	var capturedBody chatRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseData(
			`{"id":"chatcmpl-11","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
		))
	}))
	defer srv.Close()

	maxTok := 8192
	entry := testEntry(srv.URL)
	entry.MaxTokens = &maxTok

	p, err := New(entry, testLogger)
	require.NoError(t, err)

	_, err = p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)

	require.NoError(t, err)
	require.NotNil(t, capturedBody.MaxTokens)
	assert.Equal(t, 8192, *capturedBody.MaxTokens)
}

// --- Registration ---

func TestRegistration(t *testing.T) {
	p, err := llm.NewProvider(llm.ProviderEntry{
		Name:      "my-openai",
		Type:      "openai",
		ModelName: "gpt-4o",
		APIKey:    "test-key",
	}, testLogger)

	require.NoError(t, err)
	assert.Equal(t, "my-openai", p.Name())
	assert.Equal(t, "gpt-4o", p.Model())
}

// --- Stop Reason Mapping ---

func TestMapStopReason(t *testing.T) {
	assert.Equal(t, "end", mapStopReason("stop"))
	assert.Equal(t, "tool_use", mapStopReason("tool_calls"))
	assert.Equal(t, "max_tokens", mapStopReason("length"))
	assert.Equal(t, "content_filter", mapStopReason("content_filter"))
}
