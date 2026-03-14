package cli

import (
	"bufio"
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

			ag := agent.New(provider, rt, log, opts...)

			fmt.Fprintf(os.Stderr, "Chat with %s (%s). Type /quit to exit.\n\n", provider.Name(), provider.Model())

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
				if line == "/quit" || line == "/exit" {
					break
				}

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
