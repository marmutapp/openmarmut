package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPRStatus_FormatStatus(t *testing.T) {
	tests := []struct {
		name     string
		pr       PRStatus
		expected string
	}{
		{
			name:     "approved",
			pr:       PRStatus{Number: 42, Title: "Add auth", ReviewDecision: "APPROVED"},
			expected: "#42 'Add auth' — approved",
		},
		{
			name:     "changes_requested",
			pr:       PRStatus{Number: 99, Title: "Fix bug", ReviewDecision: "CHANGES_REQUESTED"},
			expected: "#99 'Fix bug' — changes requested",
		},
		{
			name:     "review_required",
			pr:       PRStatus{Number: 1, Title: "Init", ReviewDecision: "REVIEW_REQUIRED"},
			expected: "#1 'Init' — review required",
		},
		{
			name:     "open_no_review",
			pr:       PRStatus{Number: 10, Title: "Draft", State: "OPEN", ReviewDecision: ""},
			expected: "#10 'Draft' — open",
		},
		{
			name:     "merged",
			pr:       PRStatus{Number: 5, Title: "Done", State: "MERGED", ReviewDecision: ""},
			expected: "#5 'Done' — merged",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, tc.pr.FormatStatus())
		})
	}
}
