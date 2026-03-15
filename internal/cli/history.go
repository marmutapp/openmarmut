package cli

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	maxHistoryEntries = 500
	historyFileName   = "history"
)

// inputHistory manages message history for the chat REPL.
type inputHistory struct {
	entries []string
	index   int // current position when navigating; -1 means at the prompt.
	mu      sync.Mutex
	path    string // path to history file.
}

// newInputHistory creates an inputHistory, loading from the config dir if available.
func newInputHistory() *inputHistory {
	h := &inputHistory{index: -1}

	configDir, err := os.UserConfigDir()
	if err != nil {
		return h
	}
	dir := filepath.Join(configDir, "openmarmut")
	h.path = filepath.Join(dir, historyFileName)

	h.load()
	return h
}

// Add appends a message to history (deduplicating consecutive entries).
func (h *inputHistory) Add(msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Skip duplicate of last entry.
	if len(h.entries) > 0 && h.entries[len(h.entries)-1] == msg {
		h.index = -1
		return
	}

	h.entries = append(h.entries, msg)

	// Trim to max.
	if len(h.entries) > maxHistoryEntries {
		h.entries = h.entries[len(h.entries)-maxHistoryEntries:]
	}

	h.index = -1
}

// Previous returns the previous history entry (Up arrow).
// Returns the entry and true if available, or "" and false at the oldest entry.
func (h *inputHistory) Previous() (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.entries) == 0 {
		return "", false
	}

	if h.index == -1 {
		h.index = len(h.entries) - 1
	} else if h.index > 0 {
		h.index--
	} else {
		return h.entries[0], true
	}
	return h.entries[h.index], true
}

// Next returns the next history entry (Down arrow).
// Returns the entry and true, or "" and false when past the newest entry.
func (h *inputHistory) Next() (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.index == -1 || len(h.entries) == 0 {
		return "", false
	}

	if h.index < len(h.entries)-1 {
		h.index++
		return h.entries[h.index], true
	}

	// Past the end — back to prompt.
	h.index = -1
	return "", false
}

// Reset resets the navigation index.
func (h *inputHistory) Reset() {
	h.mu.Lock()
	h.index = -1
	h.mu.Unlock()
}

// Entries returns a copy of all history entries.
func (h *inputHistory) Entries() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.entries))
	copy(out, h.entries)
	return out
}

// Save writes history to disk.
func (h *inputHistory) Save() error {
	if h.path == "" {
		return nil
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	dir := filepath.Dir(h.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	f, err := os.Create(h.path)
	if err != nil {
		return err
	}
	defer f.Close()

	for _, entry := range h.entries {
		_, _ = f.WriteString(entry + "\n")
	}
	return nil
}

// load reads history from disk.
func (h *inputHistory) load() {
	if h.path == "" {
		return
	}

	f, err := os.Open(h.path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			h.entries = append(h.entries, line)
		}
	}

	// Trim to max.
	if len(h.entries) > maxHistoryEntries {
		h.entries = h.entries[len(h.entries)-maxHistoryEntries:]
	}
}
