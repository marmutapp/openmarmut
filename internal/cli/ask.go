package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/gajaai/opencode-go/internal/config"
	"github.com/gajaai/opencode-go/internal/llm"
	"github.com/gajaai/opencode-go/internal/logger"
	"github.com/spf13/cobra"

	// Register LLM wire format providers.
	_ "github.com/gajaai/opencode-go/internal/llm/anthropic"
	_ "github.com/gajaai/opencode-go/internal/llm/openai"
)

func newAskCmd(runner *Runner) *cobra.Command {
	return &cobra.Command{
		Use:   "ask <question>",
		Short: "Ask the AI a question (single-turn, no tools)",
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

			provider, err := llm.NewProvider(*entry, log)
			if err != nil {
				return fmt.Errorf("ask: %w", err)
			}

			question := strings.Join(args, " ")

			req := llm.Request{
				Messages: []llm.Message{
					{Role: llm.RoleUser, Content: question},
				},
				Temperature: cfg.LLM.DefaultTemperature,
				MaxTokens:   cfg.LLM.DefaultMaxTokens,
			}

			// Per-provider entry defaults override LLM-level defaults.
			if entry.Temperature != nil {
				req.Temperature = entry.Temperature
			}
			if entry.MaxTokens != nil {
				req.MaxTokens = entry.MaxTokens
			}

			_, err = provider.Complete(cmd.Context(), req, func(text string) error {
				_, writeErr := fmt.Fprint(os.Stdout, text)
				return writeErr
			})
			if err != nil {
				return fmt.Errorf("ask: %w", err)
			}

			fmt.Fprintln(os.Stdout)
			return nil
		},
	}
}
