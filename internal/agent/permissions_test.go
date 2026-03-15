package agent

import (
	"context"
	"testing"

	"github.com/marmutapp/openmarmut/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultPermissions(t *testing.T) {
	perms := DefaultPermissions()

	// Read-only tools should be auto.
	autoTools := []string{"read_file", "read_file_lines", "list_dir", "grep_files", "find_files"}
	for _, name := range autoTools {
		assert.Equal(t, PermAuto, perms[name], "expected %s to be auto", name)
	}

	// Write/execute tools should require confirmation.
	confirmTools := []string{"write_file", "patch_file", "delete_file", "mkdir", "execute_command"}
	for _, name := range confirmTools {
		assert.Equal(t, PermConfirm, perms[name], "expected %s to be confirm", name)
	}
}

func TestPermissionLevel_String(t *testing.T) {
	assert.Equal(t, "auto", PermAuto.String())
	assert.Equal(t, "confirm", PermConfirm.String())
	assert.Equal(t, "deny", PermDeny.String())
}

func TestParsePermissionLevel(t *testing.T) {
	tests := []struct {
		input string
		level PermissionLevel
		ok    bool
	}{
		{"auto", PermAuto, true},
		{"Auto", PermAuto, true},
		{"confirm", PermConfirm, true},
		{"CONFIRM", PermConfirm, true},
		{"deny", PermDeny, true},
		{"invalid", 0, false},
		{"", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			level, ok := ParsePermissionLevel(tt.input)
			assert.Equal(t, tt.ok, ok)
			if ok {
				assert.Equal(t, tt.level, level)
			}
		})
	}
}

func TestPermissionChecker_AutoAllowed(t *testing.T) {
	pc := NewPermissionChecker(nil, nil)

	allowed, msg := pc.Check(llm.ToolCall{Name: "read_file", Arguments: `{"path":"x.go"}`})
	assert.True(t, allowed)
	assert.Empty(t, msg)
}

func TestPermissionChecker_ConfirmWithNilFn_AutoApproves(t *testing.T) {
	pc := NewPermissionChecker(nil, nil) // nil confirmFn = auto-approve

	allowed, msg := pc.Check(llm.ToolCall{Name: "write_file", Arguments: `{"path":"x.go","content":"hello"}`})
	assert.True(t, allowed)
	assert.Empty(t, msg)
}

func TestPermissionChecker_ConfirmYes(t *testing.T) {
	confirmFn := func(tc llm.ToolCall, preview string) ConfirmResult {
		return ConfirmYes
	}
	pc := NewPermissionChecker(nil, confirmFn)

	allowed, msg := pc.Check(llm.ToolCall{Name: "write_file", Arguments: `{"path":"x.go","content":"hello"}`})
	assert.True(t, allowed)
	assert.Empty(t, msg)
}

func TestPermissionChecker_ConfirmNo(t *testing.T) {
	confirmFn := func(tc llm.ToolCall, preview string) ConfirmResult {
		return ConfirmNo
	}
	pc := NewPermissionChecker(nil, confirmFn)

	allowed, msg := pc.Check(llm.ToolCall{Name: "write_file", Arguments: `{"path":"x.go","content":"hello"}`})
	assert.False(t, allowed)
	assert.Contains(t, msg, "user denied")
}

func TestPermissionChecker_ConfirmAlways_UpgradesToAuto(t *testing.T) {
	callCount := 0
	confirmFn := func(tc llm.ToolCall, preview string) ConfirmResult {
		callCount++
		return ConfirmAlways
	}
	pc := NewPermissionChecker(nil, confirmFn)

	// First call: confirm is invoked, returns "always".
	allowed, _ := pc.Check(llm.ToolCall{Name: "write_file", Arguments: `{}`})
	assert.True(t, allowed)
	assert.Equal(t, 1, callCount)

	// Second call: should be auto now, confirm not called.
	allowed, _ = pc.Check(llm.ToolCall{Name: "write_file", Arguments: `{}`})
	assert.True(t, allowed)
	assert.Equal(t, 1, callCount, "confirmFn should not be called after 'always'")
}

func TestPermissionChecker_Deny(t *testing.T) {
	perms := DefaultPermissions()
	perms["delete_file"] = PermDeny
	pc := NewPermissionChecker(perms, nil)

	allowed, msg := pc.Check(llm.ToolCall{Name: "delete_file", Arguments: `{"path":"x.go"}`})
	assert.False(t, allowed)
	assert.Contains(t, msg, "denied by permission policy")
}

func TestPermissionChecker_UnknownToolDefaultsToConfirm(t *testing.T) {
	callCount := 0
	confirmFn := func(tc llm.ToolCall, preview string) ConfirmResult {
		callCount++
		return ConfirmYes
	}
	pc := NewPermissionChecker(nil, confirmFn)

	allowed, _ := pc.Check(llm.ToolCall{Name: "unknown_tool", Arguments: `{}`})
	assert.True(t, allowed)
	assert.Equal(t, 1, callCount, "unknown tool should trigger confirmation")
}

func TestPermissionChecker_SetPermission(t *testing.T) {
	pc := NewPermissionChecker(nil, nil)

	// read_file is auto by default.
	pc.SetPermission("read_file", PermDeny)
	allowed, _ := pc.Check(llm.ToolCall{Name: "read_file"})
	assert.False(t, allowed)
}

func TestPermissionChecker_AutoApproveAll(t *testing.T) {
	callCount := 0
	confirmFn := func(tc llm.ToolCall, preview string) ConfirmResult {
		callCount++
		return ConfirmNo
	}
	pc := NewPermissionChecker(nil, confirmFn)
	pc.AutoApproveAll()

	// All tools should now be auto — confirmFn never called.
	for _, name := range []string{"read_file", "write_file", "delete_file", "execute_command"} {
		allowed, _ := pc.Check(llm.ToolCall{Name: name})
		assert.True(t, allowed, "expected %s to be auto-approved", name)
	}
	assert.Equal(t, 0, callCount)
}

func TestPermissionChecker_Permissions_ReturnsCopy(t *testing.T) {
	pc := NewPermissionChecker(nil, nil)
	perms := pc.Permissions()

	// Mutating the copy should not affect the checker.
	perms["read_file"] = PermDeny
	allowed, _ := pc.Check(llm.ToolCall{Name: "read_file"})
	assert.True(t, allowed)
}

func TestBuildPermissions(t *testing.T) {
	perms := BuildPermissions(
		[]string{"write_file"}, // override write_file to auto
		[]string{"read_file"},  // override read_file to confirm
	)

	assert.Equal(t, PermAuto, perms["write_file"])
	assert.Equal(t, PermConfirm, perms["read_file"])
	// Others keep defaults.
	assert.Equal(t, PermAuto, perms["list_dir"])
	assert.Equal(t, PermConfirm, perms["execute_command"])
}

func TestFormatToolPreview_WriteFile(t *testing.T) {
	tc := llm.ToolCall{
		Name:      "write_file",
		Arguments: `{"path":"src/main.go","content":"package main\nimport \"fmt\"\nfunc main() {\n\tfmt.Println(\"hello\")\n}"}`,
	}
	preview := FormatToolPreview(tc)

	assert.Contains(t, preview, "write_file(src/main.go)")
	assert.Contains(t, preview, "Content (first 3 lines)")
	assert.Contains(t, preview, "package main")
	assert.Contains(t, preview, "import \"fmt\"")
	assert.Contains(t, preview, "func main() {")
}

func TestFormatToolPreview_ExecuteCommand(t *testing.T) {
	tc := llm.ToolCall{
		Name:      "execute_command",
		Arguments: `{"command":"go test ./...","workdir":"src"}`,
	}
	preview := FormatToolPreview(tc)

	assert.Contains(t, preview, "execute_command")
	assert.Contains(t, preview, "$ go test ./...")
	assert.Contains(t, preview, "workdir: src")
}

func TestFormatToolPreview_PatchFile(t *testing.T) {
	tc := llm.ToolCall{
		Name:      "patch_file",
		Arguments: `{"path":"main.go","edits":[{"old_text":"hello","new_text":"world"}]}`,
	}
	preview := FormatToolPreview(tc)

	assert.Contains(t, preview, "patch_file(main.go)")
	assert.Contains(t, preview, "1 edit(s)")
	assert.Contains(t, preview, "\"hello\"")
	assert.Contains(t, preview, "\"world\"")
}

func TestFormatToolPreview_DeleteFile(t *testing.T) {
	tc := llm.ToolCall{
		Name:      "delete_file",
		Arguments: `{"path":"temp.txt"}`,
	}
	preview := FormatToolPreview(tc)
	assert.Contains(t, preview, "delete_file(temp.txt)")
}

func TestFormatToolPreview_InvalidJSON(t *testing.T) {
	tc := llm.ToolCall{
		Name:      "write_file",
		Arguments: `not json`,
	}
	preview := FormatToolPreview(tc)
	assert.Contains(t, preview, "write_file")
}

func TestTruncatePreview(t *testing.T) {
	assert.Equal(t, "short", truncatePreview("short", 80))
	long := "a very long string that exceeds the maximum length allowed for preview display purposes"
	result := truncatePreview(long, 20)
	assert.Len(t, result, 23) // 20 + "..."
	assert.True(t, len(result) <= 23)
}

// Integration test: verify permission check in the agent loop.
func TestRun_PermissionDenied(t *testing.T) {
	rt := newMockRuntime("/project")

	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{
				StopReason: "tool_use",
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "write_file", Arguments: `{"path":"x.go","content":"hello"}`},
				},
			},
			{Content: "OK, I won't write that file.", StopReason: "end"},
		},
	}

	confirmFn := func(tc llm.ToolCall, preview string) ConfirmResult {
		return ConfirmNo
	}
	pc := NewPermissionChecker(nil, confirmFn)

	a := New(mp, rt, testLogger, WithPermissionChecker(pc))
	result, err := a.Run(context.Background(), "write a file", nil)

	require.NoError(t, err)
	require.Len(t, result.Steps, 1)
	assert.Contains(t, result.Steps[0].Error, "user denied")
	// File should NOT have been written.
	assert.Nil(t, rt.files["x.go"])
}

func TestRun_PermissionAutoAllowed(t *testing.T) {
	rt := newMockRuntime("/project")
	rt.files["x.go"] = []byte("package main")

	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{
				StopReason: "tool_use",
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "read_file", Arguments: `{"path":"x.go"}`},
				},
			},
			{Content: "It says package main.", StopReason: "end"},
		},
	}

	callCount := 0
	confirmFn := func(tc llm.ToolCall, preview string) ConfirmResult {
		callCount++
		return ConfirmNo
	}
	pc := NewPermissionChecker(nil, confirmFn)

	a := New(mp, rt, testLogger, WithPermissionChecker(pc))
	result, err := a.Run(context.Background(), "read x.go", nil)

	require.NoError(t, err)
	assert.Equal(t, "It says package main.", result.Response)
	// read_file is auto — confirmFn should never be called.
	assert.Equal(t, 0, callCount)
}

func TestRun_PermissionDeny_NeverExecutes(t *testing.T) {
	rt := newMockRuntime("/project")

	mp := &mockProvider{
		name:  "test",
		model: "test-model",
		responses: []*llm.Response{
			{
				StopReason: "tool_use",
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "delete_file", Arguments: `{"path":"important.txt"}`},
				},
			},
			{Content: "Understood.", StopReason: "end"},
		},
	}

	perms := DefaultPermissions()
	perms["delete_file"] = PermDeny
	pc := NewPermissionChecker(perms, nil)

	a := New(mp, rt, testLogger, WithPermissionChecker(pc))
	result, err := a.Run(context.Background(), "delete important.txt", nil)

	require.NoError(t, err)
	require.Len(t, result.Steps, 1)
	assert.Contains(t, result.Steps[0].Error, "denied by permission policy")
}
