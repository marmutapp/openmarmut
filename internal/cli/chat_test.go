package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/gajaai/openmarmut-go/internal/agent"
	"github.com/gajaai/openmarmut-go/internal/llm"
	"github.com/gajaai/openmarmut-go/internal/runtime"
	"github.com/gajaai/openmarmut-go/internal/ui"
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
	pc := agent.NewPermissionChecker(agent.DefaultPermissions(), nil)
	var buf bytes.Buffer
	state := &chatState{
		ag:          ag,
		permChecker: pc,
		model:       "gpt-4o",
		out:         &buf,
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

func TestSlashTools_ShowsPermissions(t *testing.T) {
	state, buf := newTestState()
	handleSlashCommand("/tools", state)

	output := buf.String()
	// read_file should be auto, write_file should be confirm.
	assert.Contains(t, output, "auto")
	assert.Contains(t, output, "confirm")
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

func TestSlashCost_ShowsBox(t *testing.T) {
	state, buf := newTestState()
	state.sessionUsage = llm.Usage{PromptTokens: 200, CompletionTokens: 80, TotalTokens: 280}
	handleSlashCommand("/cost", state)

	output := buf.String()
	assert.Contains(t, output, "Session Cost")
	assert.Contains(t, output, "Prompt tokens")
	assert.Contains(t, output, "Completion tokens")
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

	commands := []string{"/help", "/tools", "/cost", "/context", "/clear", "/quit", "/exit", "/unknown"}
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

func TestRenderHelpBox(t *testing.T) {
	var buf bytes.Buffer
	renderHelpBox(&buf)

	output := buf.String()
	assert.Contains(t, output, "Commands")
	assert.Contains(t, output, "/clear")
	assert.Contains(t, output, "Reset conversation history")
}

func TestRenderToolsTable_WithPermChecker(t *testing.T) {
	state, buf := newTestState()
	// Override one permission.
	state.permChecker.SetPermission("write_file", agent.PermDeny)

	renderToolsTable(state)
	output := buf.String()
	assert.Contains(t, output, "write_file")
	assert.Contains(t, output, "deny")
}

func TestRenderCostBox(t *testing.T) {
	state, buf := newTestState()
	state.sessionUsage = llm.Usage{
		PromptTokens:     500,
		CompletionTokens: 200,
		TotalTokens:      700,
	}

	renderCostBox(state)
	output := buf.String()
	assert.Contains(t, output, "500")
	assert.Contains(t, output, "200")
	assert.Contains(t, output, "700")
	assert.Contains(t, output, "Session Cost")
}

func TestWelcomeBanner(t *testing.T) {
	result := ui.RenderWelcomeBanner("azure-codex", "gpt-5.1", "/tmp/project", "local")
	assert.Contains(t, result, "azure-codex")
	assert.Contains(t, result, "gpt-5.1")
	assert.Contains(t, result, "/tmp/project")
	assert.Contains(t, result, "local")
}

func TestConfirmBox(t *testing.T) {
	result := ui.RenderConfirmBox("→ write_file(main.go)")
	assert.Contains(t, result, "Permission Required")
	assert.Contains(t, result, "write_file")
	assert.Contains(t, result, "[y]es")
}

func TestInteractiveConfirm_WaitsForInput(t *testing.T) {
	// Simulate stdin with pre-loaded input via a pipe.
	tests := []struct {
		name     string
		input    string
		expected agent.ConfirmResult
	}{
		{"yes", "y\n", agent.ConfirmYes},
		{"YES uppercase", "YES\n", agent.ConfirmYes},
		{"no", "n\n", agent.ConfirmNo},
		{"always", "always\n", agent.ConfirmAlways},
		{"a shorthand", "a\n", agent.ConfirmAlways},
		{"empty then EOF denies", "\n", agent.ConfirmNo},
		{"garbage denies", "maybe\n", agent.ConfirmNo},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := strings.NewReader(tt.input)
			scanner := bufio.NewScanner(r)

			state := &chatState{
				scanner: scanner,
				out:     io.Discard,
			}

			confirmFn := interactiveConfirm(state)

			tc := llm.ToolCall{
				ID:        "call_1",
				Name:      "write_file",
				Arguments: `{"path":"test.go","content":"package main"}`,
			}

			result := confirmFn(tc, "→ write_file(test.go)")
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestInteractiveConfirm_SkipsStaleNewlines(t *testing.T) {
	// Simulates stale newlines in the scanner buffer before the real input.
	// Without the empty-line-skip loop, the first empty line would hit the
	// default case and return ConfirmNo, ignoring the user's actual "a" input.
	tests := []struct {
		name     string
		input    string
		expected agent.ConfirmResult
	}{
		{"one stale newline then a", "\na\n", agent.ConfirmAlways},
		{"two stale newlines then yes", "\n\ny\n", agent.ConfirmYes},
		{"three stale newlines then always", "\n\n\nalways\n", agent.ConfirmAlways},
		{"stale newline then no", "\nn\n", agent.ConfirmNo},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := strings.NewReader(tt.input)
			scanner := bufio.NewScanner(r)

			state := &chatState{
				scanner: scanner,
				out:     io.Discard,
			}

			confirmFn := interactiveConfirm(state)

			tc := llm.ToolCall{
				ID:        "call_1",
				Name:      "write_file",
				Arguments: `{"path":"test.go","content":"package main"}`,
			}

			result := confirmFn(tc, "→ write_file(test.go)")
			assert.Equal(t, tt.expected, result,
				"input %q should return %v after skipping stale newlines", tt.input, tt.expected)
		})
	}
}

func TestInteractiveConfirm_StopsSpinner(t *testing.T) {
	r := strings.NewReader("y\n")
	scanner := bufio.NewScanner(r)

	// Create a spinner writing to a buffer (won't actually animate since not a TTY).
	var spinBuf bytes.Buffer
	spinner := ui.NewSpinner(&spinBuf, "Thinking...")

	state := &chatState{
		scanner: scanner,
		spinner: spinner,
		out:     io.Discard,
	}

	confirmFn := interactiveConfirm(state)

	tc := llm.ToolCall{
		ID:        "call_1",
		Name:      "execute_command",
		Arguments: `{"command":"rm -rf /"}`,
	}

	result := confirmFn(tc, "→ execute_command\n  $ rm -rf /")
	assert.Equal(t, agent.ConfirmYes, result)

	// After confirm, the original spinner should have been stopped and replaced.
	// state.spinner should be a NEW spinner (not the original).
	assert.NotEqual(t, spinner, state.spinner, "spinner should be replaced after confirm")
}

func TestSlashContext_NoLLMCall(t *testing.T) {
	state, buf := newTestState()
	action := handleSlashCommand("/context", state)

	assert.Equal(t, slashHandled, action)
	output := buf.String()
	assert.Contains(t, output, "Context Window")
	assert.Contains(t, output, "Model window")
	assert.Contains(t, output, "Current usage")
	assert.Contains(t, output, "Threshold")
}

func TestSlashContext_ShowsProgressBar(t *testing.T) {
	state, buf := newTestState()
	handleSlashCommand("/context", state)

	output := buf.String()
	// Progress bar uses block characters.
	assert.Contains(t, output, "░")
	assert.Contains(t, output, "%")
}

func TestSlashHelp_IncludesContext(t *testing.T) {
	state, buf := newTestState()
	handleSlashCommand("/help", state)
	assert.Contains(t, buf.String(), "/context")
}

func TestHumanizeInt(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1,000"},
		{128000, "128,000"},
		{1000000, "1,000,000"},
	}
	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, humanizeInt(tt.input))
		})
	}
}

func TestInteractiveConfirm_EOF_Denies(t *testing.T) {
	// Empty reader simulates stdin EOF (e.g. Ctrl+D).
	r := strings.NewReader("")
	scanner := bufio.NewScanner(r)

	state := &chatState{
		scanner: scanner,
		out:     io.Discard,
	}

	confirmFn := interactiveConfirm(state)

	tc := llm.ToolCall{
		ID:        "call_1",
		Name:      "write_file",
		Arguments: `{"path":"x.go","content":""}`,
	}

	result := confirmFn(tc, "→ write_file(x.go)")
	assert.Equal(t, agent.ConfirmNo, result, "EOF should deny")
}
