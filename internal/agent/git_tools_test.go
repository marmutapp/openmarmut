package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gajaai/openmarmut-go/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitMockRuntime extends mockRuntime with a custom exec function for git commands.
func newGitMockRuntime(execFn func(command string) (*runtime.ExecResult, error)) *mockRuntime {
	rt := newMockRuntime("/project")
	rt.execFn = execFn
	return rt
}

func TestGitStatus_Clean(t *testing.T) {
	rt := newGitMockRuntime(func(cmd string) (*runtime.ExecResult, error) {
		return &runtime.ExecResult{Stdout: "", ExitCode: 0}, nil
	})

	tool := gitStatusTool()
	output, err := tool.Execute(context.Background(), rt, json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Equal(t, "working tree clean", output)
}

func TestGitStatus_WithChanges(t *testing.T) {
	rt := newGitMockRuntime(func(cmd string) (*runtime.ExecResult, error) {
		return &runtime.ExecResult{
			Stdout:   " M main.go\n?? new.go\n",
			ExitCode: 0,
		}, nil
	})

	tool := gitStatusTool()
	output, err := tool.Execute(context.Background(), rt, json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Contains(t, output, "M main.go")
	assert.Contains(t, output, "?? new.go")
}

func TestGitStatus_NotGitRepo(t *testing.T) {
	rt := newGitMockRuntime(func(cmd string) (*runtime.ExecResult, error) {
		return &runtime.ExecResult{
			Stderr:   "fatal: not a git repository",
			ExitCode: 128,
		}, nil
	})

	tool := gitStatusTool()
	_, err := tool.Execute(context.Background(), rt, json.RawMessage(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a git repository")
}

func TestGitDiff_NoChanges(t *testing.T) {
	rt := newGitMockRuntime(func(cmd string) (*runtime.ExecResult, error) {
		return &runtime.ExecResult{Stdout: "", ExitCode: 0}, nil
	})

	tool := gitDiffTool()
	output, err := tool.Execute(context.Background(), rt, json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Equal(t, "no unstaged changes", output)
}

func TestGitDiff_WithPath(t *testing.T) {
	rt := newGitMockRuntime(func(cmd string) (*runtime.ExecResult, error) {
		assert.Contains(t, cmd, "-- 'main.go'")
		return &runtime.ExecResult{
			Stdout:   "diff --git a/main.go b/main.go\n",
			ExitCode: 0,
		}, nil
	})

	tool := gitDiffTool()
	output, err := tool.Execute(context.Background(), rt, json.RawMessage(`{"path":"main.go"}`))
	require.NoError(t, err)
	assert.Contains(t, output, "diff --git")
}

func TestGitDiffStaged_NoChanges(t *testing.T) {
	rt := newGitMockRuntime(func(cmd string) (*runtime.ExecResult, error) {
		assert.Contains(t, cmd, "--cached")
		return &runtime.ExecResult{Stdout: "", ExitCode: 0}, nil
	})

	tool := gitDiffStagedTool()
	output, err := tool.Execute(context.Background(), rt, json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Equal(t, "no staged changes", output)
}

func TestGitLog_Default(t *testing.T) {
	rt := newGitMockRuntime(func(cmd string) (*runtime.ExecResult, error) {
		assert.Contains(t, cmd, "-n 10")
		return &runtime.ExecResult{
			Stdout:   "abc1234 feat: add something\ndef5678 fix: bug\n",
			ExitCode: 0,
		}, nil
	})

	tool := gitLogTool()
	output, err := tool.Execute(context.Background(), rt, json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Contains(t, output, "abc1234")
	assert.Contains(t, output, "def5678")
}

func TestGitLog_CustomN(t *testing.T) {
	rt := newGitMockRuntime(func(cmd string) (*runtime.ExecResult, error) {
		assert.Contains(t, cmd, "-n 5")
		return &runtime.ExecResult{Stdout: "abc1234 feat\n", ExitCode: 0}, nil
	})

	tool := gitLogTool()
	output, err := tool.Execute(context.Background(), rt, json.RawMessage(`{"n":5}`))
	require.NoError(t, err)
	assert.Contains(t, output, "abc1234")
}

func TestGitLog_ClampsToMax(t *testing.T) {
	rt := newGitMockRuntime(func(cmd string) (*runtime.ExecResult, error) {
		assert.Contains(t, cmd, "-n 50")
		return &runtime.ExecResult{Stdout: "abc\n", ExitCode: 0}, nil
	})

	tool := gitLogTool()
	_, err := tool.Execute(context.Background(), rt, json.RawMessage(`{"n":100}`))
	require.NoError(t, err)
}

func TestGitCommit_Success(t *testing.T) {
	callIdx := 0
	rt := newGitMockRuntime(func(cmd string) (*runtime.ExecResult, error) {
		callIdx++
		if callIdx == 1 {
			assert.Contains(t, cmd, "git add -A")
			return &runtime.ExecResult{ExitCode: 0}, nil
		}
		assert.Contains(t, cmd, "git commit -m")
		assert.Contains(t, cmd, "feat: add feature")
		return &runtime.ExecResult{
			Stdout:   "[main abc1234] feat: add feature\n 1 file changed\n",
			ExitCode: 0,
		}, nil
	})

	tool := gitCommitTool()
	output, err := tool.Execute(context.Background(), rt, json.RawMessage(`{"message":"feat: add feature"}`))
	require.NoError(t, err)
	assert.Contains(t, output, "abc1234")
}

func TestGitCommit_EmptyMessage(t *testing.T) {
	rt := newGitMockRuntime(nil)
	tool := gitCommitTool()
	_, err := tool.Execute(context.Background(), rt, json.RawMessage(`{"message":""}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "message is required")
}

func TestGitBranch_List(t *testing.T) {
	rt := newGitMockRuntime(func(cmd string) (*runtime.ExecResult, error) {
		assert.Contains(t, cmd, "git branch -a")
		return &runtime.ExecResult{
			Stdout:   "* main\n  feature/x\n",
			ExitCode: 0,
		}, nil
	})

	tool := gitBranchTool()
	output, err := tool.Execute(context.Background(), rt, json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Contains(t, output, "main")
	assert.Contains(t, output, "feature/x")
}

func TestGitBranch_Create(t *testing.T) {
	rt := newGitMockRuntime(func(cmd string) (*runtime.ExecResult, error) {
		assert.Contains(t, cmd, "'new-branch'")
		return &runtime.ExecResult{Stdout: "", ExitCode: 0}, nil
	})

	tool := gitBranchTool()
	output, err := tool.Execute(context.Background(), rt, json.RawMessage(`{"name":"new-branch"}`))
	require.NoError(t, err)
	assert.Contains(t, output, "created branch new-branch")
}

func TestGitCheckout_Success(t *testing.T) {
	rt := newGitMockRuntime(func(cmd string) (*runtime.ExecResult, error) {
		assert.Contains(t, cmd, "'feature'")
		return &runtime.ExecResult{
			Stderr:   "Switched to branch 'feature'\n",
			ExitCode: 0,
		}, nil
	})

	tool := gitCheckoutTool()
	output, err := tool.Execute(context.Background(), rt, json.RawMessage(`{"branch":"feature"}`))
	require.NoError(t, err)
	assert.Contains(t, output, "Switched to branch")
}

func TestGitCheckout_EmptyBranch(t *testing.T) {
	rt := newGitMockRuntime(nil)
	tool := gitCheckoutTool()
	_, err := tool.Execute(context.Background(), rt, json.RawMessage(`{"branch":""}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "branch is required")
}

func TestGitCheckout_Error(t *testing.T) {
	rt := newGitMockRuntime(func(cmd string) (*runtime.ExecResult, error) {
		return &runtime.ExecResult{
			Stderr:   "error: pathspec 'nonexistent' did not match\n",
			ExitCode: 1,
		}, nil
	})

	tool := gitCheckoutTool()
	_, err := tool.Execute(context.Background(), rt, json.RawMessage(`{"branch":"nonexistent"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pathspec")
}

func TestGitToolsInDefaultTools(t *testing.T) {
	tools := DefaultTools()
	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Def.Name] = true
	}
	assert.True(t, names["git_status"])
	assert.True(t, names["git_diff"])
	assert.True(t, names["git_diff_staged"])
	assert.True(t, names["git_log"])
	assert.True(t, names["git_commit"])
	assert.True(t, names["git_branch"])
	assert.True(t, names["git_checkout"])
}

func TestGitToolPermissions(t *testing.T) {
	perms := DefaultPermissions()
	// Read-only git tools should be auto.
	assert.Equal(t, PermAuto, perms["git_status"])
	assert.Equal(t, PermAuto, perms["git_diff"])
	assert.Equal(t, PermAuto, perms["git_diff_staged"])
	assert.Equal(t, PermAuto, perms["git_log"])
	// Modifying git tools should be confirm.
	assert.Equal(t, PermConfirm, perms["git_commit"])
	assert.Equal(t, PermConfirm, perms["git_branch"])
	assert.Equal(t, PermConfirm, perms["git_checkout"])
}
