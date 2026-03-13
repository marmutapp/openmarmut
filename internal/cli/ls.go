package cli

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/gajaai/opencode-go/internal/runtime"
	"github.com/spf13/cobra"
)

func newLsCmd(runner *Runner) *cobra.Command {
	return &cobra.Command{
		Use:   "ls [path]",
		Short: "List directory contents",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runner.Run(cmd.Context(), func(ctx context.Context, rt runtime.Runtime) error {
				relPath := "."
				if len(args) > 0 {
					relPath = args[0]
				}

				entries, err := rt.ListDir(ctx, relPath)
				if err != nil {
					return err
				}

				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				for _, e := range entries {
					kind := "-"
					if e.IsDir {
						kind = "d"
					}
					fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", kind, e.Perm, e.Size, e.Name)
				}
				return w.Flush()
			})
		},
	}
}
