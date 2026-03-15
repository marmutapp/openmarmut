package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gajaai/openmarmut-go/internal/config"
	"github.com/gajaai/openmarmut-go/internal/llm"
	"github.com/gajaai/openmarmut-go/internal/runtime"
)

// Team Status constants.
const (
	TeamStatusPlanning  = "planning"
	TeamStatusExecuting = "executing"
	TeamStatusCompleted = "completed"
	TeamStatusFailed    = "failed"
	TeamStatusCancelled = "cancelled"
)

// Default team configuration values.
const (
	DefaultMaxMembers    = 3
	DefaultTeamStrategy  = "parallel"
	DefaultWorkerMaxIter = 15
	DefaultLeadMaxIter   = 20
)

// TeamConfig configures team behavior.
type TeamConfig struct {
	MaxMembers     int    `yaml:"max_members"`     // Max parallel agents (default 3).
	LeadProvider   string `yaml:"lead_provider"`   // Provider name for lead agent (planning).
	WorkerProvider string `yaml:"worker_provider"` // Provider name for workers (execution).
	Strategy       string `yaml:"strategy"`        // "parallel" or "sequential".
}

// TeamWorker tracks a single worker within a team.
type TeamWorker struct {
	Name     string
	TaskID   int           // ID in the shared TaskList.
	Status   string        // "idle", "running", "completed", "failed"
	Result   string        // Final output.
	Error    string        // Error message if failed.
	Usage    llm.Usage     // Token usage.
	Duration time.Duration // Wall clock time.
	Steps    int           // Number of tool calls.
}

// Team orchestrates multiple agents working on coordinated tasks.
type Team struct {
	Name     string
	Task     string        // Original task description.
	Lead     *Agent        // Orchestrator agent for planning/review.
	Workers  []*TeamWorker // Worker agents.
	TaskList *TaskList     // Shared task list.
	FileLock *FileLock     // Per-path file locking.
	Config   TeamConfig
	Status   string // "planning", "executing", "completed", "failed", "cancelled"

	// Internal fields.
	mu             sync.Mutex
	rt             runtime.Runtime
	leadProvider   llm.Provider
	workerProvider llm.Provider
	logger         *slog.Logger
	appCfg         *config.Config
	ignoreList     *IgnoreList
	cancel         context.CancelFunc
	startTime      time.Time
	endTime        time.Time
	planResult     string // Plan produced by lead agent.
	summaryResult  string // Final summary from lead agent.
}

// teamCounter generates unique IDs for unnamed teams.
var teamCounter atomic.Int64

// NewTeam creates a new team for the given task.
func NewTeam(name string, task string, cfg TeamConfig, rt runtime.Runtime, leadProvider, workerProvider llm.Provider, logger *slog.Logger, opts ...TeamOption) *Team {
	if name == "" {
		name = fmt.Sprintf("team-%d", teamCounter.Add(1))
	}
	if cfg.MaxMembers <= 0 {
		cfg.MaxMembers = DefaultMaxMembers
	}
	if cfg.Strategy == "" {
		cfg.Strategy = DefaultTeamStrategy
	}

	t := &Team{
		Name:           name,
		Task:           task,
		Config:         cfg,
		Status:         TeamStatusPlanning,
		TaskList:       NewTaskListAt(""), // In-memory only.
		FileLock:       NewFileLock(),
		rt:             rt,
		leadProvider:   leadProvider,
		workerProvider: workerProvider,
		logger:         logger,
	}

	// Use in-memory task list (no file path).
	t.TaskList = &TaskList{}

	for _, opt := range opts {
		opt(t)
	}

	return t
}

// TeamOption configures a Team.
type TeamOption func(*Team)

// WithTeamConfig sets the app config for context window settings.
func WithTeamConfig(cfg *config.Config) TeamOption {
	return func(t *Team) { t.appCfg = cfg }
}

// WithTeamIgnoreList sets the ignore list for team agents.
func WithTeamIgnoreList(il *IgnoreList) TeamOption {
	return func(t *Team) { t.ignoreList = il }
}

// Run executes the full team workflow: plan → execute → integrate.
func (t *Team) Run(ctx context.Context, stream llm.StreamCallback) (*TeamResult, error) {
	t.startTime = time.Now()
	ctx, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	defer cancel()

	result := &TeamResult{
		TeamName: t.Name,
		Task:     t.Task,
		Strategy: t.Config.Strategy,
	}

	// Phase 1: Planning — lead agent breaks the task into subtasks.
	t.logger.Info("team: planning phase", "team", t.Name, "task", t.Task)
	t.setStatus(TeamStatusPlanning)

	plan, planUsage, err := t.runPlanningPhase(ctx, stream)
	if err != nil {
		t.setStatus(TeamStatusFailed)
		return nil, fmt.Errorf("team.Run(%s): planning: %w", t.Name, err)
	}
	t.planResult = plan
	result.PlanUsage = planUsage

	// Parse tasks from the plan and add to shared TaskList.
	tasks := parsePlanTasks(plan)
	if len(tasks) == 0 {
		t.setStatus(TeamStatusFailed)
		return nil, fmt.Errorf("team.Run(%s): planning produced no tasks", t.Name)
	}

	// Cap tasks to MaxMembers for parallel execution.
	for _, taskTitle := range tasks {
		t.TaskList.Add(taskTitle)
	}
	result.TotalTasks = len(tasks)

	// Phase 2: Execution — workers run tasks.
	t.logger.Info("team: execution phase", "team", t.Name, "tasks", len(tasks), "strategy", t.Config.Strategy)
	t.setStatus(TeamStatusExecuting)

	workers, workerUsage, execErr := t.runExecutionPhase(ctx, tasks)
	t.Workers = workers
	result.WorkerUsage = workerUsage
	result.Workers = workers

	if execErr != nil {
		// Partial failure is okay — some workers may have succeeded.
		t.logger.Warn("team: execution had failures", "team", t.Name, "error", execErr)
	}

	// Phase 3: Integration — lead agent reviews results.
	t.logger.Info("team: integration phase", "team", t.Name)
	summary, integUsage, integErr := t.runIntegrationPhase(ctx, workers, stream)
	if integErr != nil {
		t.logger.Warn("team: integration failed", "team", t.Name, "error", integErr)
		summary = "Integration review failed: " + integErr.Error()
	}
	t.summaryResult = summary
	result.Summary = summary
	result.IntegrationUsage = integUsage

	// Compute totals.
	result.TotalUsage = aggregateUsage(planUsage, workerUsage, integUsage)
	t.endTime = time.Now()
	result.Duration = t.endTime.Sub(t.startTime)

	completedCount := 0
	for _, w := range workers {
		if w.Status == "completed" {
			completedCount++
		}
	}
	result.CompletedTasks = completedCount

	if completedCount == len(tasks) {
		t.setStatus(TeamStatusCompleted)
	} else if completedCount > 0 {
		t.setStatus(TeamStatusCompleted) // Partial success still counts as completed.
	} else {
		t.setStatus(TeamStatusFailed)
	}

	return result, nil
}

// Cancel stops all team execution.
func (t *Team) Cancel() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancel != nil {
		t.cancel()
	}
	t.Status = TeamStatusCancelled
}

// StatusSnapshot returns a snapshot of the current team state for display.
func (t *Team) StatusSnapshot() TeamSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()

	snap := TeamSnapshot{
		Name:     t.Name,
		Status:   t.Status,
		Strategy: t.Config.Strategy,
		Tasks:    t.TaskList.All(),
	}

	for _, w := range t.Workers {
		snap.Workers = append(snap.Workers, WorkerSnapshot{
			Name:   w.Name,
			TaskID: w.TaskID,
			Status: w.Status,
		})
	}

	return snap
}

func (t *Team) setStatus(status string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Status = status
}

// runPlanningPhase uses the lead agent to analyze the task and produce subtasks.
func (t *Team) runPlanningPhase(ctx context.Context, stream llm.StreamCallback) (string, llm.Usage, error) {
	planPrompt := fmt.Sprintf(`You are the LEAD AGENT of a team. Analyze this task and break it into subtasks for parallel execution.

Task: %s

Rules:
- Break the task into %d or fewer independent subtasks.
- Each subtask should be completable by a single agent.
- Subtasks should minimize file conflicts (different files when possible).
- Output ONLY a numbered list of subtasks, one per line, like:
  1. <subtask description>
  2. <subtask description>
  ...

Be specific about what files to create/modify in each subtask.`, t.Task, t.Config.MaxMembers)

	var agentOpts []Option
	agentOpts = append(agentOpts, WithMaxIterations(DefaultLeadMaxIter))
	agentOpts = append(agentOpts, WithSystemPrompt(planPrompt))
	if t.ignoreList != nil {
		agentOpts = append(agentOpts, WithIgnoreList(t.ignoreList))
	}
	if t.appCfg != nil {
		ctxCfg := DefaultContextConfig()
		if t.appCfg.Agent.ContextWindow > 0 {
			ctxCfg.ContextWindow = t.appCfg.Agent.ContextWindow
		}
		agentOpts = append(agentOpts, WithContextConfig(ctxCfg))
	}

	lead := New(t.leadProvider, t.rt, t.logger, agentOpts...)
	t.Lead = lead

	result, err := lead.Run(ctx, t.Task, stream)
	if err != nil {
		return "", llm.Usage{}, err
	}

	return result.Response, result.Usage, nil
}

// runExecutionPhase runs workers on the parsed tasks.
func (t *Team) runExecutionPhase(ctx context.Context, tasks []string) ([]*TeamWorker, llm.Usage, error) {
	workers := make([]*TeamWorker, len(tasks))
	for i, task := range tasks {
		workers[i] = &TeamWorker{
			Name:   fmt.Sprintf("worker-%d", i+1),
			TaskID: i + 1,
			Status: "idle",
		}
		_ = t.TaskList.Update(i+1, "pending")
		_ = tasks // suppress unused warning
		workers[i].Result = "" // Will be set after execution.
		// Store task for the goroutine.
		_ = task
	}

	var totalUsage llm.Usage
	var usageMu sync.Mutex
	var firstErr error
	var errOnce sync.Once

	if t.Config.Strategy == "sequential" {
		for i, task := range tasks {
			if ctx.Err() != nil {
				break
			}
			t.runWorker(ctx, workers[i], task, &totalUsage, &usageMu)
			if workers[i].Status == "failed" {
				errOnce.Do(func() { firstErr = fmt.Errorf("worker %s failed: %s", workers[i].Name, workers[i].Error) })
			}
		}
	} else {
		// Parallel execution.
		var wg sync.WaitGroup
		// Limit concurrency to MaxMembers.
		sem := make(chan struct{}, t.Config.MaxMembers)

		for i, task := range tasks {
			wg.Add(1)
			sem <- struct{}{} // Acquire semaphore slot.
			go func(idx int, taskDesc string) {
				defer wg.Done()
				defer func() { <-sem }() // Release semaphore slot.

				t.runWorker(ctx, workers[idx], taskDesc, &totalUsage, &usageMu)
				if workers[idx].Status == "failed" {
					errOnce.Do(func() { firstErr = fmt.Errorf("worker %s failed: %s", workers[idx].Name, workers[idx].Error) })
				}
			}(i, task)
		}
		wg.Wait()
	}

	return workers, totalUsage, firstErr
}

// runWorker executes a single worker on its assigned task.
func (t *Team) runWorker(ctx context.Context, w *TeamWorker, task string, totalUsage *llm.Usage, usageMu *sync.Mutex) {
	w.Status = "running"
	_ = t.TaskList.Update(w.TaskID, "in_progress")

	workerPrompt := fmt.Sprintf(`You are WORKER %s in a team. Complete your assigned subtask.

Your subtask: %s

Rules:
- Focus only on your assigned subtask.
- Before writing to a file, announce which file you're about to modify.
- If a file write fails due to a lock, wait briefly and retry.
- When done, summarize what you changed.`, w.Name, task)

	var agentOpts []Option
	agentOpts = append(agentOpts, WithMaxIterations(DefaultWorkerMaxIter))
	agentOpts = append(agentOpts, WithSystemPrompt(workerPrompt))
	if t.ignoreList != nil {
		agentOpts = append(agentOpts, WithIgnoreList(t.ignoreList))
	}
	if t.appCfg != nil {
		ctxCfg := DefaultContextConfig()
		if t.appCfg.Agent.ContextWindow > 0 {
			ctxCfg.ContextWindow = t.appCfg.Agent.ContextWindow
		}
		agentOpts = append(agentOpts, WithContextConfig(ctxCfg))
	}

	// Wrap the runtime with file locking for write operations.
	lockedRT := &lockedRuntime{
		Runtime:  t.rt,
		fileLock: t.FileLock,
		worker:   w.Name,
		timeout:  DefaultFileLockTimeout,
	}

	start := time.Now()
	ag := New(t.workerProvider, lockedRT, t.logger, agentOpts...)
	result, err := ag.Run(ctx, task, nil)
	w.Duration = time.Since(start)

	if err != nil {
		w.Status = "failed"
		w.Error = err.Error()
		_ = t.TaskList.Update(w.TaskID, "failed")
		t.logger.Warn("team: worker failed", "worker", w.Name, "error", err)
		return
	}

	w.Status = "completed"
	w.Result = result.Response
	w.Usage = result.Usage
	w.Steps = len(result.Steps)
	_ = t.TaskList.Update(w.TaskID, "completed")

	usageMu.Lock()
	totalUsage.PromptTokens += result.Usage.PromptTokens
	totalUsage.CompletionTokens += result.Usage.CompletionTokens
	totalUsage.TotalTokens += result.Usage.TotalTokens
	usageMu.Unlock()
}

// runIntegrationPhase has the lead agent review worker results.
func (t *Team) runIntegrationPhase(ctx context.Context, workers []*TeamWorker, stream llm.StreamCallback) (string, llm.Usage, error) {
	var sb strings.Builder
	sb.WriteString("Workers have completed their tasks. Review the results:\n\n")
	for _, w := range workers {
		sb.WriteString(fmt.Sprintf("### %s (Task #%d) — %s\n", w.Name, w.TaskID, w.Status))
		if w.Error != "" {
			sb.WriteString(fmt.Sprintf("Error: %s\n", w.Error))
		}
		if w.Result != "" {
			sb.WriteString(w.Result)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("Verify the changes work together, run tests if applicable, and provide a final summary.")

	if t.Lead == nil {
		return sb.String(), llm.Usage{}, nil
	}

	// Use the lead agent to review.
	result, err := t.Lead.Run(ctx, sb.String(), stream)
	if err != nil {
		return "", llm.Usage{}, err
	}

	return result.Response, result.Usage, nil
}

// TeamResult holds the outcome of a team execution.
type TeamResult struct {
	TeamName         string
	Task             string
	Strategy         string
	Summary          string
	TotalTasks       int
	CompletedTasks   int
	Workers          []*TeamWorker
	PlanUsage        llm.Usage
	WorkerUsage      llm.Usage
	IntegrationUsage llm.Usage
	TotalUsage       llm.Usage
	Duration         time.Duration
}

// FormatTeamResult returns a human-readable summary of a team execution.
func FormatTeamResult(r *TeamResult) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Team '%s' — %s\n", r.TeamName, r.Strategy))
	sb.WriteString(fmt.Sprintf("Tasks: %d/%d completed | Duration: %s\n\n", r.CompletedTasks, r.TotalTasks, r.Duration.Round(time.Millisecond)))

	for _, w := range r.Workers {
		icon := statusIcon(w.Status)
		sb.WriteString(fmt.Sprintf("  %s %s: %d steps, %d tokens, %s\n",
			icon, w.Name, w.Steps, w.Usage.TotalTokens, w.Duration.Round(time.Millisecond)))
	}

	sb.WriteString(fmt.Sprintf("\nCost breakdown: Plan ~%d tok + Workers ~%d tok + Review ~%d tok = ~%d tok total\n",
		r.PlanUsage.TotalTokens, r.WorkerUsage.TotalTokens, r.IntegrationUsage.TotalTokens, r.TotalUsage.TotalTokens))

	if r.Summary != "" {
		sb.WriteString("\n--- Summary ---\n")
		sb.WriteString(r.Summary)
		sb.WriteString("\n")
	}

	return sb.String()
}

// TeamSnapshot is a point-in-time view of team state for display.
type TeamSnapshot struct {
	Name     string
	Status   string
	Strategy string
	Tasks    []Task
	Workers  []WorkerSnapshot
}

// WorkerSnapshot is a point-in-time view of a worker for display.
type WorkerSnapshot struct {
	Name   string
	TaskID int
	Status string
}

// FormatTeamSnapshot returns a display-ready team status view.
func FormatTeamSnapshot(s TeamSnapshot) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Team: %s — %s (%s)\n\n", s.Name, s.Status, s.Strategy))

	sb.WriteString("Tasks:\n")
	for _, t := range s.Tasks {
		icon := statusIcon(t.Status)
		sb.WriteString(fmt.Sprintf("  %s %d. %s\n", icon, t.ID, t.Title))
	}

	if len(s.Workers) > 0 {
		sb.WriteString("\nWorkers:\n")
		for _, w := range s.Workers {
			icon := statusIcon(w.Status)
			sb.WriteString(fmt.Sprintf("  %s %s (task #%d)\n", icon, w.Name, w.TaskID))
		}
	}

	return sb.String()
}

// parsePlanTasks extracts numbered task lines from a plan response.
// Looks for lines matching "N. <description>" or "- <description>".
func parsePlanTasks(plan string) []string {
	var tasks []string
	for _, line := range strings.Split(plan, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Match "1. task description" or "1) task description".
		if len(line) >= 3 && line[0] >= '0' && line[0] <= '9' {
			// Find the separator after the number.
			for i := 1; i < len(line); i++ {
				if line[i] == '.' || line[i] == ')' {
					task := strings.TrimSpace(line[i+1:])
					if task != "" {
						tasks = append(tasks, task)
					}
					break
				}
				if line[i] < '0' || line[i] > '9' {
					break
				}
			}
		}
		// Match "- task description".
		if strings.HasPrefix(line, "- ") {
			task := strings.TrimSpace(line[2:])
			if task != "" {
				tasks = append(tasks, task)
			}
		}
	}
	return tasks
}

// aggregateUsage sums multiple Usage values.
func aggregateUsage(usages ...llm.Usage) llm.Usage {
	var total llm.Usage
	for _, u := range usages {
		total.PromptTokens += u.PromptTokens
		total.CompletionTokens += u.CompletionTokens
		total.TotalTokens += u.TotalTokens
	}
	return total
}

// lockedRuntime wraps a Runtime to add file locking on write operations.
type lockedRuntime struct {
	runtime.Runtime
	fileLock *FileLock
	worker   string
	timeout  time.Duration
}

// WriteFile acquires a file lock before writing.
func (lr *lockedRuntime) WriteFile(ctx context.Context, relPath string, data []byte, perm os.FileMode) error {
	if err := lr.fileLock.Acquire(relPath, lr.worker, lr.timeout); err != nil {
		return fmt.Errorf("lockedRuntime.WriteFile(%s): %w", relPath, err)
	}
	defer lr.fileLock.Release(relPath)
	return lr.Runtime.WriteFile(ctx, relPath, data, perm)
}

// DeleteFile acquires a file lock before deleting.
func (lr *lockedRuntime) DeleteFile(ctx context.Context, relPath string) error {
	if err := lr.fileLock.Acquire(relPath, lr.worker, lr.timeout); err != nil {
		return fmt.Errorf("lockedRuntime.DeleteFile(%s): %w", relPath, err)
	}
	defer lr.fileLock.Release(relPath)
	return lr.Runtime.DeleteFile(ctx, relPath)
}

// --- TeamConfig in AgentConfig ---

// TeamManager tracks team executions within a session.
type TeamManager struct {
	mu      sync.Mutex
	teams   []*Team
	running map[string]context.CancelFunc // name → cancel func
}

// NewTeamManager creates a new team manager.
func NewTeamManager() *TeamManager {
	return &TeamManager{
		running: make(map[string]context.CancelFunc),
	}
}

// Track adds a team to the manager.
func (m *TeamManager) Track(t *Team) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.teams = append(m.teams, t)
}

// Cancel stops a running team by name.
func (m *TeamManager) Cancel(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	cancel, ok := m.running[name]
	if !ok {
		return false
	}
	cancel()
	delete(m.running, name)
	return true
}

// CancelAll stops all running teams.
func (m *TeamManager) CancelAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, cancel := range m.running {
		cancel()
		delete(m.running, name)
	}
}

// List returns all tracked teams.
func (m *TeamManager) List() []*Team {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]*Team, len(m.teams))
	copy(cp, m.teams)
	return cp
}

// RunAsync starts a team in a goroutine and tracks it.
func (m *TeamManager) RunAsync(ctx context.Context, t *Team, stream llm.StreamCallback, onDone func(*TeamResult, error)) {
	m.mu.Lock()
	subCtx, cancel := context.WithCancel(ctx)
	m.running[t.Name] = cancel
	m.teams = append(m.teams, t)
	m.mu.Unlock()

	go func() {
		defer cancel()
		result, err := t.Run(subCtx, stream)

		m.mu.Lock()
		delete(m.running, t.Name)
		m.mu.Unlock()

		if onDone != nil {
			onDone(result, err)
		}
	}()
}

// HasRunning returns true if any teams are currently running.
func (m *TeamManager) HasRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.running) > 0
}

// --- Helper for wrapping write_file/patch_file/delete_file tools with file locking ---

// WrapToolsWithFileLock returns a copy of the tools list with write_file, patch_file,
// and delete_file wrapped to acquire/release a file lock.
func WrapToolsWithFileLock(tools []Tool, fl *FileLock, worker string, timeout time.Duration) []Tool {
	wrapped := make([]Tool, len(tools))
	copy(wrapped, tools)

	for i, t := range wrapped {
		switch t.Def.Name {
		case "write_file", "patch_file", "delete_file":
			origExec := t.Execute
			toolName := t.Def.Name
			wrapped[i].Execute = func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
				// Extract path from args.
				var p struct {
					Path string `json:"path"`
				}
				if err := json.Unmarshal(args, &p); err == nil && p.Path != "" {
					if err := fl.Acquire(p.Path, worker, timeout); err != nil {
						return "", fmt.Errorf("%s: file lock: %w", toolName, err)
					}
					defer fl.Release(p.Path)
				}
				return origExec(ctx, rt, args)
			}
		}
	}
	return wrapped
}
