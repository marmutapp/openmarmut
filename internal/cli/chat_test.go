package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/gajaai/openmarmut-go/internal/agent"
	"github.com/gajaai/openmarmut-go/internal/llm"
	"github.com/gajaai/openmarmut-go/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// panicProvider panics if Complete is ever called.
// Used to verify slash commands never reach the LLM.
type panicProvider struct{}

func (p *panicProvider) Name() string  { return "panic" }
func (p *panicProvider) Model() string { return "panic-model" }
func (p *panicProvider) Complete(_ context.Context, _ llm.Request, _ llm.StreamCallback) (*llm.Response, error) {
	panic("LLM should never be called for slash commands")
}

// stubRuntime satisfies the runtime.Runtime interface for agent construction.
type stubRuntime struct{}

func (s *stubRuntime) Init(context.Context) error                                             { return nil }
func (s *stubRuntime) Close(context.Context) error                                            { return nil }
func (s *stubRuntime) ReadFile(_ context.Context, _ string) ([]byte, error)                   { return nil, nil }
func (s *stubRuntime) WriteFile(_ context.Context, _ string, _ []byte, _ os.FileMode) error   { return nil }
func (s *stubRuntime) DeleteFile(_ context.Context, _ string) error                           { return nil }
func (s *stubRuntime) ListDir(_ context.Context, _ string) ([]runtime.FileEntry, error)       { return nil, nil }
func (s *stubRuntime) MkDir(_ context.Context, _ string, _ os.FileMode) error                 { return nil }
func (s *stubRuntime) Exec(_ context.Context, _ string, _ runtime.ExecOpts) (*runtime.ExecResult, error) {
	return &runtime.ExecResult{}, nil
}
func (s *stubRuntime) TargetDir() string { return "/test" }

var testLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

func newTestState() (*chatState, *bytes.Buffer) {
	ag := agent.New(&panicProvider{}, &stubRuntime{}, testLogger)
	var buf bytes.Buffer
	state := &chatState{
		ag:    ag,
		model: "gpt-4o",
		out:   &buf,
	}
	return state, &buf
}

func TestSlashHelp_NoLLMCall(t *testing.T) {
	state, buf := newTestState()
	action := handleSlashCommand("/help", state)

	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "/clear")
	assert.Contains(t, buf.String(), "/tools")
	assert.Contains(t, buf.String(), "/cost")
	assert.Contains(t, buf.String(), "/quit")
}

func TestSlashTools_NoLLMCall(t *testing.T) {
	state, buf := newTestState()
	action := handleSlashCommand("/tools", state)

	assert.Equal(t, slashHandled, action)
	output := buf.String()
	assert.Contains(t, output, "read_file")
	assert.Contains(t, output, "write_file")
	assert.Contains(t, output, "grep_files")
	assert.Contains(t, output, "patch_file")
}

func TestSlashCost_NoLLMCall(t *testing.T) {
	state, buf := newTestState()
	state.sessionUsage = llm.Usage{
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
	}
	action := handleSlashCommand("/cost", state)

	assert.Equal(t, slashHandled, action)
	output := buf.String()
	assert.Contains(t, output, "100")
	assert.Contains(t, output, "50")
	assert.Contains(t, output, "150")
}

func TestSlashClear_NoLLMCall(t *testing.T) {
	state, buf := newTestState()
	// Add some usage first.
	state.sessionUsage = llm.Usage{PromptTokens: 100, TotalTokens: 100}

	action := handleSlashCommand("/clear", state)

	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "History cleared")
	// Session usage should be reset.
	assert.Equal(t, 0, state.sessionUsage.TotalTokens)
	// History should be just the system prompt.
	assert.Len(t, state.ag.History(), 1)
}

func TestSlashQuit_NoLLMCall(t *testing.T) {
	state, _ := newTestState()

	assert.Equal(t, slashExit, handleSlashCommand("/quit", state))
	assert.Equal(t, slashExit, handleSlashCommand("/exit", state))
}

func TestSlashUnknown_NoLLMCall(t *testing.T) {
	state, buf := newTestState()
	action := handleSlashCommand("/foobar", state)

	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "Unknown command")
	assert.Contains(t, buf.String(), "/foobar")
}

func TestNonSlash_ReturnsNone(t *testing.T) {
	state, _ := newTestState()

	assert.Equal(t, slashNone, handleSlashCommand("hello world", state))
	assert.Equal(t, slashNone, handleSlashCommand("what is /help?", state))
}

func TestSlashCommands_NeverCallProvider(t *testing.T) {
	// This test uses panicProvider — if any slash command reaches the LLM,
	// the test panics instead of failing gracefully.
	state, _ := newTestState()

	commands := []string{"/help", "/tools", "/cost", "/clear", "/quit", "/exit", "/unknown"}
	for _, cmd := range commands {
		t.Run(cmd, func(t *testing.T) {
			require.NotPanics(t, func() {
				handleSlashCommand(cmd, state)
			}, "slash command %q should not call the LLM provider", cmd)
		})
	}
}

func TestFormatToolArgs(t *testing.T) {
	tests := []struct {
		name     string
		tc       llm.ToolCall
		expected string
	}{
		{
			"read_file",
			llm.ToolCall{Name: "read_file", Arguments: `{"path":"src/main.go"}`},
			"src/main.go",
		},
		{
			"grep_files",
			llm.ToolCall{Name: "grep_files", Arguments: `{"pattern":"func main"}`},
			"func main",
		},
		{
			"execute_command short",
			llm.ToolCall{Name: "execute_command", Arguments: `{"command":"go test ./..."}`},
			"go test ./...",
		},
		{
			"execute_command long",
			llm.ToolCall{Name: "execute_command", Arguments: fmt.Sprintf(`{"command":"%s"}`, string(make([]byte, 100)))},
			"", // 100 null bytes, truncated
		},
		{
			"invalid json",
			llm.ToolCall{Name: "read_file", Arguments: `not json`},
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatToolArgs(tt.tc)
			if tt.expected != "" {
				assert.Contains(t, result, tt.expected)
			}
		})
	}
}
