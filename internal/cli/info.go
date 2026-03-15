package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/marmutapp/openmarmut/internal/config"
	"github.com/marmutapp/openmarmut/internal/ui"
	"github.com/spf13/cobra"
)

func newInfoCmd(runner *Runner) *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Show runtime information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(runner.flags)
			if err != nil {
				return fmt.Errorf("info: %w", err)
			}

			var lines []string
			lines = append(lines, ui.FormatKeyValue("Runtime", cfg.Mode))
			lines = append(lines, ui.FormatKeyValue("Target", cfg.TargetDir))

			// Provider info if configured.
			entry, provErr := cfg.LLM.ResolveActiveProvider()
			if provErr == nil && entry != nil {
				lines = append(lines, ui.FormatKeyValue("Provider", entry.Name))
				lines = append(lines, ui.FormatKeyValue("Model", entry.ModelName))
			}

			// Docker-specific fields.
			if cfg.Mode == "docker" {
				d := cfg.Docker
				if d.Image != "" {
					lines = append(lines, ui.FormatKeyValue("Image", d.Image))
				}
				mount := d.MountPath
				if mount == "" {
					mount = "/workspace"
				}
				lines = append(lines, ui.FormatKeyValue("Mount", mount))
				if d.NetworkMode != "" {
					lines = append(lines, ui.FormatKeyValue("Network", d.NetworkMode))
				}
			}

			content := strings.Join(lines, "\n")
			fmt.Fprintln(os.Stdout, ui.RenderBox("OpenMarmut", content))
			return nil
		},
	}
}
