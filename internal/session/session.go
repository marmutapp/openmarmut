package session

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/marmutapp/openmarmut/internal/llm"
)

// Session captures the full execution context of a conversation.
type Session struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	Mode        string            `json:"mode"`
	TargetDir   string            `json:"target_dir"`
	Provider    string            `json:"provider"`
	Model       string            `json:"model"`
	Messages    []llm.Message     `json:"messages"`
	ToolCalls   int               `json:"tool_calls"`
	TotalTokens int               `json:"total_tokens"`
	TotalCost   float64           `json:"total_cost"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// SessionSummary is a lightweight view of a session without full message history.
type SessionSummary struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
	TargetDir string    `json:"target_dir"`
	Mode      string    `json:"mode"`
	Messages  int       `json:"messages"`
	ToolCalls int       `json:"tool_calls"`
}

// NewID generates a short unique session ID (8 hex chars).
func NewID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based ID.
		return fmt.Sprintf("%x", time.Now().UnixNano()&0xFFFFFFFF)
	}
	return fmt.Sprintf("%x", b)
}

// Summary returns a lightweight summary of the session.
func (s *Session) Summary() *SessionSummary {
	return &SessionSummary{
		ID:        s.ID,
		Name:      s.Name,
		CreatedAt: s.CreatedAt,
		UpdatedAt: s.UpdatedAt,
		Provider:  s.Provider,
		Model:     s.Model,
		TargetDir: s.TargetDir,
		Mode:      s.Mode,
		Messages:  len(s.Messages),
		ToolCalls: s.ToolCalls,
	}
}

// UserTurns returns the number of user messages (i.e., conversation turns from the user).
func (s *Session) UserTurns() int {
	n := 0
	for _, m := range s.Messages {
		if m.Role == llm.RoleUser {
			n++
		}
	}
	return n
}

// DisplayName returns the name if set, otherwise "(unnamed)".
func (s *Session) DisplayName() string {
	if s.Name != "" {
		return s.Name
	}
	return "(unnamed)"
}
