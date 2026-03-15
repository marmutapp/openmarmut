package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/marmutapp/openmarmut/internal/llm"
	"github.com/marmutapp/openmarmut/internal/runtime"
)

// Task represents a tracked task item.
type Task struct {
	ID        int       `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"` // "pending", "in_progress", "completed", "failed"
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TaskList manages a persistent list of tasks tied to a session.
type TaskList struct {
	mu     sync.Mutex
	Tasks  []Task `json:"tasks"`
	file   string
	nextID int
}

// NewTaskList creates a new task list stored at the given path.
func NewTaskList(sessionID string) *TaskList {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	dir := filepath.Join(home, ".openmarmut", "tasks")
	os.MkdirAll(dir, 0o755) //nolint:errcheck
	return &TaskList{
		file: filepath.Join(dir, sessionID+".json"),
	}
}

// NewTaskListAt creates a task list at a specific path (for testing).
func NewTaskListAt(path string) *TaskList {
	return &TaskList{file: path}
}

// Load reads tasks from disk.
func (tl *TaskList) Load() error {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	data, err := os.ReadFile(tl.file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("tasks.Load: %w", err)
	}

	if err := json.Unmarshal(data, &tl.Tasks); err != nil {
		return fmt.Errorf("tasks.Load: unmarshal: %w", err)
	}

	// Restore nextID from max existing ID.
	for _, t := range tl.Tasks {
		if t.ID >= tl.nextID {
			tl.nextID = t.ID
		}
	}
	return nil
}

// Save writes tasks to disk atomically.
func (tl *TaskList) Save() error {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	data, err := json.MarshalIndent(tl.Tasks, "", "  ")
	if err != nil {
		return fmt.Errorf("tasks.Save: marshal: %w", err)
	}

	dir := filepath.Dir(tl.file)
	os.MkdirAll(dir, 0o755) //nolint:errcheck

	tmpPath := tl.file + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("tasks.Save: write: %w", err)
	}
	if err := os.Rename(tmpPath, tl.file); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("tasks.Save: rename: %w", err)
	}
	return nil
}

// Add creates a new task with the given title.
func (tl *TaskList) Add(title string) *Task {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	tl.nextID++
	t := Task{
		ID:        tl.nextID,
		Title:     title,
		Status:    "pending",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	tl.Tasks = append(tl.Tasks, t)
	return &t
}

// Update changes the status of a task by ID.
func (tl *TaskList) Update(id int, status string) error {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	for i := range tl.Tasks {
		if tl.Tasks[i].ID == id {
			tl.Tasks[i].Status = status
			tl.Tasks[i].UpdatedAt = time.Now()
			return nil
		}
	}
	return fmt.Errorf("task %d not found", id)
}

// Get returns a task by ID.
func (tl *TaskList) Get(id int) *Task {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	for i := range tl.Tasks {
		if tl.Tasks[i].ID == id {
			return &tl.Tasks[i]
		}
	}
	return nil
}

// ClearCompleted removes all completed tasks.
func (tl *TaskList) ClearCompleted() int {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	var kept []Task
	removed := 0
	for _, t := range tl.Tasks {
		if t.Status == "completed" {
			removed++
		} else {
			kept = append(kept, t)
		}
	}
	tl.Tasks = kept
	return removed
}

// All returns a copy of all tasks.
func (tl *TaskList) All() []Task {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	cp := make([]Task, len(tl.Tasks))
	copy(cp, tl.Tasks)
	return cp
}

// Len returns the number of tasks.
func (tl *TaskList) Len() int {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	return len(tl.Tasks)
}

// FormatTaskList returns a human-readable task list.
func FormatTaskList(tasks []Task) string {
	if len(tasks) == 0 {
		return "No tasks."
	}

	var b []byte
	for _, t := range tasks {
		icon := statusIcon(t.Status)
		b = append(b, fmt.Sprintf("%s %d. %s\n", icon, t.ID, t.Title)...)
	}
	return string(b)
}

// statusIcon returns a status indicator for display.
func statusIcon(status string) string {
	switch status {
	case "completed":
		return "✓"
	case "in_progress":
		return "→"
	case "failed":
		return "✗"
	default:
		return "○"
	}
}

// --- Task tools for the agent ---

// TaskTools returns the 3 task management tools.
// The tools operate on the provided TaskList.
func TaskTools(tl *TaskList) []Tool {
	return []Tool{
		taskCreateTool(tl),
		taskUpdateTool(tl),
		taskListTool(tl),
	}
}

func taskCreateTool(tl *TaskList) Tool {
	return Tool{
		Def: llm.ToolDef{
			Name:        "task_create",
			Description: "Create a new task to track progress. Returns the task ID.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title": map[string]any{
						"type":        "string",
						"description": "Short description of the task",
					},
				},
				"required": []string{"title"},
			},
		},
		Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
			var p struct {
				Title string `json:"title"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("task_create: %w", err)
			}
			if p.Title == "" {
				return "", fmt.Errorf("task_create: title is required")
			}
			t := tl.Add(p.Title)
			tl.Save() //nolint:errcheck
			return fmt.Sprintf("Created task #%d: %s", t.ID, t.Title), nil
		},
	}
}

func taskUpdateTool(tl *TaskList) Tool {
	return Tool{
		Def: llm.ToolDef{
			Name:        "task_update",
			Description: "Update the status of a task. Valid statuses: pending, in_progress, completed, failed.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "integer",
						"description": "The task ID to update",
					},
					"status": map[string]any{
						"type":        "string",
						"description": "New status: pending, in_progress, completed, or failed",
						"enum":        []string{"pending", "in_progress", "completed", "failed"},
					},
				},
				"required": []string{"id", "status"},
			},
		},
		Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
			var p struct {
				ID     int    `json:"id"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("task_update: %w", err)
			}
			if err := tl.Update(p.ID, p.Status); err != nil {
				return "", fmt.Errorf("task_update: %w", err)
			}
			tl.Save() //nolint:errcheck
			return fmt.Sprintf("Task #%d updated to %s", p.ID, p.Status), nil
		},
	}
}

func taskListTool(tl *TaskList) Tool {
	return Tool{
		Def: llm.ToolDef{
			Name:        "task_list",
			Description: "List all tracked tasks with their status.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
			tasks := tl.All()
			return FormatTaskList(tasks), nil
		},
	}
}
