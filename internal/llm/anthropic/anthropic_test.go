package anthropic

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

func testProvider(t *testing.T, url string) *Provider {
	t.Helper()
	p, err := New(llm.ProviderConfig{
		Model:   "claude-sonnet-4-20250514",
		APIKey:  "test-key",
		BaseURL: url,
	}, testLogger)
	require.NoError(t, err)
	return p
}

func sseLines(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("event: message\n")
		b.WriteString("data: ")
		b.WriteString(e)
		b.WriteString("\n\n")
	}
	return b.String()
}

// --- Constructor Tests ---

func TestNew_Success(t *testing.T) {
	p, err := New(llm.ProviderConfig{
		Model:  "claude-sonnet-4-20250514",
		APIKey: "sk-test",
	}, testLogger)

	require.NoError(t, err)
	assert.Equal(t, "anthropic", p.Name())
	assert.Equal(t, "claude-sonnet-4-20250514", p.Model())
	assert.Equal(t, defaultBaseURL, p.baseURL)
}

func TestNew_CustomBaseURL(t *testing.T) {
	p, err := New(llm.ProviderConfig{
		Model:   "claude-sonnet-4-20250514",
		APIKey:  "sk-test",
		BaseURL: "https://custom.api.com/",
	}, testLogger)

	require.NoError(t, err)
	assert.Equal(t, "https://custom.api.com", p.baseURL)
}

func TestNew_MissingModel(t *testing.T) {
	_, err := New(llm.ProviderConfig{APIKey: "sk-test"}, testLogger)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model is required")
}

func TestNew_MissingAPIKey(t *testing.T) {
	_, err := New(llm.ProviderConfig{Model: "test"}, testLogger)
	require.Error(t, err)
	assert.ErrorIs(t, err, llm.ErrAuthFailed)
}

// --- Complete: Text Response ---

func TestComplete_TextResponse(t *testing.T) {
	sse := sseLines(
		`{"type":"message_start","message":{"id":"msg_01","model":"claude-sonnet-4-20250514","usage":{"input_tokens":10}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`,
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test-key", r.Header.Get("x-api-key"))
		assert.Equal(t, apiVersion, r.Header.Get("anthropic-version"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

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

// --- Complete: Tool Use Response ---

func TestComplete_ToolUse(t *testing.T) {
	sse := sseLines(
		`{"type":"message_start","message":{"id":"msg_02","model":"claude-sonnet-4-20250514","usage":{"input_tokens":20}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me read that file."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_01","name":"read_file"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"main.go\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":15}}`,
		`{"type":"message_stop"}`,
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
	assert.Equal(t, "Let me read that file.", resp.Content)
	assert.Equal(t, "tool_use", resp.StopReason)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "toolu_01", resp.ToolCalls[0].ID)
	assert.Equal(t, "read_file", resp.ToolCalls[0].Name)
	assert.Equal(t, `{"path":"main.go"}`, resp.ToolCalls[0].Arguments)
}

// --- Complete: Multiple Tool Calls ---

func TestComplete_MultipleToolCalls(t *testing.T) {
	sse := sseLines(
		`{"type":"message_start","message":{"id":"msg_03","model":"claude-sonnet-4-20250514","usage":{"input_tokens":30}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01","name":"list_dir"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\".\"}"}  }`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_02","name":"read_file"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"go.mod\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}`,
		`{"type":"message_stop"}`,
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
	assert.Equal(t, "read_file", resp.ToolCalls[1].Name)
}

// --- Complete: Nil Callback ---

func TestComplete_NilCallback(t *testing.T) {
	sse := sseLines(
		`{"type":"message_start","message":{"id":"msg_04","model":"claude-sonnet-4-20250514","usage":{"input_tokens":5}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		`{"type":"message_stop"}`,
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
	sse := sseLines(
		`{"type":"message_start","message":{"id":"msg_05","model":"claude-sonnet-4-20250514","usage":{"input_tokens":5}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" more"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		`{"type":"message_stop"}`,
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
		fmt.Fprint(w, `{"error":{"type":"authentication_error","message":"invalid api key"}}`)
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
		fmt.Fprint(w, `{"error":{"type":"rate_limit_error","message":"too many requests"}}`)
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
		fmt.Fprint(w, `{"error":{"type":"not_found_error","message":"model not found"}}`)
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)
	_, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, llm.ErrModelNotFound)
}

// --- Request Structure Verification ---

func TestComplete_RequestStructure(t *testing.T) {
	var capturedBody apiRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseLines(
			`{"type":"message_start","message":{"id":"msg_06","model":"claude-sonnet-4-20250514","usage":{"input_tokens":5}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`{"type":"message_stop"}`,
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
	}, nil)

	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-20250514", capturedBody.Model)
	assert.Equal(t, "You are helpful.", capturedBody.System)
	assert.True(t, capturedBody.Stream)
	assert.Equal(t, 2048, capturedBody.MaxTokens)
	require.NotNil(t, capturedBody.Temperature)
	assert.InDelta(t, 0.5, *capturedBody.Temperature, 0.001)
	// System message is not in messages array.
	require.Len(t, capturedBody.Messages, 1)
	assert.Equal(t, "user", capturedBody.Messages[0].Role)
}

// --- Tool Result Message Handling ---

func TestComplete_ToolResultMessages(t *testing.T) {
	var capturedBody json.RawMessage

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseLines(
			`{"type":"message_start","message":{"id":"msg_07","model":"claude-sonnet-4-20250514","usage":{"input_tokens":5}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`{"type":"message_stop"}`,
		))
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)
	_, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "read file"},
			{Role: llm.RoleAssistant, Content: "Reading...", ToolCalls: []llm.ToolCall{
				{ID: "toolu_01", Name: "read_file", Arguments: `{"path":"main.go"}`},
			}},
			{Role: llm.RoleTool, ToolCallID: "toolu_01", Content: "package main"},
		},
	}, nil)

	require.NoError(t, err)

	// Verify the tool result was sent as a user message with tool_result block.
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(capturedBody, &raw))

	var messages []json.RawMessage
	require.NoError(t, json.Unmarshal(raw["messages"], &messages))

	// Should have 3 messages: user, assistant, user (tool_result).
	require.Len(t, messages, 3)

	// Verify the third message is a user message with tool_result content.
	var toolResultMsg struct {
		Role    string `json:"role"`
		Content []struct {
			Type      string `json:"type"`
			ToolUseID string `json:"tool_use_id"`
			Content   string `json:"content"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(messages[2], &toolResultMsg))
	assert.Equal(t, "user", toolResultMsg.Role)
	require.Len(t, toolResultMsg.Content, 1)
	assert.Equal(t, "tool_result", toolResultMsg.Content[0].Type)
	assert.Equal(t, "toolu_01", toolResultMsg.Content[0].ToolUseID)
	assert.Equal(t, "package main", toolResultMsg.Content[0].Content)
}

// --- Default MaxTokens ---

func TestComplete_DefaultMaxTokens(t *testing.T) {
	var capturedBody apiRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseLines(
			`{"type":"message_start","message":{"id":"msg_08","model":"claude-sonnet-4-20250514","usage":{"input_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`{"type":"message_stop"}`,
		))
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)
	_, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)

	require.NoError(t, err)
	assert.Equal(t, 4096, capturedBody.MaxTokens)
}

// --- Registration ---

func TestRegistration(t *testing.T) {
	// The init() function should have registered the provider.
	p, err := llm.NewProvider(llm.ProviderConfig{
		Name:   "anthropic",
		Model:  "claude-sonnet-4-20250514",
		APIKey: "test-key",
	}, testLogger)

	require.NoError(t, err)
	assert.Equal(t, "anthropic", p.Name())
}
