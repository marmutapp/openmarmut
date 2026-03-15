package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	osexec "os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gajaai/openmarmut-go/internal/agent"
	"github.com/gajaai/openmarmut-go/internal/config"
	"github.com/gajaai/openmarmut-go/internal/llm"
	"github.com/gajaai/openmarmut-go/internal/logger"
	"github.com/gajaai/openmarmut-go/internal/mcp"
	"github.com/gajaai/openmarmut-go/internal/runtime"
	"github.com/gajaai/openmarmut-go/internal/session"
	"github.com/gajaai/openmarmut-go/internal/ui"
	"github.com/spf13/cobra"
)

// slashAction is the result of processing a slash command.
type slashAction int

const (
	slashNone    slashAction = iota // Not a slash command — send to agent.
	slashHandled                    // Slash command handled locally, continue loop.
	slashExit                       // /quit or /exit — stop the loop.
)

// chatState holds mutable state for the chat REPL, used by handleSlashCommand.
type chatState struct {
	ag           *agent.Agent
	permChecker  *agent.PermissionChecker
	sessionUsage llm.Usage
	model        string
	out          io.Writer      // Where to write command output (stderr in production).
	scanner      *bufio.Scanner // stdin scanner, shared between main loop and confirm prompt.
	spinner      *ui.Spinner    // current spinner, set before each ag.Run call; nil when idle.
	warned60     bool           // true after the 60% warning has been shown.
	sess         *session.Session // current session (nil if sessions disabled).
	rt           runtime.Runtime  // runtime for /rewind, /diff, /commit.
	isGitRepo    bool             // true if target dir is a git repo.
	planMode     bool             // true when plan mode is toggled on.
	pendingSkill string           // skill content to prepend to next user message.
	memoryDisabled bool           // true when auto-memory is disabled for this session.
	provider     llm.Provider     // LLM provider for memory extraction.
	targetDir    string           // target directory for project tagging.
	autoMemory   bool             // config: auto_memory enabled.
	subMgr         *agent.SubAgentManager // sub-agent tracker.
	cfg            *config.Config   // config for sub-agent context settings.
	log            *slog.Logger     // logger for sub-agent spawning.
	mcpMgr         *mcp.Manager     // MCP server manager.
	customCommands   []agent.CustomCommand // custom slash commands from .openmarmut/commands/.
	loopMgr          *loopManager     // /loop background task manager.
	customCmdContent string           // set by tryCustomCommand to replace user's line.
	taskList         *agent.TaskList  // persistent task list for /tasks.
	bgJobs           []*bgJob         // background execution jobs.
	bgMu             sync.Mutex       // protects bgJobs.
	bgNextID         int              // next background job ID.
	hooks            []agent.Hook     // configured hooks.
}

// handleSlashCommand processes slash commands locally without calling the LLM.
// Returns the action to take (none/handled/exit).
func handleSlashCommand(line string, state *chatState) slashAction {
	if !strings.HasPrefix(line, "/") {
		return slashNone
	}

	// Handle commands with arguments.
	if strings.HasPrefix(line, "/rename ") {
		newName := strings.TrimSpace(strings.TrimPrefix(line, "/rename "))
		if newName == "" {
			fmt.Fprintln(state.out, ui.FormatWarning("Usage: /rename <name>"))
			return slashHandled
		}
		if state.sess != nil {
			state.sess.Name = newName
			go session.Save(state.sess) //nolint:errcheck
			fmt.Fprintln(state.out, ui.FormatSuccess("Session renamed to: "+newName))
		} else {
			fmt.Fprintln(state.out, ui.FormatWarning("No active session to rename."))
		}
		return slashHandled
	}

	if strings.HasPrefix(line, "/rewind") {
		handleRewind(line, state)
		return slashHandled
	}

	if strings.HasPrefix(line, "/diff") {
		handleDiff(line, state)
		return slashHandled
	}

	if strings.HasPrefix(line, "/commit") {
		handleCommit(line, state)
		return slashHandled
	}

	if strings.HasPrefix(line, "/compact") {
		handleCompact(line, state)
		return slashHandled
	}

	if strings.HasPrefix(line, "/thinking") {
		handleThinking(state)
		return slashHandled
	}

	if strings.HasPrefix(line, "/effort") {
		handleEffort(line, state)
		return slashHandled
	}

	if strings.HasPrefix(line, "/memory ") {
		handleMemoryCommand(line, state)
		return slashHandled
	}

	if strings.HasPrefix(line, "/skill") {
		handleSkill(line, state)
		return slashHandled
	}

	if strings.HasPrefix(line, "/ignore") {
		handleIgnore(line, state)
		return slashHandled
	}

	if strings.HasPrefix(line, "/agents") {
		handleAgents(line, state)
		return slashHandled
	}

	if line == "/mcp" {
		renderMCPStatus(state)
		return slashHandled
	}

	if strings.HasPrefix(line, "/agent ") {
		handleAgent(line, state)
		return slashHandled
	}

	if strings.HasPrefix(line, "/btw ") {
		handleBtw(line, state)
		return slashHandled
	}

	if strings.HasPrefix(line, "/loop") {
		handleLoop(line, state)
		return slashHandled
	}

	if strings.HasPrefix(line, "/tasks") {
		handleTasks(line, state)
		return slashHandled
	}

	if strings.HasPrefix(line, "/bg") {
		handleBg(line, state)
		return slashHandled
	}

	if strings.HasPrefix(line, "/hooks") {
		handleHooks(line, state)
		return slashHandled
	}

	if strings.HasPrefix(line, "/model") {
		handleModel(line, state)
		return slashHandled
	}

	if strings.HasPrefix(line, "/provider") {
		handleProvider(line, state)
		return slashHandled
	}

	if strings.HasPrefix(line, "/plan") {
		return handlePlan(line, state)
	}

	switch line {
	case "/commands":
		renderCustomCommands(state)
		return slashHandled
	case "/quit", "/exit":
		return slashExit
	case "/rules":
		renderRules(state)
		return slashHandled
	case "/memory":
		renderMemory(state)
		return slashHandled
	case "/clear":
		state.ag.ClearHistory()
		state.sessionUsage = llm.Usage{}
		fmt.Fprintln(state.out, ui.FormatSuccess("History cleared."))
		return slashHandled
	case "/tools":
		renderToolsTable(state)
		return slashHandled
	case "/cost":
		renderCostBox(state)
		return slashHandled
	case "/context":
		renderContextBox(state)
		return slashHandled
	case "/sessions":
		renderSessionsList(state)
		return slashHandled
	case "/help":
		renderHelpBox(state.out)
		return slashHandled
	default:
		// Check for custom commands before reporting unknown.
		if result := tryCustomCommand(line, state); result != slashNone {
			return result
		}
		fmt.Fprintf(state.out, "%s (type /help for commands)\n",
			ui.FormatWarning("Unknown command: "+line))
		return slashHandled
	}
}

// renderHelpBox displays available commands in a styled box.
func renderHelpBox(w io.Writer) {
	commands := [][]string{
		{"/clear", "Reset conversation history"},
		{"/tools", "List available tools"},
		{"/cost", "Show accumulated session cost"},
		{"/context", "Show context window usage"},
		{"/diff [file]", "Show uncommitted changes"},
		{"/commit [msg]", "Commit changes (generates message if omitted)"},
		{"/rewind [n]", "Undo last N turns of file changes"},
		{"/rewind --list", "Show checkpoint history"},
		{"/compact [instr]", "Compact history (optional custom instruction)"},
		{"/thinking", "Toggle extended thinking on/off"},
		{"/effort <level>", "Set thinking effort (low/medium/high)"},
		{"/plan [msg]", "Toggle plan mode or plan one-shot"},
		{"/rules", "Show loaded rules and active status"},
		{"/skill [name]", "List skills or invoke one"},
		{"/memory", "Show stored memories"},
		{"/memory add <text>", "Save a memory manually"},
		{"/memory clear", "Clear all memories"},
		{"/memory off", "Disable auto-memory for this session"},
		{"/memory edit", "Open MEMORY.md in $EDITOR"},
		{"/ignore", "Show current ignore patterns"},
		{"/ignore add <pat>", "Add pattern to .openmarmutignore"},
		{"/ignore remove <pat>", "Remove pattern from .openmarmutignore"},
		{"/agent <task>", "Spawn a sub-agent for a task"},
		{"/agents", "List sub-agents in this session"},
		{"/agents kill <name>", "Stop a running sub-agent"},
		{"/mcp", "Show connected MCP servers and tools"},
		{"/btw <question>", "Quick side question (isolated context)"},
		{"/loop <int> <cmd>", "Run command on interval (e.g., /loop 5m go test)"},
		{"/loop status", "Show active loops"},
		{"/loop off", "Stop all loops"},
		{"/tasks", "Show tracked tasks"},
		{"/tasks add <title>", "Add a new task"},
		{"/tasks done <id>", "Mark task as completed"},
		{"/tasks clear", "Remove completed tasks"},
		{"/bg <task>", "Run task in background sub-agent"},
		{"/bg status", "Show background jobs"},
		{"/bg cancel <id>", "Cancel a background job"},
		{"/model", "Show current model"},
		{"/model <name>", "Switch model for this session"},
		{"/provider <name>", "Switch provider for this session"},
		{"/hooks", "List configured hooks"},
		{"/hooks on|off", "Enable/disable hooks for this session"},
		{"/hooks test <n>", "Test-fire a hook by index"},
		{"/commands", "List custom commands from .openmarmut/commands/"},
		{"/sessions", "List recent sessions"},
		{"/rename <name>", "Rename current session"},
		{"/help", "Show this help"},
		{"/quit", "Exit chat"},
	}
	headers := []string{"COMMAND", "DESCRIPTION"}
	fmt.Fprintln(w, ui.RenderBox("Commands", ui.RenderTable(headers, commands, -1)))
}

// renderMemory displays stored auto-memory entries.
func renderMemory(state *chatState) {
	mem := state.ag.Memory()
	if mem == nil {
		fmt.Fprintln(state.out, ui.FormatHint("Auto-memory is not enabled."))
		return
	}
	entries := mem.Entries()
	if len(entries) == 0 {
		fmt.Fprintln(state.out, ui.FormatHint("No memories stored yet."))
		return
	}

	headers := []string{"DATE", "PROJECT", "CATEGORY", "CONTENT"}
	var rows [][]string
	for _, e := range entries {
		content := e.Content
		if len(content) > 80 {
			content = content[:80] + "..."
		}
		project := "global"
		if e.Project != "" {
			project = e.Project
		}
		rows = append(rows, []string{
			e.Timestamp.Format("2006-01-02"),
			project,
			e.Category,
			content,
		})
	}
	fmt.Fprintln(state.out, ui.RenderTable(headers, rows, -1))
	fmt.Fprintln(state.out, ui.FormatHint(fmt.Sprintf("Path: %s", mem.Path())))
	if state.memoryDisabled {
		fmt.Fprintln(state.out, ui.FormatWarning("Auto-memory is disabled for this session."))
	}
}

// handleMemoryCommand processes /memory add, /memory clear, /memory off, /memory edit.
func handleMemoryCommand(line string, state *chatState) {
	arg := strings.TrimSpace(strings.TrimPrefix(line, "/memory"))

	mem := state.ag.Memory()

	if strings.HasPrefix(arg, "add ") {
		content := strings.TrimSpace(strings.TrimPrefix(arg, "add"))
		if content == "" {
			fmt.Fprintln(state.out, ui.FormatWarning("Usage: /memory add <text>"))
			return
		}
		if mem == nil {
			mem = agent.NewMemoryStore()
			if mem == nil {
				fmt.Fprintln(state.out, ui.FormatError("Cannot create memory store."))
				return
			}
		}
		if err := mem.Save("user", content); err != nil {
			fmt.Fprintln(state.out, ui.FormatError("Failed to save memory: "+err.Error()))
			return
		}
		fmt.Fprintln(state.out, ui.FormatSuccess("Memory saved."))
		return
	}

	if arg == "clear" {
		if mem == nil {
			fmt.Fprintln(state.out, ui.FormatHint("No memory store to clear."))
			return
		}
		if err := mem.Clear(); err != nil {
			fmt.Fprintln(state.out, ui.FormatError("Failed to clear memory: "+err.Error()))
			return
		}
		fmt.Fprintln(state.out, ui.FormatSuccess("All memories cleared."))
		return
	}

	if arg == "off" {
		state.memoryDisabled = true
		fmt.Fprintln(state.out, ui.FormatSuccess("Auto-memory disabled for this session."))
		return
	}

	if arg == "edit" {
		if mem == nil {
			fmt.Fprintln(state.out, ui.FormatWarning("No memory store configured."))
			return
		}
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		cmd := osexec.Command(editor, mem.Path())
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(state.out, ui.FormatError("Editor failed: "+err.Error()))
			return
		}
		// Reload memories after editing.
		if err := mem.Load(); err != nil {
			fmt.Fprintln(state.out, ui.FormatError("Failed to reload memories: "+err.Error()))
			return
		}
		fmt.Fprintln(state.out, ui.FormatSuccess(fmt.Sprintf("Reloaded %d memories.", mem.Count())))
		return
	}

	fmt.Fprintln(state.out, ui.FormatWarning("Usage: /memory, /memory add <text>, /memory clear, /memory off, /memory edit"))
}

// handleIgnore processes /ignore, /ignore add <pat>, /ignore remove <pat>.
func handleIgnore(line string, state *chatState) {
	arg := strings.TrimSpace(strings.TrimPrefix(line, "/ignore"))

	// /ignore — show current patterns.
	if arg == "" {
		il := state.ag.IgnoreList()
		fmt.Fprintln(state.out, agent.FormatIgnoreDisplay(il))
		return
	}

	ctx := context.Background()

	if strings.HasPrefix(arg, "add ") {
		pattern := strings.TrimSpace(strings.TrimPrefix(arg, "add "))
		if pattern == "" {
			fmt.Fprintln(state.out, ui.FormatWarning("Usage: /ignore add <pattern>"))
			return
		}
		if err := agent.AddPatternToFile(ctx, state.rt, pattern); err != nil {
			fmt.Fprintln(state.out, ui.FormatError("Failed to add pattern: "+err.Error()))
			return
		}
		fmt.Fprintln(state.out, ui.FormatSuccess("Added "+pattern+" to .openmarmutignore"))
		return
	}

	if strings.HasPrefix(arg, "remove ") {
		pattern := strings.TrimSpace(strings.TrimPrefix(arg, "remove "))
		if pattern == "" {
			fmt.Fprintln(state.out, ui.FormatWarning("Usage: /ignore remove <pattern>"))
			return
		}
		if err := agent.RemovePatternFromFile(ctx, state.rt, pattern); err != nil {
			fmt.Fprintln(state.out, ui.FormatError("Failed to remove pattern: "+err.Error()))
			return
		}
		fmt.Fprintln(state.out, ui.FormatSuccess("Removed "+pattern+" from .openmarmutignore"))
		return
	}

	fmt.Fprintln(state.out, ui.FormatWarning("Usage: /ignore, /ignore add <pattern>, /ignore remove <pattern>"))
}

// handleSkill processes /skill commands.
// /skill — list available skills.
// /skill <name> — display skill details and apply it.
func handleSkill(line string, state *chatState) {
	arg := strings.TrimSpace(strings.TrimPrefix(line, "/skill"))

	skills := state.ag.Skills()
	if len(skills) == 0 {
		fmt.Fprintln(state.out, ui.FormatHint("No skills loaded. Add .md files to .openmarmut/skills/"))
		return
	}

	if arg == "" || arg == "s" {
		// List all skills.
		headers := []string{"NAME", "TRIGGER", "DESCRIPTION"}
		var rows [][]string
		for _, s := range skills {
			rows = append(rows, []string{s.Name, s.Trigger, s.Description})
		}
		fmt.Fprintln(state.out, ui.RenderTable(headers, rows, -1))
		return
	}

	// Find and display a specific skill.
	skill := agent.FindSkill(skills, arg)
	if skill == nil {
		fmt.Fprintln(state.out, ui.FormatWarning("Unknown skill: "+arg))
		fmt.Fprintln(state.out, ui.FormatHint("Use /skill to list available skills"))
		return
	}

	fmt.Fprintln(state.out, ui.RenderBox("Skill: "+skill.Name, skill.Content))
	fmt.Fprintln(state.out, ui.FormatHint("This skill's prompt will be prepended to your next message."))

	// Store the skill content to prepend to the next user message.
	state.pendingSkill = skill.Content
}

// renderRules displays all loaded rules with their glob patterns and active status.
func renderRules(state *chatState) {
	rules := state.ag.Rules()
	if len(rules) == 0 {
		fmt.Fprintln(state.out, ui.FormatHint("No rules loaded. Add .md files to .openmarmut/rules/"))
		return
	}

	activeContent := state.ag.ActiveRulesContent()

	headers := []string{"SOURCE", "GLOBS", "ACTIVE"}
	var rows [][]string
	for _, r := range rules {
		globStr := strings.Join(r.Globs, ", ")
		if globStr == "" {
			globStr = "(always)"
		}
		active := "no"
		if activeContent != "" && strings.Contains(activeContent, r.Content) {
			active = "yes"
		}
		if len(r.Globs) == 0 {
			active = "yes" // No globs = always active.
		}
		rows = append(rows, []string{r.Source, globStr, active})
	}
	fmt.Fprintln(state.out, ui.RenderTable(headers, rows, -1))
}

// renderToolsTable displays tools with their permission levels.
func renderToolsTable(state *chatState) {
	headers := []string{"TOOL", "PERMISSION", "DESCRIPTION"}
	var rows [][]string

	var perms map[string]agent.PermissionLevel
	if state.permChecker != nil {
		perms = state.permChecker.Permissions()
	} else {
		perms = agent.DefaultPermissions()
	}

	for _, t := range state.ag.Tools() {
		level := perms[t.Def.Name]
		levelStr := level.String()
		if ui.ColorEnabled() {
			switch level {
			case agent.PermAuto:
				levelStr = ui.SuccessStyle.Render(levelStr)
			case agent.PermConfirm:
				levelStr = ui.WarningStyle.Render(levelStr)
			case agent.PermDeny:
				levelStr = ui.ErrorStyle.Render(levelStr)
			}
		}
		rows = append(rows, []string{t.Def.Name, levelStr, t.Def.Description})
	}
	fmt.Fprintln(state.out, ui.RenderTable(headers, rows, -1))
}

// renderCostBox displays accumulated session cost in a styled box.
func renderCostBox(state *chatState) {
	costStr := llm.FormatCost(state.sessionUsage, state.model)
	if costStr == "" {
		costStr = "unknown (no pricing for model)"
	}
	content := fmt.Sprintf("Prompt tokens:     %d\nCompletion tokens: %d\nTotal tokens:      %d\nEstimated cost:    ~%s",
		state.sessionUsage.PromptTokens,
		state.sessionUsage.CompletionTokens,
		state.sessionUsage.TotalTokens,
		costStr,
	)
	fmt.Fprintln(state.out, ui.RenderBox("Session Cost", content))
}

// renderContextBox displays detailed context window usage in a styled box.
func renderContextBox(state *chatState) {
	usage := state.ag.ContextUsage()
	cfg := state.ag.ContextConfig()

	content := fmt.Sprintf(
		"Model window:    %s tokens\n"+
			"Current usage:   ~%s tokens (%d%%)\n"+
			"History turns:   %d\n"+
			"System prompt:   ~%s tokens\n"+
			"Threshold:       %d%% (%s tokens)\n"+
			"\n%s",
		humanizeInt(usage.ContextWindow),
		humanizeInt(usage.EstimatedTokens),
		usage.Percent,
		usage.HistoryTurns,
		humanizeInt(usage.SystemTokens),
		int(cfg.TruncationRatio*100),
		humanizeInt(usage.Threshold),
		ui.RenderProgressBar(usage.Percent, 30),
	)
	fmt.Fprintln(state.out, ui.RenderBox("Context Window", content))
}

// renderMCPStatus displays connected MCP servers and their tool counts.
func renderMCPStatus(state *chatState) {
	if state.mcpMgr == nil {
		fmt.Fprintln(state.out, ui.FormatHint("No MCP servers configured."))
		return
	}

	clients := state.mcpMgr.Clients()
	if len(clients) == 0 {
		fmt.Fprintln(state.out, ui.FormatHint("No MCP servers connected."))
		return
	}

	headers := []string{"SERVER", "TRANSPORT", "TOOLS", "STATUS"}
	var rows [][]string
	totalTools := 0
	for _, c := range clients {
		tools := c.Tools()
		totalTools += len(tools)
		status := "disconnected"
		if c.Connected() {
			status = "connected"
		}
		rows = append(rows, []string{
			c.Name,
			c.Transport,
			fmt.Sprintf("%d", len(tools)),
			status,
		})
	}
	fmt.Fprintln(state.out, ui.RenderTable(headers, rows, -1))
	fmt.Fprintln(state.out, ui.FormatHint(fmt.Sprintf("%d MCP tool(s) available", totalTools)))
}

// renderSessionsList shows recent sessions inline in the chat REPL.
func renderSessionsList(state *chatState) {
	summaries, err := session.FindRecent(10)
	if err != nil {
		fmt.Fprintln(state.out, ui.FormatError("Failed to list sessions: "+err.Error()))
		return
	}
	if len(summaries) == 0 {
		fmt.Fprintln(state.out, ui.FormatHint("No sessions found."))
		return
	}

	headers := []string{"ID", "NAME", "AGE", "PROVIDER", "TURNS"}
	var rows [][]string
	for _, s := range summaries {
		rows = append(rows, []string{
			s.ID,
			displayName(s.Name),
			humanizeAge(s.UpdatedAt),
			s.Provider,
			fmt.Sprintf("%d", s.Messages),
		})
	}
	fmt.Fprintln(state.out, ui.RenderTable(headers, rows, -1))
}

// humanizeInt formats an integer with comma separators (e.g. 128000 → "128,000").
func humanizeInt(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var result []byte
	for i, ch := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(ch))
	}
	return string(result)
}

// interactiveConfirm creates a ConfirmFunc that prompts the user on stderr/stdin
// using a styled confirmation box.
// It stops the active spinner before prompting and restarts it after input.
func interactiveConfirm(state *chatState) agent.ConfirmFunc {
	return func(tc llm.ToolCall, preview string) agent.ConfirmResult {
		// Stop the spinner so it doesn't overwrite the prompt.
		if state.spinner != nil {
			state.spinner.Stop()
			state.spinner = nil
		}

		// RenderConfirmBox already includes the [y]es / [n]o / [a]lways footer.
		// Print the box, then a "> " prompt on a new line for input.
		fmt.Fprint(os.Stderr, ui.RenderConfirmBox(preview))
		fmt.Fprint(os.Stderr, "\n> ")

		// Read input, skipping stale empty lines that may be left in the
		// scanner buffer from the main input loop (e.g. if the user pressed
		// Enter twice or the kernel bundled extra newlines).
		var answer string
		for {
			if !state.scanner.Scan() {
				return agent.ConfirmNo
			}
			answer = strings.TrimSpace(strings.ToLower(state.scanner.Text()))
			if answer != "" {
				break
			}
			// Empty line — show prompt again and retry.
			fmt.Fprint(os.Stderr, "> ")
		}

		// Restart spinner — agent continues working after user responds.
		state.spinner = ui.NewSpinner(os.Stderr, "Working...")
		state.spinner.Start()

		switch answer {
		case "y", "yes":
			return agent.ConfirmYes
		case "a", "always":
			return agent.ConfirmAlways
		default:
			return agent.ConfirmNo
		}
	}
}

// handleHooks processes /hooks commands.
// /hooks          — list all configured hooks
// /hooks test <n> — test-fire a hook with sample payload
// /hooks off      — disable hooks for this session
// /hooks on       — re-enable hooks
func handleHooks(line string, state *chatState) {
	arg := strings.TrimSpace(strings.TrimPrefix(line, "/hooks"))

	switch {
	case arg == "" || arg == " ":
		if len(state.hooks) == 0 {
			fmt.Fprintln(state.out, ui.FormatHint("No hooks configured. Add hooks to .openmarmut.yaml"))
			return
		}
		fmt.Fprintln(state.out, ui.RenderBox("Hooks", agent.FormatHooksList(state.hooks)))
		if !state.ag.HooksEnabled() {
			fmt.Fprintln(state.out, ui.FormatWarning("Hooks are currently disabled for this session."))
		}

	case arg == "off":
		state.ag.SetHooksEnabled(false)
		fmt.Fprintln(state.out, ui.FormatSuccess("Hooks disabled for this session."))

	case arg == "on":
		if len(state.hooks) == 0 {
			fmt.Fprintln(state.out, ui.FormatWarning("No hooks configured."))
			return
		}
		state.ag.SetHooksEnabled(true)
		fmt.Fprintln(state.out, ui.FormatSuccess("Hooks re-enabled."))

	case strings.HasPrefix(arg, "test "):
		idxStr := strings.TrimSpace(strings.TrimPrefix(arg, "test "))
		idx, parseErr := strconv.Atoi(idxStr)
		if parseErr != nil || idx < 0 || idx >= len(state.hooks) {
			fmt.Fprintln(state.out, ui.FormatWarning(
				fmt.Sprintf("Usage: /hooks test <index> (0-%d)", len(state.hooks)-1)))
			return
		}
		h := state.hooks[idx]
		payload := agent.HookPayload{
			Tool:      "test_tool",
			Arguments: json.RawMessage(`{"test": true}`),
			Session:   state.ag.SessionID(),
		}
		fmt.Fprintf(state.out, "Testing hook %q (%s)...\n", h.Name, h.Type)
		testHooks := []agent.Hook{h}
		err := agent.RunHooks(context.Background(), testHooks, h.Event, payload, state.log)
		if err != nil {
			fmt.Fprintln(state.out, ui.FormatError("Hook test failed: "+err.Error()))
		} else {
			fmt.Fprintln(state.out, ui.FormatSuccess("Hook test passed."))
		}

	default:
		fmt.Fprintln(state.out, ui.FormatWarning("Usage: /hooks, /hooks on, /hooks off, /hooks test <index>"))
	}
}

// handleAgent processes the /agent slash command.
// Usage: /agent "task description"
//        /agent --provider <name> "task description"
//        /agent --name <name> "task description"
func handleAgent(line string, state *chatState) {
	arg := strings.TrimSpace(strings.TrimPrefix(line, "/agent"))
	if arg == "" {
		fmt.Fprintln(state.out, ui.FormatWarning("Usage: /agent [--provider <name>] [--name <name>] <task>"))
		return
	}

	// Parse flags.
	var providerName, agentName string
	parts := strings.Fields(arg)
	var taskParts []string
	for i := 0; i < len(parts); i++ {
		switch parts[i] {
		case "--provider":
			if i+1 < len(parts) {
				i++
				providerName = parts[i]
			}
		case "--name":
			if i+1 < len(parts) {
				i++
				agentName = parts[i]
			}
		default:
			taskParts = append(taskParts, parts[i])
		}
	}
	task := strings.Join(taskParts, " ")
	// Strip surrounding quotes if present.
	task = strings.Trim(task, "\"'")
	if task == "" {
		fmt.Fprintln(state.out, ui.FormatWarning("Usage: /agent <task>"))
		return
	}

	// Resolve provider.
	provider := state.provider
	if providerName != "" && state.cfg != nil {
		entry := state.cfg.LLM.FindProvider(providerName)
		if entry == nil {
			fmt.Fprintln(state.out, ui.FormatError(fmt.Sprintf("Provider '%s' not found.", providerName)))
			return
		}
		p, err := llm.NewProvider(*entry, state.log)
		if err != nil {
			fmt.Fprintln(state.out, ui.FormatError("Failed to create provider: "+err.Error()))
			return
		}
		provider = p
	}

	// Show sub-agent start box.
	name := agentName
	if name == "" {
		name = "sub-agent"
	}
	fmt.Fprintf(state.out, "%s\n", ui.RenderBox(
		fmt.Sprintf("Sub-agent: %s", name),
		fmt.Sprintf("Task: %s\nProvider: %s (%s)\nStatus: Running...",
			task, provider.Name(), provider.Model()),
	))

	// Spin up the sub-agent synchronously.
	spin := ui.NewSpinner(state.out, "Sub-agent working...")
	spin.Start()

	sa, err := agent.SpawnSubAgent(context.Background(), agent.SubAgentOpts{
		Name:       agentName,
		Task:       task,
		Provider:   provider,
		Runtime:    state.rt,
		Logger:     state.log,
		IgnoreList: state.ag.IgnoreList(),
		Config:     state.cfg,
	})
	spin.Stop()

	if state.subMgr != nil {
		// Track in the manager even for synchronous spawns.
		state.subMgr.Track(sa)
	}

	if err != nil {
		fmt.Fprintf(state.out, "%s\n", ui.FormatError("Sub-agent failed: "+err.Error()))
		return
	}

	// Show completion box.
	fmt.Fprintf(state.out, "%s\n", ui.RenderBox(
		fmt.Sprintf("Sub-agent: %s — completed", sa.Name),
		fmt.Sprintf("%s\n[%d tool calls │ %d tokens │ %s]",
			sa.Result,
			sa.Steps,
			sa.Usage.TotalTokens,
			sa.Duration.Round(time.Millisecond)),
	))
}

// handleAgents processes the /agents slash command.
// Usage: /agents          — list all sub-agents in this session
//        /agents kill <n> — stop a running sub-agent
func handleAgents(line string, state *chatState) {
	arg := strings.TrimSpace(strings.TrimPrefix(line, "/agents"))

	if state.subMgr == nil {
		fmt.Fprintln(state.out, ui.FormatHint("No sub-agents have been spawned in this session."))
		return
	}

	if strings.HasPrefix(arg, "kill ") {
		name := strings.TrimSpace(strings.TrimPrefix(arg, "kill"))
		if name == "" {
			fmt.Fprintln(state.out, ui.FormatWarning("Usage: /agents kill <name>"))
			return
		}
		if state.subMgr.Kill(name) {
			fmt.Fprintln(state.out, ui.FormatSuccess("Killed sub-agent: "+name))
		} else {
			fmt.Fprintln(state.out, ui.FormatWarning("No running sub-agent named: "+name))
		}
		return
	}

	agents := state.subMgr.List()
	if len(agents) == 0 {
		fmt.Fprintln(state.out, ui.FormatHint("No sub-agents have been spawned in this session."))
		return
	}

	headers := []string{"NAME", "STATUS", "TASK", "TOKENS", "DURATION"}
	var rows [][]string
	for _, sa := range agents {
		task := sa.Task
		if len(task) > 50 {
			task = task[:50] + "..."
		}
		dur := ""
		if sa.Duration > 0 {
			dur = sa.Duration.Round(time.Millisecond).String()
		}
		rows = append(rows, []string{
			sa.Name,
			sa.Status,
			task,
			fmt.Sprintf("%d", sa.Usage.TotalTokens),
			dur,
		})
	}
	fmt.Fprintln(state.out, ui.RenderTable(headers, rows, -1))
}

// --- Custom Commands ---

// renderCustomCommands lists all custom commands.
func renderCustomCommands(state *chatState) {
	if len(state.customCommands) == 0 {
		fmt.Fprintln(state.out, ui.FormatHint("No custom commands found. Add .md files to .openmarmut/commands/"))
		return
	}

	headers := []string{"COMMAND", "DESCRIPTION"}
	var rows [][]string
	for _, cmd := range state.customCommands {
		rows = append(rows, []string{"/" + cmd.Name, cmd.Description})
	}
	fmt.Fprintln(state.out, ui.RenderTable(headers, rows, -1))
}

// tryCustomCommand checks if the line matches a custom command and returns slashNone if not.
// If matched, sets customCmdContent on state and returns slashNone to let the main loop send it.
func tryCustomCommand(line string, state *chatState) slashAction {
	if len(state.customCommands) == 0 {
		return slashNone
	}

	// Extract command name and optional arguments.
	parts := strings.SplitN(line, " ", 2)
	cmdName := strings.TrimPrefix(parts[0], "/")
	cmdArgs := ""
	if len(parts) > 1 {
		cmdArgs = parts[1]
	}

	cmd := agent.FindCustomCommand(state.customCommands, cmdName)
	if cmd == nil {
		return slashNone
	}

	// Build the message from command content + arguments.
	msg := cmd.Content
	if cmdArgs != "" {
		msg += " " + cmdArgs
	}

	state.customCmdContent = msg
	fmt.Fprintln(state.out, ui.FormatHint(fmt.Sprintf("Running custom command: /%s", cmd.Name)))
	return slashHandled // Mark as handled; main loop will check customCmdContent.
}

// --- /btw Side Questions ---

// handleBtw processes /btw questions using a temporary sub-agent with no context.
func handleBtw(line string, state *chatState) {
	question := strings.TrimSpace(strings.TrimPrefix(line, "/btw"))
	if question == "" {
		fmt.Fprintln(state.out, ui.FormatWarning("Usage: /btw <question>"))
		return
	}

	provider := state.provider
	if provider == nil {
		fmt.Fprintln(state.out, ui.FormatError("No LLM provider configured."))
		return
	}

	spin := ui.NewSpinner(state.out, "Thinking...")
	spin.Start()

	// Create a one-shot request with no tools and no conversation context.
	req := llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "You are a helpful assistant. Answer concisely."},
			{Role: llm.RoleUser, Content: question},
		},
	}

	resp, err := provider.Complete(context.Background(), req, nil)
	spin.Stop()

	if err != nil {
		fmt.Fprintln(state.out, ui.FormatError("btw failed: "+err.Error()))
		return
	}

	// Display response in a visually distinct box.
	fmt.Fprintln(state.out, ui.RenderBox("btw", resp.Content))

	// Show cost separately tagged as "btw".
	costStr := llm.FormatCost(resp.Usage, state.model)
	if costStr != "" {
		fmt.Fprintln(state.out, ui.FormatHint(fmt.Sprintf("btw cost: ~%s (%d tokens)", costStr, resp.Usage.TotalTokens)))
	}
}

// --- /loop Mode ---

// loopEntry represents a single running loop.
type loopEntry struct {
	ID       int
	Command  string
	Interval time.Duration
	cancel   context.CancelFunc
}

// loopManager tracks background loop tasks.
type loopManager struct {
	mu      sync.Mutex
	loops   []*loopEntry
	nextID  int
	rt      runtime.Runtime
	out     io.Writer
}

// newLoopManager creates a new loop manager.
func newLoopManager(rt runtime.Runtime, out io.Writer) *loopManager {
	return &loopManager{rt: rt, out: out}
}

// Start begins a new loop with the given interval and command.
func (m *loopManager) Start(interval time.Duration, command string) int {
	m.mu.Lock()
	m.nextID++
	id := m.nextID

	ctx, cancel := context.WithCancel(context.Background())
	entry := &loopEntry{
		ID:       id,
		Command:  command,
		Interval: interval,
		cancel:   cancel,
	}
	m.loops = append(m.loops, entry)
	m.mu.Unlock()

	go m.run(ctx, entry)
	return id
}

// run executes the loop in a background goroutine.
func (m *loopManager) run(ctx context.Context, entry *loopEntry) {
	ticker := time.NewTicker(entry.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			start := time.Now()
			result, err := m.rt.Exec(ctx, entry.Command, runtime.ExecOpts{
				Timeout: 60 * time.Second,
			})
			elapsed := time.Since(start)

			now := time.Now().Format("15:04:05")
			if err != nil {
				fmt.Fprintf(m.out, "\a[loop %s] %s — ERROR (%s): %s\n",
					now, entry.Command, elapsed.Round(time.Millisecond), err.Error())
				continue
			}

			if result.ExitCode == 0 {
				fmt.Fprintf(m.out, "[loop %s] %s — PASS (%s)\n",
					now, entry.Command, elapsed.Round(time.Millisecond))
			} else {
				// Bell character for failure notification.
				stderr := strings.TrimSpace(result.Stderr)
				if stderr == "" {
					stderr = strings.TrimSpace(result.Stdout)
				}
				// Compact: show first 2 lines of output.
				lines := strings.SplitN(stderr, "\n", 3)
				summary := strings.Join(lines[:min(len(lines), 2)], "\n  → ")
				fmt.Fprintf(m.out, "\a[loop %s] %s — FAIL (exit %d, %s)\n  → %s\n",
					now, entry.Command, result.ExitCode, elapsed.Round(time.Millisecond), summary)
			}
		}
	}
}

// StopAll cancels all running loops.
func (m *loopManager) StopAll() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, entry := range m.loops {
		if entry.cancel != nil {
			entry.cancel()
			count++
		}
	}
	m.loops = nil
	return count
}

// Status returns active loops.
func (m *loopManager) Status() []*loopEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]*loopEntry, len(m.loops))
	copy(cp, m.loops)
	return cp
}

// handleLoop processes /loop commands.
// /loop <duration> <command> — start a new loop.
// /loop status — show active loops.
// /loop off — stop all loops.
func handleLoop(line string, state *chatState) {
	arg := strings.TrimSpace(strings.TrimPrefix(line, "/loop"))

	if arg == "" {
		fmt.Fprintln(state.out, ui.FormatWarning("Usage: /loop <interval> <command>, /loop status, /loop off"))
		return
	}

	if arg == "off" {
		if state.loopMgr == nil {
			fmt.Fprintln(state.out, ui.FormatHint("No active loops."))
			return
		}
		count := state.loopMgr.StopAll()
		fmt.Fprintln(state.out, ui.FormatSuccess(fmt.Sprintf("Stopped %d loop(s).", count)))
		return
	}

	if arg == "status" {
		if state.loopMgr == nil || len(state.loopMgr.Status()) == 0 {
			fmt.Fprintln(state.out, ui.FormatHint("No active loops."))
			return
		}

		headers := []string{"ID", "INTERVAL", "COMMAND"}
		var rows [][]string
		for _, entry := range state.loopMgr.Status() {
			rows = append(rows, []string{
				fmt.Sprintf("%d", entry.ID),
				entry.Interval.String(),
				entry.Command,
			})
		}
		fmt.Fprintln(state.out, ui.RenderTable(headers, rows, -1))
		return
	}

	// Parse: <duration> <command>
	parts := strings.SplitN(arg, " ", 2)
	if len(parts) < 2 {
		fmt.Fprintln(state.out, ui.FormatWarning("Usage: /loop <interval> <command>"))
		return
	}

	interval, err := time.ParseDuration(parts[0])
	if err != nil {
		fmt.Fprintln(state.out, ui.FormatError(fmt.Sprintf("Invalid interval %q: %s", parts[0], err.Error())))
		return
	}

	if interval < 1*time.Second {
		fmt.Fprintln(state.out, ui.FormatWarning("Interval must be at least 1s."))
		return
	}

	command := strings.TrimSpace(parts[1])
	if command == "" {
		fmt.Fprintln(state.out, ui.FormatWarning("Usage: /loop <interval> <command>"))
		return
	}

	if state.loopMgr == nil {
		state.loopMgr = newLoopManager(state.rt, state.out)
	}

	id := state.loopMgr.Start(interval, command)
	fmt.Fprintln(state.out, ui.FormatSuccess(
		fmt.Sprintf("Loop #%d started: %q every %s", id, command, interval)))
}

// --- /tasks Slash Command ---

// handleTasks processes /tasks commands.
// /tasks — show all tasks.
// /tasks add <title> — add a new task.
// /tasks done <id> — mark a task as completed.
// /tasks clear — remove completed tasks.
func handleTasks(line string, state *chatState) {
	arg := strings.TrimSpace(strings.TrimPrefix(line, "/tasks"))

	if state.taskList == nil {
		fmt.Fprintln(state.out, ui.FormatHint("No task list active."))
		return
	}

	if arg == "" {
		tasks := state.taskList.All()
		if len(tasks) == 0 {
			fmt.Fprintln(state.out, ui.FormatHint("No tasks. Use /tasks add <title> to create one."))
			return
		}
		fmt.Fprint(state.out, agent.FormatTaskList(tasks))
		return
	}

	if strings.HasPrefix(arg, "add ") {
		title := strings.TrimSpace(strings.TrimPrefix(arg, "add"))
		if title == "" {
			fmt.Fprintln(state.out, ui.FormatWarning("Usage: /tasks add <title>"))
			return
		}
		task := state.taskList.Add(title)
		state.taskList.Save() //nolint:errcheck
		fmt.Fprintln(state.out, ui.FormatSuccess(fmt.Sprintf("Created task #%d: %s", task.ID, task.Title)))
		return
	}

	if strings.HasPrefix(arg, "done ") {
		idStr := strings.TrimSpace(strings.TrimPrefix(arg, "done"))
		id, err := strconv.Atoi(idStr)
		if err != nil || id < 1 {
			fmt.Fprintln(state.out, ui.FormatWarning("Usage: /tasks done <id>"))
			return
		}
		if err := state.taskList.Update(id, "completed"); err != nil {
			fmt.Fprintln(state.out, ui.FormatError(err.Error()))
			return
		}
		state.taskList.Save() //nolint:errcheck
		fmt.Fprintln(state.out, ui.FormatSuccess(fmt.Sprintf("Task #%d marked as completed.", id)))
		return
	}

	if arg == "clear" {
		removed := state.taskList.ClearCompleted()
		state.taskList.Save() //nolint:errcheck
		fmt.Fprintln(state.out, ui.FormatSuccess(fmt.Sprintf("Cleared %d completed task(s).", removed)))
		return
	}

	fmt.Fprintln(state.out, ui.FormatWarning("Usage: /tasks, /tasks add <title>, /tasks done <id>, /tasks clear"))
}

// --- /bg Background Execution ---

// bgJob tracks a background sub-agent execution.
type bgJob struct {
	ID       int
	Task     string
	Status   string // "running", "completed", "failed", "cancelled"
	Result   string
	cancel   context.CancelFunc
}

// handleBg processes /bg commands.
// /bg <task> — run task in background.
// /bg status — show background jobs.
// /bg cancel <id> — cancel a background job.
func handleBg(line string, state *chatState) {
	arg := strings.TrimSpace(strings.TrimPrefix(line, "/bg"))

	if arg == "" {
		fmt.Fprintln(state.out, ui.FormatWarning("Usage: /bg <task>, /bg status, /bg cancel <id>"))
		return
	}

	if arg == "status" {
		state.bgMu.Lock()
		jobs := make([]*bgJob, len(state.bgJobs))
		copy(jobs, state.bgJobs)
		state.bgMu.Unlock()

		if len(jobs) == 0 {
			fmt.Fprintln(state.out, ui.FormatHint("No background jobs."))
			return
		}

		headers := []string{"ID", "STATUS", "TASK"}
		var rows [][]string
		for _, j := range jobs {
			task := j.Task
			if len(task) > 60 {
				task = task[:60] + "..."
			}
			rows = append(rows, []string{
				fmt.Sprintf("%d", j.ID),
				j.Status,
				task,
			})
		}
		fmt.Fprintln(state.out, ui.RenderTable(headers, rows, -1))
		return
	}

	if strings.HasPrefix(arg, "cancel ") {
		idStr := strings.TrimSpace(strings.TrimPrefix(arg, "cancel"))
		id, err := strconv.Atoi(idStr)
		if err != nil || id < 1 {
			fmt.Fprintln(state.out, ui.FormatWarning("Usage: /bg cancel <id>"))
			return
		}

		state.bgMu.Lock()
		var found *bgJob
		for _, j := range state.bgJobs {
			if j.ID == id {
				found = j
				break
			}
		}
		state.bgMu.Unlock()

		if found == nil {
			fmt.Fprintln(state.out, ui.FormatWarning(fmt.Sprintf("No background job #%d.", id)))
			return
		}
		if found.Status != "running" {
			fmt.Fprintln(state.out, ui.FormatHint(fmt.Sprintf("Job #%d is already %s.", id, found.Status)))
			return
		}
		found.cancel()
		found.Status = "cancelled"
		fmt.Fprintln(state.out, ui.FormatSuccess(fmt.Sprintf("Cancelled background job #%d.", id)))
		return
	}

	// /bg <task> — spawn background sub-agent.
	task := strings.Trim(arg, "\"'")
	if task == "" {
		fmt.Fprintln(state.out, ui.FormatWarning("Usage: /bg <task>"))
		return
	}

	provider := state.provider
	if provider == nil {
		fmt.Fprintln(state.out, ui.FormatError("No LLM provider configured."))
		return
	}

	state.bgMu.Lock()
	state.bgNextID++
	job := &bgJob{
		ID:     state.bgNextID,
		Task:   task,
		Status: "running",
	}
	ctx, cancel := context.WithCancel(context.Background())
	job.cancel = cancel
	state.bgJobs = append(state.bgJobs, job)
	state.bgMu.Unlock()

	fmt.Fprintln(state.out, ui.FormatSuccess(fmt.Sprintf("Background job #%d started: %s", job.ID, task)))

	go func() {
		defer cancel()
		sa, err := agent.SpawnSubAgent(ctx, agent.SubAgentOpts{
			Name:       fmt.Sprintf("bg-%d", job.ID),
			Task:       task,
			Provider:   provider,
			Runtime:    state.rt,
			Logger:     state.log,
			IgnoreList: state.ag.IgnoreList(),
			Config:     state.cfg,
		})

		state.bgMu.Lock()
		defer state.bgMu.Unlock()

		if job.Status == "cancelled" {
			return
		}

		if err != nil {
			job.Status = "failed"
			job.Result = err.Error()
			fmt.Fprintf(state.out, "\a[bg #%d] FAILED: %s\n", job.ID, err.Error())
			return
		}

		job.Status = "completed"
		job.Result = sa.Result
		fmt.Fprintf(state.out, "\a[bg #%d] DONE (%d tokens): %s\n", job.ID, sa.Usage.TotalTokens,
			truncatePreviewStr(sa.Result, 120))
	}()
}

// truncatePreviewStr truncates a string for display.
func truncatePreviewStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// --- /model and /provider Slash Commands ---

// handleModel processes /model commands.
// /model — show current model and provider.
// /model <name> — switch model for this session.
func handleModel(line string, state *chatState) {
	arg := strings.TrimSpace(strings.TrimPrefix(line, "/model"))

	if arg == "" {
		fmt.Fprintln(state.out, ui.RenderBox("Current Model",
			fmt.Sprintf("Provider: %s\nModel:    %s", state.provider.Name(), state.model)))
		return
	}

	// Find a provider entry with the given model name.
	if state.cfg == nil {
		fmt.Fprintln(state.out, ui.FormatError("No config available for model switching."))
		return
	}

	// Search across providers for the model name.
	var matchEntry *llm.ProviderEntry
	for i := range state.cfg.LLM.Providers {
		if state.cfg.LLM.Providers[i].ModelName == arg {
			matchEntry = &state.cfg.LLM.Providers[i]
			break
		}
	}

	if matchEntry == nil {
		// Try creating a new entry with current provider type but different model.
		currentEntry := state.cfg.LLM.FindProvider(state.provider.Name())
		if currentEntry == nil {
			fmt.Fprintln(state.out, ui.FormatError("Cannot resolve current provider for model switch."))
			return
		}
		modified := *currentEntry
		modified.ModelName = arg
		matchEntry = &modified
	}

	newProvider, err := llm.NewProvider(*matchEntry, state.log)
	if err != nil {
		fmt.Fprintln(state.out, ui.FormatError("Failed to create provider: "+err.Error()))
		return
	}
	newProvider = llm.NewRetryProvider(newProvider, llm.RetryConfig{}, state.log)

	state.provider = newProvider
	state.model = newProvider.Model()

	// Persist model in session.
	if state.sess != nil {
		state.sess.Model = state.model
		state.sess.Provider = newProvider.Name()
		go session.Save(state.sess) //nolint:errcheck
	}

	fmt.Fprintln(state.out, ui.FormatSuccess(fmt.Sprintf("Switched to model: %s (%s)", state.model, newProvider.Name())))
}

// handleProvider processes /provider commands.
// /provider <name> — switch to a configured provider.
func handleProvider(line string, state *chatState) {
	arg := strings.TrimSpace(strings.TrimPrefix(line, "/provider"))

	if arg == "" {
		fmt.Fprintln(state.out, ui.FormatWarning("Usage: /provider <name>"))
		if state.cfg != nil {
			var names []string
			for _, p := range state.cfg.LLM.Providers {
				names = append(names, p.Name)
			}
			if len(names) > 0 {
				fmt.Fprintln(state.out, ui.FormatHint("Available: "+strings.Join(names, ", ")))
			}
		}
		return
	}

	if state.cfg == nil {
		fmt.Fprintln(state.out, ui.FormatError("No config available for provider switching."))
		return
	}

	entry := state.cfg.LLM.FindProvider(arg)
	if entry == nil {
		fmt.Fprintln(state.out, ui.FormatError(fmt.Sprintf("Provider '%s' not found.", arg)))
		var names []string
		for _, p := range state.cfg.LLM.Providers {
			names = append(names, p.Name)
		}
		if len(names) > 0 {
			fmt.Fprintln(state.out, ui.FormatHint("Available: "+strings.Join(names, ", ")))
		}
		return
	}

	newProvider, err := llm.NewProvider(*entry, state.log)
	if err != nil {
		fmt.Fprintln(state.out, ui.FormatError("Failed to create provider: "+err.Error()))
		return
	}
	newProvider = llm.NewRetryProvider(newProvider, llm.RetryConfig{}, state.log)

	state.provider = newProvider
	state.model = newProvider.Model()

	// Persist in session.
	if state.sess != nil {
		state.sess.Provider = newProvider.Name()
		state.sess.Model = state.model
		go session.Save(state.sess) //nolint:errcheck
	}

	fmt.Fprintln(state.out, ui.FormatSuccess(fmt.Sprintf("Switched to provider: %s (%s)", newProvider.Name(), state.model)))
}

func newChatCmd(runner *Runner) *cobra.Command {
	var (
		continueFlag bool
		resumeFlag   string
		sessionName  string
	)

	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Interactive chat with the AI (multi-turn with tools)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(runner.flags)
			if err != nil {
				return fmt.Errorf("chat: %w", err)
			}

			log := logger.New(cfg.Log)

			entry, err := cfg.LLM.ResolveActiveProvider()
			if err != nil {
				return fmt.Errorf("chat: %w", err)
			}

			rawProvider, err := llm.NewProvider(*entry, log)
			if err != nil {
				return fmt.Errorf("chat: %w", err)
			}
			provider := llm.NewRetryProvider(rawProvider, llm.RetryConfig{}, log)

			rt, err := initRuntime(cmd.Context(), cfg, log)
			if err != nil {
				return fmt.Errorf("chat: %w", err)
			}
			defer rt.Close(cmd.Context())

			scanner := bufio.NewScanner(os.Stdin)

			// Build state early so interactiveConfirm can reference it.
			state := &chatState{
				scanner: scanner,
				model:   provider.Model(),
				out:     os.Stderr,
			}

			// Build permission checker.
			perms := agent.BuildPermissions(cfg.Agent.AutoAllow, cfg.Agent.Confirm)
			var confirmFn agent.ConfirmFunc
			if !runner.flags.AutoApprove {
				confirmFn = interactiveConfirm(state)
			}
			pc := agent.NewPermissionChecker(perms, confirmFn)

			var opts []agent.Option
			temp := resolveTemperature(cfg, entry)
			if temp != nil {
				opts = append(opts, agent.WithTemperature(temp))
			}
			maxTok := resolveMaxTokens(cfg, entry)
			if maxTok != nil {
				opts = append(opts, agent.WithMaxTokens(maxTok))
			}

			// Build context config: provider default -> agent config overrides.
			ctxCfg := agent.DefaultContextConfig()
			if entry.ContextWindow > 0 {
				ctxCfg.ContextWindow = entry.ContextWindow
			}
			if cfg.Agent.ContextWindow > 0 {
				ctxCfg.ContextWindow = cfg.Agent.ContextWindow
			}
			if cfg.Agent.TruncationThreshold > 0 {
				ctxCfg.TruncationRatio = cfg.Agent.TruncationThreshold
			}
			if cfg.Agent.KeepRecentTurns > 0 {
				ctxCfg.KeepRecentTurns = cfg.Agent.KeepRecentTurns
			}
			opts = append(opts, agent.WithContextConfig(ctxCfg))

			// Extended thinking from provider entry config.
			if entry.ExtendedThinking {
				opts = append(opts, agent.WithExtendedThinking(true, entry.ThinkingBudget))
			}

			// Show tool calls inline using styled FormatToolCall.
			opts = append(opts, agent.WithToolCallCallback(func(tc llm.ToolCall) {
				argSummary := formatToolArgs(tc)
				fmt.Fprintln(os.Stderr, ui.FormatToolCall(tc.Name, argSummary))
			}))

			opts = append(opts, agent.WithPermissionChecker(pc))

			// File checkpointing for /rewind.
			checkpoints := agent.NewCheckpointStore()
			opts = append(opts, agent.WithCheckpointStore(checkpoints))

			// Load project instructions from OPENMARMUT.md files.
			projInfo, _ := agent.LoadProjectInstructions(cmd.Context(), rt)
			if projInfo != nil && projInfo.Content != "" {
				opts = append(opts, agent.WithProjectInstructions(projInfo.Content))
			}

			// Load rules from .openmarmut/rules/.
			rules, _ := agent.LoadRules(cmd.Context(), rt)
			if len(rules) > 0 {
				opts = append(opts, agent.WithRules(rules))
			}

			// Load skills from .openmarmut/skills/.
			skills, _ := agent.LoadSkills(cmd.Context(), rt)
			if len(skills) > 0 {
				opts = append(opts, agent.WithSkills(skills))
			}

			// Load custom commands from .openmarmut/commands/.
			customCmds, _ := agent.LoadCustomCommands(cmd.Context(), rt)

			// Load ignore list from .openmarmutignore.
			ignoreList := agent.LoadIgnoreList(cmd.Context(), rt)
			if ignoreList != nil && len(ignoreList.Patterns()) > 0 {
				opts = append(opts, agent.WithIgnoreList(ignoreList))
			}

			// Load auto-memory from previous sessions.
			if cfg.Agent.AutoMemory {
				memStore := agent.NewMemoryStoreWithPath(cfg.Agent.MemoryFile)
				if memStore != nil {
					memStore.Load() //nolint:errcheck
					opts = append(opts, agent.WithMemoryStore(memStore))
				}
			}

			// Enable sub-agent spawning via tool call.
			opts = append(opts, agent.WithSubAgentProvider(provider, log))

			// Load hooks from config.
			hooks, hookErr := agent.LoadHooks(cfg)
			if hookErr != nil {
				fmt.Fprintln(os.Stderr, ui.FormatWarning("Hooks: "+hookErr.Error()))
			}

			// Pre-generate session ID for task list.
			preSessionID := session.NewID()
			taskList := agent.NewTaskList(preSessionID)
			if taskList != nil {
				taskList.Load() //nolint:errcheck
				opts = append(opts, agent.WithTaskList(taskList))
			}

			// Connect to MCP servers.
			var mcpMgr *mcp.Manager
			if len(cfg.MCP.Servers) > 0 {
				mcpMgr = mcp.NewManager()
				errs := mcpMgr.ConnectAll(cmd.Context(), cfg.MCP.Servers)
				for _, e := range errs {
					fmt.Fprintln(os.Stderr, ui.FormatWarning("MCP: "+e.Error()))
				}
				clients := mcpMgr.Clients()
				if len(clients) > 0 {
					opts = append(opts, agent.WithMCPManager(mcpMgr))
					// Register MCP tool permissions as confirm.
					mcpPerms := agent.MCPToolPermissions(mcpMgr)
					for name, level := range mcpPerms {
						perms[name] = level
					}
					// Rebuild permission checker with MCP tools.
					pc = agent.NewPermissionChecker(perms, confirmFn)
					// Replace existing WithPermissionChecker option.
					for i, opt := range opts {
						// We can't easily replace, so just append; last one wins in Agent.
						_ = i
						_ = opt
					}
					opts = append(opts, agent.WithPermissionChecker(pc))
				}
			}

			// Wire hooks into agent.
			if len(hooks) > 0 {
				opts = append(opts, agent.WithHooks(hooks, preSessionID))
			}

			ag := agent.New(provider, rt, log, opts...)

			state.ag = ag
			state.permChecker = pc
			state.rt = rt
			state.provider = provider
			state.targetDir = cfg.TargetDir
			state.autoMemory = cfg.Agent.AutoMemory
			state.subMgr = agent.NewSubAgentManager()
			state.cfg = cfg
			state.log = log
			state.mcpMgr = mcpMgr
			state.customCommands = customCmds
			state.taskList = taskList
			state.hooks = hooks
			if mcpMgr != nil {
				defer mcpMgr.CloseAll()
			}

			// Detect git repo for dirty state warning and /diff+/commit.
			state.isGitRepo = isGitRepo(cmd.Context(), rt)

			// Session cleanup on startup (non-blocking).
			retDays := cfg.Agent.SessionRetentionDays
			if retDays <= 0 {
				retDays = session.DefaultRetentionDays
			}
			go session.Cleanup(retDays) //nolint:errcheck

			// Resolve session: resume existing or create new.
			var sess *session.Session
			resumeID := resolveResumeID(continueFlag, resumeFlag, cfg.TargetDir, scanner, os.Stderr)

			if resumeID != "" {
				loaded, loadErr := session.Load(resumeID)
				if loadErr != nil {
					return fmt.Errorf("chat: resume session: %w", loadErr)
				}
				sess = loaded

				// Restore conversation history into the agent.
				if len(sess.Messages) > 0 {
					ag.SetHistory(sess.Messages)
				}

				// Warn if provider or mode changed.
				if sess.Provider != provider.Name() {
					fmt.Fprintf(os.Stderr, "%s\n",
						ui.FormatWarning(fmt.Sprintf(
							"Session was created with provider '%s' but current config uses '%s'. Continuing with current provider.",
							sess.Provider, provider.Name())))
				}
				if sess.Mode != cfg.Mode {
					fmt.Fprintf(os.Stderr, "%s\n",
						ui.FormatWarning(fmt.Sprintf(
							"Session was created with mode '%s' but current config uses '%s'. Continuing with current mode.",
							sess.Mode, cfg.Mode)))
				}

				// Show resume banner.
				renderResumeBanner(sess, os.Stderr)
			} else {
				// Create new session.
				sess = &session.Session{
					ID:        preSessionID,
					Name:      sessionName,
					CreatedAt: time.Now(),
					UpdatedAt: time.Now(),
					Mode:      cfg.Mode,
					TargetDir: cfg.TargetDir,
					Provider:  provider.Name(),
					Model:     provider.Model(),
					Metadata:  make(map[string]string),
				}
				if cfg.Mode == "docker" {
					sess.Metadata["docker_image"] = cfg.Docker.Image
					sess.Metadata["docker_mount"] = cfg.Docker.MountPath
					sess.Metadata["docker_network"] = cfg.Docker.NetworkMode
				}
				// Initial save to create the file.
				go session.Save(sess) //nolint:errcheck

				// Welcome banner.
				fmt.Fprintln(os.Stderr, ui.RenderWelcomeBanner(
					provider.Name(), provider.Model(),
					cfg.TargetDir, cfg.Mode,
				))

				// Display project instructions status.
				if projInfo != nil && projInfo.Content != "" {
					msg := fmt.Sprintf("Instructions: %s (%d lines)", projInfo.Source, projInfo.Lines)
					if projInfo.Truncated {
						msg += " [truncated]"
					}
					fmt.Fprintln(os.Stderr, ui.FormatHint(msg))
				} else {
					fmt.Fprintln(os.Stderr, ui.FormatHint("No OPENMARMUT.md found"))
				}

				// Display MCP server status.
				if mcpMgr != nil {
					clients := mcpMgr.Clients()
					if len(clients) > 0 {
						totalTools := 0
						for _, c := range clients {
							totalTools += len(c.Tools())
						}
						fmt.Fprintln(os.Stderr, ui.FormatHint(
							fmt.Sprintf("MCP: %d server(s), %d tool(s)", len(clients), totalTools)))
					}
				}

				// Display custom commands if any.
				if len(customCmds) > 0 {
					fmt.Fprintln(os.Stderr, ui.FormatHint(
						fmt.Sprintf("Custom commands: %d loaded (type /commands to list)", len(customCmds))))
				}

				// Warn about pre-existing uncommitted changes.
				if state.isGitRepo {
					warnDirtyState(cmd.Context(), rt, os.Stderr)
				}
			}

			state.sess = sess

			// Run pre_session hooks.
			if ag.HooksEnabled() && len(hooks) > 0 {
				payload := agent.HookPayload{Session: preSessionID}
				if hookErr := agent.RunHooks(cmd.Context(), hooks, "pre_session", payload, log); hookErr != nil {
					fmt.Fprintln(os.Stderr, ui.FormatWarning("pre_session hook: "+hookErr.Error()))
				}
			}

			// Display hooks status on chat start.
			if len(hooks) > 0 {
				fmt.Fprintln(os.Stderr, ui.FormatHint(
					fmt.Sprintf("Hooks: %d configured (type /hooks to list)", len(hooks))))
			}

			fmt.Fprintln(os.Stderr)

			for {
				if ui.ColorEnabled() {
					fmt.Fprint(os.Stderr, ui.UserPromptStyle.Render("you> "))
				} else {
					fmt.Fprint(os.Stderr, "you> ")
				}
				if !state.scanner.Scan() {
					break
				}

				line := strings.TrimSpace(state.scanner.Text())
				if line == "" {
					continue
				}

				// Handle slash commands locally — no LLM call.
				action := handleSlashCommand(line, state)
				switch action {
				case slashExit:
					// Stop background loops.
					if state.loopMgr != nil {
						state.loopMgr.StopAll()
					}
					// Run post_session hooks.
					runPostSessionHooks(cmd.Context(), state, log)
					// Extract memories and final save.
					extractMemoriesOnExit(cmd.Context(), state)
					saveSession(state)
					return nil
				case slashHandled:
					// Check if a custom command set content to send.
					if state.customCmdContent != "" {
						line = state.customCmdContent
						state.customCmdContent = ""
						// Fall through to send to agent.
					} else {
						continue
					}
				}

				// Resolve @file references before sending to agent.
				line, fileWarnings := resolveFileRefs(cmd.Context(), line, rt)
				for _, w := range fileWarnings {
					fmt.Fprintln(os.Stderr, ui.FormatWarning(w))
				}

				// Prepend pending skill content if a skill was just invoked.
				if state.pendingSkill != "" {
					line = state.pendingSkill + "\n\n" + line
					state.pendingSkill = ""
				}

				// If plan mode is toggled on, route through plan flow.
				if state.planMode {
					executePlanFlow(line, state)
					fmt.Fprintln(os.Stderr)
					continue
				}

				// Show spinner while waiting for first token.
				state.spinner = ui.NewSpinner(os.Stderr, "Thinking...")
				state.spinner.Start()
				firstToken := true

				// Stream tokens as they arrive, rendering markdown at the end.
				var responseBuf strings.Builder
				streamCB := func(text string) error {
					if firstToken {
						if state.spinner != nil {
							state.spinner.Stop()
							state.spinner = nil
						}
						firstToken = false
					}
					responseBuf.WriteString(text)
					_, writeErr := fmt.Fprint(os.Stdout, text)
					return writeErr
				}

				result, err := ag.Run(cmd.Context(), line, streamCB)
				// Ensure spinner is stopped on error or if no tokens were streamed.
				if state.spinner != nil {
					state.spinner.Stop()
					state.spinner = nil
				}
				if err != nil {
					fmt.Fprintf(os.Stderr, "\n%s\n", ui.FormatError(err.Error()))
					continue
				}

				fmt.Fprintln(os.Stdout)

				// Accumulate session usage.
				state.sessionUsage.PromptTokens += result.Usage.PromptTokens
				state.sessionUsage.CompletionTokens += result.Usage.CompletionTokens
				state.sessionUsage.TotalTokens += result.Usage.TotalTokens

				// Update session after each turn.
				if state.sess != nil {
					state.sess.Messages = ag.History()
					state.sess.ToolCalls += len(result.Steps)
					state.sess.TotalTokens = state.sessionUsage.TotalTokens
					if cost, ok := llm.EstimateCost(state.sessionUsage, state.model); ok {
						state.sess.TotalCost = cost
					}
					go session.Save(state.sess) //nolint:errcheck
				}

				// Summary line with context usage.
				ctxUsage := ag.ContextUsage()
				costStr := llm.FormatCost(result.Usage, provider.Model())
				summary := ui.FormatSummary(
					len(result.Steps),
					result.Usage.PromptTokens,
					result.Usage.CompletionTokens,
					costStr,
					result.Duration,
					ctxUsage.Percent,
				)
				fmt.Fprintln(os.Stderr, summary)

				// Truncation notification.
				if result.Truncated {
					fmt.Fprintln(os.Stderr, ui.FormatWarning(
						fmt.Sprintf("Context at %d%% — older messages summarized to free space", ctxUsage.Percent)))
				}

				// Proactive 60% warning (one-time).
				if ctxUsage.Percent >= 60 && ctxUsage.Percent < 80 && !state.warned60 {
					state.warned60 = true
					fmt.Fprintln(os.Stderr, ui.FormatHint(
						fmt.Sprintf("Context %d%% full — consider /clear if switching topics", ctxUsage.Percent)))
				}

				// Auto-commit suggestion if files were modified.
				if state.isGitRepo && hasFileChanges(result) {
					fmt.Fprintln(os.Stderr, ui.FormatHint(
						"Tip: Run /commit to save these changes, or /rewind to undo"))
				}

				fmt.Fprintln(os.Stderr)
			}

			// Stop background loops.
			if state.loopMgr != nil {
				state.loopMgr.StopAll()
			}
			// Run post_session hooks.
			runPostSessionHooks(cmd.Context(), state, log)
			// Extract memories and final save on EOF.
			extractMemoriesOnExit(cmd.Context(), state)
			saveSession(state)

			if err := state.scanner.Err(); err != nil {
				return fmt.Errorf("chat: %w", err)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&continueFlag, "continue", false, "resume most recent session for current target directory")
	cmd.Flags().StringVar(&resumeFlag, "resume", "", "resume a session by ID (empty = interactive picker)")
	cmd.Flags().StringVar(&sessionName, "name", "", "name for the new session")

	return cmd
}

// resolveResumeID determines which session to resume based on flags.
// Returns empty string if a new session should be created.
func resolveResumeID(continueFlag bool, resumeFlag string, targetDir string, scanner *bufio.Scanner, w io.Writer) string {
	if resumeFlag != "" {
		return resumeFlag
	}

	if continueFlag {
		// Find most recent session for this target dir.
		sessions, err := session.FindByTarget(targetDir)
		if err != nil || len(sessions) == 0 {
			fmt.Fprintln(w, ui.FormatHint("No previous sessions found for this directory. Starting new session."))
			return ""
		}
		return sessions[0].ID
	}

	// Check if --resume was specified without a value (flag present but empty).
	// This case is handled by cobra setting resumeFlag to "" when --resume is used without value.
	// We detect this via the flag being changed.
	// Actually, cobra will set the flag to its default value. For interactive picker,
	// we need a different approach. Let's use a sentinel.
	// For now, interactive picker is triggered by `openmarmut chat --resume ""` which won't
	// happen naturally. The sessions command + --resume <id> is the main flow.

	return ""
}

// saveSession persists the current session state to disk.
// runPostSessionHooks fires post_session hooks if enabled.
func runPostSessionHooks(ctx context.Context, state *chatState, log *slog.Logger) {
	if state.ag.HooksEnabled() && len(state.hooks) > 0 {
		payload := agent.HookPayload{Session: state.ag.SessionID()}
		if err := agent.RunHooks(ctx, state.hooks, "post_session", payload, log); err != nil {
			fmt.Fprintln(state.out, ui.FormatWarning("post_session hook: "+err.Error()))
		}
	}
}

func saveSession(state *chatState) {
	if state.sess == nil {
		return
	}
	state.sess.Messages = state.ag.History()
	state.sess.TotalTokens = state.sessionUsage.TotalTokens
	if cost, ok := llm.EstimateCost(state.sessionUsage, state.model); ok {
		state.sess.TotalCost = cost
	}
	session.Save(state.sess) //nolint:errcheck
}

// extractMemoriesOnExit runs LLM-based memory extraction if auto_memory is enabled.
func extractMemoriesOnExit(ctx context.Context, state *chatState) {
	if !state.autoMemory || state.memoryDisabled {
		return
	}

	mem := state.ag.Memory()
	if mem == nil {
		mem = agent.NewMemoryStore()
		if mem == nil {
			return
		}
	}

	// Need at least a few turns to extract anything useful.
	history := state.ag.History()
	turnCount := 0
	for _, m := range history {
		if m.Role == llm.RoleUser {
			turnCount++
		}
	}
	if turnCount < 2 {
		return
	}

	// Read existing memory content for deduplication.
	existingContent := ""
	if data, err := os.ReadFile(mem.Path()); err == nil {
		existingContent = string(data)
	}

	memories, err := agent.ExtractMemories(ctx, state.provider, history, state.targetDir, existingContent)
	if err != nil {
		slog.Debug("auto-memory extraction failed", "error", err)
		return
	}

	for _, m := range memories {
		// Detect if this is a preference (global) or project-specific.
		category := "learning"
		project := state.targetDir
		lower := strings.ToLower(m)
		if strings.Contains(lower, "prefer") || strings.Contains(lower, "style") || strings.Contains(lower, "always") {
			category = "preference"
			project = "" // Preferences are global.
		}
		if err := mem.SaveWithProject(project, category, m); err != nil {
			slog.Debug("auto-memory save failed", "error", err)
		}
	}

	if len(memories) > 0 {
		fmt.Fprintf(state.out, "%s\n", ui.FormatHint(fmt.Sprintf("Saved %d new memories.", len(memories))))
	}
}

// renderResumeBanner displays a styled box with session resume information.
func renderResumeBanner(sess *session.Session, w io.Writer) {
	content := fmt.Sprintf(
		"Session:   %s (%s)\n"+
			"Started:   %s\n"+
			"Provider:  %s (%s)\n"+
			"Target:    %s\n"+
			"Mode:      %s\n"+
			"Messages:  %d (%d turns)\n"+
			"Cost:      ~$%.4f",
		sess.DisplayName(), sess.ID,
		sess.CreatedAt.Format("2006-01-02 15:04"),
		sess.Provider, sess.Model,
		sess.TargetDir,
		sess.Mode,
		len(sess.Messages), sess.UserTurns(),
		sess.TotalCost,
	)
	fmt.Fprintln(w, ui.RenderBox("Resuming Session", content))
}

// showSessionPicker displays an interactive list of recent sessions.
// Returns the selected session ID or empty string if cancelled.
func showSessionPicker(scanner *bufio.Scanner, w io.Writer) string {
	summaries, err := session.FindRecent(10)
	if err != nil || len(summaries) == 0 {
		fmt.Fprintln(w, ui.FormatHint("No sessions found."))
		return ""
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, ui.HeaderStyle.Render("Recent Sessions:"))

	headers := []string{"#", "NAME", "AGE", "PROVIDER", "TARGET", "TURNS"}
	var rows [][]string
	for i, s := range summaries {
		rows = append(rows, []string{
			fmt.Sprintf("%d", i+1),
			displayName(s.Name),
			humanizeAge(s.UpdatedAt),
			s.Provider,
			truncatePath(s.TargetDir, 35),
			fmt.Sprintf("%d", s.Messages),
		})
	}
	fmt.Fprintln(w, ui.RenderTable(headers, rows, -1))
	fmt.Fprint(w, "Enter number or 'q' to cancel: ")

	if !scanner.Scan() {
		return ""
	}
	input := strings.TrimSpace(scanner.Text())
	if input == "q" || input == "" {
		return ""
	}

	n, err := strconv.Atoi(input)
	if err != nil || n < 1 || n > len(summaries) {
		fmt.Fprintln(w, ui.FormatWarning("Invalid selection."))
		return ""
	}

	return summaries[n-1].ID
}

// handleRewind processes /rewind commands.
func handleRewind(line string, state *chatState) {
	cs := state.ag.Checkpoints()
	if cs == nil {
		fmt.Fprintln(state.out, ui.FormatWarning("Checkpointing is not enabled."))
		return
	}

	arg := strings.TrimSpace(strings.TrimPrefix(line, "/rewind"))

	if arg == "--list" {
		cps := cs.Checkpoints()
		if len(cps) == 0 {
			fmt.Fprintln(state.out, ui.FormatHint("No checkpoints recorded."))
			return
		}
		headers := []string{"#", "AGE", "FILES"}
		var rows [][]string
		for _, cp := range cps {
			var files []string
			for path := range cp.Files {
				files = append(files, path)
			}
			fileStr := strings.Join(files, ", ")
			if len(fileStr) > 60 {
				fileStr = fileStr[:60] + "..."
			}
			rows = append(rows, []string{
				fmt.Sprintf("%d", cp.ID),
				humanizeAge(cp.Timestamp),
				fileStr,
			})
		}
		fmt.Fprintln(state.out, ui.RenderTable(headers, rows, -1))
		return
	}

	n := 1
	if arg != "" {
		parsed, err := strconv.Atoi(arg)
		if err != nil || parsed < 1 {
			fmt.Fprintln(state.out, ui.FormatWarning("Usage: /rewind [n] or /rewind --list"))
			return
		}
		n = parsed
	}

	if cs.Len() == 0 {
		fmt.Fprintln(state.out, ui.FormatHint("No checkpoints to rewind."))
		return
	}

	ctx := context.Background()
	actions, err := cs.Rewind(ctx, state.rt, n)
	if err != nil {
		fmt.Fprintln(state.out, ui.FormatError("Rewind failed: "+err.Error()))
		return
	}

	if len(actions) == 0 {
		fmt.Fprintln(state.out, ui.FormatHint("No file changes to rewind."))
		return
	}

	fmt.Fprintln(state.out, ui.FormatSuccess(
		fmt.Sprintf("Reverted %d file(s) from %d checkpoint(s)", len(actions), n)))
	for _, a := range actions {
		if a.Action == "deleted" {
			fmt.Fprintf(state.out, "  %s %s (deleted — was newly created)\n",
				ui.FormatError("✗"), a.Path)
		} else {
			fmt.Fprintf(state.out, "  %s %s (restored)\n",
				ui.FormatSuccess("↺"), a.Path)
		}
		if a.Error != "" {
			fmt.Fprintf(state.out, "    %s\n", ui.FormatError(a.Error))
		}
	}
}

// handleDiff processes /diff commands.
func handleDiff(line string, state *chatState) {
	if !state.isGitRepo {
		fmt.Fprintln(state.out, ui.FormatWarning("Not a git repository."))
		return
	}

	arg := strings.TrimSpace(strings.TrimPrefix(line, "/diff"))

	cmd := "git diff"
	if arg != "" {
		cmd += " -- " + shellQuoteCLI(arg)
	}

	ctx := context.Background()
	result, err := state.rt.Exec(ctx, cmd, runtime.ExecOpts{})
	if err != nil {
		fmt.Fprintln(state.out, ui.FormatError("git diff failed: "+err.Error()))
		return
	}
	if result.ExitCode != 0 {
		fmt.Fprintln(state.out, ui.FormatError("git diff: "+result.Stderr))
		return
	}

	output := strings.TrimRight(result.Stdout, "\n")
	if output == "" {
		fmt.Fprintln(state.out, ui.FormatHint("No unstaged changes."))
		return
	}
	fmt.Fprintln(state.out, output)
}

// handleCommit processes /commit commands.
func handleCommit(line string, state *chatState) {
	if !state.isGitRepo {
		fmt.Fprintln(state.out, ui.FormatWarning("Not a git repository."))
		return
	}

	ctx := context.Background()

	// Check for staged/unstaged changes.
	statusResult, err := state.rt.Exec(ctx, "git status --short", runtime.ExecOpts{})
	if err != nil {
		fmt.Fprintln(state.out, ui.FormatError("git status failed: "+err.Error()))
		return
	}
	if strings.TrimSpace(statusResult.Stdout) == "" {
		fmt.Fprintln(state.out, ui.FormatHint("No changes to commit."))
		return
	}

	// Show status.
	fmt.Fprintln(state.out, ui.DimStyle.Render("Changes to commit:"))
	fmt.Fprintln(state.out, statusResult.Stdout)

	// Get or generate commit message.
	msg := strings.TrimSpace(strings.TrimPrefix(line, "/commit"))
	if msg == "" {
		// Generate a message from git diff --stat.
		statResult, _ := state.rt.Exec(ctx, "git diff --stat HEAD 2>/dev/null || git diff --stat --cached 2>/dev/null", runtime.ExecOpts{})
		if statResult != nil && statResult.Stdout != "" {
			msg = "Update files\n\n" + strings.TrimRight(statResult.Stdout, "\n")
		} else {
			msg = "Update files"
		}
		fmt.Fprintf(state.out, "%s %s\n", ui.DimStyle.Render("Commit message:"), msg)
	}

	// Confirm before committing.
	fmt.Fprint(state.out, "Commit? [y/n]: ")
	if !state.scanner.Scan() {
		return
	}
	answer := strings.TrimSpace(strings.ToLower(state.scanner.Text()))
	if answer != "y" && answer != "yes" {
		fmt.Fprintln(state.out, ui.FormatHint("Commit cancelled."))
		return
	}

	// Stage all and commit.
	addResult, err := state.rt.Exec(ctx, "git add -A", runtime.ExecOpts{})
	if err != nil || addResult.ExitCode != 0 {
		errMsg := "git add failed"
		if addResult != nil {
			errMsg += ": " + addResult.Stderr
		}
		fmt.Fprintln(state.out, ui.FormatError(errMsg))
		return
	}

	commitCmd := fmt.Sprintf("git commit -m %s", shellQuoteCLI(msg))
	commitResult, err := state.rt.Exec(ctx, commitCmd, runtime.ExecOpts{})
	if err != nil {
		fmt.Fprintln(state.out, ui.FormatError("git commit failed: "+err.Error()))
		return
	}
	if commitResult.ExitCode != 0 {
		fmt.Fprintln(state.out, ui.FormatError("git commit: "+commitResult.Stderr))
		return
	}
	fmt.Fprintln(state.out, ui.FormatSuccess("Committed: "+strings.TrimRight(commitResult.Stdout, "\n")))
}

// isGitRepo checks if the runtime target directory is a git repository.
func isGitRepo(ctx context.Context, rt runtime.Runtime) bool {
	result, err := rt.Exec(ctx, "git rev-parse --is-inside-work-tree 2>/dev/null", runtime.ExecOpts{})
	if err != nil {
		return false
	}
	return result.ExitCode == 0 && strings.TrimSpace(result.Stdout) == "true"
}

// warnDirtyState warns if there are uncommitted changes when starting a new session.
func warnDirtyState(ctx context.Context, rt runtime.Runtime, w io.Writer) {
	result, err := rt.Exec(ctx, "git status --short", runtime.ExecOpts{})
	if err != nil || result.ExitCode != 0 {
		return
	}
	output := strings.TrimSpace(result.Stdout)
	if output == "" {
		return
	}
	lines := strings.Split(output, "\n")
	fmt.Fprintf(w, "%s\n",
		ui.FormatWarning(fmt.Sprintf("Working directory has uncommitted changes (%d files modified)", len(lines))))
}

// hasFileChanges checks if the agent result includes any file-modifying tool calls.
func hasFileChanges(result *agent.Result) bool {
	for _, step := range result.Steps {
		switch step.ToolCall.Name {
		case "write_file", "patch_file", "delete_file":
			if step.Error == "" {
				return true
			}
		}
	}
	return false
}

// shellQuoteCLI wraps a string in single quotes for safe shell interpolation (CLI-side).
func shellQuoteCLI(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// handlePlan processes /plan commands.
// /plan on — toggle plan mode on.
// /plan off — toggle plan mode off.
// /plan — toggle plan mode.
// /plan <message> — one-shot plan: analyze, display, approve, execute.
func handlePlan(line string, state *chatState) slashAction {
	arg := strings.TrimSpace(strings.TrimPrefix(line, "/plan"))

	switch arg {
	case "":
		// Toggle.
		state.planMode = !state.planMode
		if state.planMode {
			fmt.Fprintln(state.out, ui.FormatSuccess("Plan mode ON — next messages will produce a plan before executing"))
		} else {
			fmt.Fprintln(state.out, ui.FormatSuccess("Plan mode OFF — back to normal execution"))
		}
		return slashHandled
	case "on":
		state.planMode = true
		fmt.Fprintln(state.out, ui.FormatSuccess("Plan mode ON"))
		return slashHandled
	case "off":
		state.planMode = false
		fmt.Fprintln(state.out, ui.FormatSuccess("Plan mode OFF"))
		return slashHandled
	}

	// One-shot plan mode: /plan <message>.
	executePlanFlow(arg, state)
	return slashHandled
}

// executePlanFlow runs the plan→approve→execute flow for a given message.
func executePlanFlow(userMessage string, state *chatState) {
	// Phase 1: Analysis — run with read-only tools.
	state.spinner = ui.NewSpinner(state.out, "Planning...")
	state.spinner.Start()
	firstToken := true

	var planBuf strings.Builder
	streamCB := func(text string) error {
		if firstToken {
			if state.spinner != nil {
				state.spinner.Stop()
				state.spinner = nil
			}
			firstToken = false
		}
		planBuf.WriteString(text)
		return nil
	}

	result, err := state.ag.RunPlan(context.Background(), userMessage, streamCB)
	if state.spinner != nil {
		state.spinner.Stop()
		state.spinner = nil
	}
	if err != nil {
		fmt.Fprintf(state.out, "\n%s\n", ui.FormatError("Plan failed: "+err.Error()))
		return
	}

	plan := result.Response
	if plan == "" {
		plan = planBuf.String()
	}

	// Phase 2: Display the plan in a styled box.
	fmt.Fprintln(state.out, ui.RenderPlanBox(plan))

	// Show plan cost.
	costStr := llm.FormatCost(result.Usage, state.model)
	summary := ui.FormatSummary(
		len(result.Steps),
		result.Usage.PromptTokens,
		result.Usage.CompletionTokens,
		costStr,
		result.Duration,
	)
	fmt.Fprintln(state.out, summary)

	// Phase 3: Approval.
	fmt.Fprintln(state.out, ui.RenderPlanApproval())
	fmt.Fprint(state.out, "> ")
	if !state.scanner.Scan() {
		return
	}
	answer := strings.TrimSpace(strings.ToLower(state.scanner.Text()))

	switch answer {
	case "y", "yes":
		// Phase 4: Execute — send plan + original request through the normal agent loop.
		executeMsg := fmt.Sprintf(
			"Execute the following plan. The original request was: %s\n\n---\n\n%s",
			userMessage, plan,
		)

		state.spinner = ui.NewSpinner(state.out, "Executing plan...")
		state.spinner.Start()
		firstToken = true

		var responseBuf strings.Builder
		execStreamCB := func(text string) error {
			if firstToken {
				if state.spinner != nil {
					state.spinner.Stop()
					state.spinner = nil
				}
				firstToken = false
			}
			responseBuf.WriteString(text)
			_, writeErr := fmt.Fprint(state.out, text)
			return writeErr
		}

		execResult, execErr := state.ag.Run(context.Background(), executeMsg, execStreamCB)
		if state.spinner != nil {
			state.spinner.Stop()
			state.spinner = nil
		}
		if execErr != nil {
			fmt.Fprintf(state.out, "\n%s\n", ui.FormatError("Execution failed: "+execErr.Error()))
			return
		}

		fmt.Fprintln(state.out)

		// Accumulate usage.
		state.sessionUsage.PromptTokens += result.Usage.PromptTokens + execResult.Usage.PromptTokens
		state.sessionUsage.CompletionTokens += result.Usage.CompletionTokens + execResult.Usage.CompletionTokens
		state.sessionUsage.TotalTokens += result.Usage.TotalTokens + execResult.Usage.TotalTokens

		// Show execution summary.
		execCostStr := llm.FormatCost(execResult.Usage, state.model)
		execSummary := ui.FormatSummary(
			len(execResult.Steps),
			execResult.Usage.PromptTokens,
			execResult.Usage.CompletionTokens,
			execCostStr,
			execResult.Duration,
		)
		fmt.Fprintln(state.out, execSummary)

		// Auto-commit suggestion.
		if state.isGitRepo && hasFileChanges(execResult) {
			fmt.Fprintln(state.out, ui.FormatHint(
				"Tip: Run /commit to save these changes, or /rewind to undo"))
		}

		// Update session.
		if state.sess != nil {
			state.sess.Messages = state.ag.History()
			state.sess.ToolCalls += len(result.Steps) + len(execResult.Steps)
			state.sess.TotalTokens = state.sessionUsage.TotalTokens
			if cost, ok := llm.EstimateCost(state.sessionUsage, state.model); ok {
				state.sess.TotalCost = cost
			}
			go session.Save(state.sess) //nolint:errcheck
		}

	case "e", "edit":
		fmt.Fprintln(state.out, ui.FormatHint("Refine your request and run /plan again."))

	default:
		fmt.Fprintln(state.out, ui.FormatHint("Plan discarded."))
	}
}

// handleCompact processes /compact commands.
// /compact — auto-summarize conversation history.
// /compact "instruction" — summarize with custom instruction for what to preserve.
func handleCompact(line string, state *chatState) {
	arg := strings.TrimSpace(strings.TrimPrefix(line, "/compact"))
	// Strip surrounding quotes from custom instruction.
	arg = strings.Trim(arg, "\"'")

	state.spinner = ui.NewSpinner(state.out, "Compacting...")
	state.spinner.Start()

	before, after, err := state.ag.CompactHistory(context.Background(), arg)
	state.spinner.Stop()
	state.spinner = nil

	if err != nil {
		fmt.Fprintln(state.out, ui.FormatError("Compact failed: "+err.Error()))
		return
	}

	if before == after {
		fmt.Fprintln(state.out, ui.FormatHint("Nothing to compact (too few messages)."))
		return
	}

	pct := 0
	if before > 0 {
		pct = 100 - (after*100)/before
	}
	fmt.Fprintln(state.out, ui.FormatSuccess(
		fmt.Sprintf("Compacted: %s → %s tokens (%d%% reduction)",
			humanizeInt(before), humanizeInt(after), pct)))

	// Update session with compacted history.
	if state.sess != nil {
		state.sess.Messages = state.ag.History()
		go session.Save(state.sess) //nolint:errcheck
	}
}

// handleThinking toggles extended thinking mode on/off.
func handleThinking(state *chatState) {
	enabled := !state.ag.ExtendedThinking()
	state.ag.SetExtendedThinking(enabled)
	if enabled {
		fmt.Fprintln(state.out, ui.FormatSuccess("Extended thinking ON"))
	} else {
		fmt.Fprintln(state.out, ui.FormatSuccess("Extended thinking OFF"))
	}
}

// handleEffort sets the thinking effort level.
func handleEffort(line string, state *chatState) {
	arg := strings.TrimSpace(strings.TrimPrefix(line, "/effort"))
	var budget int
	switch strings.ToLower(arg) {
	case "low":
		budget = 5000
	case "medium":
		budget = 10000
	case "high":
		budget = 50000
	default:
		fmt.Fprintln(state.out, ui.FormatWarning("Usage: /effort low|medium|high"))
		return
	}
	state.ag.SetThinkingBudget(budget)
	if !state.ag.ExtendedThinking() {
		state.ag.SetExtendedThinking(true)
		fmt.Fprintln(state.out, ui.FormatSuccess(
			fmt.Sprintf("Effort set to %s (extended thinking enabled)", arg)))
	} else {
		fmt.Fprintln(state.out, ui.FormatSuccess("Effort set to "+arg))
	}
}

// formatToolArgs extracts a short summary of tool call arguments for display.
func formatToolArgs(tc llm.ToolCall) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
		return ""
	}

	// Show the most relevant argument for common tools.
	switch tc.Name {
	case "read_file", "delete_file", "read_file_lines":
		if p, ok := args["path"]; ok {
			return fmt.Sprintf("%v", p)
		}
	case "write_file", "patch_file":
		if p, ok := args["path"]; ok {
			return fmt.Sprintf("%v", p)
		}
	case "list_dir", "mkdir":
		if p, ok := args["path"]; ok {
			return fmt.Sprintf("%v", p)
		}
	case "execute_command":
		if c, ok := args["command"]; ok {
			s := fmt.Sprintf("%v", c)
			if len(s) > 60 {
				s = s[:60] + "..."
			}
			return s
		}
	case "grep_files":
		if p, ok := args["pattern"]; ok {
			return fmt.Sprintf("%v", p)
		}
	case "find_files":
		if p, ok := args["pattern"]; ok {
			return fmt.Sprintf("%v", p)
		}
	case "git_diff":
		if p, ok := args["path"]; ok {
			return fmt.Sprintf("%v", p)
		}
	case "git_log":
		if n, ok := args["n"]; ok {
			return fmt.Sprintf("n=%v", n)
		}
	case "git_commit":
		if m, ok := args["message"]; ok {
			s := fmt.Sprintf("%v", m)
			if len(s) > 60 {
				s = s[:60] + "..."
			}
			return s
		}
	case "git_branch":
		if n, ok := args["name"]; ok && n != "" {
			return fmt.Sprintf("create: %v", n)
		}
	case "git_checkout":
		if b, ok := args["branch"]; ok {
			return fmt.Sprintf("%v", b)
		}
	}

	// Fallback: show first string value.
	for _, v := range args {
		if s, ok := v.(string); ok {
			if len(s) > 60 {
				s = s[:60] + "..."
			}
			return s
		}
	}
	return ""
}
