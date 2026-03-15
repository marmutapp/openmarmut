package cli

import (
	"fmt"
	"os"

	"github.com/marmutapp/openmarmut/internal/config"
	"github.com/marmutapp/openmarmut/internal/llm"
	"github.com/marmutapp/openmarmut/internal/logger"
	"github.com/marmutapp/openmarmut/internal/ui"
	"github.com/spf13/cobra"
)

func newProvidersCmd(runner *Runner) *cobra.Command {
	return &cobra.Command{
		Use:   "providers",
		Short: "List configured LLM providers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(runner.flags)
			if err != nil {
				return fmt.Errorf("providers: %w", err)
			}

			if len(cfg.LLM.Providers) == 0 {
				fmt.Fprintln(os.Stderr, ui.FormatWarning("No LLM providers configured."))
				fmt.Fprintln(os.Stderr, ui.FormatHint("Add a providers section to .openmarmut.yaml"))
				return nil
			}

			log := logger.New(cfg.Log)
			activeName := cfg.LLM.ActiveProviderName()

			headers := []string{"", "NAME", "TYPE", "MODEL", "ENDPOINT"}
			var rows [][]string
			activeRow := -1

			for i, p := range cfg.LLM.Providers {
				marker := " "
				if p.Name == activeName {
					marker = "★"
					activeRow = i
				}
				endpoint := p.EndpointURL
				if endpoint == "" {
					endpoint = llm.DefaultEndpointURL(p.Type)
				}
				endpoint = ui.TruncateEnd(endpoint, 45)

				typeName := ui.FormatProviderType(p.Type)

				rows = append(rows, []string{marker, p.Name, typeName, p.ModelName, endpoint})
			}

			fmt.Fprintln(os.Stdout, ui.RenderTable(headers, rows, activeRow))

			_ = log
			return nil
		},
	}
}
