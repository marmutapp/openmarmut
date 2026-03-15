package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marmutapp/openmarmut/internal/config"
	"github.com/marmutapp/openmarmut/internal/llm"
	"github.com/marmutapp/openmarmut/internal/runtime"
)

// SubAgent wraps an isolated Agent instance for a specific subtask.
type SubAgent struct {
	Name     string        // user-assigned or auto-generated name
	Task     string        // task description
	Agent    *Agent        // isolated agent with own history
	Provider llm.Provider  // can differ from parent
	Runtime  runtime.Runtime
	Result   string        // final output when done
	Status   string        // "running", "completed", "failed"
	Error    string        // error message if failed
	Usage    llm.Usage     // token usage
	Duration time.Duration // wall clock time
	Steps    int           // number of tool calls
}

// SubAgentOpts configures a sub-agent spawn.
type SubAgentOpts struct {
	Name         string          // optional name (auto-generated if empty)
	Task         string          // the task description (required)
	Provider     llm.Provider    // provider to use (nil = use parent's)
	Runtime      runtime.Runtime // shared runtime (required)
	Config       *config.Config  // config for context window settings
	ParentCtx    []llm.Message   // optional context from parent
	MaxIter      int             // max iterations (default 10)
	SystemPrompt string          // optional system prompt override
	Logger       *slog.Logger    // logger (required)
	IgnoreList   *IgnoreList     // optional ignore list
}

// subAgentCounter generates unique IDs for unnamed sub-agents.
var subAgentCounter atomic.Int64

// SpawnSubAgent creates and runs an isolated sub-agent for the given task.
// The sub-agent shares the same Runtime but has its own conversation history.
// It runs to completion and returns the result.
func SpawnSubAgent(ctx context.Context, opts SubAgentOpts) (*SubAgent, error) {
	if opts.Task == "" {
		return nil, fmt.Errorf("agent.SpawnSubAgent: task is required")
	}
	if opts.Runtime == nil {
		return nil, fmt.Errorf("agent.SpawnSubAgent: runtime is required")
	}
	if opts.Provider == nil {
		return nil, fmt.Errorf("agent.SpawnSubAgent: provider is required")
	}
	if opts.Logger == nil {
		return nil, fmt.Errorf("agent.SpawnSubAgent: logger is required")
	}

	name := opts.Name
	if name == "" {
		name = fmt.Sprintf("sub-%d", subAgentCounter.Add(1))
	}

	maxIter := opts.MaxIter
	if maxIter <= 0 {
		maxIter = defaultMaxIterations
	}

	// Build agent options.
	var agentOpts []Option
	agentOpts = append(agentOpts, WithMaxIterations(maxIter))

	if opts.SystemPrompt != "" {
		agentOpts = append(agentOpts, WithSystemPrompt(opts.SystemPrompt))
	}

	if opts.IgnoreList != nil {
		agentOpts = append(agentOpts, WithIgnoreList(opts.IgnoreList))
	}

	// Apply context window from config if available.
	if opts.Config != nil {
		ctxCfg := DefaultContextConfig()
		if opts.Config.Agent.ContextWindow > 0 {
			ctxCfg.ContextWindow = opts.Config.Agent.ContextWindow
		}
		agentOpts = append(agentOpts, WithContextConfig(ctxCfg))
	}

	// Create isolated agent — fresh history, no permission checker (sub-agents auto-approve).
	ag := New(opts.Provider, opts.Runtime, opts.Logger, agentOpts...)

	// Inject parent context if provided — insert after system prompt.
	if len(opts.ParentCtx) > 0 {
		history := ag.History()
		// Build context summary from parent messages.
		var ctxBuf strings.Builder
		ctxBuf.WriteString("[Context from parent conversation]\n")
		for _, m := range opts.ParentCtx {
			if m.Role == llm.RoleSystem {
				continue // Don't duplicate system prompts.
			}
			ctxBuf.WriteString(fmt.Sprintf("[%s]: %s\n", m.Role, m.Content))
		}
		contextMsg := llm.Message{
			Role:    llm.RoleUser,
			Content: ctxBuf.String(),
		}
		ackMsg := llm.Message{
			Role:    llm.RoleAssistant,
			Content: "I have the context from the parent conversation. I'll now work on the assigned task.",
		}
		// Insert context after system prompt.
		newHistory := make([]llm.Message, 0, len(history)+2)
		newHistory = append(newHistory, history[0]) // system
		newHistory = append(newHistory, contextMsg, ackMsg)
		newHistory = append(newHistory, history[1:]...)
		ag.SetHistory(newHistory)
	}

	sa := &SubAgent{
		Name:     name,
		Task:     opts.Task,
		Agent:    ag,
		Provider: opts.Provider,
		Runtime:  opts.Runtime,
		Status:   "running",
	}

	// Run the sub-agent.
	start := time.Now()
	result, err := ag.Run(ctx, opts.Task, nil)
	sa.Duration = time.Since(start)

	if err != nil {
		sa.Status = "failed"
		sa.Error = err.Error()
		return sa, err
	}

	sa.Status = "completed"
	sa.Result = result.Response
	sa.Usage = result.Usage
	sa.Steps = len(result.Steps)

	return sa, nil
}

// SubAgentManager tracks sub-agents within a session.
type SubAgentManager struct {
	mu       sync.Mutex
	agents   []*SubAgent
	running  map[string]context.CancelFunc // name → cancel func for running agents
}

// NewSubAgentManager creates a new sub-agent manager.
func NewSubAgentManager() *SubAgentManager {
	return &SubAgentManager{
		running: make(map[string]context.CancelFunc),
	}
}

// SpawnAsync starts a sub-agent in a goroutine and tracks it.
func (m *SubAgentManager) SpawnAsync(ctx context.Context, opts SubAgentOpts) *SubAgent {
	name := opts.Name
	if name == "" {
		name = fmt.Sprintf("sub-%d", subAgentCounter.Add(1))
		opts.Name = name
	}

	sa := &SubAgent{
		Name:    name,
		Task:    opts.Task,
		Status:  "running",
		Runtime: opts.Runtime,
	}

	m.mu.Lock()
	m.agents = append(m.agents, sa)
	subCtx, cancel := context.WithCancel(ctx)
	m.running[name] = cancel
	m.mu.Unlock()

	go func() {
		result, err := SpawnSubAgent(subCtx, opts)

		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.running, name)

		if err != nil {
			sa.Status = "failed"
			sa.Error = err.Error()
			return
		}
		sa.Status = result.Status
		sa.Result = result.Result
		sa.Usage = result.Usage
		sa.Duration = result.Duration
		sa.Steps = result.Steps
		sa.Agent = result.Agent
	}()

	return sa
}

// Kill stops a running sub-agent by name.
func (m *SubAgentManager) Kill(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	cancel, ok := m.running[name]
	if !ok {
		return false
	}
	cancel()
	delete(m.running, name)

	// Update status.
	for _, sa := range m.agents {
		if sa.Name == name && sa.Status == "running" {
			sa.Status = "failed"
			sa.Error = "killed by user"
			break
		}
	}
	return true
}

// Track adds a completed sub-agent to the manager's list.
func (m *SubAgentManager) Track(sa *SubAgent) {
	if sa == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Check if already tracked (from SpawnAsync).
	for _, existing := range m.agents {
		if existing.Name == sa.Name {
			return
		}
	}
	m.agents = append(m.agents, sa)
}

// List returns all tracked sub-agents.
func (m *SubAgentManager) List() []*SubAgent {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]*SubAgent, len(m.agents))
	copy(cp, m.agents)
	return cp
}

// spawnSubAgentTool returns a tool definition for the LLM to spawn sub-agents.
func spawnSubAgentTool() Tool {
	return Tool{
		Def: llm.ToolDef{
			Name:        "spawn_subagent",
			Description: "Spawn an isolated sub-agent to handle a subtask. The sub-agent has access to the same tools and project directory but runs with its own conversation history. Returns the sub-agent's final response.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task": map[string]any{
						"type":        "string",
						"description": "Description of the task for the sub-agent to perform",
					},
					"name": map[string]any{
						"type":        "string",
						"description": "Optional name for the sub-agent (for tracking)",
					},
				},
				"required": []string{"task"},
			},
		},
		// Execute is set in the agent via WithSubAgentProvider since it needs
		// access to the parent agent's provider and runtime.
		Execute: func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
			return "", fmt.Errorf("spawn_subagent: not configured — use WithSubAgentProvider to enable")
		},
	}
}

// WithSubAgentProvider enables the spawn_subagent tool with the given provider and logger.
func WithSubAgentProvider(provider llm.Provider, logger *slog.Logger) Option {
	return func(a *Agent) {
		a.subAgentProvider = provider
		a.subAgentLogger = logger
	}
}

// configureSubAgentTool sets up the spawn_subagent tool's Execute function
// after the agent is fully constructed.
func (a *Agent) configureSubAgentTool() {
	if a.subAgentProvider == nil {
		return
	}

	for i, t := range a.tools {
		if t.Def.Name == "spawn_subagent" {
			a.tools[i].Execute = func(ctx context.Context, rt runtime.Runtime, args json.RawMessage) (string, error) {
				var p struct {
					Task string `json:"task"`
					Name string `json:"name"`
				}
				if err := json.Unmarshal(args, &p); err != nil {
					return "", fmt.Errorf("spawn_subagent: %w", err)
				}
				if p.Task == "" {
					return "", fmt.Errorf("spawn_subagent: task is required")
				}

				sa, err := SpawnSubAgent(ctx, SubAgentOpts{
					Name:       p.Name,
					Task:       p.Task,
					Provider:   a.subAgentProvider,
					Runtime:    a.rt,
					Logger:     a.subAgentLogger,
					IgnoreList: a.ignoreList,
				})
				if err != nil {
					return fmt.Sprintf("Sub-agent failed: %s", err.Error()), nil
				}

				// Format result summary.
				var sb strings.Builder
				fmt.Fprintf(&sb, "Sub-agent '%s' completed.\n", sa.Name)
				fmt.Fprintf(&sb, "Tool calls: %d | Tokens: %d | Duration: %s\n\n",
					sa.Steps, sa.Usage.TotalTokens, sa.Duration.Round(time.Millisecond))
				sb.WriteString(sa.Result)
				return sb.String(), nil
			}
			a.toolMap["spawn_subagent"] = a.tools[i]
			break
		}
	}
}
