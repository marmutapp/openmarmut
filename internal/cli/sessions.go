package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/marmutapp/openmarmut/internal/session"
	"github.com/marmutapp/openmarmut/internal/ui"
	"github.com/spf13/cobra"
)

func newSessionsCmd() *cobra.Command {
	var targetDir string

	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List saved chat sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			var summaries []*session.SessionSummary
			var err error

			if targetDir != "" {
				abs, absErr := filepath.Abs(targetDir)
				if absErr != nil {
					return fmt.Errorf("sessions: %w", absErr)
				}
				summaries, err = session.FindByTarget(abs)
			} else {
				summaries, err = session.List()
			}
			if err != nil {
				return fmt.Errorf("sessions: %w", err)
			}

			if len(summaries) == 0 {
				fmt.Fprintln(os.Stderr, ui.FormatHint("No sessions found."))
				return nil
			}

			headers := []string{"ID", "NAME", "AGE", "PROVIDER", "TARGET", "TURNS"}
			var rows [][]string
			for _, s := range summaries {
				rows = append(rows, []string{
					s.ID,
					displayName(s.Name),
					humanizeAge(s.UpdatedAt),
					s.Provider,
					truncatePath(s.TargetDir, 40),
					fmt.Sprintf("%d", s.Messages),
				})
			}
			fmt.Fprintln(os.Stdout, ui.RenderTable(headers, rows, -1))
			return nil
		},
	}

	cmd.Flags().StringVar(&targetDir, "target", "", "filter sessions by target directory")

	deleteCmd := &cobra.Command{
		Use:   "delete <session-id>",
		Short: "Delete a saved session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := session.Delete(args[0]); err != nil {
				return fmt.Errorf("sessions delete: %w", err)
			}
			fmt.Fprintln(os.Stderr, ui.FormatSuccess("Session "+args[0]+" deleted."))
			return nil
		},
	}

	cmd.AddCommand(deleteCmd)
	return cmd
}

// displayName returns the session name or "(unnamed)".
func displayName(name string) string {
	if name != "" {
		return name
	}
	return "(unnamed)"
}

// humanizeAge formats a time as a human-readable age string.
func humanizeAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1m ago"
		}
		return fmt.Sprintf("%dm ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1h ago"
		}
		return fmt.Sprintf("%dh ago", h)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1d ago"
		}
		return fmt.Sprintf("%dd ago", days)
	}
}

// truncatePath shortens a path if it exceeds maxLen.
func truncatePath(p string, maxLen int) string {
	if len(p) <= maxLen {
		return p
	}
	return "..." + p[len(p)-maxLen+3:]
}
