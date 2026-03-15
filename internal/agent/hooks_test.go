package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/marmutapp/openmarmut/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testLogger is defined in agent_test.go as a package-level var.

func TestLoadHooks_Empty(t *testing.T) {
	cfg := &config.Config{}
	hooks, err := LoadHooks(cfg)
	require.NoError(t, err)
	assert.Nil(t, hooks)
}

func TestLoadHooks_Valid(t *testing.T) {
	cfg := &config.Config{
		Hooks: []config.HookConfig{
			{
				Name:    "test-hook",
				Event:   "pre_tool",
				Type:    "shell",
				Command: "echo hello",
				Tools:   []string{"write_file"},
			},
			{
				Name:    "notify",
				Event:   "post_tool",
				Type:    "http",
				URL:     "https://example.com/webhook",
				Headers: map[string]string{"Authorization": "Bearer token"},
				Timeout: 5 * time.Second,
				OnError: "abort",
			},
		},
	}
	hooks, err := LoadHooks(cfg)
	require.NoError(t, err)
	require.Len(t, hooks, 2)

	assert.Equal(t, "test-hook", hooks[0].Name)
	assert.Equal(t, "pre_tool", hooks[0].Event)
	assert.Equal(t, "shell", hooks[0].Type)
	assert.Equal(t, "echo hello", hooks[0].Command)
	assert.Equal(t, []string{"write_file"}, hooks[0].Tools)
	assert.Equal(t, 10*time.Second, hooks[0].Timeout) // default
	assert.Equal(t, "continue", hooks[0].OnError)     // default

	assert.Equal(t, "notify", hooks[1].Name)
	assert.Equal(t, "abort", hooks[1].OnError)
	assert.Equal(t, 5*time.Second, hooks[1].Timeout)
}

func TestLoadHooks_ValidationErrors(t *testing.T) {
	cfg := &config.Config{
		Hooks: []config.HookConfig{
			{Name: "", Event: "pre_tool", Type: "shell", Command: "echo"},        // missing name
			{Name: "bad-event", Event: "invalid", Type: "shell", Command: "echo"}, // bad event
			{Name: "bad-type", Event: "pre_tool", Type: "webhook"},                // bad type
			{Name: "no-cmd", Event: "pre_tool", Type: "shell"},                    // shell without command
			{Name: "no-url", Event: "pre_tool", Type: "http"},                     // http without url
		},
	}
	hooks, err := LoadHooks(cfg)
	require.Error(t, err)
	assert.Len(t, hooks, 0) // all invalid
	assert.Contains(t, err.Error(), "name is required")
	assert.Contains(t, err.Error(), "invalid event")
	assert.Contains(t, err.Error(), "type must be")
	assert.Contains(t, err.Error(), "shell hook requires command")
	assert.Contains(t, err.Error(), "http hook requires url")
}

func TestMatchesHookEvent(t *testing.T) {
	tests := []struct {
		name    string
		hook    Hook
		event   string
		tool    string
		matches bool
	}{
		{
			name:    "exact match, no tools filter",
			hook:    Hook{Event: "pre_tool"},
			event:   "pre_tool",
			tool:    "write_file",
			matches: true,
		},
		{
			name:    "wrong event",
			hook:    Hook{Event: "post_tool"},
			event:   "pre_tool",
			tool:    "write_file",
			matches: false,
		},
		{
			name:    "tools filter match",
			hook:    Hook{Event: "pre_tool", Tools: []string{"write_file", "delete_file"}},
			event:   "pre_tool",
			tool:    "write_file",
			matches: true,
		},
		{
			name:    "tools filter no match",
			hook:    Hook{Event: "pre_tool", Tools: []string{"write_file"}},
			event:   "pre_tool",
			tool:    "read_file",
			matches: false,
		},
		{
			name:    "session event ignores tool",
			hook:    Hook{Event: "pre_session"},
			event:   "pre_session",
			tool:    "",
			matches: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.matches, matchesHookEvent(tt.hook, tt.event, tt.tool))
		})
	}
}

func TestRunHooks_ShellHook_Success(t *testing.T) {
	hooks := []Hook{
		{
			Name:    "echo-hook",
			Event:   "pre_tool",
			Type:    "shell",
			Command: "cat > /dev/null", // consume stdin
			Timeout: 5 * time.Second,
			OnError: "continue",
		},
	}

	payload := HookPayload{
		Tool:      "write_file",
		Arguments: json.RawMessage(`{"path":"test.go"}`),
		Session:   "test-session",
	}

	err := RunHooks(context.Background(), hooks, "pre_tool", payload, testLogger)
	require.NoError(t, err)
}

func TestRunHooks_ShellHook_Failure_Continue(t *testing.T) {
	hooks := []Hook{
		{
			Name:    "fail-hook",
			Event:   "pre_tool",
			Type:    "shell",
			Command: "exit 1",
			Timeout: 5 * time.Second,
			OnError: "continue",
		},
	}

	payload := HookPayload{Tool: "write_file"}
	err := RunHooks(context.Background(), hooks, "pre_tool", payload, testLogger)
	// Should not return error when on_error=continue.
	require.NoError(t, err)
}

func TestRunHooks_ShellHook_Failure_Abort(t *testing.T) {
	hooks := []Hook{
		{
			Name:    "guard-hook",
			Event:   "pre_tool",
			Type:    "shell",
			Command: "echo 'blocked' >&2; exit 1",
			Timeout: 5 * time.Second,
			OnError: "abort",
		},
	}

	payload := HookPayload{Tool: "write_file"}
	err := RunHooks(context.Background(), hooks, "pre_tool", payload, testLogger)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrHookAbort))
	assert.Contains(t, err.Error(), "guard-hook")
}

func TestRunHooks_ShellHook_EnvVars(t *testing.T) {
	hooks := []Hook{
		{
			Name:    "env-hook",
			Event:   "pre_tool",
			Type:    "shell",
			Command: `test "$OPENMARMUT_EVENT" = "pre_tool" && test "$OPENMARMUT_TOOL" = "write_file" && test "$OPENMARMUT_SESSION" = "sess123"`,
			Timeout: 5 * time.Second,
			OnError: "abort",
		},
	}

	payload := HookPayload{
		Tool:    "write_file",
		Session: "sess123",
	}
	err := RunHooks(context.Background(), hooks, "pre_tool", payload, testLogger)
	require.NoError(t, err)
}

func TestRunHooks_ShellHook_StdinPayload(t *testing.T) {
	hooks := []Hook{
		{
			Name:    "stdin-hook",
			Event:   "pre_tool",
			Type:    "shell",
			Command: `grep -q '"tool":"read_file"'`, // verify payload arrives on stdin
			Timeout: 5 * time.Second,
			OnError: "abort",
		},
	}

	payload := HookPayload{
		Tool:      "read_file",
		Arguments: json.RawMessage(`{"path":"main.go"}`),
	}
	err := RunHooks(context.Background(), hooks, "pre_tool", payload, testLogger)
	require.NoError(t, err)
}

func TestRunHooks_HTTPHook_Success(t *testing.T) {
	var receivedPayload HookPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		if err := json.NewDecoder(r.Body).Decode(&receivedPayload); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"ok": true}`))
	}))
	defer server.Close()

	hooks := []Hook{
		{
			Name:    "http-hook",
			Event:   "post_tool",
			Type:    "http",
			URL:     server.URL,
			Timeout: 5 * time.Second,
			OnError: "continue",
		},
	}

	payload := HookPayload{
		Tool:      "write_file",
		Arguments: json.RawMessage(`{"path":"test.go"}`),
		Result:    "wrote 100 bytes to test.go",
		Session:   "sess-abc",
	}
	err := RunHooks(context.Background(), hooks, "post_tool", payload, testLogger)
	require.NoError(t, err)
	assert.Equal(t, "write_file", receivedPayload.Tool)
	assert.Equal(t, "sess-abc", receivedPayload.Session)
}

func TestRunHooks_HTTPHook_AbortResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"abort": true, "reason": "not allowed"}`))
	}))
	defer server.Close()

	hooks := []Hook{
		{
			Name:    "guard-http",
			Event:   "pre_tool",
			Type:    "http",
			URL:     server.URL,
			Timeout: 5 * time.Second,
			OnError: "abort",
		},
	}

	payload := HookPayload{Tool: "write_file"}
	err := RunHooks(context.Background(), hooks, "pre_tool", payload, testLogger)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrHookAbort))
	assert.Contains(t, err.Error(), "not allowed")
}

func TestRunHooks_HTTPHook_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	hooks := []Hook{
		{
			Name:    "bad-server",
			Event:   "post_tool",
			Type:    "http",
			URL:     server.URL,
			Timeout: 5 * time.Second,
			OnError: "continue",
		},
	}

	payload := HookPayload{Tool: "write_file"}
	// Should not return error when on_error=continue.
	err := RunHooks(context.Background(), hooks, "post_tool", payload, testLogger)
	require.NoError(t, err)
}

func TestRunHooks_HTTPHook_CustomHeaders(t *testing.T) {
	t.Setenv("MY_TOKEN", "secret-123")

	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.WriteHeader(200)
	}))
	defer server.Close()

	hooks := []Hook{
		{
			Name:    "header-hook",
			Event:   "post_tool",
			Type:    "http",
			URL:     server.URL,
			Headers: map[string]string{"Authorization": "Bearer $MY_TOKEN"},
			Timeout: 5 * time.Second,
			OnError: "continue",
		},
	}

	payload := HookPayload{Tool: "write_file"}
	err := RunHooks(context.Background(), hooks, "post_tool", payload, testLogger)
	require.NoError(t, err)
	assert.Equal(t, "Bearer secret-123", authHeader)
}

func TestRunHooks_ToolFilter(t *testing.T) {
	// Hook only triggers for write_file, not read_file.
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))
	defer server.Close()

	hooks := []Hook{
		{
			Name:    "write-only",
			Event:   "post_tool",
			Type:    "http",
			URL:     server.URL,
			Tools:   []string{"write_file"},
			Timeout: 5 * time.Second,
			OnError: "continue",
		},
	}

	payload := HookPayload{Tool: "read_file"}
	err := RunHooks(context.Background(), hooks, "post_tool", payload, testLogger)
	require.NoError(t, err)
	assert.False(t, called, "hook should not be called for read_file")

	payload.Tool = "write_file"
	err = RunHooks(context.Background(), hooks, "post_tool", payload, testLogger)
	require.NoError(t, err)
	assert.True(t, called, "hook should be called for write_file")
}

func TestRunHooks_EventFilter(t *testing.T) {
	hooks := []Hook{
		{
			Name:    "post-only",
			Event:   "post_tool",
			Type:    "shell",
			Command: "exit 1", // would fail if called
			Timeout: 5 * time.Second,
			OnError: "abort",
		},
	}

	payload := HookPayload{Tool: "write_file"}
	// pre_tool should not trigger a post_tool hook.
	err := RunHooks(context.Background(), hooks, "pre_tool", payload, testLogger)
	require.NoError(t, err)
}

func TestRunHooks_MultipleHooks(t *testing.T) {
	callOrder := []string{}
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callOrder = append(callOrder, "hook1")
		w.WriteHeader(200)
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callOrder = append(callOrder, "hook2")
		w.WriteHeader(200)
	}))
	defer server2.Close()

	hooks := []Hook{
		{
			Name: "hook1", Event: "post_tool", Type: "http",
			URL: server1.URL, Timeout: 5 * time.Second, OnError: "continue",
		},
		{
			Name: "hook2", Event: "post_tool", Type: "http",
			URL: server2.URL, Timeout: 5 * time.Second, OnError: "continue",
		},
	}

	payload := HookPayload{Tool: "write_file"}
	err := RunHooks(context.Background(), hooks, "post_tool", payload, testLogger)
	require.NoError(t, err)
	assert.Equal(t, []string{"hook1", "hook2"}, callOrder)
}

func TestRunHooks_AbortStopsSubsequent(t *testing.T) {
	secondCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalled = true
		w.WriteHeader(200)
	}))
	defer server.Close()

	hooks := []Hook{
		{
			Name: "aborter", Event: "pre_tool", Type: "shell",
			Command: "exit 1", Timeout: 5 * time.Second, OnError: "abort",
		},
		{
			Name: "after-abort", Event: "pre_tool", Type: "http",
			URL: server.URL, Timeout: 5 * time.Second, OnError: "continue",
		},
	}

	payload := HookPayload{Tool: "write_file"}
	err := RunHooks(context.Background(), hooks, "pre_tool", payload, testLogger)
	require.Error(t, err)
	assert.False(t, secondCalled, "second hook should not run after abort")
}

func TestInterpolateEnvVars(t *testing.T) {
	t.Setenv("FOO", "bar")
	t.Setenv("TOKEN", "abc123")

	assert.Equal(t, "Bearer abc123", interpolateEnvVars("Bearer $TOKEN"))
	assert.Equal(t, "bar-baz", interpolateEnvVars("$FOO-baz"))
	assert.Equal(t, "", interpolateEnvVars("$NONEXISTENT"))
}

func TestFormatHooksList_Empty(t *testing.T) {
	assert.Equal(t, "No hooks configured.", FormatHooksList(nil))
}

func TestFormatHooksList(t *testing.T) {
	hooks := []Hook{
		{
			Name:    "audit",
			Event:   "post_tool",
			Type:    "shell",
			Command: "echo logged",
			Tools:   []string{"write_file"},
			OnError: "continue",
		},
		{
			Name:    "notify",
			Event:   "post_tool",
			Type:    "http",
			URL:     "https://example.com",
			OnError: "abort",
		},
	}

	output := FormatHooksList(hooks)
	assert.Contains(t, output, "audit")
	assert.Contains(t, output, "post_tool")
	assert.Contains(t, output, "shell")
	assert.Contains(t, output, "write_file")
	assert.Contains(t, output, "echo logged")
	assert.Contains(t, output, "notify")
	assert.Contains(t, output, "https://example.com")
	assert.Contains(t, output, "(all)")
}

func TestRunHooks_SessionEvents(t *testing.T) {
	for _, event := range []string{"pre_session", "post_session"} {
		t.Run(event, func(t *testing.T) {
			hooks := []Hook{
				{
					Name:    event + "-hook",
					Event:   event,
					Type:    "shell",
					Command: "cat > /dev/null",
					Timeout: 5 * time.Second,
					OnError: "continue",
				},
			}

			payload := HookPayload{Session: "test-sess"}
			err := RunHooks(context.Background(), hooks, event, payload, testLogger)
			require.NoError(t, err)
		})
	}
}

func TestRunHooks_CompactEvents(t *testing.T) {
	for _, event := range []string{"pre_compact", "post_compact"} {
		t.Run(event, func(t *testing.T) {
			hooks := []Hook{
				{
					Name:    event + "-hook",
					Event:   event,
					Type:    "shell",
					Command: "cat > /dev/null",
					Timeout: 5 * time.Second,
					OnError: "continue",
				},
			}

			payload := HookPayload{Session: "test-sess"}
			err := RunHooks(context.Background(), hooks, event, payload, testLogger)
			require.NoError(t, err)
		})
	}
}

func TestLoadHooks_DefaultValues(t *testing.T) {
	cfg := &config.Config{
		Hooks: []config.HookConfig{
			{
				Name:    "minimal",
				Event:   "pre_tool",
				Type:    "shell",
				Command: "true",
			},
		},
	}
	hooks, err := LoadHooks(cfg)
	require.NoError(t, err)
	require.Len(t, hooks, 1)
	assert.Equal(t, 10*time.Second, hooks[0].Timeout, "default timeout should be 10s")
	assert.Equal(t, "continue", hooks[0].OnError, "default on_error should be continue")
}

func TestValidHookEvents(t *testing.T) {
	expected := []string{"pre_tool", "post_tool", "pre_session", "post_session", "pre_compact", "post_compact"}
	for _, e := range expected {
		assert.True(t, validHookEvents[e], "event %q should be valid", e)
	}
	assert.False(t, validHookEvents["invalid_event"])
}
