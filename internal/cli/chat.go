package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/gajaai/opencode-go/internal/agent"
	"github.com/gajaai/opencode-go/internal/config"
	"github.com/gajaai/opencode-go/internal/llm"
	"github.com/gajaai/opencode-go/internal/logger"
	"github.com/spf13/cobra"
)

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

			// Track cumulative session usage for /cost.
			var sessionUsage llm.Usage

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

				// Handle slash commands.
				if strings.HasPrefix(line, "/") {
					switch line {
					case "/quit", "/exit":
						return nil
					case "/clear":
						ag.ClearHistory()
						sessionUsage = llm.Usage{}
						fmt.Fprintln(os.Stderr, "History cleared.")
						continue
					case "/tools":
						fmt.Fprintln(os.Stderr, "Available tools:")
						for _, t := range ag.Tools() {
							fmt.Fprintf(os.Stderr, "  %-20s %s\n", t.Def.Name, t.Def.Description)
						}
						continue
					case "/cost":
						costStr := llm.FormatCost(sessionUsage, provider.Model())
						if costStr == "" {
							costStr = "unknown (no pricing for model)"
						}
						fmt.Fprintf(os.Stderr, "Session: %d prompt + %d completion = %d tokens | ~%s\n",
							sessionUsage.PromptTokens,
							sessionUsage.CompletionTokens,
							sessionUsage.TotalTokens,
							costStr,
						)
						continue
					case "/help":
						fmt.Fprintln(os.Stderr, "Commands:")
						fmt.Fprintln(os.Stderr, "  /clear    Reset conversation history")
						fmt.Fprintln(os.Stderr, "  /tools    List available tools")
						fmt.Fprintln(os.Stderr, "  /cost     Show accumulated session cost")
						fmt.Fprintln(os.Stderr, "  /help     Show this help")
						fmt.Fprintln(os.Stderr, "  /quit     Exit chat")
						continue
					default:
						fmt.Fprintf(os.Stderr, "Unknown command: %s (type /help for commands)\n", line)
						continue
					}
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
				sessionUsage.PromptTokens += result.Usage.PromptTokens
				sessionUsage.CompletionTokens += result.Usage.CompletionTokens
				sessionUsage.TotalTokens += result.Usage.TotalTokens

				if len(result.Steps) > 0 {
					costStr := llm.FormatCost(result.Usage, provider.Model())
					if costStr != "" {
						costStr = " | ~" + costStr
					}
					fmt.Fprintf(os.Stderr, "[%d tool calls | %d tokens%s]\n",
						len(result.Steps), result.Usage.TotalTokens, costStr)
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
