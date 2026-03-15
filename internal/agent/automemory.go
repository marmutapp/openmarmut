package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gajaai/openmarmut-go/internal/llm"
)

const (
	memoryDirName  = ".openmarmut"
	memoryFileName = "MEMORY.md"
	maxMemoryChars = 5000 // Cap memory content loaded into system prompt.
)

// MemoryEntry represents a single auto-memory item.
type MemoryEntry struct {
	Timestamp time.Time
	Project   string // Project path tag, empty means "global".
	Category  string // "learning", "preference", "pattern", "context"
	Content   string
}

// MemoryStore manages persistent auto-memory in ~/.openmarmut/memory/MEMORY.md.
type MemoryStore struct {
	path    string
	entries []MemoryEntry
}

// NewMemoryStore creates a new memory store. Returns nil if the home directory
// cannot be determined.
func NewMemoryStore() *MemoryStore {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	dir := filepath.Join(home, memoryDirName, "memory")
	return &MemoryStore{
		path: filepath.Join(dir, memoryFileName),
	}
}

// NewMemoryStoreWithPath creates a new memory store at the given custom path.
// Returns nil if path is empty and home directory cannot be determined.
func NewMemoryStoreWithPath(customPath string) *MemoryStore {
	if customPath != "" {
		return &MemoryStore{path: customPath}
	}
	return NewMemoryStore()
}

// NewMemoryStoreAt creates a memory store at a specific path (for testing).
func NewMemoryStoreAt(path string) *MemoryStore {
	return &MemoryStore{path: path}
}

// Load reads existing memories from disk.
func (m *MemoryStore) Load() error {
	data, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No memories yet.
		}
		return fmt.Errorf("automemory.Load: %w", err)
	}

	m.entries = parseMemoryEntries(string(data))
	return nil
}

// Save appends a new memory entry to disk.
func (m *MemoryStore) Save(category, content string) error {
	return m.SaveWithProject("", category, content)
}

// SaveWithProject appends a new memory entry tagged with a project path.
func (m *MemoryStore) SaveWithProject(project, category, content string) error {
	entry := MemoryEntry{
		Timestamp: time.Now(),
		Project:   project,
		Category:  category,
		Content:   content,
	}

	// Ensure directory exists.
	dir := filepath.Dir(m.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("automemory.SaveWithProject: mkdir: %w", err)
	}

	// Append to file.
	line := formatMemoryEntry(entry)
	f, err := os.OpenFile(m.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("automemory.SaveWithProject: open: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("automemory.SaveWithProject: write: %w", err)
	}

	m.entries = append(m.entries, entry)
	return nil
}

// Entries returns all loaded memory entries.
func (m *MemoryStore) Entries() []MemoryEntry {
	return m.entries
}

// EntriesForProject returns entries that match the given target directory.
// An entry matches if:
// - It has no project tag (global).
// - Its project path equals the target dir or is a parent of it.
func (m *MemoryStore) EntriesForProject(targetDir string) []MemoryEntry {
	var result []MemoryEntry
	for _, e := range m.entries {
		if entryMatchesProject(e, targetDir) {
			result = append(result, e)
		}
	}
	return result
}

// entryMatchesProject returns true if the entry is global or its project path
// matches (or is a parent of) the target directory.
func entryMatchesProject(e MemoryEntry, targetDir string) bool {
	if e.Project == "" {
		return true // Global entry.
	}
	if targetDir == "" {
		return true // No filter.
	}
	// Exact match or target is under the project.
	if e.Project == targetDir {
		return true
	}
	// Check if targetDir starts with the project path (parent directory).
	prefix := e.Project
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(targetDir, prefix)
}

// FormatForPrompt returns memory content formatted for inclusion in the system prompt.
// Caps output at maxMemoryChars. Shows all entries (unfiltered).
func (m *MemoryStore) FormatForPrompt() string {
	return m.FormatForPromptFiltered("")
}

// FormatForPromptFiltered returns memory content filtered to a target directory,
// formatted for inclusion in the system prompt. Caps output at maxMemoryChars.
func (m *MemoryStore) FormatForPromptFiltered(targetDir string) string {
	entries := m.entries
	if targetDir != "" {
		entries = m.EntriesForProject(targetDir)
	}

	if len(entries) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n## Auto-Memory (learned from previous sessions)\n\n")

	totalChars := sb.Len()
	for _, e := range entries {
		line := formatMemoryLine(e)
		if totalChars+len(line) > maxMemoryChars {
			sb.WriteString("- (older memories truncated)\n")
			break
		}
		sb.WriteString(line)
		totalChars += len(line)
	}

	return sb.String()
}

// Count returns the number of memory entries.
func (m *MemoryStore) Count() int {
	return len(m.entries)
}

// Path returns the file path of the memory store.
func (m *MemoryStore) Path() string {
	return m.path
}

// Clear removes all memories.
func (m *MemoryStore) Clear() error {
	m.entries = nil
	if err := os.Remove(m.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("automemory.Clear: %w", err)
	}
	return nil
}

// ExtractMemories uses the LLM to extract useful memories from a conversation.
// It sends the conversation history to the provider and parses the response
// for new memory lines. Returns the extracted memory strings.
func ExtractMemories(ctx context.Context, provider llm.Provider, history []llm.Message, targetDir string, existingContent string) ([]string, error) {
	// Build conversation summary for the LLM.
	var convBuf strings.Builder
	for _, m := range history {
		if m.Role == llm.RoleSystem {
			continue // Skip system prompt.
		}
		convBuf.WriteString(fmt.Sprintf("[%s]: %s\n", m.Role, m.Content))
		for _, tc := range m.ToolCalls {
			convBuf.WriteString(fmt.Sprintf("  tool_call: %s(%s)\n", tc.Name, tc.Arguments))
		}
	}

	prompt := fmt.Sprintf(`Review this conversation and extract any useful facts that would help in future sessions with this project. Only save things that are:
- Project-specific (build commands, architecture decisions, conventions)
- User preferences (coding style, testing preferences, commit style)
- Debugging insights (what worked, what didn't)

Do NOT save: conversation-specific details, temporary state, or information that's already in OPENMARMUT.md.

Current memories:
%s

Output ONLY new memories to add, one per line, prefixed with '- '.
If nothing new to add, output 'NONE'.`, existingContent)

	req := llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: prompt},
			{Role: llm.RoleUser, Content: convBuf.String()},
		},
	}

	resp, err := provider.Complete(ctx, req, nil)
	if err != nil {
		return nil, fmt.Errorf("automemory.ExtractMemories: %w", err)
	}

	return parseExtractedMemories(resp.Content), nil
}

// parseExtractedMemories parses LLM response into individual memory strings.
func parseExtractedMemories(content string) []string {
	content = strings.TrimSpace(content)
	if content == "" || strings.EqualFold(content, "NONE") {
		return nil
	}

	var memories []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") {
			mem := strings.TrimSpace(strings.TrimPrefix(line, "- "))
			if mem != "" && !strings.EqualFold(mem, "NONE") {
				memories = append(memories, mem)
			}
		}
	}
	return memories
}

// formatMemoryEntry serializes a memory entry to the markdown format.
func formatMemoryEntry(e MemoryEntry) string {
	projectTag := "global"
	if e.Project != "" {
		projectTag = "project:" + e.Project
	}
	return fmt.Sprintf("- [%s] %s | %s | %s\n",
		e.Timestamp.Format("2006-01-02"),
		projectTag,
		e.Category,
		e.Content,
	)
}

// formatMemoryLine formats a single entry for prompt display.
func formatMemoryLine(e MemoryEntry) string {
	projectTag := "global"
	if e.Project != "" {
		projectTag = "project:" + e.Project
	}
	return fmt.Sprintf("- [%s] %s | %s | %s\n",
		e.Timestamp.Format("2006-01-02"),
		projectTag,
		e.Category,
		e.Content,
	)
}

// parseMemoryEntries parses memory entries from the MEMORY.md format.
// Supports both old format (- [date] category | content) and
// new format (- [date] project-tag | category | content).
func parseMemoryEntries(content string) []MemoryEntry {
	var entries []MemoryEntry
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- [") {
			continue
		}

		// Format: - [2026-01-15] project-tag | category | content
		// or old: - [2026-01-15] category | content
		line = strings.TrimPrefix(line, "- [")
		closeBracket := strings.Index(line, "]")
		if closeBracket < 0 {
			continue
		}

		dateStr := line[:closeBracket]
		rest := strings.TrimSpace(line[closeBracket+1:])

		t, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			continue
		}

		// Split on " | " to get parts.
		parts := strings.SplitN(rest, " | ", 3)
		switch len(parts) {
		case 3:
			// New format: project-tag | category | content
			project := ""
			tag := strings.TrimSpace(parts[0])
			if strings.HasPrefix(tag, "project:") {
				project = strings.TrimPrefix(tag, "project:")
			}
			// "global" tag means no project.
			entries = append(entries, MemoryEntry{
				Timestamp: t,
				Project:   project,
				Category:  strings.TrimSpace(parts[1]),
				Content:   strings.TrimSpace(parts[2]),
			})
		case 2:
			// Old format: category | content (no project tag).
			entries = append(entries, MemoryEntry{
				Timestamp: t,
				Category:  strings.TrimSpace(parts[0]),
				Content:   strings.TrimSpace(parts[1]),
			})
		default:
			continue // Invalid format.
		}
	}
	return entries
}
