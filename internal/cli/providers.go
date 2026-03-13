package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/gajaai/opencode-go/internal/config"
	"github.com/gajaai/opencode-go/internal/llm"
	"github.com/gajaai/opencode-go/internal/logger"
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
				fmt.Fprintln(os.Stderr, "No LLM providers configured. Add a providers section to .opencode.yaml.")
				return nil
			}

			log := logger.New(cfg.Log)
			activeName := cfg.LLM.ActiveProviderName()

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "  NAME\tTYPE\tMODEL\tENDPOINT\n")

			for _, p := range cfg.LLM.Providers {
				marker := " "
				if p.Name == activeName {
					marker = "*"
				}
				endpoint := p.EndpointURL
				if endpoint == "" {
					endpoint = llm.DefaultEndpointURL(p.Type)
				}
				fmt.Fprintf(w, "%s %s\t%s\t%s\t%s\n", marker, p.Name, p.Type, p.ModelName, endpoint)
			}
			w.Flush()

			_ = log // logger available for future use
			return nil
		},
	}
}
