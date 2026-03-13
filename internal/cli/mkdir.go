package cli

import (
	"context"
	"os"

	"github.com/gajaai/opencode-go/internal/runtime"
	"github.com/spf13/cobra"
)

func newMkdirCmd(runner *Runner) *cobra.Command {
	var perm uint32

	cmd := &cobra.Command{
		Use:   "mkdir <path>",
		Short: "Create a directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runner.Run(cmd.Context(), func(ctx context.Context, rt runtime.Runtime) error {
				return rt.MkDir(ctx, args[0], os.FileMode(perm))
			})
		},
	}

	cmd.Flags().Uint32Var(&perm, "perm", 0o755, "directory permission bits (octal)")

	return cmd
}
