package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/gajaai/openmarmut-go/internal/runtime"
	"github.com/spf13/cobra"
)

func newInfoCmd(runner *Runner) *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Show runtime information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runner.Run(cmd.Context(), func(ctx context.Context, rt runtime.Runtime) error {
				fmt.Fprintf(os.Stdout, "Target directory: %s\n", rt.TargetDir())
				return nil
			})
		},
	}
}
