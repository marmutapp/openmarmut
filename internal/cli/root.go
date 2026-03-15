package cli

import (
	"github.com/marmutapp/openmarmut/internal/config"
	"github.com/spf13/cobra"
)

// NewRootCmd creates the root cobra command with all subcommands.
func NewRootCmd() *cobra.Command {
	flags := &config.FlagOverrides{}

	var (
		mode        string
		target      string
		cfgPath     string
		logLevel    string
		logFormat   string
		llmProvider string
		llmModel    string
		llmTemp     float64
		autoApprove bool
	)

	root := &cobra.Command{
		Use:           "openmarmut",
		Short:         "CLI tool for AI-assisted development with local or Docker runtimes",
		Version:       VersionString(),
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if cmd.Flags().Changed("mode") {
				flags.Mode = &mode
			}
			if cmd.Flags().Changed("target") {
				flags.TargetDir = &target
			}
			if cmd.Flags().Changed("config") {
				flags.ConfigPath = &cfgPath
			}
			if cmd.Flags().Changed("log-level") {
				flags.LogLevel = &logLevel
			}
			if cmd.Flags().Changed("log-format") {
				flags.LogFormat = &logFormat
			}
			if cmd.Flags().Changed("provider") {
				flags.LLMProvider = &llmProvider
			}
			if cmd.Flags().Changed("model") {
				flags.LLMModel = &llmModel
			}
			if cmd.Flags().Changed("temperature") {
				flags.LLMTemperature = &llmTemp
			}
			if cmd.Flags().Changed("auto-approve") {
				flags.AutoApprove = autoApprove
			}
		},
	}

	pf := root.PersistentFlags()
	pf.StringVarP(&mode, "mode", "m", "", `runtime mode: "local" or "docker"`)
	pf.StringVarP(&target, "target", "t", "", "target directory (default: cwd)")
	pf.StringVarP(&cfgPath, "config", "c", "", "config file path")
	pf.StringVar(&logLevel, "log-level", "", "log level: debug/info/warn/error")
	pf.StringVar(&logFormat, "log-format", "", "log format: text/json")
	pf.StringVarP(&llmProvider, "provider", "p", "", "LLM provider name")
	pf.StringVar(&llmModel, "model", "", "override model for the active LLM provider")
	pf.Float64Var(&llmTemp, "temperature", 0, "sampling temperature (0.0–2.0)")
	pf.BoolVar(&autoApprove, "auto-approve", false, "skip all tool confirmation prompts")

	runner := NewRunner(flags)

	root.AddCommand(
		newReadCmd(runner),
		newWriteCmd(runner),
		newDeleteCmd(runner),
		newLsCmd(runner),
		newMkdirCmd(runner),
		newExecCmd(runner),
		newInfoCmd(runner),
		newProvidersCmd(runner),
		newAskCmd(runner),
		newChatCmd(runner),
		newSessionsCmd(),
		newMCPCmd(runner),
	)

	return root
}
