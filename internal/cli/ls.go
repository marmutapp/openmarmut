package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/gajaai/openmarmut-go/internal/runtime"
	"github.com/gajaai/openmarmut-go/internal/ui"
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

				headers := []string{"PERM", "SIZE", "NAME"}
				var rows [][]string

				for _, e := range entries {
					perm := ui.FormatPermission(e.Perm.String())
					size := ui.HumanizeBytes(e.Size)
					name := ui.FormatDirEntry(e.Name, e.IsDir)
					rows = append(rows, []string{perm, size, name})
				}

				fmt.Fprintln(os.Stdout, ui.RenderTable(headers, rows, -1))
				return nil
			})
		},
	}
}
