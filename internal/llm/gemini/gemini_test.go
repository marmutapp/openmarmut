package gemini

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
		Type:        "gemini",
		ModelName:   "gemini-2.0-flash",
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

func sseData(chunks ...string) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString("data: ")
		b.WriteString(c)
		b.WriteString("\n\n")
	}
	return b.String()
}

// --- Constructor Tests ---

func TestNew_Success(t *testing.T) {
	p, err := New(llm.ProviderEntry{
		Name:        "my-gemini",
		ModelName:   "gemini-2.0-flash",
		EndpointURL: "https://generativelanguage.googleapis.com",
		Auth:        llm.AuthConfig{Type: "query", QueryParam: "key"},
	}, testLogger)

	require.NoError(t, err)
	assert.Equal(t, "my-gemini", p.Name())
	assert.Equal(t, "gemini-2.0-flash", p.Model())
}

func TestNew_MissingModel(t *testing.T) {
	_, err := New(llm.ProviderEntry{Name: "test"}, testLogger)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model is required")
}

// --- Complete: Text Response ---

func TestComplete_TextResponse(t *testing.T) {
	sse := sseData(
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]},"finishReason":""}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":1,"totalTokenCount":6}}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":" world"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2,"totalTokenCount":7}}`,
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, r.URL.Path, "/v1beta/models/gemini-2.0-flash:streamGenerateContent")
		assert.Equal(t, "sse", r.URL.Query().Get("alt"))

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
	assert.Equal(t, 5, resp.Usage.PromptTokens)
	assert.Equal(t, 2, resp.Usage.CompletionTokens)
	assert.Equal(t, 7, resp.Usage.TotalTokens)
}

// --- Complete: Tool Call ---

func TestComplete_ToolCall(t *testing.T) {
	sse := sseData(
		`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read_file","args":{"path":"main.go"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`,
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
	assert.Equal(t, "read_file", resp.ToolCalls[0].Name)
	assert.JSONEq(t, `{"path":"main.go"}`, resp.ToolCalls[0].Arguments)
}

// --- Complete: Multiple Tool Calls ---

func TestComplete_MultipleToolCalls(t *testing.T) {
	sse := sseData(
		`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"list_dir","args":{"path":"."}}},{"functionCall":{"name":"read_file","args":{"path":"go.mod"}}}]},"finishReason":"STOP"}]}`,
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
	sse := sseData(
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`,
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
	sse := sseData(
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]}}]}`,
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
		return fmt.Errorf("cancelled")
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, llm.ErrStreamAborted)
}

// --- Error Responses ---

func TestComplete_AuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"error":{"code":403,"message":"API key not valid"}}`)
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
		fmt.Fprint(w, `{"error":{"code":429,"message":"Quota exceeded"}}`)
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
		fmt.Fprint(w, `{"error":{"code":404,"message":"Model not found"}}`)
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
	var capturedBody apiRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseData(
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`,
		))
	}))
	defer srv.Close()

	temp := 0.7
	maxTok := 4096

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

	// System instruction.
	require.NotNil(t, capturedBody.SystemInstruction)
	require.Len(t, capturedBody.SystemInstruction.Parts, 1)
	assert.Equal(t, "You are helpful.", capturedBody.SystemInstruction.Parts[0].Text)

	// Contents: only user message (system is separate).
	require.Len(t, capturedBody.Contents, 1)
	assert.Equal(t, "user", capturedBody.Contents[0].Role)
	assert.Equal(t, "hello", capturedBody.Contents[0].Parts[0].Text)

	// Generation config.
	require.NotNil(t, capturedBody.GenerationConfig)
	require.NotNil(t, capturedBody.GenerationConfig.Temperature)
	assert.InDelta(t, 0.7, *capturedBody.GenerationConfig.Temperature, 0.001)
	require.NotNil(t, capturedBody.GenerationConfig.MaxOutputTokens)
	assert.Equal(t, 4096, *capturedBody.GenerationConfig.MaxOutputTokens)

	// Tools.
	require.Len(t, capturedBody.Tools, 1)
	require.Len(t, capturedBody.Tools[0].FunctionDeclarations, 1)
	assert.Equal(t, "read_file", capturedBody.Tools[0].FunctionDeclarations[0].Name)
}

// --- Tool Result Messages ---

func TestComplete_ToolResultMessages(t *testing.T) {
	var capturedBody apiRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseData(
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]},"finishReason":"STOP"}]}`,
		))
	}))
	defer srv.Close()

	p := testProvider(t, srv.URL)
	_, err := p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "read file"},
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
				{ID: "call_1", Name: "read_file", Arguments: `{"path":"main.go"}`},
			}},
			{Role: llm.RoleTool, ToolCallID: "read_file", Content: `{"result":"package main"}`},
		},
	}, nil)

	require.NoError(t, err)

	// Contents: user, model (with functionCall), user (with functionResponse).
	require.Len(t, capturedBody.Contents, 3)

	// Model message with functionCall.
	modelContent := capturedBody.Contents[1]
	assert.Equal(t, "model", modelContent.Role)
	require.Len(t, modelContent.Parts, 1)
	require.NotNil(t, modelContent.Parts[0].FunctionCall)
	assert.Equal(t, "read_file", modelContent.Parts[0].FunctionCall.Name)

	// User message with functionResponse.
	toolContent := capturedBody.Contents[2]
	assert.Equal(t, "user", toolContent.Role)
	require.Len(t, toolContent.Parts, 1)
	require.NotNil(t, toolContent.Parts[0].FunctionResponse)
	assert.Equal(t, "read_file", toolContent.Parts[0].FunctionResponse.Name)
}

// --- Auth: Query Param ---

func TestComplete_QueryParamAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test-api-key", r.URL.Query().Get("key"))

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseData(
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`,
		))
	}))
	defer srv.Close()

	entry := testEntry(srv.URL)
	entry.APIKey = "test-api-key"
	entry.Auth = llm.AuthConfig{Type: "query", QueryParam: "key"}

	p, err := New(entry, testLogger)
	require.NoError(t, err)

	_, err = p.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}, nil)
	require.NoError(t, err)
}

// --- MAX_TOKENS finish reason ---

func TestComplete_MaxTokensFinishReason(t *testing.T) {
	sse := sseData(
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"truncated"}]},"finishReason":"MAX_TOKENS"}]}`,
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
	assert.Equal(t, "max_tokens", resp.StopReason)
}

// --- Registration ---

func TestRegistration(t *testing.T) {
	p, err := llm.NewProvider(llm.ProviderEntry{
		Name:      "my-gemini",
		Type:      "gemini",
		ModelName: "gemini-2.0-flash",
		APIKey:    "test-key",
	}, testLogger)

	require.NoError(t, err)
	assert.Equal(t, "my-gemini", p.Name())
	assert.Equal(t, "gemini-2.0-flash", p.Model())
}
