package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gajaai/openmarmut-go/internal/runtime"
	"github.com/spf13/cobra"
)

func newExecCmd(runner *Runner) *cobra.Command {
	var (
		workdir string
		timeout time.Duration
		env     []string
	)

	cmd := &cobra.Command{
		Use:   "exec <command>",
		Short: "Execute a shell command",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runner.Run(cmd.Context(), func(ctx context.Context, rt runtime.Runtime) error {
				command := strings.Join(args, " ")
				result, err := rt.Exec(ctx, command, runtime.ExecOpts{
					RelDir:  workdir,
					Timeout: timeout,
					Env:     env,
				})
				if err != nil {
					return err
				}

				if result.Stdout != "" {
					fmt.Fprint(os.Stdout, result.Stdout)
				}
				if result.Stderr != "" {
					fmt.Fprint(os.Stderr, result.Stderr)
				}

				if result.ExitCode != 0 {
					os.Exit(result.ExitCode)
				}
				return nil
			})
		},
	}

	cmd.Flags().StringVarP(&workdir, "workdir", "w", "", "working directory relative to target")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "command timeout (e.g., 10s, 1m)")
	cmd.Flags().StringSliceVarP(&env, "env", "e", nil, "environment variables (KEY=VALUE)")

	return cmd
}
