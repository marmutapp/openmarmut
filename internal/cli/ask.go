package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/marmutapp/openmarmut/internal/agent"
	"github.com/marmutapp/openmarmut/internal/config"
	"github.com/marmutapp/openmarmut/internal/llm"
	"github.com/marmutapp/openmarmut/internal/logger"
	"github.com/marmutapp/openmarmut/internal/ui"
	"github.com/spf13/cobra"

	// Register LLM wire format providers.
	_ "github.com/marmutapp/openmarmut/internal/llm/anthropic"
	_ "github.com/marmutapp/openmarmut/internal/llm/custom"
	_ "github.com/marmutapp/openmarmut/internal/llm/gemini"
	_ "github.com/marmutapp/openmarmut/internal/llm/ollama"
	_ "github.com/marmutapp/openmarmut/internal/llm/openai"
	_ "github.com/marmutapp/openmarmut/internal/llm/responses"
)

func newAskCmd(runner *Runner) *cobra.Command {
	var noTools bool
	var planFlag bool
	var imageFlags []string

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
				// Load --image flags from OS (no Runtime in no-tools mode).
				var cliImages []llm.ImageContent
				for _, imgPath := range imageFlags {
					img, imgErr := agent.LoadImageFromOS(imgPath)
					if imgErr != nil {
						spinner.Stop()
						return fmt.Errorf("ask: %w", imgErr)
					}
					cliImages = append(cliImages, *img)
				}
				for _, img := range cliImages {
					fmt.Fprintln(os.Stderr, ui.FormatImageAttachment(img.Path, img.MimeType, len(img.Data)*3/4))
				}

				req := llm.Request{
					Messages: []llm.Message{
						{Role: llm.RoleUser, Content: question, Images: cliImages},
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

			ctxCfg := agent.DefaultContextConfig()
			if entry.ContextWindow > 0 {
				ctxCfg.ContextWindow = entry.ContextWindow
			}
			if cfg.Agent.ContextWindow > 0 {
				ctxCfg.ContextWindow = cfg.Agent.ContextWindow
			}
			if cfg.Agent.TruncationThreshold > 0 {
				ctxCfg.TruncationRatio = cfg.Agent.TruncationThreshold
			}
			if cfg.Agent.KeepRecentTurns > 0 {
				ctxCfg.KeepRecentTurns = cfg.Agent.KeepRecentTurns
			}
			opts = append(opts, agent.WithContextConfig(ctxCfg))

			// Extended thinking from provider entry config.
			if entry.ExtendedThinking {
				opts = append(opts, agent.WithExtendedThinking(true, entry.ThinkingBudget))
			}

			// In non-interactive ask mode, auto-approve all tools.
			pc := agent.NewPermissionChecker(
				agent.BuildPermissions(cfg.Agent.AutoAllow, cfg.Agent.Confirm),
				nil,
			)
			opts = append(opts, agent.WithPermissionChecker(pc))

			// Load project instructions from OPENMARMUT.md files.
			projInfo, _ := agent.LoadProjectInstructions(cmd.Context(), rt)
			if projInfo != nil && projInfo.Content != "" {
				opts = append(opts, agent.WithProjectInstructions(projInfo.Content))
			}

			// Load rules from .openmarmut/rules/.
			rules, _ := agent.LoadRules(cmd.Context(), rt)
			if len(rules) > 0 {
				opts = append(opts, agent.WithRules(rules))
			}

			// Load ignore list from .openmarmutignore.
			ignoreList := agent.LoadIgnoreList(cmd.Context(), rt)
			if ignoreList != nil && len(ignoreList.Patterns()) > 0 {
				opts = append(opts, agent.WithIgnoreList(ignoreList))
			}

			ag := agent.New(provider, rt, log, opts...)

			// Resolve @file references in the question.
			question, refImages, fileWarnings := resolveFileRefs(cmd.Context(), question, rt)
			for _, w := range fileWarnings {
				fmt.Fprintln(os.Stderr, ui.FormatWarning(w))
			}
			// Load images from --image flags.
			for _, imgPath := range imageFlags {
				img, imgErr := agent.LoadImage(cmd.Context(), rt, imgPath)
				if imgErr != nil {
					spinner.Stop()
					return fmt.Errorf("ask: %w", imgErr)
				}
				refImages = append(refImages, *img)
			}
			// Display loaded images.
			for _, img := range refImages {
				fmt.Fprintln(os.Stderr, ui.FormatImageAttachment(img.Path, img.MimeType, len(img.Data)*3/4))
			}

			if planFlag {
				// Plan mode: analyze first, then execute.
				planResult, planErr := ag.RunPlan(cmd.Context(), question, streamCB)
				spinner.Stop()
				if planErr != nil {
					return fmt.Errorf("ask: plan: %w", planErr)
				}

				plan := planResult.Response

				// Display plan.
				fmt.Fprintln(os.Stderr, ui.RenderPlanBox(plan))
				planCostStr := llm.FormatCost(planResult.Usage, provider.Model())
				fmt.Fprintln(os.Stderr, ui.FormatSummary(
					len(planResult.Steps), planResult.Usage.PromptTokens,
					planResult.Usage.CompletionTokens, planCostStr, planResult.Duration,
				))

				// Execute the plan.
				executeMsg := fmt.Sprintf(
					"Execute the following plan. The original request was: %s\n\n---\n\n%s",
					question, plan,
				)

				spinner = ui.NewSpinner(os.Stderr, "Executing plan...")
				spinner.Start()
				firstToken = true

				result, err := ag.Run(cmd.Context(), executeMsg, streamCB)
				spinner.Stop()
				if err != nil {
					return fmt.Errorf("ask: execute: %w", err)
				}

				fmt.Fprintln(os.Stdout)
				costStr := llm.FormatCost(result.Usage, provider.Model())
				fmt.Fprintln(os.Stderr, "\n"+ui.FormatSummary(
					len(result.Steps), result.Usage.PromptTokens,
					result.Usage.CompletionTokens, costStr, result.Duration,
				))
				return nil
			}

			result, err := ag.RunWithImages(cmd.Context(), question, refImages, streamCB)
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
	cmd.Flags().BoolVar(&planFlag, "plan", false, "plan first, then execute (analyze before acting)")
	cmd.Flags().StringArrayVar(&imageFlags, "image", nil, "attach image file(s) to the question (can be repeated)")
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
