package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/gajaai/openmarmut-go/internal/agent"
	"github.com/gajaai/openmarmut-go/internal/config"
	"github.com/gajaai/openmarmut-go/internal/llm"
	"github.com/gajaai/openmarmut-go/internal/logger"
	"github.com/gajaai/openmarmut-go/internal/ui"
	"github.com/spf13/cobra"

	// Register LLM wire format providers.
	_ "github.com/gajaai/openmarmut-go/internal/llm/anthropic"
	_ "github.com/gajaai/openmarmut-go/internal/llm/custom"
	_ "github.com/gajaai/openmarmut-go/internal/llm/gemini"
	_ "github.com/gajaai/openmarmut-go/internal/llm/ollama"
	_ "github.com/gajaai/openmarmut-go/internal/llm/openai"
	_ "github.com/gajaai/openmarmut-go/internal/llm/responses"
)

func newAskCmd(runner *Runner) *cobra.Command {
	var noTools bool

	cmd := &cobra.Command{
		Use:   "ask <question>",
		Short: "Ask the AI a question about the project",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(runner.flags)
			if err != nil {
				return fmt.Errorf("ask: %w", err)
			}

			log := logger.New(cfg.Log)

			entry, err := cfg.LLM.ResolveActiveProvider()
			if err != nil {
				return fmt.Errorf("ask: %w", err)
			}

			rawProvider, err := llm.NewProvider(*entry, log)
			if err != nil {
				return fmt.Errorf("ask: %w", err)
			}
			provider := llm.NewRetryProvider(rawProvider, llm.RetryConfig{}, log)

			question := strings.Join(args, " ")

			// Spinner while waiting for first token.
			spinner := ui.NewSpinner(os.Stderr, "Thinking...")
			spinner.Start()
			firstToken := true

			streamCB := func(text string) error {
				if firstToken {
					spinner.Stop()
					firstToken = false
				}
				_, writeErr := fmt.Fprint(os.Stdout, text)
				return writeErr
			}

			if noTools {
				// Simple single-turn, no agent loop.
				req := llm.Request{
					Messages: []llm.Message{
						{Role: llm.RoleUser, Content: question},
					},
					Temperature: cfg.LLM.DefaultTemperature,
					MaxTokens:   cfg.LLM.DefaultMaxTokens,
				}
				if entry.Temperature != nil {
					req.Temperature = entry.Temperature
				}
				if entry.MaxTokens != nil {
					req.MaxTokens = entry.MaxTokens
				}

				_, err = provider.Complete(cmd.Context(), req, streamCB)
				spinner.Stop()
				if err != nil {
					return fmt.Errorf("ask: %w", err)
				}
				fmt.Fprintln(os.Stdout)
				return nil
			}

			// Agent loop with tools — needs a runtime.
			rt, err := initRuntime(cmd.Context(), cfg, log)
			if err != nil {
				spinner.Stop()
				return fmt.Errorf("ask: %w", err)
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

			// In non-interactive ask mode, auto-approve all tools.
			pc := agent.NewPermissionChecker(
				agent.BuildPermissions(cfg.Agent.AutoAllow, cfg.Agent.Confirm),
				nil,
			)
			opts = append(opts, agent.WithPermissionChecker(pc))

			ag := agent.New(provider, rt, log, opts...)
			result, err := ag.Run(cmd.Context(), question, streamCB)
			spinner.Stop()
			if err != nil {
				return fmt.Errorf("ask: %w", err)
			}

			fmt.Fprintln(os.Stdout)

			// Styled summary line.
			costStr := llm.FormatCost(result.Usage, provider.Model())
			summary := ui.FormatSummary(
				len(result.Steps),
				result.Usage.PromptTokens,
				result.Usage.CompletionTokens,
				costStr,
				result.Duration,
			)
			fmt.Fprintln(os.Stderr, "\n"+summary)

			return nil
		},
	}

	cmd.Flags().BoolVar(&noTools, "no-tools", false, "disable tools (simple single-turn question)")
	return cmd
}

func resolveTemperature(cfg *config.Config, entry *llm.ProviderEntry) *float64 {
	if entry.Temperature != nil {
		return entry.Temperature
	}
	return cfg.LLM.DefaultTemperature
}

func resolveMaxTokens(cfg *config.Config, entry *llm.ProviderEntry) *int {
	if entry.MaxTokens != nil {
		return entry.MaxTokens
	}
	return cfg.LLM.DefaultMaxTokens
}
