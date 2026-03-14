package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gajaai/opencode-go/internal/llm"
)

// PermissionLevel defines what happens when a tool is called.
type PermissionLevel int

const (
	// PermAuto executes without asking (read-only operations).
	PermAuto PermissionLevel = iota
	// PermConfirm shows the tool call and asks the user before executing.
	PermConfirm
	// PermDeny always blocks the tool call.
	PermDeny
)

func (p PermissionLevel) String() string {
	switch p {
	case PermAuto:
		return "auto"
	case PermConfirm:
		return "confirm"
	case PermDeny:
		return "deny"
	default:
		return "unknown"
	}
}

// ParsePermissionLevel converts a string to a PermissionLevel.
func ParsePermissionLevel(s string) (PermissionLevel, bool) {
	switch strings.ToLower(s) {
	case "auto":
		return PermAuto, true
	case "confirm":
		return PermConfirm, true
	case "deny":
		return PermDeny, true
	default:
		return 0, false
	}
}

// DefaultPermissions returns the default permission map:
// read-only tools are auto, write/execute tools require confirmation.
func DefaultPermissions() map[string]PermissionLevel {
	return map[string]PermissionLevel{
		"read_file":       PermAuto,
		"read_file_lines": PermAuto,
		"list_dir":        PermAuto,
		"grep_files":      PermAuto,
		"find_files":      PermAuto,
		"write_file":      PermConfirm,
		"patch_file":      PermConfirm,
		"delete_file":     PermConfirm,
		"mkdir":           PermConfirm,
		"execute_command": PermConfirm,
	}
}

// ConfirmResult is the user's response to a confirmation prompt.
type ConfirmResult int

const (
	ConfirmYes    ConfirmResult = iota // Allow this one call.
	ConfirmNo                          // Deny this call.
	ConfirmAlways                      // Auto-allow this tool for the rest of the session.
)

// ConfirmFunc is called when a tool call needs user confirmation.
// It receives the tool call and a formatted preview string.
// Returns the user's decision.
type ConfirmFunc func(tc llm.ToolCall, preview string) ConfirmResult

// PermissionChecker manages tool permission levels and session overrides.
type PermissionChecker struct {
	permissions map[string]PermissionLevel
	confirmFn   ConfirmFunc
}

// NewPermissionChecker creates a checker with the given permission map.
// If perms is nil, DefaultPermissions() is used.
// If confirmFn is nil, all confirmations are auto-approved.
func NewPermissionChecker(perms map[string]PermissionLevel, confirmFn ConfirmFunc) *PermissionChecker {
	if perms == nil {
		perms = DefaultPermissions()
	}
	return &PermissionChecker{
		permissions: perms,
		confirmFn:   confirmFn,
	}
}

// Check returns whether a tool call should proceed.
// Returns true to allow, false to deny. If denied, returns an error message
// to send back to the LLM.
func (pc *PermissionChecker) Check(tc llm.ToolCall) (bool, string) {
	level, ok := pc.permissions[tc.Name]
	if !ok {
		// Unknown tools default to confirm.
		level = PermConfirm
	}

	switch level {
	case PermAuto:
		return true, ""
	case PermDeny:
		return false, fmt.Sprintf("error: tool %q is denied by permission policy", tc.Name)
	case PermConfirm:
		if pc.confirmFn == nil {
			// No confirm function = auto-approve.
			return true, ""
		}
		preview := FormatToolPreview(tc)
		result := pc.confirmFn(tc, preview)
		switch result {
		case ConfirmYes:
			return true, ""
		case ConfirmAlways:
			// Upgrade to auto for the rest of the session.
			pc.permissions[tc.Name] = PermAuto
			return true, ""
		case ConfirmNo:
			return false, "error: user denied this operation"
		}
	}

	return true, ""
}

// SetPermission sets or overrides the permission for a tool.
func (pc *PermissionChecker) SetPermission(toolName string, level PermissionLevel) {
	pc.permissions[toolName] = level
}

// AutoApproveAll sets all tools to auto permission.
func (pc *PermissionChecker) AutoApproveAll() {
	for name := range pc.permissions {
		pc.permissions[name] = PermAuto
	}
}

// Permissions returns a copy of the current permission map.
func (pc *PermissionChecker) Permissions() map[string]PermissionLevel {
	cp := make(map[string]PermissionLevel, len(pc.permissions))
	for k, v := range pc.permissions {
		cp[k] = v
	}
	return cp
}

// FormatToolPreview creates a human-readable preview of a tool call
// for the confirmation prompt. Long content is truncated to 3 lines.
func FormatToolPreview(tc llm.ToolCall) string {
	var b strings.Builder
	fmt.Fprintf(&b, "→ %s", tc.Name)

	var args map[string]any
	if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
		fmt.Fprintf(&b, "(%s)", tc.Arguments)
		return b.String()
	}

	switch tc.Name {
	case "write_file":
		if p, ok := args["path"]; ok {
			fmt.Fprintf(&b, "(%v)", p)
		}
		if content, ok := args["content"].(string); ok {
			b.WriteString("\n  Content (first 3 lines):")
			lines := strings.SplitN(content, "\n", 4)
			limit := 3
			if len(lines) < limit {
				limit = len(lines)
			}
			for _, line := range lines[:limit] {
				if len(line) > 120 {
					line = line[:120] + "..."
				}
				fmt.Fprintf(&b, "\n    %s", line)
			}
			if len(lines) > 3 {
				fmt.Fprintf(&b, "\n    ... (%d more lines)", strings.Count(content, "\n")-2)
			}
		}
	case "patch_file":
		if p, ok := args["path"]; ok {
			fmt.Fprintf(&b, "(%v)", p)
		}
		if edits, ok := args["edits"].([]any); ok {
			fmt.Fprintf(&b, "\n  %d edit(s)", len(edits))
			for i, e := range edits {
				if i >= 2 {
					fmt.Fprintf(&b, "\n  ... and %d more", len(edits)-2)
					break
				}
				if edit, ok := e.(map[string]any); ok {
					old := truncatePreview(fmt.Sprintf("%v", edit["old_text"]), 80)
					new := truncatePreview(fmt.Sprintf("%v", edit["new_text"]), 80)
					fmt.Fprintf(&b, "\n  [%d] %q → %q", i+1, old, new)
				}
			}
		}
	case "delete_file":
		if p, ok := args["path"]; ok {
			fmt.Fprintf(&b, "(%v)", p)
		}
	case "mkdir":
		if p, ok := args["path"]; ok {
			fmt.Fprintf(&b, "(%v)", p)
		}
	case "execute_command":
		if c, ok := args["command"]; ok {
			cmd := fmt.Sprintf("%v", c)
			if len(cmd) > 120 {
				cmd = cmd[:120] + "..."
			}
			fmt.Fprintf(&b, "\n  $ %s", cmd)
		}
		if w, ok := args["workdir"]; ok && w != "" {
			fmt.Fprintf(&b, "\n  workdir: %v", w)
		}
	default:
		// Generic: show all args.
		for k, v := range args {
			s := fmt.Sprintf("%v", v)
			s = truncatePreview(s, 80)
			fmt.Fprintf(&b, "\n  %s: %s", k, s)
		}
	}

	return b.String()
}

// truncatePreview truncates a string to maxLen characters.
func truncatePreview(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// BuildPermissions creates a permission map from config lists.
// Tools in autoAllow get PermAuto, tools in confirm get PermConfirm.
// Unmentioned tools keep their defaults.
func BuildPermissions(autoAllow, confirm []string) map[string]PermissionLevel {
	perms := DefaultPermissions()
	for _, name := range autoAllow {
		perms[name] = PermAuto
	}
	for _, name := range confirm {
		perms[name] = PermConfirm
	}
	return perms
}
