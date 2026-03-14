package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gajaai/openmarmut-go/internal/agent"
	"github.com/gajaai/openmarmut-go/internal/config"
	"github.com/gajaai/openmarmut-go/internal/llm"
	"github.com/gajaai/openmarmut-go/internal/logger"
	"github.com/gajaai/openmarmut-go/internal/ui"
	"github.com/spf13/cobra"
)

// slashAction is the result of processing a slash command.
type slashAction int

const (
	slashNone    slashAction = iota // Not a slash command — send to agent.
	slashHandled                    // Slash command handled locally, continue loop.
	slashExit                       // /quit or /exit — stop the loop.
)

// chatState holds mutable state for the chat REPL, used by handleSlashCommand.
type chatState struct {
	ag          *agent.Agent
	permChecker *agent.PermissionChecker
	sessionUsage llm.Usage
	model        string
	out          io.Writer // Where to write command output (stderr in production).
	scanner      *bufio.Scanner // stdin scanner, shared between main loop and confirm prompt.
	spinner      *ui.Spinner    // current spinner, set before each ag.Run call; nil when idle.
}

// handleSlashCommand processes slash commands locally without calling the LLM.
// Returns the action to take (none/handled/exit).
func handleSlashCommand(line string, state *chatState) slashAction {
	if !strings.HasPrefix(line, "/") {
		return slashNone
	}

	switch line {
	case "/quit", "/exit":
		return slashExit
	case "/clear":
		state.ag.ClearHistory()
		state.sessionUsage = llm.Usage{}
		fmt.Fprintln(state.out, ui.FormatSuccess("History cleared."))
		return slashHandled
	case "/tools":
		renderToolsTable(state)
		return slashHandled
	case "/cost":
		renderCostBox(state)
		return slashHandled
	case "/help":
		renderHelpBox(state.out)
		return slashHandled
	default:
		fmt.Fprintf(state.out, "%s (type /help for commands)\n",
			ui.FormatWarning("Unknown command: "+line))
		return slashHandled
	}
}

// renderHelpBox displays available commands in a styled box.
func renderHelpBox(w io.Writer) {
	commands := [][]string{
		{"/clear", "Reset conversation history"},
		{"/tools", "List available tools"},
		{"/cost", "Show accumulated session cost"},
		{"/help", "Show this help"},
		{"/quit", "Exit chat"},
	}
	headers := []string{"COMMAND", "DESCRIPTION"}
	fmt.Fprintln(w, ui.RenderBox("Commands", ui.RenderTable(headers, commands, -1)))
}

// renderToolsTable displays tools with their permission levels.
func renderToolsTable(state *chatState) {
	headers := []string{"TOOL", "PERMISSION", "DESCRIPTION"}
	var rows [][]string

	var perms map[string]agent.PermissionLevel
	if state.permChecker != nil {
		perms = state.permChecker.Permissions()
	} else {
		perms = agent.DefaultPermissions()
	}

	for _, t := range state.ag.Tools() {
		level := perms[t.Def.Name]
		levelStr := level.String()
		if ui.ColorEnabled() {
			switch level {
			case agent.PermAuto:
				levelStr = ui.SuccessStyle.Render(levelStr)
			case agent.PermConfirm:
				levelStr = ui.WarningStyle.Render(levelStr)
			case agent.PermDeny:
				levelStr = ui.ErrorStyle.Render(levelStr)
			}
		}
		rows = append(rows, []string{t.Def.Name, levelStr, t.Def.Description})
	}
	fmt.Fprintln(state.out, ui.RenderTable(headers, rows, -1))
}

// renderCostBox displays accumulated session cost in a styled box.
func renderCostBox(state *chatState) {
	costStr := llm.FormatCost(state.sessionUsage, state.model)
	if costStr == "" {
		costStr = "unknown (no pricing for model)"
	}
	content := fmt.Sprintf("Prompt tokens:     %d\nCompletion tokens: %d\nTotal tokens:      %d\nEstimated cost:    ~%s",
		state.sessionUsage.PromptTokens,
		state.sessionUsage.CompletionTokens,
		state.sessionUsage.TotalTokens,
		costStr,
	)
	fmt.Fprintln(state.out, ui.RenderBox("Session Cost", content))
}

// interactiveConfirm creates a ConfirmFunc that prompts the user on stderr/stdin
// using a styled confirmation box.
// It stops the active spinner before prompting and restarts it after input.
func interactiveConfirm(state *chatState) agent.ConfirmFunc {
	return func(tc llm.ToolCall, preview string) agent.ConfirmResult {
		// Stop the spinner so it doesn't overwrite the prompt.
		if state.spinner != nil {
			state.spinner.Stop()
			state.spinner = nil
		}

		fmt.Fprint(os.Stderr, ui.RenderConfirmBox(preview))
		fmt.Fprint(os.Stderr, "\n[y]es / [n]o / [a]lways: ")

		if !state.scanner.Scan() {
			return agent.ConfirmNo
		}
		answer := strings.TrimSpace(strings.ToLower(state.scanner.Text()))

		// Restart spinner — agent continues working after user responds.
		state.spinner = ui.NewSpinner(os.Stderr, "Working...")
		state.spinner.Start()

		switch answer {
		case "y", "yes":
			return agent.ConfirmYes
		case "always", "a":
			return agent.ConfirmAlways
		default:
			return agent.ConfirmNo
		}
	}
}

func newChatCmd(runner *Runner) *cobra.Command {
	return &cobra.Command{
		Use:   "chat",
		Short: "Interactive chat with the AI (multi-turn with tools)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(runner.flags)
			if err != nil {
				return fmt.Errorf("chat: %w", err)
			}

			log := logger.New(cfg.Log)

			entry, err := cfg.LLM.ResolveActiveProvider()
			if err != nil {
				return fmt.Errorf("chat: %w", err)
			}

			rawProvider, err := llm.NewProvider(*entry, log)
			if err != nil {
				return fmt.Errorf("chat: %w", err)
			}
			provider := llm.NewRetryProvider(rawProvider, llm.RetryConfig{}, log)

			rt, err := initRuntime(cmd.Context(), cfg, log)
			if err != nil {
				return fmt.Errorf("chat: %w", err)
			}
			defer rt.Close(cmd.Context())

			scanner := bufio.NewScanner(os.Stdin)

			// Build state early so interactiveConfirm can reference it.
			state := &chatState{
				scanner: scanner,
				model:   provider.Model(),
				out:     os.Stderr,
			}

			// Build permission checker.
			perms := agent.BuildPermissions(cfg.Agent.AutoAllow, cfg.Agent.Confirm)
			var confirmFn agent.ConfirmFunc
			if !runner.flags.AutoApprove {
				confirmFn = interactiveConfirm(state)
			}
			pc := agent.NewPermissionChecker(perms, confirmFn)

			var opts []agent.Option
			temp := resolveTemperature(cfg, entry)
			if temp != nil {
				opts = append(opts, agent.WithTemperature(temp))
			}
			maxTok := resolveMaxTokens(cfg, entry)
			if maxTok != nil {
				opts = append(opts, agent.WithMaxTokens(maxTok))
			}

			if entry.ContextWindow > 0 {
				opts = append(opts, agent.WithContextConfig(agent.ContextConfig{
					ContextWindow:   entry.ContextWindow,
					TruncationRatio: 0.80,
				}))
			}

			// Show tool calls inline using styled FormatToolCall.
			opts = append(opts, agent.WithToolCallCallback(func(tc llm.ToolCall) {
				argSummary := formatToolArgs(tc)
				fmt.Fprintln(os.Stderr, ui.FormatToolCall(tc.Name, argSummary))
			}))

			opts = append(opts, agent.WithPermissionChecker(pc))

			ag := agent.New(provider, rt, log, opts...)

			state.ag = ag
			state.permChecker = pc

			// Welcome banner.
			fmt.Fprintln(os.Stderr, ui.RenderWelcomeBanner(
				provider.Name(), provider.Model(),
				cfg.TargetDir, cfg.Mode,
			))
			fmt.Fprintln(os.Stderr)

			for {
				if ui.ColorEnabled() {
					fmt.Fprint(os.Stderr, ui.UserPromptStyle.Render("you> "))
				} else {
					fmt.Fprint(os.Stderr, "you> ")
				}
				if !state.scanner.Scan() {
					break
				}

				line := strings.TrimSpace(state.scanner.Text())
				if line == "" {
					continue
				}

				// Handle slash commands locally — no LLM call.
				action := handleSlashCommand(line, state)
				switch action {
				case slashExit:
					return nil
				case slashHandled:
					continue
				}

				// Show spinner while waiting for first token.
				state.spinner = ui.NewSpinner(os.Stderr, "Thinking...")
				state.spinner.Start()
				firstToken := true

				// Stream tokens as they arrive, rendering markdown at the end.
				var responseBuf strings.Builder
				streamCB := func(text string) error {
					if firstToken {
						if state.spinner != nil {
							state.spinner.Stop()
							state.spinner = nil
						}
						firstToken = false
					}
					responseBuf.WriteString(text)
					_, writeErr := fmt.Fprint(os.Stdout, text)
					return writeErr
				}

				result, err := ag.Run(cmd.Context(), line, streamCB)
				// Ensure spinner is stopped on error or if no tokens were streamed.
				if state.spinner != nil {
					state.spinner.Stop()
					state.spinner = nil
				}
				if err != nil {
					fmt.Fprintf(os.Stderr, "\n%s\n", ui.FormatError(err.Error()))
					continue
				}

				fmt.Fprintln(os.Stdout)

				// Accumulate session usage.
				state.sessionUsage.PromptTokens += result.Usage.PromptTokens
				state.sessionUsage.CompletionTokens += result.Usage.CompletionTokens
				state.sessionUsage.TotalTokens += result.Usage.TotalTokens

				// Summary line.
				costStr := llm.FormatCost(result.Usage, provider.Model())
				summary := ui.FormatSummary(
					len(result.Steps),
					result.Usage.PromptTokens,
					result.Usage.CompletionTokens,
					costStr,
					result.Duration,
				)
				fmt.Fprintln(os.Stderr, summary)
				fmt.Fprintln(os.Stderr)
			}

			if err := state.scanner.Err(); err != nil {
				return fmt.Errorf("chat: %w", err)
			}

			return nil
		},
	}
}

// formatToolArgs extracts a short summary of tool call arguments for display.
func formatToolArgs(tc llm.ToolCall) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
		return ""
	}

	// Show the most relevant argument for common tools.
	switch tc.Name {
	case "read_file", "delete_file", "read_file_lines":
		if p, ok := args["path"]; ok {
			return fmt.Sprintf("%v", p)
		}
	case "write_file", "patch_file":
		if p, ok := args["path"]; ok {
			return fmt.Sprintf("%v", p)
		}
	case "list_dir", "mkdir":
		if p, ok := args["path"]; ok {
			return fmt.Sprintf("%v", p)
		}
	case "execute_command":
		if c, ok := args["command"]; ok {
			s := fmt.Sprintf("%v", c)
			if len(s) > 60 {
				s = s[:60] + "..."
			}
			return s
		}
	case "grep_files":
		if p, ok := args["pattern"]; ok {
			return fmt.Sprintf("%v", p)
		}
	case "find_files":
		if p, ok := args["pattern"]; ok {
			return fmt.Sprintf("%v", p)
		}
	}

	// Fallback: show first string value.
	for _, v := range args {
		if s, ok := v.(string); ok {
			if len(s) > 60 {
				s = s[:60] + "..."
			}
			return s
		}
	}
	return ""
}
