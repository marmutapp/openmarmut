package cli

import (
	"github.com/gajaai/opencode-go/internal/config"
	"github.com/spf13/cobra"
)

// NewRootCmd creates the root cobra command with all subcommands.
func NewRootCmd() *cobra.Command {
	flags := &config.FlagOverrides{}

	var (
		mode      string
		target    string
		cfgPath   string
		logLevel  string
		logFormat string
	)

	root := &cobra.Command{
		Use:           "opencode",
		Short:         "CLI tool for AI-assisted development with local or Docker runtimes",
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
		},
	}

	pf := root.PersistentFlags()
	pf.StringVarP(&mode, "mode", "m", "", `runtime mode: "local" or "docker"`)
	pf.StringVarP(&target, "target", "t", "", "target directory (default: cwd)")
	pf.StringVarP(&cfgPath, "config", "c", "", "config file path")
	pf.StringVar(&logLevel, "log-level", "", "log level: debug/info/warn/error")
	pf.StringVar(&logFormat, "log-format", "", "log format: text/json")

	runner := NewRunner(flags)

	root.AddCommand(
		newReadCmd(runner),
		newWriteCmd(runner),
		newDeleteCmd(runner),
		newLsCmd(runner),
		newMkdirCmd(runner),
		newExecCmd(runner),
		newInfoCmd(runner),
	)

	return root
}
