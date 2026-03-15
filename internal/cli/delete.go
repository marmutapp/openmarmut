package cli

import (
	"context"

	"github.com/marmutapp/openmarmut/internal/runtime"
	"github.com/spf13/cobra"
)

func newDeleteCmd(runner *Runner) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <path>",
		Short: "Delete a file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runner.Run(cmd.Context(), func(ctx context.Context, rt runtime.Runtime) error {
				return rt.DeleteFile(ctx, args[0])
			})
		},
	}
}
