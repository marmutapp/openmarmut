package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gajaai/openmarmut-go/internal/agent"
	"github.com/gajaai/openmarmut-go/internal/config"
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
	rt := &stubRuntime{}
	ag := agent.New(&panicProvider{}, rt, testLogger)
	pc := agent.NewPermissionChecker(agent.DefaultPermissions(), nil)
	var buf bytes.Buffer
	state := &chatState{
		ag:          ag,
		permChecker: pc,
		model:       "gpt-4o",
		out:         &buf,
		rt:          rt,
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

	commands := []string{"/help", "/tools", "/cost", "/context", "/clear", "/quit", "/exit", "/sessions", "/rewind --list", "/diff", "/plan", "/plan on", "/plan off", "/agents", "/unknown"}
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

func TestSlashRewindList_NoCheckpoints(t *testing.T) {
	state, buf := newTestState()
	action := handleSlashCommand("/rewind --list", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "not enabled")
}

func TestSlashRewind_NoCheckpointStore(t *testing.T) {
	state, buf := newTestState()
	action := handleSlashCommand("/rewind", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "not enabled")
}

func TestSlashDiff_NotGitRepo(t *testing.T) {
	state, buf := newTestState()
	state.isGitRepo = false
	action := handleSlashCommand("/diff", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "Not a git repository")
}

func TestSlashCommit_NotGitRepo(t *testing.T) {
	state, buf := newTestState()
	state.isGitRepo = false
	action := handleSlashCommand("/commit", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "Not a git repository")
}

func TestSlashHelp_IncludesGitCommands(t *testing.T) {
	state, buf := newTestState()
	handleSlashCommand("/help", state)
	output := buf.String()
	assert.Contains(t, output, "/diff")
	assert.Contains(t, output, "/commit")
	assert.Contains(t, output, "/rewind")
}

func TestHasFileChanges(t *testing.T) {
	tests := []struct {
		name     string
		result   *agent.Result
		expected bool
	}{
		{
			"no steps",
			&agent.Result{},
			false,
		},
		{
			"read only",
			&agent.Result{Steps: []agent.Step{
				{ToolCall: llm.ToolCall{Name: "read_file"}},
			}},
			false,
		},
		{
			"write file",
			&agent.Result{Steps: []agent.Step{
				{ToolCall: llm.ToolCall{Name: "write_file"}, Output: "ok"},
			}},
			true,
		},
		{
			"write with error",
			&agent.Result{Steps: []agent.Step{
				{ToolCall: llm.ToolCall{Name: "write_file"}, Error: "failed"},
			}},
			false,
		},
		{
			"patch file",
			&agent.Result{Steps: []agent.Step{
				{ToolCall: llm.ToolCall{Name: "patch_file"}, Output: "ok"},
			}},
			true,
		},
		{
			"delete file",
			&agent.Result{Steps: []agent.Step{
				{ToolCall: llm.ToolCall{Name: "delete_file"}, Output: "ok"},
			}},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, hasFileChanges(tt.result))
		})
	}
}

func TestFormatToolArgs_GitTools(t *testing.T) {
	tests := []struct {
		name     string
		tc       llm.ToolCall
		expected string
	}{
		{
			"git_diff with path",
			llm.ToolCall{Name: "git_diff", Arguments: `{"path":"main.go"}`},
			"main.go",
		},
		{
			"git_commit",
			llm.ToolCall{Name: "git_commit", Arguments: `{"message":"feat: add feature"}`},
			"feat: add feature",
		},
		{
			"git_branch create",
			llm.ToolCall{Name: "git_branch", Arguments: `{"name":"feature"}`},
			"create: feature",
		},
		{
			"git_checkout",
			llm.ToolCall{Name: "git_checkout", Arguments: `{"branch":"main"}`},
			"main",
		},
		{
			"git_log with n",
			llm.ToolCall{Name: "git_log", Arguments: `{"n":5}`},
			"n=5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatToolArgs(tt.tc)
			assert.Contains(t, result, tt.expected)
		})
	}
}

func TestShellQuoteCLI(t *testing.T) {
	assert.Equal(t, "'hello'", shellQuoteCLI("hello"))
	assert.Equal(t, "'it'\\''s'", shellQuoteCLI("it's"))
}

// --- Plan Mode Tests ---

func TestSlashPlan_Toggle(t *testing.T) {
	state, buf := newTestState()

	// Initially off.
	assert.False(t, state.planMode)

	// Toggle on.
	action := handleSlashCommand("/plan", state)
	assert.Equal(t, slashHandled, action)
	assert.True(t, state.planMode)
	assert.Contains(t, buf.String(), "Plan mode ON")

	// Toggle off.
	buf.Reset()
	action = handleSlashCommand("/plan", state)
	assert.Equal(t, slashHandled, action)
	assert.False(t, state.planMode)
	assert.Contains(t, buf.String(), "Plan mode OFF")
}

func TestSlashPlan_OnOff(t *testing.T) {
	state, buf := newTestState()

	handleSlashCommand("/plan on", state)
	assert.True(t, state.planMode)
	assert.Contains(t, buf.String(), "ON")

	buf.Reset()
	handleSlashCommand("/plan off", state)
	assert.False(t, state.planMode)
	assert.Contains(t, buf.String(), "OFF")
}

func TestSlashPlan_NeverCallsProvider(t *testing.T) {
	state, _ := newTestState()
	require.NotPanics(t, func() {
		handleSlashCommand("/plan", state)
	}, "/plan toggle should not call the LLM provider")

	require.NotPanics(t, func() {
		handleSlashCommand("/plan on", state)
	})

	require.NotPanics(t, func() {
		handleSlashCommand("/plan off", state)
	})
}

func TestSlashHelp_IncludesPlan(t *testing.T) {
	state, buf := newTestState()
	handleSlashCommand("/help", state)
	assert.Contains(t, buf.String(), "/plan")
}

func TestSlashCommands_NeverCallProvider_IncludesPlan(t *testing.T) {
	state, _ := newTestState()
	// Plan toggle commands should never reach the LLM.
	planCommands := []string{"/plan", "/plan on", "/plan off"}
	for _, cmd := range planCommands {
		t.Run(cmd, func(t *testing.T) {
			require.NotPanics(t, func() {
				handleSlashCommand(cmd, state)
			}, "command %q should not call the LLM", cmd)
		})
	}
}

func TestSlashHelp_IncludesCompact(t *testing.T) {
	state, buf := newTestState()
	handleSlashCommand("/help", state)
	assert.Contains(t, buf.String(), "/compact")
}

// compactProvider returns a summary when Complete is called (used for /compact tests).
type compactProvider struct {
	summary string
}

func (p *compactProvider) Name() string  { return "compact-test" }
func (p *compactProvider) Model() string { return "compact-model" }
func (p *compactProvider) Complete(_ context.Context, _ llm.Request, _ llm.StreamCallback) (*llm.Response, error) {
	return &llm.Response{Content: p.summary}, nil
}

func TestSlashCompact_ReducesHistory(t *testing.T) {
	rt := &stubRuntime{}
	provider := &compactProvider{summary: "Summary: user asked about auth."}
	ag := agent.New(provider, rt, testLogger)
	pc := agent.NewPermissionChecker(agent.DefaultPermissions(), nil)

	// Seed history with enough messages.
	ag.SetHistory([]llm.Message{
		{Role: llm.RoleSystem, Content: "System prompt."},
		{Role: llm.RoleUser, Content: "Refactor auth module."},
		{Role: llm.RoleAssistant, Content: "Working on it..."},
		{Role: llm.RoleUser, Content: "Also update tests."},
		{Role: llm.RoleAssistant, Content: "Done with all changes."},
	})

	var buf bytes.Buffer
	state := &chatState{
		ag:          ag,
		permChecker: pc,
		model:       "test-model",
		out:         &buf,
		rt:          rt,
	}

	action := handleSlashCommand("/compact", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "Compacted")
	assert.Contains(t, buf.String(), "reduction")

	// History should be reduced to 3 messages.
	assert.Len(t, ag.History(), 3)
}

func TestSlashCompact_WithCustomInstruction(t *testing.T) {
	rt := &stubRuntime{}
	provider := &compactProvider{summary: "Summary focused on auth."}
	ag := agent.New(provider, rt, testLogger)

	ag.SetHistory([]llm.Message{
		{Role: llm.RoleSystem, Content: "System."},
		{Role: llm.RoleUser, Content: "Q1."},
		{Role: llm.RoleAssistant, Content: "A1."},
		{Role: llm.RoleUser, Content: "Q2."},
	})

	var buf bytes.Buffer
	state := &chatState{
		ag:    ag,
		model: "test",
		out:   &buf,
		rt:    rt,
	}

	action := handleSlashCommand(`/compact "Preserve the auth changes"`, state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "Compacted")
}

func TestSlashCompact_TooFewMessages(t *testing.T) {
	state, buf := newTestState()

	// Default agent has only system prompt, too few to compact.
	action := handleSlashCommand("/compact", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "Nothing to compact")
}

func TestSlashThinking_Toggle(t *testing.T) {
	state, buf := newTestState()

	assert.False(t, state.ag.ExtendedThinking())

	action := handleSlashCommand("/thinking", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "ON")
	assert.True(t, state.ag.ExtendedThinking())

	buf.Reset()
	action = handleSlashCommand("/thinking", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "OFF")
	assert.False(t, state.ag.ExtendedThinking())
}

func TestSlashEffort_SetsLevel(t *testing.T) {
	tests := []struct {
		cmd    string
		output string
	}{
		{"/effort low", "low"},
		{"/effort medium", "medium"},
		{"/effort high", "high"},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			state, buf := newTestState()
			action := handleSlashCommand(tt.cmd, state)
			assert.Equal(t, slashHandled, action)
			assert.Contains(t, buf.String(), tt.output)
			assert.True(t, state.ag.ExtendedThinking(), "effort should enable thinking")
		})
	}
}

func TestSlashEffort_InvalidLevel(t *testing.T) {
	state, buf := newTestState()
	action := handleSlashCommand("/effort banana", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "Usage")
}

func TestSlashHelp_IncludesThinking(t *testing.T) {
	state, buf := newTestState()
	handleSlashCommand("/help", state)
	assert.Contains(t, buf.String(), "/thinking")
	assert.Contains(t, buf.String(), "/effort")
}

func TestLsIgnoreFiltering(t *testing.T) {
	// BUG 1 regression: ls command should filter entries via ignore list.
	il := agent.NewIgnoreListForTest([]string{".git/", "node_modules/", "*.pyc"})

	entries := []runtime.FileEntry{
		{Name: ".git", IsDir: true},
		{Name: "node_modules", IsDir: true},
		{Name: "src", IsDir: true},
		{Name: "main.go", IsDir: false},
		{Name: "cache.pyc", IsDir: false},
	}

	var filtered []runtime.FileEntry
	hidden := 0
	for _, e := range entries {
		if il.ShouldIgnoreEntry(e.Name, e.IsDir) {
			hidden++
			continue
		}
		filtered = append(filtered, e)
	}

	assert.Len(t, filtered, 2) // src + main.go
	assert.Equal(t, 3, hidden)  // .git + node_modules + cache.pyc
	assert.Equal(t, "src", filtered[0].Name)
	assert.Equal(t, "main.go", filtered[1].Name)
}

func TestRenderMemory_WithEmptyStore(t *testing.T) {
	// BUG 3 regression: memory store should be attached even with zero entries.
	rt := &stubRuntime{}
	memStore := agent.NewMemoryStoreAt(filepath.Join(t.TempDir(), "MEMORY.md"))
	ag := agent.New(&panicProvider{}, rt, testLogger, agent.WithMemoryStore(memStore))
	var buf bytes.Buffer
	state := &chatState{
		ag:         ag,
		out:        &buf,
		autoMemory: true,
	}
	renderMemory(state)
	// Should show "No memories stored yet" not "not enabled".
	assert.Contains(t, buf.String(), "No memories stored yet")
	assert.NotContains(t, buf.String(), "not enabled")
}

// --- Sub-agent slash command tests ---

func TestSlashAgents_EmptyList(t *testing.T) {
	state, buf := newTestState()
	action := handleSlashCommand("/agents", state)

	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "No sub-agents")
}

func TestSlashAgents_WithManager(t *testing.T) {
	state, buf := newTestState()
	state.subMgr = agent.NewSubAgentManager()
	state.subMgr.Track(&agent.SubAgent{
		Name:   "test-agent",
		Task:   "Find TODO comments",
		Status: "completed",
		Usage:  llm.Usage{TotalTokens: 150},
	})

	action := handleSlashCommand("/agents", state)

	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "test-agent")
	assert.Contains(t, buf.String(), "completed")
	assert.Contains(t, buf.String(), "Find TODO")
}

func TestSlashAgentsKill_NoManager(t *testing.T) {
	state, buf := newTestState()
	action := handleSlashCommand("/agents kill test", state)

	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "No sub-agents")
}

func TestSlashAgentsKill_NonExistent(t *testing.T) {
	state, buf := newTestState()
	state.subMgr = agent.NewSubAgentManager()
	action := handleSlashCommand("/agents kill nonexistent", state)

	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "No running sub-agent")
}

func TestSlashAgent_MissingTask(t *testing.T) {
	state, buf := newTestState()
	action := handleSlashCommand("/agent ", state)

	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "Usage")
}

func TestSlashHelp_IncludesAgent(t *testing.T) {
	state, buf := newTestState()
	handleSlashCommand("/help", state)
	output := buf.String()

	assert.Contains(t, output, "/agent")
	assert.Contains(t, output, "/agents")
	assert.Contains(t, output, "sub-agent")
}

func TestSlashCommands_NeverCallProvider_IncludesAgents(t *testing.T) {
	state, _ := newTestState()
	commands := []string{"/agents", "/agents kill test"}
	for _, cmd := range commands {
		t.Run(cmd, func(t *testing.T) {
			require.NotPanics(t, func() {
				handleSlashCommand(cmd, state)
			}, "slash command %q should not call the LLM provider", cmd)
		})
	}
}

// --- Custom Commands Tests ---

func TestSlashCommands_ListEmpty(t *testing.T) {
	state, buf := newTestState()
	action := handleSlashCommand("/commands", state)

	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "No custom commands")
}

func TestSlashCommands_ListWithCommands(t *testing.T) {
	state, buf := newTestState()
	state.customCommands = []agent.CustomCommand{
		{Name: "test", Description: "Run all tests"},
		{Name: "review", Description: "Code review"},
	}
	action := handleSlashCommand("/commands", state)

	assert.Equal(t, slashHandled, action)
	output := buf.String()
	assert.Contains(t, output, "/test")
	assert.Contains(t, output, "Run all tests")
	assert.Contains(t, output, "/review")
}

func TestTryCustomCommand_Found(t *testing.T) {
	state, buf := newTestState()
	state.customCommands = []agent.CustomCommand{
		{Name: "test", Description: "Run tests", Content: "Run go test ./..."},
	}

	action := tryCustomCommand("/test", state)

	assert.Equal(t, slashHandled, action)
	assert.Equal(t, "Run go test ./...", state.customCmdContent)
	assert.Contains(t, buf.String(), "Running custom command")
}

func TestTryCustomCommand_WithArgs(t *testing.T) {
	state, _ := newTestState()
	state.customCommands = []agent.CustomCommand{
		{Name: "test", Description: "Run tests", Content: "Run go test"},
	}

	tryCustomCommand("/test src/auth/", state)
	assert.Equal(t, "Run go test src/auth/", state.customCmdContent)
}

func TestTryCustomCommand_NotFound(t *testing.T) {
	state, _ := newTestState()
	state.customCommands = []agent.CustomCommand{
		{Name: "test", Description: "Run tests", Content: "content"},
	}

	action := tryCustomCommand("/unknown", state)
	assert.Equal(t, slashNone, action)
	assert.Empty(t, state.customCmdContent)
}

func TestTryCustomCommand_NoCommands(t *testing.T) {
	state, _ := newTestState()
	action := tryCustomCommand("/test", state)
	assert.Equal(t, slashNone, action)
}

// --- /btw Tests ---

// btwProvider returns a fixed response for /btw tests.
type btwProvider struct {
	response string
}

func (p *btwProvider) Name() string  { return "btw-test" }
func (p *btwProvider) Model() string { return "btw-model" }
func (p *btwProvider) Complete(_ context.Context, _ llm.Request, _ llm.StreamCallback) (*llm.Response, error) {
	return &llm.Response{Content: p.response, Usage: llm.Usage{TotalTokens: 50}}, nil
}

func TestSlashBtw_ShowsResponse(t *testing.T) {
	rt := &stubRuntime{}
	provider := &btwProvider{response: "Use fmt.Errorf with %w."}
	ag := agent.New(&panicProvider{}, rt, testLogger)
	var buf bytes.Buffer
	state := &chatState{
		ag:       ag,
		provider: provider,
		model:    "test-model",
		out:      &buf,
		rt:       rt,
	}

	handleBtw("/btw What is error wrapping?", state)

	output := buf.String()
	assert.Contains(t, output, "btw")
	assert.Contains(t, output, "fmt.Errorf")
}

func TestSlashBtw_EmptyQuestion(t *testing.T) {
	state, buf := newTestState()
	handleBtw("/btw ", state)
	assert.Contains(t, buf.String(), "Usage")
}

func TestSlashBtw_NoProvider(t *testing.T) {
	state, buf := newTestState()
	state.provider = nil
	handleBtw("/btw test question", state)
	assert.Contains(t, buf.String(), "No LLM provider")
}

func TestSlashBtw_NeverPollutesHistory(t *testing.T) {
	rt := &stubRuntime{}
	provider := &btwProvider{response: "answer"}
	ag := agent.New(&panicProvider{}, rt, testLogger)
	historyBefore := len(ag.History())
	var buf bytes.Buffer
	state := &chatState{
		ag:       ag,
		provider: provider,
		model:    "test",
		out:      &buf,
		rt:       rt,
	}

	handleBtw("/btw test", state)
	assert.Equal(t, historyBefore, len(ag.History()), "btw should not modify main history")
}

// --- /loop Tests ---

func TestSlashLoop_EmptyArgs(t *testing.T) {
	state, buf := newTestState()
	handleLoop("/loop", state)
	assert.Contains(t, buf.String(), "Usage")
}

func TestSlashLoop_InvalidInterval(t *testing.T) {
	state, buf := newTestState()
	handleLoop("/loop banana go test", state)
	assert.Contains(t, buf.String(), "Invalid interval")
}

func TestSlashLoop_TooShortInterval(t *testing.T) {
	state, buf := newTestState()
	handleLoop("/loop 100ms go test", state)
	assert.Contains(t, buf.String(), "at least 1s")
}

func TestSlashLoop_MissingCommand(t *testing.T) {
	state, buf := newTestState()
	handleLoop("/loop 5m", state)
	assert.Contains(t, buf.String(), "Usage")
}

func TestSlashLoop_Start(t *testing.T) {
	state, buf := newTestState()
	handleLoop("/loop 5m go test ./...", state)

	output := buf.String()
	assert.Contains(t, output, "Loop #1 started")
	assert.Contains(t, output, "go test ./...")
	assert.NotNil(t, state.loopMgr)

	// Clean up.
	state.loopMgr.StopAll()
}

func TestSlashLoop_StatusEmpty(t *testing.T) {
	state, buf := newTestState()
	handleLoop("/loop status", state)
	assert.Contains(t, buf.String(), "No active loops")
}

func TestSlashLoop_StatusWithEntry(t *testing.T) {
	state, buf := newTestState()
	handleLoop("/loop 5m go test", state)

	buf.Reset()
	handleLoop("/loop status", state)
	output := buf.String()
	assert.Contains(t, output, "go test")
	assert.Contains(t, output, "5m0s")

	state.loopMgr.StopAll()
}

func TestSlashLoop_Off(t *testing.T) {
	state, buf := newTestState()
	handleLoop("/loop 5m go test", state)

	buf.Reset()
	handleLoop("/loop off", state)
	assert.Contains(t, buf.String(), "Stopped 1 loop")

	// After stopping, loops list is empty.
	buf.Reset()
	handleLoop("/loop status", state)
	assert.Contains(t, buf.String(), "No active loops")
}

func TestSlashLoop_MultipleLoops(t *testing.T) {
	state, _ := newTestState()
	handleLoop("/loop 5m go test", state)
	handleLoop("/loop 10s curl localhost", state)

	loops := state.loopMgr.Status()
	assert.Len(t, loops, 2)

	count := state.loopMgr.StopAll()
	assert.Equal(t, 2, count)
}

// --- Help includes new commands ---

func TestSlashHelp_IncludesNewCommands(t *testing.T) {
	state, buf := newTestState()
	handleSlashCommand("/help", state)
	output := buf.String()
	assert.Contains(t, output, "/btw")
	assert.Contains(t, output, "/loop")
	assert.Contains(t, output, "/commands")
}

func TestSlashCommands_NeverCallProvider_IncludesNewCommands(t *testing.T) {
	state, _ := newTestState()
	newCommands := []string{"/commands", "/loop status", "/loop off"}
	for _, cmd := range newCommands {
		t.Run(cmd, func(t *testing.T) {
			require.NotPanics(t, func() {
				handleSlashCommand(cmd, state)
			}, "slash command %q should not call the LLM provider", cmd)
		})
	}
}

// --- Phase 12.4: Task Tracking Tests ---

func TestSlashTasks_Empty(t *testing.T) {
	state, buf := newTestState()
	tl := agent.NewTaskListAt(filepath.Join(t.TempDir(), "tasks.json"))
	state.taskList = tl

	action := handleSlashCommand("/tasks", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "No tasks")
}

func TestSlashTasks_Add(t *testing.T) {
	state, buf := newTestState()
	tl := agent.NewTaskListAt(filepath.Join(t.TempDir(), "tasks.json"))
	state.taskList = tl

	action := handleSlashCommand("/tasks add Write unit tests", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "Created task #1")

	// Verify task was actually created.
	tasks := tl.All()
	require.Len(t, tasks, 1)
	assert.Equal(t, "Write unit tests", tasks[0].Title)
}

func TestSlashTasks_Done(t *testing.T) {
	state, buf := newTestState()
	tl := agent.NewTaskListAt(filepath.Join(t.TempDir(), "tasks.json"))
	state.taskList = tl
	tl.Add("Complete me")

	action := handleSlashCommand("/tasks done 1", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "completed")
	assert.Equal(t, "completed", tl.Get(1).Status)
}

func TestSlashTasks_DoneInvalidID(t *testing.T) {
	state, buf := newTestState()
	tl := agent.NewTaskListAt(filepath.Join(t.TempDir(), "tasks.json"))
	state.taskList = tl

	action := handleSlashCommand("/tasks done 99", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "not found")
}

func TestSlashTasks_Clear(t *testing.T) {
	state, buf := newTestState()
	tl := agent.NewTaskListAt(filepath.Join(t.TempDir(), "tasks.json"))
	state.taskList = tl
	tl.Add("Done task")
	tl.Add("Pending task")
	tl.Update(1, "completed") //nolint:errcheck

	action := handleSlashCommand("/tasks clear", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "Cleared 1 completed")
	assert.Equal(t, 1, tl.Len())
}

func TestSlashTasks_ShowWithTasks(t *testing.T) {
	state, buf := newTestState()
	tl := agent.NewTaskListAt(filepath.Join(t.TempDir(), "tasks.json"))
	state.taskList = tl
	tl.Add("Task A")
	tl.Add("Task B")

	action := handleSlashCommand("/tasks", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "Task A")
	assert.Contains(t, buf.String(), "Task B")
}

func TestSlashTasks_NoTaskList(t *testing.T) {
	state, buf := newTestState()
	// state.taskList is nil

	action := handleSlashCommand("/tasks", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "No task list")
}

func TestSlashTasks_AddEmptyTitle(t *testing.T) {
	state, buf := newTestState()
	tl := agent.NewTaskListAt(filepath.Join(t.TempDir(), "tasks.json"))
	state.taskList = tl

	action := handleSlashCommand("/tasks add ", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "Usage")
}

// --- Phase 12.4: Background Execution Tests ---

func TestSlashBg_EmptyUsage(t *testing.T) {
	state, buf := newTestState()
	action := handleSlashCommand("/bg", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "Usage")
}

func TestSlashBg_StatusEmpty(t *testing.T) {
	state, buf := newTestState()
	action := handleSlashCommand("/bg status", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "No background jobs")
}

func TestSlashBg_CancelInvalidID(t *testing.T) {
	state, buf := newTestState()
	action := handleSlashCommand("/bg cancel 99", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "No background job")
}

// --- Phase 12.4: Model Switching Tests ---

func TestSlashModel_ShowCurrent(t *testing.T) {
	state, buf := newTestState()
	state.provider = &panicProvider{}
	state.model = "panic-model"

	action := handleSlashCommand("/model", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "panic-model")
	assert.Contains(t, buf.String(), "panic") // provider name
}

func TestSlashModel_SwitchNoConfig(t *testing.T) {
	state, buf := newTestState()
	state.provider = &panicProvider{}
	state.cfg = nil

	action := handleSlashCommand("/model gpt-4", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "No config")
}

func TestSlashProvider_EmptyUsage(t *testing.T) {
	state, buf := newTestState()
	action := handleSlashCommand("/provider", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "Usage")
}

func TestSlashProvider_NotFound(t *testing.T) {
	state, buf := newTestState()
	state.cfg = &config.Config{
		LLM: config.LLMConfig{
			Providers: []llm.ProviderEntry{
				{Name: "openai", Type: "openai", ModelName: "gpt-4"},
			},
		},
	}

	action := handleSlashCommand("/provider nonexistent", state)
	assert.Equal(t, slashHandled, action)
	assert.Contains(t, buf.String(), "not found")
}

// --- Phase 12.4: No-LLM Guarantee ---

func TestPhase12_4_SlashCommands_NoLLMCall(t *testing.T) {
	state, _ := newTestState()
	tl := agent.NewTaskListAt(filepath.Join(t.TempDir(), "tasks.json"))
	state.taskList = tl
	state.provider = &panicProvider{}

	commands := []string{
		"/tasks",
		"/tasks add test",
		"/tasks clear",
		"/bg status",
		"/model",
		"/provider",
	}
	for _, cmd := range commands {
		t.Run(cmd, func(t *testing.T) {
			require.NotPanics(t, func() {
				handleSlashCommand(cmd, state)
			}, "slash command %q should not call the LLM provider", cmd)
		})
	}
}
