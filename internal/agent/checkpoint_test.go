package agent

import (
	"context"
	"testing"

	"github.com/marmutapp/openmarmut/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckpointStore_StartTurn(t *testing.T) {
	cs := NewCheckpointStore()
	assert.Equal(t, 0, cs.Len())

	cs.StartTurn()
	assert.Equal(t, 1, cs.Len())
	assert.Equal(t, 0, cs.Checkpoints()[0].ID)

	cs.StartTurn()
	assert.Equal(t, 2, cs.Len())
	assert.Equal(t, 1, cs.Checkpoints()[1].ID)
}

func TestCheckpointStore_CaptureFile_ExistingFile(t *testing.T) {
	cs := NewCheckpointStore()
	rt := newMockRuntime("/tmp")
	rt.files["main.go"] = []byte("package main\n")

	cs.StartTurn()
	cs.CaptureFile(context.Background(), rt, "main.go")

	cps := cs.Checkpoints()
	require.Len(t, cps, 1)
	snap, ok := cps[0].Files["main.go"]
	require.True(t, ok)
	assert.True(t, snap.Existed)
	assert.Equal(t, []byte("package main\n"), snap.Content)
}

func TestCheckpointStore_CaptureFile_NewFile(t *testing.T) {
	cs := NewCheckpointStore()
	rt := newMockRuntime("/tmp")
	// File doesn't exist in mock runtime.

	cs.StartTurn()
	cs.CaptureFile(context.Background(), rt, "new.go")

	cps := cs.Checkpoints()
	require.Len(t, cps, 1)
	snap, ok := cps[0].Files["new.go"]
	require.True(t, ok)
	assert.False(t, snap.Existed)
	assert.Nil(t, snap.Content)
}

func TestCheckpointStore_CaptureFile_NoDuplicates(t *testing.T) {
	cs := NewCheckpointStore()
	rt := newMockRuntime("/tmp")
	rt.files["main.go"] = []byte("v1")

	cs.StartTurn()
	cs.CaptureFile(context.Background(), rt, "main.go")

	// Modify file in mock, re-capture — should keep the first snapshot.
	rt.files["main.go"] = []byte("v2")
	cs.CaptureFile(context.Background(), rt, "main.go")

	snap := cs.Checkpoints()[0].Files["main.go"]
	assert.Equal(t, []byte("v1"), snap.Content)
}

func TestCheckpointStore_MaxCheckpoints(t *testing.T) {
	cs := NewCheckpointStore()
	rt := newMockRuntime("/tmp")

	for i := 0; i < maxCheckpoints+10; i++ {
		cs.StartTurn()
		cs.CaptureFile(context.Background(), rt, "file.go")
	}

	assert.LessOrEqual(t, cs.Len(), maxCheckpoints)
}

func TestCheckpointStore_Rewind_RestoreFile(t *testing.T) {
	cs := NewCheckpointStore()
	rt := newMockRuntime("/tmp")
	rt.files["main.go"] = []byte("original content")

	// Capture the file before "modifying" it.
	cs.StartTurn()
	cs.CaptureFile(context.Background(), rt, "main.go")

	// Simulate agent modifying the file.
	rt.files["main.go"] = []byte("modified content")

	// Rewind should restore original.
	actions, err := cs.Rewind(context.Background(), rt, 1)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	assert.Equal(t, "main.go", actions[0].Path)
	assert.Equal(t, "restored", actions[0].Action)
	assert.Equal(t, []byte("original content"), rt.files["main.go"])
}

func TestCheckpointStore_Rewind_DeleteNewFile(t *testing.T) {
	cs := NewCheckpointStore()
	rt := newMockRuntime("/tmp")
	// File doesn't exist yet.

	cs.StartTurn()
	cs.CaptureFile(context.Background(), rt, "new.go")

	// Simulate agent creating the file.
	rt.files["new.go"] = []byte("new content")

	// Rewind should delete the newly created file.
	actions, err := cs.Rewind(context.Background(), rt, 1)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	assert.Equal(t, "new.go", actions[0].Path)
	assert.Equal(t, "deleted", actions[0].Action)
	_, exists := rt.files["new.go"]
	assert.False(t, exists)
}

func TestCheckpointStore_Rewind_MultipleCheckpoints(t *testing.T) {
	cs := NewCheckpointStore()
	rt := newMockRuntime("/tmp")
	rt.files["a.go"] = []byte("a-original")
	rt.files["b.go"] = []byte("b-original")

	// Turn 1: modify a.go.
	cs.StartTurn()
	cs.CaptureFile(context.Background(), rt, "a.go")
	rt.files["a.go"] = []byte("a-modified")

	// Turn 2: modify b.go.
	cs.StartTurn()
	cs.CaptureFile(context.Background(), rt, "b.go")
	rt.files["b.go"] = []byte("b-modified")

	// Rewind 2 turns.
	actions, err := cs.Rewind(context.Background(), rt, 2)
	require.NoError(t, err)
	assert.Len(t, actions, 2)
	assert.Equal(t, []byte("a-original"), rt.files["a.go"])
	assert.Equal(t, []byte("b-original"), rt.files["b.go"])
	assert.Equal(t, 0, cs.Len())
}

func TestCheckpointStore_Rewind_Zero(t *testing.T) {
	cs := NewCheckpointStore()
	actions, err := cs.Rewind(context.Background(), newMockRuntime("/tmp"), 0)
	require.NoError(t, err)
	assert.Nil(t, actions)
}

func TestCheckpointStore_Rewind_MoreThanAvailable(t *testing.T) {
	cs := NewCheckpointStore()
	rt := newMockRuntime("/tmp")
	rt.files["a.go"] = []byte("original")

	cs.StartTurn()
	cs.CaptureFile(context.Background(), rt, "a.go")
	rt.files["a.go"] = []byte("modified")

	// Request rewind of 10 but only 1 checkpoint exists.
	actions, err := cs.Rewind(context.Background(), rt, 10)
	require.NoError(t, err)
	assert.Len(t, actions, 1)
	assert.Equal(t, []byte("original"), rt.files["a.go"])
}

func TestCheckpointStore_HasChanges(t *testing.T) {
	cs := NewCheckpointStore()
	assert.False(t, cs.HasChanges())

	cs.StartTurn()
	assert.False(t, cs.HasChanges())

	rt := newMockRuntime("/tmp")
	cs.CaptureFile(context.Background(), rt, "file.go")
	assert.True(t, cs.HasChanges())
}

func TestCheckpointStore_LastN(t *testing.T) {
	cs := NewCheckpointStore()
	rt := newMockRuntime("/tmp")

	for i := 0; i < 5; i++ {
		cs.StartTurn()
		cs.CaptureFile(context.Background(), rt, "file.go")
	}

	last := cs.LastN(3)
	assert.Len(t, last, 3)
	assert.Equal(t, 2, last[0].ID)
	assert.Equal(t, 4, last[2].ID)
}

func TestCheckpointStore_LastN_Zero(t *testing.T) {
	cs := NewCheckpointStore()
	assert.Nil(t, cs.LastN(0))
}

func TestCheckpointStore_SetCheckpoints(t *testing.T) {
	cs := NewCheckpointStore()
	cps := []Checkpoint{
		{ID: 5, Files: map[string]FileSnapshot{"a.go": {Path: "a.go", Existed: true}}},
		{ID: 10, Files: map[string]FileSnapshot{"b.go": {Path: "b.go", Existed: false}}},
	}
	cs.SetCheckpoints(cps)

	assert.Equal(t, 2, cs.Len())
	// Next ID should be 11 (max ID + 1).
	cs.StartTurn()
	assert.Equal(t, 11, cs.Checkpoints()[2].ID)
}

// Test checkpoint integration with agent.
func TestAgent_CheckpointCapture(t *testing.T) {
	rt := newMockRuntime("/test")
	rt.files["existing.go"] = []byte("package main")

	cs := NewCheckpointStore()

	mp := &mockProvider{
		name:  "test",
		model: "test",
		responses: []*llm.Response{
			{
				Content: "",
				ToolCalls: []llm.ToolCall{
					{ID: "1", Name: "write_file", Arguments: `{"path":"existing.go","content":"package main\nfunc main(){}"}`},
				},
			},
			{Content: "done"},
		},
	}

	ag := New(mp, rt, testLogger,
		WithCheckpointStore(cs),
		WithMaxIterations(3),
	)

	_, err := ag.Run(context.Background(), "update existing.go", nil)
	require.NoError(t, err)

	// Should have one checkpoint with one file.
	require.Equal(t, 1, cs.Len())
	snap, ok := cs.Checkpoints()[0].Files["existing.go"]
	require.True(t, ok)
	assert.True(t, snap.Existed)
	assert.Equal(t, []byte("package main"), snap.Content)
}

func TestAgent_CheckpointCapture_NewFile(t *testing.T) {
	rt := newMockRuntime("/test")
	cs := NewCheckpointStore()

	mp := &mockProvider{
		name:  "test",
		model: "test",
		responses: []*llm.Response{
			{
				Content: "",
				ToolCalls: []llm.ToolCall{
					{ID: "1", Name: "write_file", Arguments: `{"path":"new.go","content":"package new"}`},
				},
			},
			{Content: "done"},
		},
	}

	ag := New(mp, rt, testLogger,
		WithCheckpointStore(cs),
		WithMaxIterations(3),
	)

	_, err := ag.Run(context.Background(), "create new.go", nil)
	require.NoError(t, err)

	snap, ok := cs.Checkpoints()[0].Files["new.go"]
	require.True(t, ok)
	assert.False(t, snap.Existed)
}
