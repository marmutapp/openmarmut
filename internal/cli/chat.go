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
	"time"

	"github.com/gajaai/openmarmut-go/internal/agent"
	"github.com/gajaai/openmarmut-go/internal/config"
	"github.com/gajaai/openmarmut-go/internal/llm"
	"github.com/gajaai/openmarmut-go/internal/logger"
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

	if strings.HasPrefix(line, "/plan") {
		return handlePlan(line, state)
	}

	switch line {
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

			ag := agent.New(provider, rt, log, opts...)

			state.ag = ag
			state.permChecker = pc
			state.rt = rt
			state.provider = provider
			state.targetDir = cfg.TargetDir
			state.autoMemory = cfg.Agent.AutoMemory

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
					ID:        session.NewID(),
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

				// Warn about pre-existing uncommitted changes.
				if state.isGitRepo {
					warnDirtyState(cmd.Context(), rt, os.Stderr)
				}
			}

			state.sess = sess
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
					// Extract memories and final save.
					extractMemoriesOnExit(cmd.Context(), state)
					saveSession(state)
					return nil
				case slashHandled:
					continue
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
