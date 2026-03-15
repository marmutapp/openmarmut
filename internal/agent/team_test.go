package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gajaai/openmarmut-go/internal/llm"
	"github.com/gajaai/openmarmut-go/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// teamTestLogger is a silent logger for team tests.
var teamTestLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// --- Mock provider for team tests ---

// mockTeamProvider implements llm.Provider for testing team orchestration.
type mockTeamProvider struct {
	name      string
	model     string
	mu        sync.Mutex
	calls     int
	responses []llm.Response // Responses to return in order.
}

func newMockTeamProvider(name string, responses ...llm.Response) *mockTeamProvider {
	return &mockTeamProvider{
		name:      name,
		model:     "mock-model",
		responses: responses,
	}
}

func (m *mockTeamProvider) Complete(ctx context.Context, req llm.Request, cb llm.StreamCallback) (*llm.Response, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	m.mu.Lock()
	idx := m.calls
	m.calls++
	m.mu.Unlock()

	if idx < len(m.responses) {
		resp := m.responses[idx]
		return &resp, nil
	}
	// Default: return a simple text response.
	return &llm.Response{
		Content: "done",
		Usage:   llm.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
	}, nil
}

func (m *mockTeamProvider) Name() string  { return m.name }
func (m *mockTeamProvider) Model() string { return m.model }

// --- Mock runtime for team tests ---

type mockTeamRuntime struct {
	targetDir string
	mu        sync.Mutex
	files     map[string][]byte
	execCount int
}

func newMockTeamRuntime() *mockTeamRuntime {
	return &mockTeamRuntime{
		targetDir: "/tmp/team-test",
		files:     make(map[string][]byte),
	}
}

func (m *mockTeamRuntime) Init(ctx context.Context) error { return nil }
func (m *mockTeamRuntime) Close(ctx context.Context) error { return nil }
func (m *mockTeamRuntime) TargetDir() string { return m.targetDir }

func (m *mockTeamRuntime) ReadFile(ctx context.Context, relPath string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.files[relPath]
	if !ok {
		return nil, fmt.Errorf("file not found: %s", relPath)
	}
	return data, nil
}

func (m *mockTeamRuntime) WriteFile(ctx context.Context, relPath string, data []byte, perm os.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[relPath] = data
	return nil
}

func (m *mockTeamRuntime) DeleteFile(ctx context.Context, relPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, relPath)
	return nil
}

func (m *mockTeamRuntime) ListDir(ctx context.Context, relPath string) ([]runtime.FileEntry, error) {
	return nil, nil
}

func (m *mockTeamRuntime) MkDir(ctx context.Context, relPath string, perm os.FileMode) error {
	return nil
}

func (m *mockTeamRuntime) Exec(ctx context.Context, command string, opts runtime.ExecOpts) (*runtime.ExecResult, error) {
	m.mu.Lock()
	m.execCount++
	m.mu.Unlock()
	return &runtime.ExecResult{ExitCode: 0, Stdout: "ok"}, nil
}

// --- Tests ---

func TestParsePlanTasks(t *testing.T) {
	tests := []struct {
		name string
		plan string
		want []string
	}{
		{
			name: "numbered list",
			plan: "1. Create the config module\n2. Add validation logic\n3. Write tests",
			want: []string{"Create the config module", "Add validation logic", "Write tests"},
		},
		{
			name: "numbered with parens",
			plan: "1) Create the config module\n2) Add validation logic",
			want: []string{"Create the config module", "Add validation logic"},
		},
		{
			name: "dash list",
			plan: "- Create the config module\n- Add validation logic\n- Write tests",
			want: []string{"Create the config module", "Add validation logic", "Write tests"},
		},
		{
			name: "mixed with blank lines",
			plan: "Here is the plan:\n\n1. Create config\n\n2. Add validation\n\n",
			want: []string{"Create config", "Add validation"},
		},
		{
			name: "empty plan",
			plan: "",
			want: nil,
		},
		{
			name: "no tasks",
			plan: "This is just text with no numbered items.",
			want: nil,
		},
		{
			name: "double digit numbers",
			plan: "10. Task ten\n11. Task eleven",
			want: []string{"Task ten", "Task eleven"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePlanTasks(tt.plan)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAggregateUsage(t *testing.T) {
	u1 := llm.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
	u2 := llm.Usage{PromptTokens: 200, CompletionTokens: 100, TotalTokens: 300}
	u3 := llm.Usage{PromptTokens: 50, CompletionTokens: 25, TotalTokens: 75}

	total := aggregateUsage(u1, u2, u3)
	assert.Equal(t, 350, total.PromptTokens)
	assert.Equal(t, 175, total.CompletionTokens)
	assert.Equal(t, 525, total.TotalTokens)
}

func TestNewTeam_Defaults(t *testing.T) {
	rt := newMockTeamRuntime()
	provider := newMockTeamProvider("test", llm.Response{Content: "ok"})
	logger := teamTestLogger

	team := NewTeam("", "test task", TeamConfig{}, rt, provider, provider, logger)
	assert.Contains(t, team.Name, "team-")
	assert.Equal(t, DefaultMaxMembers, team.Config.MaxMembers)
	assert.Equal(t, DefaultTeamStrategy, team.Config.Strategy)
	assert.NotNil(t, team.TaskList)
	assert.NotNil(t, team.FileLock)
}

func TestNewTeam_CustomConfig(t *testing.T) {
	rt := newMockTeamRuntime()
	provider := newMockTeamProvider("test", llm.Response{Content: "ok"})
	logger := teamTestLogger

	cfg := TeamConfig{
		MaxMembers: 5,
		Strategy:   "sequential",
	}
	team := NewTeam("my-team", "test task", cfg, rt, provider, provider, logger)
	assert.Equal(t, "my-team", team.Name)
	assert.Equal(t, 5, team.Config.MaxMembers)
	assert.Equal(t, "sequential", team.Config.Strategy)
}

func TestTeam_Cancel(t *testing.T) {
	rt := newMockTeamRuntime()
	provider := newMockTeamProvider("test", llm.Response{Content: "ok"})
	logger := teamTestLogger

	team := NewTeam("cancel-test", "test task", TeamConfig{}, rt, provider, provider, logger)

	// Set up a cancel function.
	_, cancel := context.WithCancel(context.Background())
	team.cancel = cancel

	team.Cancel()
	assert.Equal(t, TeamStatusCancelled, team.Status)
}

func TestTeam_StatusSnapshot(t *testing.T) {
	rt := newMockTeamRuntime()
	provider := newMockTeamProvider("test", llm.Response{Content: "ok"})
	logger := teamTestLogger

	team := NewTeam("snap-test", "test task", TeamConfig{}, rt, provider, provider, logger)
	team.Status = TeamStatusExecuting

	// Add some tasks and workers.
	team.TaskList.Add("Task A")
	team.TaskList.Add("Task B")
	team.Workers = []*TeamWorker{
		{Name: "worker-1", TaskID: 1, Status: "running"},
		{Name: "worker-2", TaskID: 2, Status: "completed"},
	}

	snap := team.StatusSnapshot()
	assert.Equal(t, "snap-test", snap.Name)
	assert.Equal(t, TeamStatusExecuting, snap.Status)
	assert.Len(t, snap.Tasks, 2)
	assert.Len(t, snap.Workers, 2)
	assert.Equal(t, "worker-1", snap.Workers[0].Name)
	assert.Equal(t, "running", snap.Workers[0].Status)
}

func TestTeam_RunParallel(t *testing.T) {
	rt := newMockTeamRuntime()
	logger := teamTestLogger

	// Lead provider returns a plan with 2 tasks, then a summary.
	leadProvider := newMockTeamProvider("lead",
		llm.Response{
			Content: "1. Create module A\n2. Create module B",
			Usage:   llm.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
		},
		llm.Response{
			Content: "All tasks completed successfully. Changes verified.",
			Usage:   llm.Usage{PromptTokens: 200, CompletionTokens: 100, TotalTokens: 300},
		},
	)
	workerProvider := newMockTeamProvider("worker",
		llm.Response{
			Content: "Module A created.",
			Usage:   llm.Usage{PromptTokens: 80, CompletionTokens: 40, TotalTokens: 120},
		},
		llm.Response{
			Content: "Module B created.",
			Usage:   llm.Usage{PromptTokens: 80, CompletionTokens: 40, TotalTokens: 120},
		},
	)

	team := NewTeam("parallel-test", "Create modules A and B", TeamConfig{
		MaxMembers: 2,
		Strategy:   "parallel",
	}, rt, leadProvider, workerProvider, logger)

	result, err := team.Run(context.Background(), nil)
	require.NoError(t, err)

	assert.Equal(t, "parallel-test", result.TeamName)
	assert.Equal(t, "parallel", result.Strategy)
	assert.Equal(t, 2, result.TotalTasks)
	assert.Equal(t, 2, result.CompletedTasks)
	assert.Len(t, result.Workers, 2)
	assert.Equal(t, TeamStatusCompleted, team.Status)
	assert.True(t, result.Duration > 0)
	assert.True(t, result.TotalUsage.TotalTokens > 0)
}

func TestTeam_RunSequential(t *testing.T) {
	rt := newMockTeamRuntime()
	logger := teamTestLogger

	leadProvider := newMockTeamProvider("lead",
		llm.Response{
			Content: "1. Step one\n2. Step two",
			Usage:   llm.Usage{TotalTokens: 100},
		},
		llm.Response{
			Content: "Sequential execution completed.",
			Usage:   llm.Usage{TotalTokens: 100},
		},
	)
	workerProvider := newMockTeamProvider("worker",
		llm.Response{Content: "Step one done.", Usage: llm.Usage{TotalTokens: 80}},
		llm.Response{Content: "Step two done.", Usage: llm.Usage{TotalTokens: 80}},
	)

	team := NewTeam("seq-test", "Do steps sequentially", TeamConfig{
		MaxMembers: 2,
		Strategy:   "sequential",
	}, rt, leadProvider, workerProvider, logger)

	result, err := team.Run(context.Background(), nil)
	require.NoError(t, err)

	assert.Equal(t, "sequential", result.Strategy)
	assert.Equal(t, 2, result.CompletedTasks)
}

func TestTeam_RunNoTasks(t *testing.T) {
	rt := newMockTeamRuntime()
	logger := teamTestLogger

	// Lead returns a plan with no parseable tasks.
	leadProvider := newMockTeamProvider("lead",
		llm.Response{Content: "I don't know how to break this down.", Usage: llm.Usage{TotalTokens: 100}},
	)
	workerProvider := newMockTeamProvider("worker")

	team := NewTeam("no-tasks", "Vague task", TeamConfig{}, rt, leadProvider, workerProvider, logger)

	_, err := team.Run(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no tasks")
}

func TestTeam_RunContextCancelled(t *testing.T) {
	rt := newMockTeamRuntime()
	logger := teamTestLogger

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	leadProvider := newMockTeamProvider("lead")
	workerProvider := newMockTeamProvider("worker")

	team := NewTeam("cancel-run", "Task", TeamConfig{}, rt, leadProvider, workerProvider, logger)

	_, err := team.Run(ctx, nil)
	require.Error(t, err)
}

func TestFormatTeamResult(t *testing.T) {
	result := &TeamResult{
		TeamName:       "test-team",
		Task:           "build something",
		Strategy:       "parallel",
		TotalTasks:     3,
		CompletedTasks: 2,
		Duration:       5 * time.Second,
		Workers: []*TeamWorker{
			{Name: "worker-1", Status: "completed", Steps: 5, Usage: llm.Usage{TotalTokens: 200}, Duration: 2 * time.Second},
			{Name: "worker-2", Status: "completed", Steps: 3, Usage: llm.Usage{TotalTokens: 150}, Duration: 3 * time.Second},
			{Name: "worker-3", Status: "failed", Error: "timeout"},
		},
		PlanUsage:        llm.Usage{TotalTokens: 100},
		WorkerUsage:      llm.Usage{TotalTokens: 350},
		IntegrationUsage: llm.Usage{TotalTokens: 50},
		TotalUsage:       llm.Usage{TotalTokens: 500},
		Summary:          "Two out of three tasks completed.",
	}

	output := FormatTeamResult(result)
	assert.Contains(t, output, "test-team")
	assert.Contains(t, output, "2/3 completed")
	assert.Contains(t, output, "worker-1")
	assert.Contains(t, output, "worker-2")
	assert.Contains(t, output, "Two out of three tasks completed")
}

func TestFormatTeamSnapshot(t *testing.T) {
	snap := TeamSnapshot{
		Name:     "snap-team",
		Status:   TeamStatusExecuting,
		Strategy: "parallel",
		Tasks: []Task{
			{ID: 1, Title: "Task A", Status: "completed"},
			{ID: 2, Title: "Task B", Status: "in_progress"},
			{ID: 3, Title: "Task C", Status: "pending"},
		},
		Workers: []WorkerSnapshot{
			{Name: "worker-1", TaskID: 1, Status: "completed"},
			{Name: "worker-2", TaskID: 2, Status: "running"},
		},
	}

	output := FormatTeamSnapshot(snap)
	assert.Contains(t, output, "snap-team")
	assert.Contains(t, output, "executing")
	assert.Contains(t, output, "Task A")
	assert.Contains(t, output, "worker-1")
}

func TestTeamManager_TrackAndList(t *testing.T) {
	mgr := NewTeamManager()
	rt := newMockTeamRuntime()
	provider := newMockTeamProvider("test")
	logger := teamTestLogger

	t1 := NewTeam("team-1", "task 1", TeamConfig{}, rt, provider, provider, logger)
	t2 := NewTeam("team-2", "task 2", TeamConfig{}, rt, provider, provider, logger)

	mgr.Track(t1)
	mgr.Track(t2)

	teams := mgr.List()
	assert.Len(t, teams, 2)
	assert.Equal(t, "team-1", teams[0].Name)
	assert.Equal(t, "team-2", teams[1].Name)
}

func TestTeamManager_CancelAll(t *testing.T) {
	mgr := NewTeamManager()

	_, cancel1 := context.WithCancel(context.Background())
	_, cancel2 := context.WithCancel(context.Background())

	mgr.mu.Lock()
	mgr.running["team-1"] = cancel1
	mgr.running["team-2"] = cancel2
	mgr.mu.Unlock()

	assert.True(t, mgr.HasRunning())

	mgr.CancelAll()

	assert.False(t, mgr.HasRunning())
}

func TestWrapToolsWithFileLock(t *testing.T) {
	fl := NewFileLock()
	callCount := 0

	tools := []Tool{
		{
			Def: llm.ToolDef{Name: "read_file"},
			Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
				callCount++
				return "read", nil
			},
		},
		{
			Def: llm.ToolDef{Name: "write_file"},
			Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
				callCount++
				return "written", nil
			},
		},
	}

	wrapped := WrapToolsWithFileLock(tools, fl, "worker-1", 5*time.Second)

	// read_file should not be wrapped (no lock behavior).
	result, err := wrapped[0].Execute(context.Background(), nil, json.RawMessage(`{"path":"test.go"}`))
	require.NoError(t, err)
	assert.Equal(t, "read", result)

	// write_file should be wrapped.
	result, err = wrapped[1].Execute(context.Background(), nil, json.RawMessage(`{"path":"test.go"}`))
	require.NoError(t, err)
	assert.Equal(t, "written", result)

	assert.Equal(t, 2, callCount)
}

func TestLockedRuntime_WriteFile(t *testing.T) {
	fl := NewFileLock()
	baseRT := newMockTeamRuntime()

	lr := &lockedRuntime{
		Runtime:  baseRT,
		fileLock: fl,
		worker:   "worker-1",
		timeout:  5 * time.Second,
	}

	err := lr.WriteFile(context.Background(), "test.go", []byte("content"), 0o644)
	require.NoError(t, err)

	baseRT.mu.Lock()
	assert.Equal(t, []byte("content"), baseRT.files["test.go"])
	baseRT.mu.Unlock()

	// Lock should be released after WriteFile returns.
	assert.Equal(t, "", fl.Holder("test.go"))
}

func TestLockedRuntime_DeleteFile(t *testing.T) {
	fl := NewFileLock()
	baseRT := newMockTeamRuntime()
	baseRT.files["delete-me.go"] = []byte("data")

	lr := &lockedRuntime{
		Runtime:  baseRT,
		fileLock: fl,
		worker:   "worker-1",
		timeout:  5 * time.Second,
	}

	err := lr.DeleteFile(context.Background(), "delete-me.go")
	require.NoError(t, err)

	baseRT.mu.Lock()
	_, exists := baseRT.files["delete-me.go"]
	baseRT.mu.Unlock()
	assert.False(t, exists)
}
