package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskList_AddAndGet(t *testing.T) {
	tl := NewTaskListAt(filepath.Join(t.TempDir(), "tasks.json"))

	task := tl.Add("Write tests")
	assert.Equal(t, 1, task.ID)
	assert.Equal(t, "Write tests", task.Title)
	assert.Equal(t, "pending", task.Status)

	task2 := tl.Add("Fix bug")
	assert.Equal(t, 2, task2.ID)

	got := tl.Get(1)
	require.NotNil(t, got)
	assert.Equal(t, "Write tests", got.Title)

	assert.Nil(t, tl.Get(99))
}

func TestTaskList_Update(t *testing.T) {
	tl := NewTaskListAt(filepath.Join(t.TempDir(), "tasks.json"))
	tl.Add("Task A")

	err := tl.Update(1, "in_progress")
	require.NoError(t, err)
	assert.Equal(t, "in_progress", tl.Get(1).Status)

	err = tl.Update(1, "completed")
	require.NoError(t, err)
	assert.Equal(t, "completed", tl.Get(1).Status)

	err = tl.Update(99, "completed")
	assert.Error(t, err)
}

func TestTaskList_ClearCompleted(t *testing.T) {
	tl := NewTaskListAt(filepath.Join(t.TempDir(), "tasks.json"))
	tl.Add("Done task")
	tl.Add("Pending task")
	tl.Update(1, "completed") //nolint:errcheck

	removed := tl.ClearCompleted()
	assert.Equal(t, 1, removed)
	assert.Equal(t, 1, tl.Len())
	assert.Equal(t, "Pending task", tl.All()[0].Title)
}

func TestTaskList_All(t *testing.T) {
	tl := NewTaskListAt(filepath.Join(t.TempDir(), "tasks.json"))
	tl.Add("A")
	tl.Add("B")
	tl.Add("C")

	all := tl.All()
	assert.Len(t, all, 3)
	// Verify it's a copy.
	all[0].Title = "modified"
	assert.Equal(t, "A", tl.Get(1).Title)
}

func TestTaskList_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.json")

	tl := NewTaskListAt(path)
	tl.Add("Persist me")
	tl.Update(1, "in_progress") //nolint:errcheck
	require.NoError(t, tl.Save())

	// Verify file exists.
	_, err := os.Stat(path)
	require.NoError(t, err)

	// Load into new task list.
	tl2 := NewTaskListAt(path)
	require.NoError(t, tl2.Load())
	assert.Equal(t, 1, tl2.Len())
	assert.Equal(t, "Persist me", tl2.Get(1).Title)
	assert.Equal(t, "in_progress", tl2.Get(1).Status)

	// nextID should be restored — new task gets ID 2.
	task := tl2.Add("Second")
	assert.Equal(t, 2, task.ID)
}

func TestTaskList_LoadNonexistent(t *testing.T) {
	tl := NewTaskListAt(filepath.Join(t.TempDir(), "nonexistent.json"))
	err := tl.Load()
	assert.NoError(t, err) // Non-existent file is not an error.
	assert.Equal(t, 0, tl.Len())
}

func TestFormatTaskList_Empty(t *testing.T) {
	result := FormatTaskList(nil)
	assert.Equal(t, "No tasks.", result)
}

func TestFormatTaskList_WithTasks(t *testing.T) {
	tasks := []Task{
		{ID: 1, Title: "Pending task", Status: "pending"},
		{ID: 2, Title: "In progress", Status: "in_progress"},
		{ID: 3, Title: "Done", Status: "completed"},
		{ID: 4, Title: "Failed", Status: "failed"},
	}

	result := FormatTaskList(tasks)
	assert.Contains(t, result, "○ 1. Pending task")
	assert.Contains(t, result, "→ 2. In progress")
	assert.Contains(t, result, "✓ 3. Done")
	assert.Contains(t, result, "✗ 4. Failed")
}

func TestStatusIcon(t *testing.T) {
	assert.Equal(t, "✓", statusIcon("completed"))
	assert.Equal(t, "→", statusIcon("in_progress"))
	assert.Equal(t, "✗", statusIcon("failed"))
	assert.Equal(t, "○", statusIcon("pending"))
	assert.Equal(t, "○", statusIcon("unknown"))
}

// TestTaskTools_Create tests the task_create tool.
func TestTaskTools_Create(t *testing.T) {
	tl := NewTaskListAt(filepath.Join(t.TempDir(), "tasks.json"))
	tools := TaskTools(tl)
	require.Len(t, tools, 3)

	// Find task_create.
	var createTool Tool
	for _, tool := range tools {
		if tool.Def.Name == "task_create" {
			createTool = tool
			break
		}
	}
	require.Equal(t, "task_create", createTool.Def.Name)

	// Execute with valid args.
	args, _ := json.Marshal(map[string]string{"title": "Test task"})
	result, err := createTool.Execute(context.Background(), nil, args)
	require.NoError(t, err)
	assert.Contains(t, result, "Created task #1")
	assert.Equal(t, 1, tl.Len())

	// Execute with empty title.
	args, _ = json.Marshal(map[string]string{"title": ""})
	_, err = createTool.Execute(context.Background(), nil, args)
	assert.Error(t, err)
}

// TestTaskTools_Update tests the task_update tool.
func TestTaskTools_Update(t *testing.T) {
	tl := NewTaskListAt(filepath.Join(t.TempDir(), "tasks.json"))
	tl.Add("Update me")

	tools := TaskTools(tl)
	var updateTool Tool
	for _, tool := range tools {
		if tool.Def.Name == "task_update" {
			updateTool = tool
			break
		}
	}

	args, _ := json.Marshal(map[string]any{"id": 1, "status": "completed"})
	result, err := updateTool.Execute(context.Background(), nil, args)
	require.NoError(t, err)
	assert.Contains(t, result, "updated to completed")
	assert.Equal(t, "completed", tl.Get(1).Status)

	// Update non-existent.
	args, _ = json.Marshal(map[string]any{"id": 99, "status": "failed"})
	_, err = updateTool.Execute(context.Background(), nil, args)
	assert.Error(t, err)
}

// TestTaskTools_List tests the task_list tool.
func TestTaskTools_List(t *testing.T) {
	tl := NewTaskListAt(filepath.Join(t.TempDir(), "tasks.json"))
	tl.Add("Task A")
	tl.Add("Task B")

	tools := TaskTools(tl)
	var listTool Tool
	for _, tool := range tools {
		if tool.Def.Name == "task_list" {
			listTool = tool
			break
		}
	}

	result, err := listTool.Execute(context.Background(), nil, json.RawMessage("{}"))
	require.NoError(t, err)
	assert.Contains(t, result, "Task A")
	assert.Contains(t, result, "Task B")
}

// TestTaskList_ConcurrentAccess verifies mutex safety.
func TestTaskList_ConcurrentAccess(t *testing.T) {
	tl := NewTaskListAt(filepath.Join(t.TempDir(), "tasks.json"))

	done := make(chan struct{}, 10)
	for i := 0; i < 10; i++ {
		go func(n int) {
			tl.Add("concurrent task")
			tl.All()
			tl.Len()
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}

	assert.Equal(t, 10, tl.Len())
}
