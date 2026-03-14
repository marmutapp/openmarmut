package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/gajaai/openmarmut-go/internal/runtime"
)

const maxCheckpoints = 50

// FileSnapshot captures the state of a file before modification.
type FileSnapshot struct {
	Path    string `json:"path"`
	Content []byte `json:"content,omitempty"` // Original content (nil if file didn't exist).
	Existed bool   `json:"existed"`           // False if the file was newly created.
}

// Checkpoint captures file state before an agent turn modifies files.
type Checkpoint struct {
	ID        int                     `json:"id"`
	Timestamp time.Time               `json:"timestamp"`
	Files     map[string]FileSnapshot `json:"files"` // relPath → snapshot
}

// CheckpointStore manages file checkpoints across agent turns.
type CheckpointStore struct {
	checkpoints []Checkpoint
	nextID      int
}

// NewCheckpointStore creates an empty checkpoint store.
func NewCheckpointStore() *CheckpointStore {
	return &CheckpointStore{}
}

// Checkpoints returns all stored checkpoints.
func (cs *CheckpointStore) Checkpoints() []Checkpoint {
	return cs.checkpoints
}

// SetCheckpoints replaces all checkpoints (used for session restore).
func (cs *CheckpointStore) SetCheckpoints(cps []Checkpoint) {
	cs.checkpoints = cps
	cs.nextID = 0
	for _, cp := range cps {
		if cp.ID >= cs.nextID {
			cs.nextID = cp.ID + 1
		}
	}
}

// Len returns the number of stored checkpoints.
func (cs *CheckpointStore) Len() int {
	return len(cs.checkpoints)
}

// CaptureFile snapshots a file before it is modified.
// If no active checkpoint exists for the current turn, one is created.
// Each file is only captured once per checkpoint (first snapshot wins).
func (cs *CheckpointStore) CaptureFile(ctx context.Context, rt runtime.Runtime, relPath string) {
	// Get or create current checkpoint.
	var cp *Checkpoint
	if len(cs.checkpoints) > 0 {
		cp = &cs.checkpoints[len(cs.checkpoints)-1]
	}

	// If no checkpoint or the last one is sealed (from a previous turn), create a new one.
	if cp == nil || cp.Files == nil {
		cs.checkpoints = append(cs.checkpoints, Checkpoint{
			ID:        cs.nextID,
			Timestamp: time.Now(),
			Files:     make(map[string]FileSnapshot),
		})
		cs.nextID++
		cp = &cs.checkpoints[len(cs.checkpoints)-1]

		// Enforce max checkpoints.
		if len(cs.checkpoints) > maxCheckpoints {
			cs.checkpoints = cs.checkpoints[len(cs.checkpoints)-maxCheckpoints:]
		}
	}

	// Don't re-capture if already snapshotted in this checkpoint.
	if _, exists := cp.Files[relPath]; exists {
		return
	}

	snap := FileSnapshot{Path: relPath}
	data, err := rt.ReadFile(ctx, relPath)
	if err != nil {
		// File doesn't exist yet — it will be newly created.
		snap.Existed = false
		snap.Content = nil
	} else {
		snap.Existed = true
		snap.Content = data
	}

	cp.Files[relPath] = snap
}

// StartTurn begins a new checkpoint for the current agent turn.
// Any file captures before the next StartTurn go into this checkpoint.
func (cs *CheckpointStore) StartTurn() {
	cs.checkpoints = append(cs.checkpoints, Checkpoint{
		ID:        cs.nextID,
		Timestamp: time.Now(),
		Files:     make(map[string]FileSnapshot),
	})
	cs.nextID++

	// Enforce max checkpoints.
	if len(cs.checkpoints) > maxCheckpoints {
		cs.checkpoints = cs.checkpoints[len(cs.checkpoints)-maxCheckpoints:]
	}
}

// Rewind restores files from the last N checkpoints.
// Returns the list of files restored and any errors.
func (cs *CheckpointStore) Rewind(ctx context.Context, rt runtime.Runtime, n int) ([]RewindAction, error) {
	if n <= 0 || len(cs.checkpoints) == 0 {
		return nil, nil
	}
	if n > len(cs.checkpoints) {
		n = len(cs.checkpoints)
	}

	// Process checkpoints in reverse order (most recent first).
	start := len(cs.checkpoints) - n
	toRewind := cs.checkpoints[start:]

	var actions []RewindAction

	// Collect all unique files to restore (earliest snapshot wins for content).
	restored := make(map[string]bool)
	for i := len(toRewind) - 1; i >= 0; i-- {
		cp := toRewind[i]
		for path, snap := range cp.Files {
			if restored[path] {
				continue
			}
			restored[path] = true

			action := RewindAction{
				Path:       path,
				CheckpointID: cp.ID,
			}

			if !snap.Existed {
				// File was newly created — delete it.
				if err := rt.DeleteFile(ctx, path); err != nil {
					action.Error = fmt.Sprintf("delete: %s", err.Error())
				}
				action.Action = "deleted"
			} else {
				// Restore original content.
				if err := rt.WriteFile(ctx, path, snap.Content, 0644); err != nil {
					action.Error = fmt.Sprintf("restore: %s", err.Error())
				}
				action.Action = "restored"
			}

			actions = append(actions, action)
		}
	}

	// Remove the rewound checkpoints.
	cs.checkpoints = cs.checkpoints[:start]

	return actions, nil
}

// RewindAction describes what happened to a single file during rewind.
type RewindAction struct {
	Path         string `json:"path"`
	Action       string `json:"action"` // "restored" or "deleted"
	CheckpointID int    `json:"checkpoint_id"`
	Error        string `json:"error,omitempty"`
}

// LastN returns the last N checkpoints (for display).
func (cs *CheckpointStore) LastN(n int) []Checkpoint {
	if n <= 0 || len(cs.checkpoints) == 0 {
		return nil
	}
	if n > len(cs.checkpoints) {
		n = len(cs.checkpoints)
	}
	return cs.checkpoints[len(cs.checkpoints)-n:]
}

// HasChanges returns true if there are any checkpoints with file modifications.
func (cs *CheckpointStore) HasChanges() bool {
	for _, cp := range cs.checkpoints {
		if len(cp.Files) > 0 {
			return true
		}
	}
	return false
}
