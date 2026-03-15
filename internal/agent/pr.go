package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/marmutapp/openmarmut/internal/runtime"
)

// PRStatus holds the current pull request status for the target directory.
type PRStatus struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	State          string `json:"state"`          // OPEN, CLOSED, MERGED
	ReviewDecision string `json:"reviewDecision"` // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, ""
	URL            string `json:"url"`
	HeadRefName    string `json:"headRefName"`
}

// PRDetector detects PR status using the gh CLI.
type PRDetector struct {
	rt runtime.Runtime
}

// NewPRDetector creates a PR detector.
func NewPRDetector(rt runtime.Runtime) *PRDetector {
	return &PRDetector{rt: rt}
}

// Detect checks for a PR associated with the current branch.
// Returns nil if no PR is found or gh is not available.
func (d *PRDetector) Detect(ctx context.Context) *PRStatus {
	// Check if gh CLI is available.
	result, err := d.rt.Exec(ctx, "command -v gh", runtime.ExecOpts{})
	if err != nil || result.ExitCode != 0 {
		return nil
	}

	// Query PR for current branch.
	result, err = d.rt.Exec(ctx, "gh pr view --json number,title,state,reviewDecision,url,headRefName 2>/dev/null", runtime.ExecOpts{})
	if err != nil || result.ExitCode != 0 {
		return nil
	}

	var pr PRStatus
	if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &pr); jsonErr != nil {
		return nil
	}
	if pr.Number == 0 {
		return nil
	}
	return &pr
}

// Checks returns CI check status for the current PR.
func (d *PRDetector) Checks(ctx context.Context) (string, error) {
	result, err := d.rt.Exec(ctx, "gh pr checks 2>/dev/null", runtime.ExecOpts{})
	if err != nil {
		return "", fmt.Errorf("PRDetector.Checks: %w", err)
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("PRDetector.Checks: gh pr checks exited %d", result.ExitCode)
	}
	return strings.TrimRight(result.Stdout, "\n"), nil
}

// OpenInBrowser opens the current PR in the default browser.
func (d *PRDetector) OpenInBrowser(ctx context.Context) error {
	result, err := d.rt.Exec(ctx, "gh pr view --web 2>/dev/null", runtime.ExecOpts{})
	if err != nil {
		return fmt.Errorf("PRDetector.OpenInBrowser: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("PRDetector.OpenInBrowser: gh pr view --web exited %d", result.ExitCode)
	}
	return nil
}

// FormatStatus returns a human-readable one-line status string.
func (pr *PRStatus) FormatStatus() string {
	status := pr.reviewStatus()
	return fmt.Sprintf("#%d '%s' — %s", pr.Number, pr.Title, status)
}

// reviewStatus returns the review status as a lowercase string.
func (pr *PRStatus) reviewStatus() string {
	switch pr.ReviewDecision {
	case "APPROVED":
		return "approved"
	case "CHANGES_REQUESTED":
		return "changes requested"
	case "REVIEW_REQUIRED":
		return "review required"
	default:
		return strings.ToLower(pr.State)
	}
}

// CurrentBranch returns the current git branch name.
func CurrentBranch(ctx context.Context, rt runtime.Runtime) string {
	result, err := rt.Exec(ctx, "git rev-parse --abbrev-ref HEAD 2>/dev/null", runtime.ExecOpts{})
	if err != nil || result.ExitCode != 0 {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}
