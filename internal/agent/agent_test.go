package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gajaai/openmarmut-go/internal/llm"
	"github.com/gajaai/openmarmut-go/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// --- Mock Provider ---

// mockProvider returns scripted responses in order.
type mockProvider struct {
	name      string
	model     string
	responses []*llm.Response
	requests  []llm.Request // captured requests
	callIdx   int
}

func (m *mockProvider) Name() string  { return m.name }
func (m *mockProvider) Model() string { return m.model }

func (m *mockProvider) Complete(_ context.Context, req llm.Request, cb llm.StreamCallback) (*llm.Response, error) {
	m.requests = append(m.requests, req)
	if m.callIdx >= len(m.responses) {
		return nil, fmt.Errorf("mock: no more responses (call %d)", m.callIdx)
	}
	resp := m.responses[m.callIdx]
	m.callIdx++

	if cb != nil && resp.Content != "" {
		if err := cb(resp.Content); err != nil {
			return nil, fmt.Errorf("mock: %w: %w", llm.ErrStreamAborted, err)
		}
	}

	return resp, nil
}

// --- Mock Runtime ---

// mockRuntime is a minimal in-memory runtime for testing.
type mockRuntime struct {
	targetDir string
	files     map[string][]byte
	dirs      map[string]bool
	execFn    func(command string) (*runtime.ExecResult, error)
}

func newMockRuntime(targetDir string) *mockRuntime {
	return &mockRuntime{
		targetDir: targetDir,
		files:     make(map[string][]byte),
		dirs:      map[string]bool{".": true},
	}
}

func (m *mockRuntime) Init(context.Context) error  { return nil }
func (m *mockRuntime) Close(context.Context) error { return nil }
func (m *mockRuntime) TargetDir() string           { return m.targetDir }

func (m *mockRuntime) ReadFile(_ context.Context, relPath string) ([]byte, error) {
	data, ok := m.files[relPath]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}

func (m *mockRuntime) WriteFile(_ context.Context, relPath string, data []byte, _ os.FileMode) error {
	m.files[relPath] = data
	return nil
}

func (m *mockRuntime) DeleteFile(_ context.Context, relPath string) error {
	if _, ok := m.files[relPath]; !ok {
		return os.ErrNotExist
	}
	delete(m.files, relPath)
	return nil
}

func (m *mockRuntime) ListDir(_ context.Context, relPath string) ([]runtime.FileEntry, error) {
	if !m.dirs[relPath] {
		return nil, os.ErrNotExist
	}
	var entries []runtime.FileEntry
	prefix := relPath
	if prefix != "." {
		prefix += "/"
	} else {
		prefix = ""
	}
	for path := range m.files {
		if prefix == "" || strings.HasPrefix(path, prefix) {
			name := strings.TrimPrefix(path, prefix)
			if !strings.Contains(name, "/") {
				entries = append(entries, runtime.FileEntry{
					Name: name, Size: int64(len(m.files[path])),
				})
			}
		}
	}
	return entries, nil
}

func (m *mockRuntime) MkDir(_ context.Context, relPath string, _ os.FileMode) error {
	m.dirs[relPath] = true
	return nil
}

func (m *mockRuntime) Exec(_ context.Context, command string, _ runtime.ExecOpts) (*runtime.ExecResult, error) {
	if m.execFn != nil {
		return m.execFn(command)
	}
	return &runtime.ExecResult{Stdout: "ok", ExitCode: 0}, nil
}

// --- Tests ---

func TestRun_TextOnly(t *testing.T) {
	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{Content: "Hello!", StopReason: "end", Usage: llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}},
		},
	}
	rt := newMockRuntime("/project")

	a := New(mp, rt, testLogger)
	result, err := a.Run(context.Background(), "hi", nil)

	require.NoError(t, err)
	assert.Equal(t, "Hello!", result.Response)
	assert.Empty(t, result.Steps)
	assert.Equal(t, 10, result.Usage.PromptTokens)
	assert.Equal(t, 5, result.Usage.CompletionTokens)
	assert.Equal(t, 15, result.Usage.TotalTokens)
}

func TestRun_TextStreaming(t *testing.T) {
	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{Content: "Hello world", StopReason: "end"},
		},
	}
	rt := newMockRuntime("/project")

	var streamed strings.Builder
	a := New(mp, rt, testLogger)
	result, err := a.Run(context.Background(), "hi", func(text string) error {
		streamed.WriteString(text)
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, "Hello world", result.Response)
	assert.Equal(t, "Hello world", streamed.String())
}

func TestRun_StreamingAfterToolCalls(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.files["x.txt"] = []byte("content")

	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{
				StopReason: "tool_use",
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "read_file", Arguments: `{"path":"x.txt"}`},
				},
			},
			{Content: "The file says: content", StopReason: "end"},
		},
	}

	var streamed strings.Builder
	a := New(mp, rt, testLogger)
	result, err := a.Run(context.Background(), "read x.txt", func(text string) error {
		streamed.WriteString(text)
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, "The file says: content", result.Response)
	// The final text response must be streamed even after tool call iterations.
	assert.Equal(t, "The file says: content", streamed.String())
}

func TestRun_SingleToolCall(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.files["main.go"] = []byte("package main")

	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{
				StopReason: "tool_use",
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "read_file", Arguments: `{"path":"main.go"}`},
				},
				Usage: llm.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
			},
			{
				Content:    "The file contains: package main",
				StopReason: "end",
				Usage:      llm.Usage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30},
			},
		},
	}

	a := New(mp, rt, testLogger)
	result, err := a.Run(context.Background(), "read main.go", nil)

	require.NoError(t, err)
	assert.Equal(t, "The file contains: package main", result.Response)
	require.Len(t, result.Steps, 1)
	assert.Equal(t, "read_file", result.Steps[0].ToolCall.Name)
	assert.Equal(t, "package main", result.Steps[0].Output)
	assert.Empty(t, result.Steps[0].Error)

	// Usage should be aggregated.
	assert.Equal(t, 30, result.Usage.PromptTokens)
	assert.Equal(t, 15, result.Usage.CompletionTokens)
	assert.Equal(t, 45, result.Usage.TotalTokens)
}

func TestRun_MultipleToolCallsOneIteration(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.files["a.txt"] = []byte("aaa")
	rt.files["b.txt"] = []byte("bbb")

	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{
				StopReason: "tool_use",
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "read_file", Arguments: `{"path":"a.txt"}`},
					{ID: "call_2", Name: "read_file", Arguments: `{"path":"b.txt"}`},
				},
			},
			{Content: "Both files read", StopReason: "end"},
		},
	}

	a := New(mp, rt, testLogger)
	result, err := a.Run(context.Background(), "read both files", nil)

	require.NoError(t, err)
	require.Len(t, result.Steps, 2)
	assert.Equal(t, "aaa", result.Steps[0].Output)
	assert.Equal(t, "bbb", result.Steps[1].Output)
	assert.Equal(t, "Both files read", result.Response)
}

func TestRun_ToolError(t *testing.T) {
	rt := newMockRuntime("/project")
	// No files — read_file will return os.ErrNotExist.

	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{
				StopReason: "tool_use",
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "read_file", Arguments: `{"path":"missing.go"}`},
				},
			},
			{Content: "File not found, creating it.", StopReason: "end"},
		},
	}

	a := New(mp, rt, testLogger)
	result, err := a.Run(context.Background(), "read missing.go", nil)

	require.NoError(t, err)
	require.Len(t, result.Steps, 1)
	assert.Contains(t, result.Steps[0].Error, "not exist")
	assert.Contains(t, result.Steps[0].Output, "error:")
}

func TestRun_UnknownTool(t *testing.T) {
	rt := newMockRuntime("/project")

	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{
				StopReason: "tool_use",
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "fly_to_moon", Arguments: `{}`},
				},
			},
			{Content: "I can't do that.", StopReason: "end"},
		},
	}

	a := New(mp, rt, testLogger)
	result, err := a.Run(context.Background(), "go to moon", nil)

	require.NoError(t, err)
	require.Len(t, result.Steps, 1)
	assert.Contains(t, result.Steps[0].Error, "unknown tool")
}

func TestRun_MaxIterations(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.files["x.txt"] = []byte("x")

	// Always return a tool call — never a text response.
	var responses []*llm.Response
	for i := 0; i < 5; i++ {
		responses = append(responses, &llm.Response{
			StopReason: "tool_use",
			ToolCalls: []llm.ToolCall{
				{ID: fmt.Sprintf("call_%d", i), Name: "read_file", Arguments: `{"path":"x.txt"}`},
			},
		})
	}

	mp := &mockProvider{name: "test", model: "m", responses: responses}
	a := New(mp, rt, testLogger, WithMaxIterations(3))
	_, err := a.Run(context.Background(), "loop forever", nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMaxIterations)
}

func TestRun_WriteFile(t *testing.T) {
	rt := newMockRuntime("/project")

	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{
				StopReason: "tool_use",
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "write_file", Arguments: `{"path":"hello.txt","content":"hello world"}`},
				},
			},
			{Content: "Created hello.txt", StopReason: "end"},
		},
	}

	a := New(mp, rt, testLogger)
	result, err := a.Run(context.Background(), "create hello.txt", nil)

	require.NoError(t, err)
	assert.Equal(t, "Created hello.txt", result.Response)
	require.Len(t, result.Steps, 1)
	assert.Contains(t, result.Steps[0].Output, "wrote 11 bytes")

	// Verify file was written.
	assert.Equal(t, []byte("hello world"), rt.files["hello.txt"])
}

func TestRun_ListDir(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.files["a.go"] = []byte("package a")
	rt.files["b.go"] = []byte("package b")

	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{
				StopReason: "tool_use",
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "list_dir", Arguments: `{"path":"."}`},
				},
			},
			{Content: "Found 2 files", StopReason: "end"},
		},
	}

	a := New(mp, rt, testLogger)
	result, err := a.Run(context.Background(), "list files", nil)

	require.NoError(t, err)
	require.Len(t, result.Steps, 1)

	var entries []map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Steps[0].Output), &entries))
	assert.Len(t, entries, 2)
}

func TestRun_DeleteFile(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.files["temp.txt"] = []byte("temp")

	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{
				StopReason: "tool_use",
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "delete_file", Arguments: `{"path":"temp.txt"}`},
				},
			},
			{Content: "Deleted", StopReason: "end"},
		},
	}

	a := New(mp, rt, testLogger)
	result, err := a.Run(context.Background(), "delete temp.txt", nil)

	require.NoError(t, err)
	assert.Contains(t, result.Steps[0].Output, "deleted temp.txt")
	_, exists := rt.files["temp.txt"]
	assert.False(t, exists)
}

func TestRun_Mkdir(t *testing.T) {
	rt := newMockRuntime("/project")

	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{
				StopReason: "tool_use",
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "mkdir", Arguments: `{"path":"src/pkg"}`},
				},
			},
			{Content: "Created", StopReason: "end"},
		},
	}

	a := New(mp, rt, testLogger)
	result, err := a.Run(context.Background(), "create dir", nil)

	require.NoError(t, err)
	assert.Contains(t, result.Steps[0].Output, "created directory src/pkg")
	assert.True(t, rt.dirs["src/pkg"])
}

func TestRun_ExecCommand(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.execFn = func(command string) (*runtime.ExecResult, error) {
		return &runtime.ExecResult{
			Stdout:   "PASS",
			Stderr:   "",
			ExitCode: 0,
		}, nil
	}

	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{
				StopReason: "tool_use",
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "execute_command", Arguments: `{"command":"go test ./..."}`},
				},
			},
			{Content: "Tests passed", StopReason: "end"},
		},
	}

	a := New(mp, rt, testLogger)
	result, err := a.Run(context.Background(), "run tests", nil)

	require.NoError(t, err)
	require.Len(t, result.Steps, 1)

	var execOut map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Steps[0].Output), &execOut))
	assert.Equal(t, "PASS", execOut["stdout"])
	assert.Equal(t, float64(0), execOut["exit_code"])
}

func TestRun_HistoryMaintained(t *testing.T) {
	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{Content: "First response", StopReason: "end"},
			{Content: "Second response", StopReason: "end"},
		},
	}
	rt := newMockRuntime("/project")

	a := New(mp, rt, testLogger)

	_, err := a.Run(context.Background(), "first", nil)
	require.NoError(t, err)

	_, err = a.Run(context.Background(), "second", nil)
	require.NoError(t, err)

	// History should have: system, user1, assistant1, user2, assistant2.
	history := a.History()
	assert.Len(t, history, 5)
	assert.Equal(t, llm.RoleSystem, history[0].Role)
	assert.Equal(t, llm.RoleUser, history[1].Role)
	assert.Equal(t, "first", history[1].Content)
	assert.Equal(t, llm.RoleAssistant, history[2].Role)
	assert.Equal(t, llm.RoleUser, history[3].Role)
	assert.Equal(t, "second", history[3].Content)
	assert.Equal(t, llm.RoleAssistant, history[4].Role)

	// Second request should include full history.
	require.Len(t, mp.requests, 2)
	assert.Len(t, mp.requests[1].Messages, 4) // system + user1 + assistant1 + user2
}

func TestRun_ToolResultSentBack(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.files["x.txt"] = []byte("content")

	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{
				StopReason: "tool_use",
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "read_file", Arguments: `{"path":"x.txt"}`},
				},
			},
			{Content: "Done", StopReason: "end"},
		},
	}

	a := New(mp, rt, testLogger)
	_, err := a.Run(context.Background(), "read x.txt", nil)
	require.NoError(t, err)

	// The second provider call should include the tool result.
	require.Len(t, mp.requests, 2)
	msgs := mp.requests[1].Messages
	// system + user + assistant(tool_call) + tool_result
	require.Len(t, msgs, 4)
	assert.Equal(t, llm.RoleTool, msgs[3].Role)
	assert.Equal(t, "call_1", msgs[3].ToolCallID)
	assert.Equal(t, "content", msgs[3].Content)
}

func TestRun_MultiTurnToolUse(t *testing.T) {
	rt := newMockRuntime("/project")

	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			// Turn 1: list directory
			{
				StopReason: "tool_use",
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "list_dir", Arguments: `{"path":"."}`},
				},
			},
			// Turn 2: write a file based on what was listed
			{
				StopReason: "tool_use",
				ToolCalls: []llm.ToolCall{
					{ID: "call_2", Name: "write_file", Arguments: `{"path":"new.txt","content":"new file"}`},
				},
			},
			// Turn 3: final text response
			{Content: "Created new.txt in empty project", StopReason: "end"},
		},
	}

	a := New(mp, rt, testLogger)
	result, err := a.Run(context.Background(), "add a file", nil)

	require.NoError(t, err)
	assert.Equal(t, "Created new.txt in empty project", result.Response)
	require.Len(t, result.Steps, 2)
	assert.Equal(t, "list_dir", result.Steps[0].ToolCall.Name)
	assert.Equal(t, "write_file", result.Steps[1].ToolCall.Name)
	assert.Equal(t, []byte("new file"), rt.files["new.txt"])
}

func TestRun_SystemPromptContainsTargetDir(t *testing.T) {
	mp := &mockProvider{
		name:      "test",
		model:     "test-model",
		responses: []*llm.Response{{Content: "ok", StopReason: "end"}},
	}
	rt := newMockRuntime("/my/project")

	a := New(mp, rt, testLogger)
	_, err := a.Run(context.Background(), "hi", nil)
	require.NoError(t, err)

	require.Len(t, mp.requests, 1)
	sysMsg := mp.requests[0].Messages[0]
	assert.Equal(t, llm.RoleSystem, sysMsg.Role)
	assert.Contains(t, sysMsg.Content, "/my/project")
}

func TestRun_CustomSystemPrompt(t *testing.T) {
	mp := &mockProvider{
		name:      "test",
		model:     "test-model",
		responses: []*llm.Response{{Content: "ok", StopReason: "end"}},
	}
	rt := newMockRuntime("/project")

	a := New(mp, rt, testLogger, WithSystemPrompt("Custom prompt"))
	_, err := a.Run(context.Background(), "hi", nil)
	require.NoError(t, err)

	sysMsg := mp.requests[0].Messages[0]
	assert.Equal(t, "Custom prompt", sysMsg.Content)
}

func TestRun_ToolsIncludedInRequest(t *testing.T) {
	mp := &mockProvider{
		name:      "test",
		model:     "test-model",
		responses: []*llm.Response{{Content: "ok", StopReason: "end"}},
	}
	rt := newMockRuntime("/project")

	a := New(mp, rt, testLogger)
	_, err := a.Run(context.Background(), "hi", nil)
	require.NoError(t, err)

	require.Len(t, mp.requests, 1)
	tools := mp.requests[0].Tools
	assert.Len(t, tools, 18)

	toolNames := make(map[string]bool)
	for _, t := range tools {
		toolNames[t.Name] = true
	}
	assert.True(t, toolNames["read_file"])
	assert.True(t, toolNames["read_file_lines"])
	assert.True(t, toolNames["write_file"])
	assert.True(t, toolNames["patch_file"])
	assert.True(t, toolNames["delete_file"])
	assert.True(t, toolNames["list_dir"])
	assert.True(t, toolNames["mkdir"])
	assert.True(t, toolNames["execute_command"])
	assert.True(t, toolNames["grep_files"])
	assert.True(t, toolNames["find_files"])
}

// --- Tool Tests (unit level) ---

func TestReadFileTool_Truncation(t *testing.T) {
	rt := newMockRuntime("/project")
	// Create a file larger than 100KB.
	bigContent := strings.Repeat("x", 150*1024)
	rt.files["big.txt"] = []byte(bigContent)

	tool := readFileTool()
	output, err := tool.Execute(context.Background(), rt, json.RawMessage(`{"path":"big.txt"}`))

	require.NoError(t, err)
	assert.Contains(t, output, "[truncated")
	assert.Less(t, len(output), len(bigContent))
}

func TestClearHistory(t *testing.T) {
	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{Content: "first", StopReason: "end"},
			{Content: "after clear", StopReason: "end"},
		},
	}
	rt := newMockRuntime("/project")
	a := New(mp, rt, testLogger)

	_, err := a.Run(context.Background(), "hello", nil)
	require.NoError(t, err)
	assert.Len(t, a.History(), 3) // system + user + assistant

	a.ClearHistory()
	assert.Len(t, a.History(), 1) // just system
	assert.Equal(t, llm.RoleSystem, a.History()[0].Role)

	_, err = a.Run(context.Background(), "after clear", nil)
	require.NoError(t, err)
	assert.Len(t, a.History(), 3) // system + user + assistant
}

func TestToolsAccessor(t *testing.T) {
	mp := &mockProvider{name: "test", model: "m"}
	rt := newMockRuntime("/project")
	a := New(mp, rt, testLogger)

	tools := a.Tools()
	assert.Len(t, tools, 18)
}

func TestToolCallCallback(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.files["x.txt"] = []byte("content")

	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{
				StopReason: "tool_use",
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "read_file", Arguments: `{"path":"x.txt"}`},
				},
			},
			{Content: "done", StopReason: "end"},
		},
	}

	var called []string
	a := New(mp, rt, testLogger, WithToolCallCallback(func(tc llm.ToolCall) {
		called = append(called, tc.Name)
	}))

	_, err := a.Run(context.Background(), "read x.txt", nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"read_file"}, called)
}

// --- New Tool Tests ---

func TestReadFileLinesTool_HappyPath(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.files["lines.txt"] = []byte("line1\nline2\nline3\nline4\nline5")

	tool := readFileLinesTool()
	output, err := tool.Execute(context.Background(), rt, json.RawMessage(`{"path":"lines.txt","start_line":2,"end_line":4}`))

	require.NoError(t, err)
	assert.Contains(t, output, "2\tline2")
	assert.Contains(t, output, "3\tline3")
	assert.Contains(t, output, "4\tline4")
	assert.NotContains(t, output, "line1")
	assert.NotContains(t, output, "line5")
}

func TestReadFileLinesTool_ClampToEnd(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.files["short.txt"] = []byte("a\nb\nc")

	tool := readFileLinesTool()
	output, err := tool.Execute(context.Background(), rt, json.RawMessage(`{"path":"short.txt","start_line":2,"end_line":100}`))

	require.NoError(t, err)
	assert.Contains(t, output, "2\tb")
	assert.Contains(t, output, "3\tc")
}

func TestReadFileLinesTool_InvalidRange(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.files["x.txt"] = []byte("hello")

	tool := readFileLinesTool()
	_, err := tool.Execute(context.Background(), rt, json.RawMessage(`{"path":"x.txt","start_line":5,"end_line":2}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "end_line")
}

func TestReadFileLinesTool_StartBeyondFile(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.files["x.txt"] = []byte("one\ntwo")

	tool := readFileLinesTool()
	_, err := tool.Execute(context.Background(), rt, json.RawMessage(`{"path":"x.txt","start_line":10,"end_line":20}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds file length")
}

func TestPatchFileTool_HappyPath(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.files["main.go"] = []byte("package main\n\nfunc hello() {}\n")

	tool := patchFileTool()
	output, err := tool.Execute(context.Background(), rt, json.RawMessage(`{
		"path": "main.go",
		"edits": [{"old_text": "func hello() {}", "new_text": "func hello() {\n\tfmt.Println(\"hi\")\n}"}]
	}`))

	require.NoError(t, err)
	assert.Contains(t, output, "applied 1 edit(s)")
	assert.Contains(t, string(rt.files["main.go"]), "fmt.Println")
}

func TestPatchFileTool_MultipleEdits(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.files["cfg.yaml"] = []byte("debug: false\nport: 8080\n")

	tool := patchFileTool()
	output, err := tool.Execute(context.Background(), rt, json.RawMessage(`{
		"path": "cfg.yaml",
		"edits": [
			{"old_text": "debug: false", "new_text": "debug: true"},
			{"old_text": "port: 8080", "new_text": "port: 9090"}
		]
	}`))

	require.NoError(t, err)
	assert.Contains(t, output, "applied 2 edit(s)")
	content := string(rt.files["cfg.yaml"])
	assert.Contains(t, content, "debug: true")
	assert.Contains(t, content, "port: 9090")
}

func TestPatchFileTool_OldTextNotFound(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.files["x.txt"] = []byte("hello")

	tool := patchFileTool()
	_, err := tool.Execute(context.Background(), rt, json.RawMessage(`{
		"path": "x.txt",
		"edits": [{"old_text": "goodbye", "new_text": "hi"}]
	}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestPatchFileTool_AmbiguousMatch(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.files["x.txt"] = []byte("foo bar foo baz foo")

	tool := patchFileTool()
	_, err := tool.Execute(context.Background(), rt, json.RawMessage(`{
		"path": "x.txt",
		"edits": [{"old_text": "foo", "new_text": "qux"}]
	}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "matches 3 times")
}

func TestPatchFileTool_NoEdits(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.files["x.txt"] = []byte("hello")

	tool := patchFileTool()
	_, err := tool.Execute(context.Background(), rt, json.RawMessage(`{"path":"x.txt","edits":[]}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no edits")
}

func TestGrepFilesTool_HappyPath(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.execFn = func(command string) (*runtime.ExecResult, error) {
		return &runtime.ExecResult{
			Stdout:   "src/main.go:10:func main() {\nsrc/main.go:15:func helper() {\n",
			ExitCode: 0,
		}, nil
	}

	tool := grepFilesTool(nil)
	output, err := tool.Execute(context.Background(), rt, json.RawMessage(`{"pattern":"func.*\\(","path":"src","include_glob":"*.go"}`))

	require.NoError(t, err)
	assert.Contains(t, output, "main.go:10")
	assert.Contains(t, output, "func main()")
}

func TestGrepFilesTool_NoMatches(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.execFn = func(command string) (*runtime.ExecResult, error) {
		return &runtime.ExecResult{Stdout: "", ExitCode: 1}, nil
	}

	tool := grepFilesTool(nil)
	output, err := tool.Execute(context.Background(), rt, json.RawMessage(`{"pattern":"nonexistent"}`))

	require.NoError(t, err)
	assert.Equal(t, "no matches found", output)
}

func TestGrepFilesTool_GrepError(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.execFn = func(command string) (*runtime.ExecResult, error) {
		return &runtime.ExecResult{Stderr: "invalid regex", ExitCode: 2}, nil
	}

	tool := grepFilesTool(nil)
	_, err := tool.Execute(context.Background(), rt, json.RawMessage(`{"pattern":"[invalid"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "grep error")
}

func TestGrepFilesTool_DefaultPath(t *testing.T) {
	rt := newMockRuntime("/project")
	var capturedCmd string
	rt.execFn = func(command string) (*runtime.ExecResult, error) {
		capturedCmd = command
		return &runtime.ExecResult{Stdout: "", ExitCode: 1}, nil
	}

	tool := grepFilesTool(nil)
	_, _ = tool.Execute(context.Background(), rt, json.RawMessage(`{"pattern":"test"}`))
	assert.Contains(t, capturedCmd, "'.'")
}

func TestFindFilesTool_HappyPath(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.execFn = func(command string) (*runtime.ExecResult, error) {
		return &runtime.ExecResult{
			Stdout:   "src/main.go\nsrc/util.go\n",
			ExitCode: 0,
		}, nil
	}

	tool := findFilesTool(nil)
	output, err := tool.Execute(context.Background(), rt, json.RawMessage(`{"pattern":"*.go","path":"src"}`))

	require.NoError(t, err)
	assert.Contains(t, output, "src/main.go")
	assert.Contains(t, output, "src/util.go")
}

func TestFindFilesTool_NoResults(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.execFn = func(command string) (*runtime.ExecResult, error) {
		return &runtime.ExecResult{Stdout: "", ExitCode: 0}, nil
	}

	tool := findFilesTool(nil)
	output, err := tool.Execute(context.Background(), rt, json.RawMessage(`{"pattern":"*.xyz"}`))

	require.NoError(t, err)
	assert.Equal(t, "no files found", output)
}

func TestFindFilesTool_DefaultPath(t *testing.T) {
	rt := newMockRuntime("/project")
	var capturedCmd string
	rt.execFn = func(command string) (*runtime.ExecResult, error) {
		capturedCmd = command
		return &runtime.ExecResult{Stdout: "", ExitCode: 0}, nil
	}

	tool := findFilesTool(nil)
	_, _ = tool.Execute(context.Background(), rt, json.RawMessage(`{"pattern":"*.go"}`))
	assert.Contains(t, capturedCmd, "'.'")
}

func TestShellQuote(t *testing.T) {
	assert.Equal(t, "'hello'", shellQuote("hello"))
	assert.Equal(t, "'it'\\''s'", shellQuote("it's"))
	assert.Equal(t, "'a b c'", shellQuote("a b c"))
}

func TestExecTool_WithWorkdir(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "sub")
	require.NoError(t, os.MkdirAll(subDir, 0755))

	var capturedOpts runtime.ExecOpts
	rt := newMockRuntime(dir)
	rt.execFn = func(command string) (*runtime.ExecResult, error) {
		return &runtime.ExecResult{Stdout: "ok"}, nil
	}

	// Override Exec to capture opts (the mock doesn't capture ExecOpts by default).
	tool := execTool()
	_, err := tool.Execute(context.Background(), rt, json.RawMessage(`{"command":"ls","workdir":"sub"}`))
	require.NoError(t, err)
	_ = capturedOpts // The mock doesn't capture opts, but this tests the JSON parsing.
}

// --- Plan Mode Tests ---

func TestRunPlan_TextOnly(t *testing.T) {
	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{Content: "## Plan: Add feature\n\n1. Create file", StopReason: "end"},
		},
	}
	rt := newMockRuntime("/project")

	a := New(mp, rt, testLogger)
	result, err := a.RunPlan(context.Background(), "add a feature", nil)

	require.NoError(t, err)
	assert.Contains(t, result.Response, "Plan:")
	assert.Contains(t, result.Response, "Create file")
}

func TestRunPlan_UsesReadOnlyTools(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.files["main.go"] = []byte("package main")

	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{
				StopReason: "tool_use",
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "read_file", Arguments: `{"path":"main.go"}`},
				},
			},
			{Content: "Plan: modify main.go", StopReason: "end"},
		},
	}

	a := New(mp, rt, testLogger)
	result, err := a.RunPlan(context.Background(), "analyze the project", nil)

	require.NoError(t, err)
	assert.Contains(t, result.Response, "Plan:")
	require.Len(t, result.Steps, 1)
	assert.Equal(t, "read_file", result.Steps[0].ToolCall.Name)
	assert.Equal(t, "package main", result.Steps[0].Output)
}

func TestRunPlan_BlocksWriteTools(t *testing.T) {
	rt := newMockRuntime("/project")

	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{
				StopReason: "tool_use",
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "write_file", Arguments: `{"path":"x.txt","content":"bad"}`},
				},
			},
			{Content: "Plan done", StopReason: "end"},
		},
	}

	a := New(mp, rt, testLogger)
	result, err := a.RunPlan(context.Background(), "hack the project", nil)

	require.NoError(t, err)
	require.Len(t, result.Steps, 1)
	assert.Contains(t, result.Steps[0].Error, "not available in plan mode")
	// File should NOT have been created.
	_, exists := rt.files["x.txt"]
	assert.False(t, exists)
}

func TestRunPlan_DoesNotPollutHistory(t *testing.T) {
	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{Content: "plan text", StopReason: "end"},
			{Content: "normal response", StopReason: "end"},
		},
	}
	rt := newMockRuntime("/project")

	a := New(mp, rt, testLogger)

	// Run plan — should not add to main history.
	_, err := a.RunPlan(context.Background(), "plan something", nil)
	require.NoError(t, err)
	assert.Len(t, a.History(), 1) // only system prompt

	// Normal run should work fine after plan.
	_, err = a.Run(context.Background(), "do something", nil)
	require.NoError(t, err)
	assert.Len(t, a.History(), 3) // system + user + assistant
}

func TestRunPlan_SystemPromptContainsPlanMode(t *testing.T) {
	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{Content: "plan", StopReason: "end"},
		},
	}
	rt := newMockRuntime("/project")

	a := New(mp, rt, testLogger)
	_, err := a.RunPlan(context.Background(), "analyze", nil)
	require.NoError(t, err)

	require.Len(t, mp.requests, 1)
	sysMsg := mp.requests[0].Messages[0]
	assert.Equal(t, llm.RoleSystem, sysMsg.Role)
	assert.Contains(t, sysMsg.Content, "PLAN MODE")
	assert.Contains(t, sysMsg.Content, "ANALYSIS ONLY")
}

func TestRunPlan_OnlyReadOnlyToolDefs(t *testing.T) {
	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{Content: "plan", StopReason: "end"},
		},
	}
	rt := newMockRuntime("/project")

	a := New(mp, rt, testLogger)
	_, err := a.RunPlan(context.Background(), "analyze", nil)
	require.NoError(t, err)

	require.Len(t, mp.requests, 1)
	tools := mp.requests[0].Tools
	for _, td := range tools {
		assert.True(t, ReadOnlyToolNames()[td.Name],
			"tool %q should be read-only in plan mode", td.Name)
	}
	// Should only contain read-only tools (count may vary based on agent config).
	assert.Greater(t, len(tools), 0, "plan mode should have at least one tool")
}

func TestReadOnlyToolNames(t *testing.T) {
	names := ReadOnlyToolNames()
	assert.True(t, names["read_file"])
	assert.True(t, names["list_dir"])
	assert.True(t, names["grep_files"])
	assert.True(t, names["find_files"])
	assert.True(t, names["git_status"])
	assert.False(t, names["write_file"])
	assert.False(t, names["delete_file"])
	assert.False(t, names["execute_command"])
	assert.False(t, names["git_commit"])
}

// --- CompactHistory Tests ---

func TestCompactHistory_ReducesTokens(t *testing.T) {
	// Build a long history that will be summarized.
	summaryText := "Summary: user asked to refactor auth module."
	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{Content: summaryText},
		},
	}
	rt := newMockRuntime("/test")
	ag := New(mp, rt, testLogger)

	// Seed history with system + several turns.
	ag.SetHistory([]llm.Message{
		{Role: llm.RoleSystem, Content: "You are a helpful assistant."},
		{Role: llm.RoleUser, Content: "Please refactor the auth module to use JWT tokens instead of session cookies."},
		{Role: llm.RoleAssistant, Content: "I'll refactor the auth module. Let me start by reading the current implementation..."},
		{Role: llm.RoleUser, Content: "Also update the tests to match."},
		{Role: llm.RoleAssistant, Content: strings.Repeat("Here is the detailed implementation with lots of code. ", 50)},
	})

	beforeEstimate := EstimateMessagesTokens(ag.History())

	before, after, err := ag.CompactHistory(context.Background(), "")
	require.NoError(t, err)

	assert.Equal(t, beforeEstimate, before)
	assert.Less(t, after, before, "compacted history should use fewer tokens")

	// History should be: system + summary + compaction note.
	history := ag.History()
	assert.Len(t, history, 3)
	assert.Equal(t, llm.RoleSystem, history[0].Role)
	assert.Equal(t, llm.RoleAssistant, history[1].Role)
	assert.Equal(t, summaryText, history[1].Content)
	assert.Equal(t, llm.RoleUser, history[2].Role)
	assert.Contains(t, history[2].Content, "Conversation compacted")
}

func TestCompactHistory_CustomInstruction(t *testing.T) {
	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{Content: "Summary with auth focus."},
		},
	}
	rt := newMockRuntime("/test")
	ag := New(mp, rt, testLogger)

	ag.SetHistory([]llm.Message{
		{Role: llm.RoleSystem, Content: "System prompt."},
		{Role: llm.RoleUser, Content: "Do something."},
		{Role: llm.RoleAssistant, Content: "Done."},
		{Role: llm.RoleUser, Content: "More work."},
	})

	_, _, err := ag.CompactHistory(context.Background(), "Preserve the auth changes")
	require.NoError(t, err)

	// The custom instruction should appear in the summarization request.
	require.Len(t, mp.requests, 1)
	systemMsg := mp.requests[0].Messages[0].Content
	assert.Contains(t, systemMsg, "Preserve the auth changes")
}

func TestCompactHistory_TooFewMessages(t *testing.T) {
	mp := &mockProvider{
		name:  "test",
		model: "test-model",
	}
	rt := newMockRuntime("/test")
	ag := New(mp, rt, testLogger)

	// Only system + 1 message = 2 messages, below threshold.
	ag.SetHistory([]llm.Message{
		{Role: llm.RoleSystem, Content: "System."},
		{Role: llm.RoleUser, Content: "Hello."},
	})

	before, after, err := ag.CompactHistory(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, before, after, "should return same tokens when nothing to compact")

	// Provider should NOT have been called.
	assert.Empty(t, mp.requests)
}

func TestCompactHistory_PreservesSystemPrompt(t *testing.T) {
	systemPrompt := "You are a coding assistant with specific rules."
	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{Content: "Compacted summary."},
		},
	}
	rt := newMockRuntime("/test")
	ag := New(mp, rt, testLogger)

	ag.SetHistory([]llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: "Work on feature X."},
		{Role: llm.RoleAssistant, Content: "Working on it..."},
		{Role: llm.RoleUser, Content: "Continue."},
	})

	_, _, err := ag.CompactHistory(context.Background(), "")
	require.NoError(t, err)

	history := ag.History()
	assert.Equal(t, systemPrompt, history[0].Content, "system prompt must be preserved")
}

func TestCompactHistory_LLMError(t *testing.T) {
	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		// No responses → will return error.
	}
	rt := newMockRuntime("/test")
	ag := New(mp, rt, testLogger)

	ag.SetHistory([]llm.Message{
		{Role: llm.RoleSystem, Content: "System."},
		{Role: llm.RoleUser, Content: "Q1."},
		{Role: llm.RoleAssistant, Content: "A1."},
		{Role: llm.RoleUser, Content: "Q2."},
	})

	originalLen := len(ag.History())
	_, _, err := ag.CompactHistory(context.Background(), "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "agent.CompactHistory")

	// History should be unchanged on error.
	assert.Len(t, ag.History(), originalLen)
}

// --- Extended Thinking Tests ---

func TestExtendedThinking_Option(t *testing.T) {
	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{Content: "Done."},
		},
	}
	rt := newMockRuntime("/test")
	ag := New(mp, rt, testLogger, WithExtendedThinking(true, 20000))

	assert.True(t, ag.ExtendedThinking())

	_, err := ag.Run(context.Background(), "test", nil)
	require.NoError(t, err)

	// The request should have extended thinking enabled.
	require.Len(t, mp.requests, 1)
	assert.True(t, mp.requests[0].ExtendedThinking)
	assert.Equal(t, 20000, mp.requests[0].ThinkingBudget)
}

func TestExtendedThinking_Toggle(t *testing.T) {
	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{Content: "R1."},
			{Content: "R2."},
		},
	}
	rt := newMockRuntime("/test")
	ag := New(mp, rt, testLogger)

	assert.False(t, ag.ExtendedThinking())

	ag.SetExtendedThinking(true)
	assert.True(t, ag.ExtendedThinking())

	ag.SetThinkingBudget(5000)

	_, err := ag.Run(context.Background(), "test", nil)
	require.NoError(t, err)

	require.Len(t, mp.requests, 1)
	assert.True(t, mp.requests[0].ExtendedThinking)
	assert.Equal(t, 5000, mp.requests[0].ThinkingBudget)

	ag.SetExtendedThinking(false)
	_, err = ag.Run(context.Background(), "test2", nil)
	require.NoError(t, err)

	require.Len(t, mp.requests, 2)
	assert.False(t, mp.requests[1].ExtendedThinking)
}

func TestExtendedThinking_ResponseThinking(t *testing.T) {
	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{
				Content:        "The answer is 42.",
				Thinking:       "Let me think step by step...",
				ThinkingTokens: 150,
			},
		},
	}
	rt := newMockRuntime("/test")
	ag := New(mp, rt, testLogger, WithExtendedThinking(true, 10000))

	result, err := ag.Run(context.Background(), "What is the answer?", nil)
	require.NoError(t, err)
	assert.Equal(t, "The answer is 42.", result.Response)
}
