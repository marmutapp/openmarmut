package cli

import (
	"context"
	"os"

	"github.com/gajaai/opencode-go/internal/runtime"
	"github.com/spf13/cobra"
)

func newReadCmd(runner *Runner) *cobra.Command {
	return &cobra.Command{
		Use:   "read <path>",
		Short: "Read a file and print to stdout",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runner.Run(cmd.Context(), func(ctx context.Context, rt runtime.Runtime) error {
				data, err := rt.ReadFile(ctx, args[0])
				if err != nil {
					return err
				}
				_, err = os.Stdout.Write(data)
				return err
			})
		},
	}
}
