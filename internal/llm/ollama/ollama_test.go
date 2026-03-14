package ollama

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

	"github.com/gajaai/opencode-go/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

func testEntry(url string) llm.ProviderEntry {
	return llm.ProviderEntry{
		Name:        "test",
		Type:        "ollama",
		ModelName:   "llama3.2",
		EndpointURL: url,
		Auth:        llm.AuthConfig{Type: "none"},
	}
}

func testProvider(t *testing.T, url string) *Provider {
	t.Helper()
	p, err := New(testEntry(url), testLogger)
	require.NoError(t, err)
	return p
}

// ndjsonLines formats lines as newline-delimited JSON.
func ndjsonLines(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\n")
	}
	return b.String()
}

// --- Constructor Tests ---

func TestNew_Success(t *testing.T) {
	p, err := New(llm.ProviderEntry{
		Name:        "my-ollama",
		Type:        "ollama",
		ModelName:   "llama3.2",
		EndpointURL: "http://localhost:11434",
		Auth:        llm.AuthConfig{Type: "none"},
	}, testLogger)

	require.NoError(t, err)
	assert.Equal(t, "my-ollama", p.Name())
	assert.Equal(t, "llama3.2", p.Model())
	assert.Equal(t, "http://localhost:11434", p.baseURL)
}

func TestNew_MissingModel(t *testing.T) {
	_, err := New(llm.ProviderEntry{
		Name: "test",
	}, testLogger)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model is required")
}

// --- Complete: Text Response Streaming ---

func TestComplete_TextResponse(t *testing.T) {
	ndjson := ndjsonLines(
		`{"message":{"role":"assistant","content":"Hello"},"done":false}`,
		`{"message":{"role":"assistant","content":" world"},"done":false}`,
		`{"message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":10,"eval_count":5}`,
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, chatPath, r.URL.Path)

		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprint(w, ndjson)
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
	ndjson := ndjsonLines(
		`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"read_file","arguments":{"path":"main.go"}}}]},"done":false}`,
		`{"message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":20,"eval_count":10}`,
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprint(w, ndjson)
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
	assert.Equal(t, "call_read_file_0", resp.ToolCalls[0].ID)
	assert.Equal(t, "read_file", resp.ToolCalls[0].Name)
	assert.Equal(t, `{"path":"main.go"}`, resp.ToolCalls[0].Arguments)
	assert.Equal(t, 20, resp.Usage.PromptTokens)
	assert.Equal(t, 10, resp.Usage.CompletionTokens)
}

// --- Complete: Multiple Tool Calls ---

func TestComplete_MultipleToolCalls(t *testing.T) {
	ndjson := ndjsonLines(
		`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"list_dir","arguments":{"path":"."}}},{"function":{"name":"read_file","arguments":{"path":"go.mod"}}}]},"done":false}`,
		`{"message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":15,"eval_count":8}`,
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprint(w, ndjson)
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

// --- Complete: Nil Callback ---

func TestComplete_NilCallback(t *testing.T) {
	ndjson := ndjsonLines(
		`{"message":{"role":"assistant","content":"ok"},"done":false}`,
		`{"message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":5,"eval_count":1}`,
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprint(w, ndjson)
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
	ndjson := ndjsonLines(
		`{"message":{"role":"assistant","content":"hello"},"done":false}`,
		`{"message":{"role":"assistant","content":" more"},"done":false}`,
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprint(w, ndjson)
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

func TestComplete_NotFoundError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":"model 'nonexistent' not found"}`)
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)
	_, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, llm.ErrModelNotFound)
}

func TestComplete_AuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":"unauthorized"}`)
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
		fmt.Fprint(w, `{"error":"rate limited"}`)
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)
	_, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, llm.ErrRateLimited)
}

func TestComplete_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, `{"error":"internal server error"}`)
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

		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprint(w, ndjsonLines(
			`{"message":{"role":"assistant","content":"ok"},"done":true,"prompt_eval_count":1,"eval_count":1}`,
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
	assert.Equal(t, "llama3.2", capturedBody.Model)
	assert.True(t, capturedBody.Stream)

	// Options.
	require.NotNil(t, capturedBody.Options)
	require.NotNil(t, capturedBody.Options.Temperature)
	assert.InDelta(t, 0.5, *capturedBody.Options.Temperature, 0.001)
	require.NotNil(t, capturedBody.Options.NumPredict)
	assert.Equal(t, 2048, *capturedBody.Options.NumPredict)

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

func TestComplete_NoOptionsWhenNotSet(t *testing.T) {
	var rawBody map[string]json.RawMessage

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &rawBody)

		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprint(w, ndjsonLines(
			`{"message":{"role":"assistant","content":"ok"},"done":true,"prompt_eval_count":1,"eval_count":1}`,
		))
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)
	_, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)

	require.NoError(t, err)
	// Options should be omitted from JSON when not set.
	_, hasOptions := rawBody["options"]
	assert.False(t, hasOptions, "options should be omitted when no temperature or max_tokens set")
}

// --- Tool Result Messages ---

func TestComplete_ToolResultMessages(t *testing.T) {
	var capturedBody chatRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)

		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprint(w, ndjsonLines(
			`{"message":{"role":"assistant","content":"done"},"done":true,"prompt_eval_count":10,"eval_count":1}`,
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
	assert.Equal(t, "read_file", capturedBody.Messages[1].ToolCalls[0].Function.Name)
	assert.Equal(t, map[string]any{"path": "main.go"}, capturedBody.Messages[1].ToolCalls[0].Function.Arguments)

	// Tool result message.
	assert.Equal(t, "tool", capturedBody.Messages[2].Role)
	assert.Equal(t, "package main", capturedBody.Messages[2].Content)
}

// --- Registration via llm.NewProvider ---

func TestRegistration(t *testing.T) {
	p, err := llm.NewProvider(llm.ProviderEntry{
		Name:      "my-ollama",
		Type:      "ollama",
		ModelName: "llama3.2",
	}, testLogger)

	require.NoError(t, err)
	assert.Equal(t, "my-ollama", p.Name())
	assert.Equal(t, "llama3.2", p.Model())
}
