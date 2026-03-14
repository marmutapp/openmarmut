package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gajaai/opencode-go/internal/agent"
	"github.com/gajaai/opencode-go/internal/config"
	"github.com/gajaai/opencode-go/internal/llm"
	"github.com/gajaai/opencode-go/internal/logger"
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
	ag           *agent.Agent
	sessionUsage llm.Usage
	model        string
	out          io.Writer // Where to write command output (stderr in production).
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
		fmt.Fprintln(state.out, "History cleared.")
		return slashHandled
	case "/tools":
		fmt.Fprintln(state.out, "Available tools:")
		for _, t := range state.ag.Tools() {
			fmt.Fprintf(state.out, "  %-20s %s\n", t.Def.Name, t.Def.Description)
		}
		return slashHandled
	case "/cost":
		costStr := llm.FormatCost(state.sessionUsage, state.model)
		if costStr == "" {
			costStr = "unknown (no pricing for model)"
		}
		fmt.Fprintf(state.out, "Session: %d prompt + %d completion = %d tokens | ~%s\n",
			state.sessionUsage.PromptTokens,
			state.sessionUsage.CompletionTokens,
			state.sessionUsage.TotalTokens,
			costStr,
		)
		return slashHandled
	case "/help":
		fmt.Fprintln(state.out, "Commands:")
		fmt.Fprintln(state.out, "  /clear    Reset conversation history")
		fmt.Fprintln(state.out, "  /tools    List available tools")
		fmt.Fprintln(state.out, "  /cost     Show accumulated session cost")
		fmt.Fprintln(state.out, "  /help     Show this help")
		fmt.Fprintln(state.out, "  /quit     Exit chat")
		return slashHandled
	default:
		fmt.Fprintf(state.out, "Unknown command: %s (type /help for commands)\n", line)
		return slashHandled
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

			// Show tool calls inline in dim text.
			opts = append(opts, agent.WithToolCallCallback(func(tc llm.ToolCall) {
				argSummary := formatToolArgs(tc)
				fmt.Fprintf(os.Stderr, "\033[2m→ %s(%s)\033[0m\n", tc.Name, argSummary)
			}))

			ag := agent.New(provider, rt, log, opts...)

			state := &chatState{
				ag:    ag,
				model: provider.Model(),
				out:   os.Stderr,
			}

			fmt.Fprintf(os.Stderr, "Chat with %s (%s). Type /help for commands, /quit to exit.\n\n",
				provider.Name(), provider.Model())

			scanner := bufio.NewScanner(os.Stdin)
			for {
				fmt.Fprint(os.Stderr, "you> ")
				if !scanner.Scan() {
					break
				}

				line := strings.TrimSpace(scanner.Text())
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

				// Stream tokens as they arrive.
				streamCB := func(text string) error {
					_, writeErr := fmt.Fprint(os.Stdout, text)
					return writeErr
				}

				result, err := ag.Run(cmd.Context(), line, streamCB)
				if err != nil {
					fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
					continue
				}

				fmt.Fprintln(os.Stdout)

				// Accumulate session usage.
				state.sessionUsage.PromptTokens += result.Usage.PromptTokens
				state.sessionUsage.CompletionTokens += result.Usage.CompletionTokens
				state.sessionUsage.TotalTokens += result.Usage.TotalTokens

				{
					costStr := llm.FormatCost(result.Usage, provider.Model())
					if costStr != "" {
						costStr = " | ~" + costStr
					}
					elapsed := fmt.Sprintf("%.1fs", result.Duration.Seconds())
					if len(result.Steps) > 0 {
						fmt.Fprintf(os.Stderr, "[%d tool calls | %d + %d = %d tokens%s | %s]\n",
							len(result.Steps),
							result.Usage.PromptTokens,
							result.Usage.CompletionTokens,
							result.Usage.TotalTokens,
							costStr,
							elapsed,
						)
					} else {
						fmt.Fprintf(os.Stderr, "[%d + %d = %d tokens%s | %s]\n",
							result.Usage.PromptTokens,
							result.Usage.CompletionTokens,
							result.Usage.TotalTokens,
							costStr,
							elapsed,
						)
					}
				}
				fmt.Fprintln(os.Stderr)
			}

			if err := scanner.Err(); err != nil {
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
