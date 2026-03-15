package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gajaai/openmarmut-go/internal/llm"
	"github.com/gajaai/openmarmut-go/internal/runtime"
)

const defaultMaxIterations = 10

// readOnlyTools lists tools that are safe to use during plan mode (no side effects).
var readOnlyTools = map[string]bool{
	"read_file":       true,
	"read_file_lines": true,
	"list_dir":        true,
	"grep_files":      true,
	"find_files":      true,
	"git_status":      true,
	"git_diff":        true,
	"git_diff_staged":  true,
	"git_log":         true,
}

const planSystemPrompt = `You are an AI coding assistant in PLAN MODE. You are analyzing a project to create a detailed implementation plan.

IMPORTANT: You are in ANALYSIS ONLY mode. You must NOT modify any files. You can only read and explore.

Available tools (read-only):
- read_file: Read entire file contents.
- read_file_lines: Read a specific range of lines.
- list_dir: List directory contents.
- grep_files: Search for regex patterns across files.
- find_files: Find files by name pattern.
- git_status: Show working tree status.
- git_diff: Show unstaged changes.
- git_diff_staged: Show staged changes.
- git_log: Show recent commit history.

The project is located at: %s

Your task:
1. Thoroughly analyze the codebase to understand the current state.
2. Identify all files that need to be created or modified.
3. Consider edge cases, dependencies, and potential issues.
4. Produce a clear, step-by-step implementation plan.

Format your plan as:
## Plan: <brief title>

### Steps
1. **<action>** — <description>
   - Files: <list of files to create/modify>
   - Details: <what specifically needs to change>

2. ...

### Risks & Considerations
- <any potential issues or trade-offs>

### Testing Strategy
- <how to verify the changes work>

Be specific about file paths, function names, and code changes. The plan will be reviewed before execution.`

const defaultSystemPrompt = `You are an AI coding assistant operating on a project directory.

Available tools:
- read_file: Read entire file contents.
- read_file_lines: Read a specific range of lines (for large files).
- write_file: Write complete file contents.
- patch_file: Apply surgical edits to a file (find-and-replace, preferred over write_file for modifications).
- delete_file: Delete a file.
- list_dir: List directory contents.
- mkdir: Create directories.
- execute_command: Run shell commands.
- grep_files: Search for regex patterns across files.
- find_files: Find files by name pattern.
- git_status: Show working tree status.
- git_diff: Show unstaged changes (optionally for a specific file).
- git_diff_staged: Show staged changes.
- git_log: Show recent commit history.
- git_commit: Stage all changes and commit with a message.
- git_branch: List branches or create a new one.
- git_checkout: Switch to a different branch.

The project is located at: %s

Rules:
- Always read a file before modifying it.
- Prefer patch_file over write_file when making targeted edits to existing files.
- Use grep_files and find_files to explore the codebase before making changes.
- Use read_file_lines for large files instead of reading the entire file.
- After writing files, verify your changes by reading them back or running tests.
- Use execute_command to run build commands, tests, and linters.
- Use git_status to check for changes before committing.
- Write clear, descriptive commit messages with git_commit.
- Explain what you are doing and why before each action.
- If a command fails, analyze the error and try a different approach.
- Never include API keys, secrets, or credentials in tool call arguments.`

// ErrMaxIterations is returned when the agent loop exceeds the iteration limit.
var ErrMaxIterations = errors.New("agent: max iterations reached")

// Result holds the outcome of an agent run.
type Result struct {
	Response  string        // Final text response from the model.
	Steps     []Step        // Executed tool calls and their results.
	Usage     llm.Usage     // Aggregated token usage across all turns.
	Duration  time.Duration // Wall clock time for all LLM calls (excludes tool execution).
	Truncated bool          // True if history was truncated during this run.
}

// Step records one tool invocation within an agent run.
type Step struct {
	ToolCall llm.ToolCall // What the model requested.
	Output   string       // What the tool returned.
	Error    string       // Non-empty if the tool failed.
}

// ToolCallCallback is called when the agent is about to execute a tool.
// Useful for displaying tool calls inline in a REPL.
type ToolCallCallback func(tc llm.ToolCall)

// Agent orchestrates the observe→plan→act→verify loop.
type Agent struct {
	provider             llm.Provider
	rt                   runtime.Runtime
	tools                []Tool
	toolMap              map[string]Tool
	logger               *slog.Logger
	history              []llm.Message
	maxIterations        int
	systemPrompt         string
	projectInstructions  string
	temperature          *float64
	maxTokens            *int
	credentialKeys       []string
	contextCfg           ContextConfig
	onToolCall           ToolCallCallback
	extendedThinking     bool
	thinkingBudget       int
	permChecker          *PermissionChecker
	checkpoints          *CheckpointStore
	rules                []Rule
	activeRulesContent   string
	skills               []Skill
	memory               *MemoryStore
	ignoreList           *IgnoreList
}

// Option configures the Agent.
type Option func(*Agent)

// WithMaxIterations sets the maximum number of tool-call loop iterations.
func WithMaxIterations(n int) Option {
	return func(a *Agent) { a.maxIterations = n }
}

// WithSystemPrompt overrides the default system prompt.
func WithSystemPrompt(prompt string) Option {
	return func(a *Agent) { a.systemPrompt = prompt }
}

// WithTemperature sets the sampling temperature for requests.
func WithTemperature(t *float64) Option {
	return func(a *Agent) { a.temperature = t }
}

// WithMaxTokens sets the max output tokens for requests.
func WithMaxTokens(m *int) Option {
	return func(a *Agent) { a.maxTokens = m }
}

// WithCredentialKeys sets the credential values to detect and redact.
func WithCredentialKeys(keys []string) Option {
	return func(a *Agent) { a.credentialKeys = keys }
}

// WithContextConfig sets the context window management configuration.
func WithContextConfig(cfg ContextConfig) Option {
	return func(a *Agent) { a.contextCfg = cfg }
}

// WithToolCallCallback sets a callback invoked before each tool execution.
func WithToolCallCallback(cb ToolCallCallback) Option {
	return func(a *Agent) { a.onToolCall = cb }
}

// WithPermissionChecker sets the permission checker for tool call authorization.
func WithPermissionChecker(pc *PermissionChecker) Option {
	return func(a *Agent) { a.permChecker = pc }
}

// WithCheckpointStore sets the checkpoint store for file change tracking.
func WithCheckpointStore(cs *CheckpointStore) Option {
	return func(a *Agent) { a.checkpoints = cs }
}

// WithProjectInstructions prepends project instructions to the system prompt.
func WithProjectInstructions(instructions string) Option {
	return func(a *Agent) { a.projectInstructions = instructions }
}

// WithRules sets the loaded rules for dynamic activation.
func WithRules(rules []Rule) Option {
	return func(a *Agent) { a.rules = rules }
}

// WithSkills sets the loaded skills.
func WithSkills(skills []Skill) Option {
	return func(a *Agent) { a.skills = skills }
}

// WithMemoryStore sets the auto-memory store.
func WithMemoryStore(m *MemoryStore) Option {
	return func(a *Agent) { a.memory = m }
}

// WithIgnoreList sets the ignore list for file filtering.
func WithIgnoreList(il *IgnoreList) Option {
	return func(a *Agent) { a.ignoreList = il }
}

// WithExtendedThinking enables extended thinking / reasoning tokens.
func WithExtendedThinking(enabled bool, budget int) Option {
	return func(a *Agent) {
		a.extendedThinking = enabled
		a.thinkingBudget = budget
	}
}

// ExtendedThinking returns whether extended thinking is enabled.
func (a *Agent) ExtendedThinking() bool { return a.extendedThinking }

// SetExtendedThinking toggles extended thinking at runtime.
func (a *Agent) SetExtendedThinking(enabled bool) { a.extendedThinking = enabled }

// SetThinkingBudget updates the thinking budget at runtime.
func (a *Agent) SetThinkingBudget(budget int) { a.thinkingBudget = budget }

// New creates an Agent with the given provider and runtime.
func New(provider llm.Provider, rt runtime.Runtime, logger *slog.Logger, opts ...Option) *Agent {
	a := &Agent{
		provider:      provider,
		rt:            rt,
		logger:        logger,
		maxIterations: defaultMaxIterations,
		contextCfg:    DefaultContextConfig(),
	}

	for _, opt := range opts {
		opt(a)
	}

	// Build tools after options are applied so ignoreList is available.
	tools := DefaultTools(a.ignoreList)
	toolMap := make(map[string]Tool, len(tools))
	for _, t := range tools {
		toolMap[t.Def.Name] = t
	}
	a.tools = tools
	a.toolMap = toolMap

	if a.systemPrompt == "" {
		base := fmt.Sprintf(defaultSystemPrompt, rt.TargetDir())
		if a.projectInstructions != "" {
			a.systemPrompt = FormatProjectInstructionsPrompt(a.projectInstructions) + base
		} else {
			a.systemPrompt = base
		}
		// Append auto-skill descriptions (2% of context window budget).
		if len(a.skills) > 0 {
			maxChars := a.contextCfg.ContextWindow * 4 / 100 * 2 // ~2% of context in chars
			a.systemPrompt += FormatAutoSkillDescriptions(a.skills, maxChars)
		}
		// Append auto-memory from previous sessions.
		if a.memory != nil {
			a.systemPrompt += a.memory.FormatForPrompt()
		}
		// Append ignore list.
		if a.ignoreList != nil {
			a.systemPrompt += FormatIgnorePrompt(a.ignoreList)
		}
	}

	// Prepend system message to history.
	a.history = []llm.Message{
		{Role: llm.RoleSystem, Content: a.systemPrompt},
	}

	return a
}

// Run sends a user message and runs the agentic loop until the model
// produces a final text response (no more tool calls).
func (a *Agent) Run(ctx context.Context, userMessage string, stream llm.StreamCallback) (*Result, error) {
	a.history = append(a.history, llm.Message{
		Role:    llm.RoleUser,
		Content: userMessage,
	})

	result := &Result{}
	toolDefs := ToolDefs(a.tools)

	// Start a new checkpoint turn for file change tracking.
	if a.checkpoints != nil {
		a.checkpoints.StartTurn()
	}

	for i := 0; i < a.maxIterations; i++ {
		// Truncate history if approaching context window limit.
		prevLen := len(a.history)
		a.history = TruncateHistory(a.history, a.contextCfg)
		if len(a.history) < prevLen {
			result.Truncated = true
		}

		// Update active rules based on recently accessed files.
		a.refreshActiveRules()

		req := llm.Request{
			Messages:         a.history,
			Tools:            toolDefs,
			Temperature:      a.temperature,
			MaxTokens:        a.maxTokens,
			ExtendedThinking: a.extendedThinking,
			ThinkingBudget:   a.thinkingBudget,
		}

		callStart := time.Now()
		resp, err := a.provider.Complete(ctx, req, stream)
		result.Duration += time.Since(callStart)
		if err != nil {
			return nil, fmt.Errorf("agent.Run: %w", err)
		}

		// Aggregate usage.
		result.Usage.PromptTokens += resp.Usage.PromptTokens
		result.Usage.CompletionTokens += resp.Usage.CompletionTokens
		result.Usage.TotalTokens += resp.Usage.TotalTokens

		// Append assistant message to history.
		assistantMsg := llm.Message{
			Role:      llm.RoleAssistant,
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		}
		a.history = append(a.history, assistantMsg)

		// No tool calls — we're done.
		if len(resp.ToolCalls) == 0 {
			result.Response = resp.Content
			return result, nil
		}

		// Execute each tool call and append results.
		for _, tc := range resp.ToolCalls {
			// Redact credentials in tool call arguments before execution.
			redactedArgs := RedactCredentials(tc.Arguments, a.credentialKeys)

			step := Step{ToolCall: tc}

			if a.onToolCall != nil {
				a.onToolCall(tc)
			}

			// Check permissions before executing.
			if a.permChecker != nil {
				allowed, denyMsg := a.permChecker.Check(tc)
				if !allowed {
					step.Error = denyMsg
					step.Output = denyMsg
					a.logger.Info("tool call denied by permission", "tool", tc.Name)
					result.Steps = append(result.Steps, step)
					a.history = append(a.history, llm.Message{
						Role:       llm.RoleTool,
						ToolCallID: tc.ID,
						Content:    step.Output,
					})
					continue
				}
			}

			tool, ok := a.toolMap[tc.Name]
			if !ok {
				step.Error = fmt.Sprintf("unknown tool: %s", tc.Name)
				step.Output = step.Error
				a.logger.Warn("unknown tool call", "name", tc.Name)
			} else if tc.Name == "execute_command" && DetectCredentialLeak(tc.Arguments, a.credentialKeys) {
				step.Error = ErrCredentialLeak.Error()
				step.Output = "error: command blocked — it contains a credential value. " +
					"Never include API keys or secrets in shell commands."
				a.logger.Warn("credential leak blocked", "tool", tc.Name)
			} else {
				// Capture file state before modifications for checkpoint/rewind.
				if a.checkpoints != nil {
					a.captureCheckpoint(ctx, tc.Name, redactedArgs)
				}
				output, execErr := tool.Execute(ctx, a.rt, json.RawMessage(redactedArgs))
				if execErr != nil {
					step.Error = execErr.Error()
					step.Output = fmt.Sprintf("error: %s", execErr.Error())
					a.logger.Debug("tool execution failed", "tool", tc.Name, "error", execErr)
				} else {
					// Redact credentials in tool output before sending back to LLM.
					output = RedactCredentials(output, a.credentialKeys)
					// Truncate oversized tool results to fit context window.
					maxToolTokens := a.contextCfg.ContextWindow / 4
					if maxToolTokens > 0 {
						output = TruncateLargeToolResult(output, maxToolTokens)
					}
					step.Output = output
					a.logger.Debug("tool executed", "tool", tc.Name)
				}
			}

			result.Steps = append(result.Steps, step)

			a.history = append(a.history, llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: tc.ID,
				Content:    step.Output,
			})
		}

		// The stream callback is kept for the next iteration so the final
		// text response is streamed to the caller.
	}

	return nil, ErrMaxIterations
}

// History returns the current conversation history.
func (a *Agent) History() []llm.Message {
	return a.history
}

// ClearHistory resets the conversation to just the system prompt.
func (a *Agent) ClearHistory() {
	a.history = []llm.Message{
		{Role: llm.RoleSystem, Content: a.systemPrompt},
	}
}

// ContextUsage returns the current context window usage.
func (a *Agent) ContextUsage() ContextUsageInfo {
	return ComputeContextUsage(a.history, a.contextCfg)
}

// ContextConfig returns the agent's context configuration.
func (a *Agent) ContextConfig() ContextConfig {
	return a.contextCfg
}

// SetHistory replaces the conversation history (used for session resume).
// The first message should be the system prompt.
func (a *Agent) SetHistory(msgs []llm.Message) {
	a.history = msgs
}

// Checkpoints returns the agent's checkpoint store (may be nil).
func (a *Agent) Checkpoints() *CheckpointStore {
	return a.checkpoints
}

// captureCheckpoint snapshots file content before a modifying tool runs.
func (a *Agent) captureCheckpoint(ctx context.Context, toolName, args string) {
	// Only capture for file-modifying tools.
	switch toolName {
	case "write_file", "patch_file", "delete_file":
	default:
		return
	}

	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil || p.Path == "" {
		return
	}
	a.checkpoints.CaptureFile(ctx, a.rt, p.Path)
}

// RunPlan sends a user message through the agent loop using only read-only tools
// and the plan-specific system prompt. Returns the plan text.
func (a *Agent) RunPlan(ctx context.Context, userMessage string, stream llm.StreamCallback) (*Result, error) {
	// Build read-only tool set.
	var planTools []Tool
	planToolMap := make(map[string]Tool)
	for _, t := range a.tools {
		if readOnlyTools[t.Def.Name] {
			planTools = append(planTools, t)
			planToolMap[t.Def.Name] = t
		}
	}
	planToolDefs := ToolDefs(planTools)

	// Swap system prompt to plan mode.
	planPrompt := fmt.Sprintf(planSystemPrompt, a.rt.TargetDir())
	origHistory := a.history
	a.history = []llm.Message{
		{Role: llm.RoleSystem, Content: planPrompt},
		{Role: llm.RoleUser, Content: userMessage},
	}

	result := &Result{}

	for i := 0; i < a.maxIterations; i++ {
		a.history = TruncateHistory(a.history, a.contextCfg)

		req := llm.Request{
			Messages:         a.history,
			Tools:            planToolDefs,
			Temperature:      a.temperature,
			MaxTokens:        a.maxTokens,
			ExtendedThinking: a.extendedThinking,
			ThinkingBudget:   a.thinkingBudget,
		}

		callStart := time.Now()
		resp, err := a.provider.Complete(ctx, req, stream)
		result.Duration += time.Since(callStart)
		if err != nil {
			a.history = origHistory
			return nil, fmt.Errorf("agent.RunPlan: %w", err)
		}

		result.Usage.PromptTokens += resp.Usage.PromptTokens
		result.Usage.CompletionTokens += resp.Usage.CompletionTokens
		result.Usage.TotalTokens += resp.Usage.TotalTokens

		assistantMsg := llm.Message{
			Role:      llm.RoleAssistant,
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		}
		a.history = append(a.history, assistantMsg)

		if len(resp.ToolCalls) == 0 {
			result.Response = resp.Content
			// Restore original history — plan doesn't pollute the main conversation.
			a.history = origHistory
			return result, nil
		}

		for _, tc := range resp.ToolCalls {
			step := Step{ToolCall: tc}

			if a.onToolCall != nil {
				a.onToolCall(tc)
			}

			tool, ok := planToolMap[tc.Name]
			if !ok {
				step.Error = fmt.Sprintf("tool not available in plan mode: %s", tc.Name)
				step.Output = step.Error
			} else {
				output, execErr := tool.Execute(ctx, a.rt, json.RawMessage(tc.Arguments))
				if execErr != nil {
					step.Error = execErr.Error()
					step.Output = fmt.Sprintf("error: %s", execErr.Error())
				} else {
					maxToolTokens := a.contextCfg.ContextWindow / 4
					if maxToolTokens > 0 {
						output = TruncateLargeToolResult(output, maxToolTokens)
					}
					step.Output = output
				}
			}

			result.Steps = append(result.Steps, step)
			a.history = append(a.history, llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: tc.ID,
				Content:    step.Output,
			})
		}
	}

	a.history = origHistory
	return nil, ErrMaxIterations
}

// CompactHistory summarizes the conversation history using the LLM, replacing
// all messages between the system prompt and a summary. Returns the before/after
// estimated token counts. customInstruction is an optional user hint for what to preserve.
func (a *Agent) CompactHistory(ctx context.Context, customInstruction string) (beforeTokens, afterTokens int, err error) {
	beforeTokens = EstimateMessagesTokens(a.history)

	// Need at least system + 1 user + 1 assistant to compact.
	if len(a.history) < 3 {
		return beforeTokens, beforeTokens, nil
	}

	// Build the summarization prompt.
	var sb strings.Builder
	sb.WriteString("Summarize this conversation for context continuity. Preserve:\n")
	sb.WriteString("- All file changes made (paths and what changed)\n")
	sb.WriteString("- Key decisions and their rationale\n")
	sb.WriteString("- Current task status and next steps\n")
	sb.WriteString("- Any errors encountered and how they were resolved\n")
	if customInstruction != "" {
		sb.WriteString("- ")
		sb.WriteString(customInstruction)
		sb.WriteString("\n")
	}
	sb.WriteString("\nOutput a concise summary that would let a new conversation continue where this one left off.")

	// Serialize the conversation (skip system prompt).
	var convBuf strings.Builder
	for _, m := range a.history[1:] {
		convBuf.WriteString(fmt.Sprintf("[%s]: %s\n", m.Role, m.Content))
		for _, tc := range m.ToolCalls {
			convBuf.WriteString(fmt.Sprintf("  tool_call: %s(%s)\n", tc.Name, tc.Arguments))
		}
		if m.ToolCallID != "" {
			convBuf.WriteString(fmt.Sprintf("  (tool result for %s)\n", m.ToolCallID))
		}
	}

	// Count turns for the compaction note.
	turnCount := 0
	for _, m := range a.history {
		if m.Role == llm.RoleUser {
			turnCount++
		}
	}

	// Send to LLM for summarization.
	req := llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: sb.String()},
			{Role: llm.RoleUser, Content: convBuf.String()},
		},
		Temperature: a.temperature,
		MaxTokens:   a.maxTokens,
	}

	resp, llmErr := a.provider.Complete(ctx, req, nil)
	if llmErr != nil {
		return beforeTokens, beforeTokens, fmt.Errorf("agent.CompactHistory: %w", llmErr)
	}

	summary := resp.Content
	if summary == "" {
		return beforeTokens, beforeTokens, fmt.Errorf("agent.CompactHistory: LLM returned empty summary")
	}

	// Replace history: system + summary + compaction note.
	a.history = []llm.Message{
		a.history[0], // system prompt unchanged
		{Role: llm.RoleAssistant, Content: summary},
		{Role: llm.RoleUser, Content: fmt.Sprintf("[Conversation compacted at turn %d. Summary above.]", turnCount)},
	}

	afterTokens = EstimateMessagesTokens(a.history)
	return beforeTokens, afterTokens, nil
}

// ReadOnlyToolNames returns the set of tool names available in plan mode.
func ReadOnlyToolNames() map[string]bool {
	return readOnlyTools
}

// Rules returns the loaded rules.
func (a *Agent) Rules() []Rule {
	return a.rules
}

// Skills returns the loaded skills.
func (a *Agent) Skills() []Skill {
	return a.skills
}

// Memory returns the auto-memory store (may be nil).
func (a *Agent) Memory() *MemoryStore {
	return a.memory
}

// ActiveRulesContent returns the currently active rules content.
func (a *Agent) ActiveRulesContent() string {
	return a.activeRulesContent
}

// IgnoreList returns the agent's ignore list (may be nil).
func (a *Agent) IgnoreList() *IgnoreList {
	return a.ignoreList
}

// refreshActiveRules updates the system prompt with rules matching recently accessed files.
func (a *Agent) refreshActiveRules() {
	if len(a.rules) == 0 {
		return
	}

	// Extract file paths from last 10 messages.
	filePaths := ExtractRecentFilePaths(a.history, 10)
	newContent := MatchRules(a.rules, filePaths)

	if newContent == a.activeRulesContent {
		return // No change.
	}

	// Update the system prompt: remove old active rules section, add new one.
	oldSuffix := FormatActiveRules(a.activeRulesContent)
	base := a.history[0].Content
	if oldSuffix != "" {
		base = strings.TrimSuffix(base, oldSuffix)
	}
	a.activeRulesContent = newContent
	a.history[0].Content = base + FormatActiveRules(newContent)
}

// Tools returns the agent's available tools.
func (a *Agent) Tools() []Tool {
	return a.tools
}
