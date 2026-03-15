package responses

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

func testEntry(url string) llm.ProviderEntry {
	return llm.ProviderEntry{
		Name:        "test",
		Type:        "openai-responses",
		ModelName:   "o4-mini",
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

func sseEvents(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("data: ")
		b.WriteString(e)
		b.WriteString("\n\n")
	}
	return b.String()
}

// --- Constructor Tests ---

func TestNew_Success(t *testing.T) {
	p, err := New(llm.ProviderEntry{
		Name:        "my-responses",
		Type:        "openai-responses",
		ModelName:   "o4-mini",
		APIKey:      "sk-test",
		EndpointURL: "https://api.openai.com",
		Auth:        llm.AuthConfig{Type: "bearer"},
	}, testLogger)

	require.NoError(t, err)
	assert.Equal(t, "my-responses", p.Name())
	assert.Equal(t, "o4-mini", p.Model())
}

func TestNew_MissingModel(t *testing.T) {
	_, err := New(llm.ProviderEntry{
		Name:   "test",
		APIKey: "sk-test",
	}, testLogger)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model is required")
}

func TestNew_TrailingSlashTrimmed(t *testing.T) {
	p, err := New(llm.ProviderEntry{
		Name:        "test",
		ModelName:   "o3",
		EndpointURL: "https://api.openai.com/",
		Auth:        llm.AuthConfig{Type: "none"},
	}, testLogger)
	require.NoError(t, err)
	assert.Equal(t, "https://api.openai.com", p.baseURL)
}

// --- Complete: Text Response ---

func TestComplete_TextResponse(t *testing.T) {
	sse := sseEvents(
		`{"type":"response.output_text.delta","delta":"Hello"}`,
		`{"type":"response.output_text.delta","delta":" world"}`,
		`{"type":"response.completed","response":{"id":"resp_1","output":[],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`,
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, responsesPath, r.URL.Path)

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
	assert.Equal(t, "end", resp.StopReason)
	assert.Equal(t, 10, resp.Usage.PromptTokens)
	assert.Equal(t, 5, resp.Usage.CompletionTokens)
	assert.Equal(t, 15, resp.Usage.TotalTokens)
}

// --- Complete: Tool Call ---

func TestComplete_ToolCall(t *testing.T) {
	sse := sseEvents(
		`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_abc","name":"read_file"}}`,
		`{"type":"response.function_call_arguments.delta","delta":"{\"pa"}`,
		`{"type":"response.function_call_arguments.delta","delta":"th\":\"main.go\"}"}`,
		`{"type":"response.completed","response":{"id":"resp_2","output":[],"usage":{"input_tokens":20,"output_tokens":10,"total_tokens":30}}}`,
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
			Parameters:  map[string]any{"type": "object"},
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
	sse := sseEvents(
		`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1","name":"list_dir"}}`,
		`{"type":"response.function_call_arguments.delta","delta":"{\"path\":\".\"}"}`,
		`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_2","name":"read_file"}}`,
		`{"type":"response.function_call_arguments.delta","delta":"{\"path\":\"go.mod\"}"}`,
		`{"type":"response.completed","response":{"id":"resp_3","output":[],"usage":{"input_tokens":5,"output_tokens":5,"total_tokens":10}}}`,
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
	sse := sseEvents(
		`{"type":"response.output_text.delta","delta":"Let me read that."}`,
		`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_x","name":"read_file"}}`,
		`{"type":"response.function_call_arguments.delta","delta":"{\"path\":\"main.go\"}"}`,
		`{"type":"response.completed","response":{"id":"resp_4","output":[],"usage":{"input_tokens":5,"output_tokens":5,"total_tokens":10}}}`,
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
	assert.Equal(t, "Let me read that.", resp.Content)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "read_file", resp.ToolCalls[0].Name)
}

// --- Complete: Nil Callback ---

func TestComplete_NilCallback(t *testing.T) {
	sse := sseEvents(
		`{"type":"response.output_text.delta","delta":"ok"}`,
		`{"type":"response.completed","response":{"id":"resp_5","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
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

// --- Complete: Stream Abort ---

func TestComplete_StreamAbort(t *testing.T) {
	sse := sseEvents(
		`{"type":"response.output_text.delta","delta":"hello"}`,
		`{"type":"response.output_text.delta","delta":" more"}`,
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

// --- Complete: Server Failure ---

func TestComplete_ServerFailure(t *testing.T) {
	sse := sseEvents(
		`{"type":"response.failed"}`,
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse)
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)
	_, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "server reported failure")
}

// --- Error Responses ---

func TestComplete_AuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":{"message":"Invalid API key","type":"invalid_request_error"}}`)
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
		fmt.Fprint(w, `{"error":{"message":"Rate limit exceeded"}}`)
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
		fmt.Fprint(w, `{"error":{"message":"Model not found"}}`)
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)
	_, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, llm.ErrModelNotFound)
}

// --- Request Structure ---

func TestComplete_RequestStructure(t *testing.T) {
	var capturedBody json.RawMessage

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseEvents(
			`{"type":"response.output_text.delta","delta":"ok"}`,
			`{"type":"response.completed","response":{"id":"r","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
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

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(capturedBody, &parsed))
	assert.Equal(t, "o4-mini", parsed["model"])
	assert.Equal(t, true, parsed["stream"])
	assert.Equal(t, "You are helpful.", parsed["instructions"])
	assert.InDelta(t, 0.5, parsed["temperature"].(float64), 0.001)
	assert.InDelta(t, 2048, parsed["max_output_tokens"].(float64), 0.001)

	tools := parsed["tools"].([]any)
	require.Len(t, tools, 1)
	tool := tools[0].(map[string]any)
	assert.Equal(t, "function", tool["type"])
	assert.Equal(t, "read_file", tool["name"])
}

// --- Simple Input (single user message → string) ---

func TestComplete_SimpleInputString(t *testing.T) {
	var capturedBody json.RawMessage

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseEvents(
			`{"type":"response.output_text.delta","delta":"ok"}`,
			`{"type":"response.completed","response":{"id":"r","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		))
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)
	_, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hello"}},
	}, nil)

	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(capturedBody, &parsed))
	assert.Equal(t, "hello", parsed["input"])
}

// --- Tool Result Messages ---

func TestComplete_ToolResultMessages(t *testing.T) {
	var capturedBody json.RawMessage

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseEvents(
			`{"type":"response.output_text.delta","delta":"done"}`,
			`{"type":"response.completed","response":{"id":"r","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
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

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(capturedBody, &parsed))

	items := parsed["input"].([]any)
	// user msg + function_call + function_call_output (no empty assistant message)
	require.Len(t, items, 3)

	// Function call item (uses call_id, not id).
	fcItem := items[1].(map[string]any)
	assert.Equal(t, "function_call", fcItem["type"])
	assert.Equal(t, "call_abc", fcItem["call_id"])
	assert.Equal(t, "read_file", fcItem["name"])
	assert.Equal(t, `{"path":"main.go"}`, fcItem["arguments"])

	// Function call output item.
	fcoItem := items[2].(map[string]any)
	assert.Equal(t, "function_call_output", fcoItem["type"])
	assert.Equal(t, "call_abc", fcoItem["call_id"])
	assert.Equal(t, "package main", fcoItem["output"])
}

// BUG 2 regression: function_call_output must include "output" field even when empty.
func TestComplete_ToolResultEmptyOutput(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseEvents(
			`{"type":"response.output_text.delta","delta":"done"}`,
			`{"type":"response.completed","response":{"id":"r","output":[],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`,
		))
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)

	_, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "delete temp.txt"},
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
				{ID: "call_del", Name: "delete_file", Arguments: `{"path":"temp.txt"}`},
			}},
			{Role: llm.RoleTool, ToolCallID: "call_del", Content: ""},
		},
	}, nil)

	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(capturedBody, &parsed))

	items := parsed["input"].([]any)
	require.Len(t, items, 3)

	fcoItem := items[2].(map[string]any)
	assert.Equal(t, "function_call_output", fcoItem["type"])
	// The "output" field MUST be present even when empty.
	_, hasOutput := fcoItem["output"]
	assert.True(t, hasOutput, "output field must be present on function_call_output even when empty")
	assert.Equal(t, "", fcoItem["output"])
}

// --- Custom Headers ---

func TestComplete_CustomHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "custom-value", r.Header.Get("X-Custom"))

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseEvents(
			`{"type":"response.output_text.delta","delta":"ok"}`,
			`{"type":"response.completed","response":{"id":"r","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		))
	}))
	defer srv.Close()

	entry := testEntry(srv.URL)
	entry.Headers = map[string]string{"X-Custom": "custom-value"}

	p, err := New(entry, testLogger)
	require.NoError(t, err)

	_, err = p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)
	require.NoError(t, err)
}

// --- URL: Bare Host (appends /v1/responses) ---

func TestComplete_BareHostAppendsPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/responses", r.URL.Path)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseEvents(
			`{"type":"response.output_text.delta","delta":"ok"}`,
			`{"type":"response.completed","response":{"id":"r","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		))
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL) // bare host, no path
	_, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)
	require.NoError(t, err)
}

// --- URL: Full URL With Path (used as-is) ---

func TestComplete_FullURLUsedAsIs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/openai/responses", r.URL.Path)
		assert.Equal(t, "2025-04-01-preview", r.URL.Query().Get("api-version"))

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseEvents(
			`{"type":"response.output_text.delta","delta":"ok"}`,
			`{"type":"response.completed","response":{"id":"r","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		))
	}))
	defer srv.Close()

	entry := testEntry(srv.URL + "/openai/responses?api-version=2025-04-01-preview")
	p, err := New(entry, testLogger)
	require.NoError(t, err)

	_, err = p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)
	require.NoError(t, err)
}

// --- Multi-Turn Agent Flow ---

func TestComplete_MultiTurnAgentFlow(t *testing.T) {
	// Simulates the agent loop: turn 1 returns a tool call, turn 2 receives
	// the tool result and returns a text response. Verifies the request body
	// on the second turn has the correct input structure.
	var callCount int
	var secondBody json.RawMessage

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "text/event-stream")

		if callCount == 1 {
			// Turn 1: return a tool call.
			fmt.Fprint(w, sseEvents(
				`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_xyz","name":"read_file"}}`,
				`{"type":"response.function_call_arguments.delta","delta":"{\"path\":\"test.txt\"}"}`,
				`{"type":"response.completed","response":{"id":"resp_1","output":[],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`,
			))
		} else {
			// Turn 2: capture the body, return text.
			body, _ := io.ReadAll(r.Body)
			secondBody = body
			fmt.Fprint(w, sseEvents(
				`{"type":"response.output_text.delta","delta":"File contains: hello"}`,
				`{"type":"response.completed","response":{"id":"resp_2","output":[],"usage":{"input_tokens":20,"output_tokens":10,"total_tokens":30}}}`,
			))
		}
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)

	// Turn 1: user message → tool call.
	resp1, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "You are helpful."},
			{Role: llm.RoleUser, Content: "read test.txt"},
		},
		Tools: []llm.ToolDef{{
			Name:        "read_file",
			Description: "Read a file",
			Parameters:  map[string]any{"type": "object"},
		}},
	}, nil)
	require.NoError(t, err)
	require.Len(t, resp1.ToolCalls, 1)
	assert.Equal(t, "call_xyz", resp1.ToolCalls[0].ID)

	// Turn 2: send tool result back.
	resp2, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "You are helpful."},
			{Role: llm.RoleUser, Content: "read test.txt"},
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
				{ID: "call_xyz", Name: "read_file", Arguments: `{"path":"test.txt"}`},
			}},
			{Role: llm.RoleTool, ToolCallID: "call_xyz", Content: "hello world"},
		},
		Tools: []llm.ToolDef{{
			Name:        "read_file",
			Description: "Read a file",
			Parameters:  map[string]any{"type": "object"},
		}},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "File contains: hello", resp2.Content)

	// Verify the second request body structure.
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(secondBody, &parsed))

	assert.Equal(t, "You are helpful.", parsed["instructions"])

	items := parsed["input"].([]any)
	// user message + function_call + function_call_output (no empty assistant message)
	require.Len(t, items, 3)

	userItem := items[0].(map[string]any)
	assert.Equal(t, "message", userItem["type"])
	assert.Equal(t, "user", userItem["role"])
	assert.Equal(t, "read test.txt", userItem["content"])

	fcItem := items[1].(map[string]any)
	assert.Equal(t, "function_call", fcItem["type"])
	assert.Equal(t, "call_xyz", fcItem["call_id"])
	assert.Equal(t, "read_file", fcItem["name"])
	assert.Equal(t, `{"path":"test.txt"}`, fcItem["arguments"])

	fcoItem := items[2].(map[string]any)
	assert.Equal(t, "function_call_output", fcoItem["type"])
	assert.Equal(t, "call_xyz", fcoItem["call_id"])
	assert.Equal(t, "hello world", fcoItem["output"])
}

// --- Assistant Message With Text And Tool Calls ---

func TestComplete_AssistantTextAndToolCalls(t *testing.T) {
	// When an assistant message has both text content AND tool calls,
	// we should emit both the message item and the function_call items.
	var capturedBody json.RawMessage

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseEvents(
			`{"type":"response.output_text.delta","delta":"done"}`,
			`{"type":"response.completed","response":{"id":"r","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		))
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)
	_, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "read file"},
			{Role: llm.RoleAssistant, Content: "Let me read that for you.", ToolCalls: []llm.ToolCall{
				{ID: "call_1", Name: "read_file", Arguments: `{"path":"x.go"}`},
			}},
			{Role: llm.RoleTool, ToolCallID: "call_1", Content: "package x"},
		},
	}, nil)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(capturedBody, &parsed))

	items := parsed["input"].([]any)
	// user msg + assistant msg (with text) + function_call + function_call_output
	require.Len(t, items, 4)

	assistantItem := items[1].(map[string]any)
	assert.Equal(t, "message", assistantItem["type"])
	assert.Equal(t, "assistant", assistantItem["role"])
	assert.Equal(t, "Let me read that for you.", assistantItem["content"])

	fcItem := items[2].(map[string]any)
	assert.Equal(t, "function_call", fcItem["type"])
	assert.Equal(t, "call_1", fcItem["call_id"])
}

// --- Registration ---

func TestRegistration(t *testing.T) {
	p, err := llm.NewProvider(llm.ProviderEntry{
		Name:      "my-responses",
		Type:      "openai-responses",
		ModelName: "o4-mini",
		APIKey:    "test-key",
	}, testLogger)

	require.NoError(t, err)
	assert.Equal(t, "my-responses", p.Name())
	assert.Equal(t, "o4-mini", p.Model())
}
