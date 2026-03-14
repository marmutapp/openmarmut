package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultRetentionDays is the default number of days to keep sessions.
const DefaultRetentionDays = 30

// sessionsDir returns the path to the sessions directory (~/.openmarmut/sessions/).
func sessionsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("session.sessionsDir: %w", err)
	}
	return filepath.Join(home, ".openmarmut", "sessions"), nil
}

// ensureDir creates the sessions directory if it doesn't exist.
func ensureDir() (string, error) {
	dir, err := sessionsDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("session.ensureDir(%s): %w", dir, err)
	}
	return dir, nil
}

// sessionPath returns the file path for a given session ID.
func sessionPath(id string) (string, error) {
	dir, err := sessionsDir()
	if err != nil {
		return "", err
	}
	// Sanitize ID to prevent path traversal.
	clean := filepath.Base(id)
	if clean != id || strings.ContainsAny(id, `/\.`) {
		return "", fmt.Errorf("session.sessionPath: invalid session ID %q", id)
	}
	return filepath.Join(dir, clean+".json"), nil
}

// Save writes a session to disk as JSON.
func Save(s *Session) error {
	dir, err := ensureDir()
	if err != nil {
		return err
	}

	s.UpdatedAt = time.Now()

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("session.Save(%s): marshal: %w", s.ID, err)
	}

	path := filepath.Join(dir, s.ID+".json")

	// Atomic write: temp file + rename.
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("session.Save(%s): write: %w", s.ID, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("session.Save(%s): rename: %w", s.ID, err)
	}

	return nil
}

// Load reads a session from disk by ID.
func Load(id string) (*Session, error) {
	path, err := sessionPath(id)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("session.Load(%s): %w", id, err)
	}

	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("session.Load(%s): unmarshal: %w", id, err)
	}

	return &s, nil
}

// Delete removes a session file by ID.
func Delete(id string) error {
	path, err := sessionPath(id)
	if err != nil {
		return err
	}

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("session.Delete(%s): %w", id, err)
	}
	return nil
}

// List returns summaries of all sessions, sorted by UpdatedAt descending.
func List() ([]*SessionSummary, error) {
	dir, err := sessionsDir()
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("session.List: %w", err)
	}

	var summaries []*SessionSummary
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		s, err := Load(id)
		if err != nil {
			continue // skip corrupt files
		}
		summaries = append(summaries, s.Summary())
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
	})

	return summaries, nil
}

// FindRecent returns the most recent N sessions.
func FindRecent(n int) ([]*SessionSummary, error) {
	all, err := List()
	if err != nil {
		return nil, err
	}
	if len(all) > n {
		all = all[:n]
	}
	return all, nil
}

// FindByTarget returns sessions for a specific target directory, sorted by most recent.
func FindByTarget(dir string) ([]*SessionSummary, error) {
	all, err := List()
	if err != nil {
		return nil, err
	}

	var matched []*SessionSummary
	for _, s := range all {
		if s.TargetDir == dir {
			matched = append(matched, s)
		}
	}
	return matched, nil
}

// Cleanup removes sessions older than the given number of days.
// Returns the number of sessions deleted.
func Cleanup(retentionDays int) (int, error) {
	dir, err := sessionsDir()
	if err != nil {
		return 0, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("session.Cleanup: %w", err)
	}

	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	deleted := 0

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		s, err := Load(id)
		if err != nil {
			continue
		}
		if s.UpdatedAt.Before(cutoff) {
			if err := Delete(id); err == nil {
				deleted++
			}
		}
	}

	return deleted, nil
}
