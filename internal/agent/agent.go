package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/gajaai/opencode-go/internal/llm"
	"github.com/gajaai/opencode-go/internal/runtime"
)

const defaultMaxIterations = 10

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

The project is located at: %s

Rules:
- Always read a file before modifying it.
- Prefer patch_file over write_file when making targeted edits to existing files.
- Use grep_files and find_files to explore the codebase before making changes.
- Use read_file_lines for large files instead of reading the entire file.
- After writing files, verify your changes by reading them back or running tests.
- Use execute_command to run build commands, tests, and linters.
- Explain what you are doing and why before each action.
- If a command fails, analyze the error and try a different approach.
- Never include API keys, secrets, or credentials in tool call arguments.`

// ErrMaxIterations is returned when the agent loop exceeds the iteration limit.
var ErrMaxIterations = errors.New("agent: max iterations reached")

// Result holds the outcome of an agent run.
type Result struct {
	Response string    // Final text response from the model.
	Steps    []Step    // Executed tool calls and their results.
	Usage    llm.Usage // Aggregated token usage across all turns.
}

// Step records one tool invocation within an agent run.
type Step struct {
	ToolCall llm.ToolCall // What the model requested.
	Output   string       // What the tool returned.
	Error    string       // Non-empty if the tool failed.
}

// Agent orchestrates the observe→plan→act→verify loop.
type Agent struct {
	provider       llm.Provider
	rt             runtime.Runtime
	tools          []Tool
	toolMap        map[string]Tool
	logger         *slog.Logger
	history        []llm.Message
	maxIterations  int
	systemPrompt   string
	temperature    *float64
	maxTokens      *int
	credentialKeys []string
	contextCfg     ContextConfig
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

// New creates an Agent with the given provider and runtime.
func New(provider llm.Provider, rt runtime.Runtime, logger *slog.Logger, opts ...Option) *Agent {
	tools := DefaultTools()
	toolMap := make(map[string]Tool, len(tools))
	for _, t := range tools {
		toolMap[t.Def.Name] = t
	}

	a := &Agent{
		provider:      provider,
		rt:            rt,
		tools:         tools,
		toolMap:       toolMap,
		logger:        logger,
		maxIterations: defaultMaxIterations,
		contextCfg:    DefaultContextConfig(),
	}

	for _, opt := range opts {
		opt(a)
	}

	if a.systemPrompt == "" {
		a.systemPrompt = fmt.Sprintf(defaultSystemPrompt, rt.TargetDir())
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

	for i := 0; i < a.maxIterations; i++ {
		// Truncate history if approaching context window limit.
		a.history = TruncateHistory(a.history, a.contextCfg)

		req := llm.Request{
			Messages:    a.history,
			Tools:       toolDefs,
			Temperature: a.temperature,
			MaxTokens:   a.maxTokens,
		}

		resp, err := a.provider.Complete(ctx, req, stream)
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
				output, execErr := tool.Execute(ctx, a.rt, json.RawMessage(redactedArgs))
				if execErr != nil {
					step.Error = execErr.Error()
					step.Output = fmt.Sprintf("error: %s", execErr.Error())
					a.logger.Debug("tool execution failed", "tool", tc.Name, "error", execErr)
				} else {
					// Redact credentials in tool output before sending back to LLM.
					output = RedactCredentials(output, a.credentialKeys)
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
