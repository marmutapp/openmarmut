package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/marmutapp/openmarmut/internal/runtime"
	"github.com/marmutapp/openmarmut/internal/ui"
	"github.com/spf13/cobra"
)

// extToLang maps file extensions to glamour language hints.
var extToLang = map[string]string{
	".go":   "go",
	".py":   "python",
	".js":   "javascript",
	".ts":   "typescript",
	".yaml": "yaml",
	".yml":  "yaml",
	".json": "json",
	".md":   "markdown",
	".sh":   "bash",
}

func newReadCmd(runner *Runner) *cobra.Command {
	return &cobra.Command{
		Use:   "read <path>",
		Short: "Read a file and print to stdout",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runner.Run(cmd.Context(), func(ctx context.Context, rt runtime.Runtime) error {
				relPath := args[0]
				data, err := rt.ReadFile(ctx, relPath)
				if err != nil {
					return err
				}

				ext := strings.ToLower(filepath.Ext(relPath))
				lang, ok := extToLang[ext]
				if ok && ui.ColorEnabled() {
					fmt.Fprint(os.Stdout, ui.RenderCodeBlock(string(data), lang))
					return nil
				}

				_, err = os.Stdout.Write(data)
				return err
			})
		},
	}
}
