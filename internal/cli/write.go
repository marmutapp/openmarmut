package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/gajaai/openmarmut-go/internal/runtime"
	"github.com/spf13/cobra"
)

func newWriteCmd(runner *Runner) *cobra.Command {
	var perm uint32

	cmd := &cobra.Command{
		Use:   "write <path>",
		Short: "Write stdin to a file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runner.Run(cmd.Context(), func(ctx context.Context, rt runtime.Runtime) error {
				data, err := io.ReadAll(os.Stdin)
				if err != nil {
					return fmt.Errorf("read stdin: %w", err)
				}
				return rt.WriteFile(ctx, args[0], data, os.FileMode(perm))
			})
		},
	}

	cmd.Flags().Uint32Var(&perm, "perm", 0o644, "file permission bits (octal)")

	return cmd
}
