package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/marmutapp/openmarmut/internal/config"
)

// Hook defines a user-configured hook that runs before or after tool execution.
type Hook struct {
	Name    string            `yaml:"name"`
	Event   string            `yaml:"event"`   // pre_tool, post_tool, pre_session, post_session, pre_compact, post_compact
	Tools   []string          `yaml:"tools"`   // which tools trigger this hook (empty = all)
	Type    string            `yaml:"type"`    // "shell" or "http"
	Command string            `yaml:"command"` // for shell hooks
	URL     string            `yaml:"url"`     // for http hooks
	Headers map[string]string `yaml:"headers"` // for http hooks
	Timeout time.Duration     `yaml:"timeout"` // max wait (default 10s)
	OnError string            `yaml:"on_error"` // "continue" (default) or "abort"
}

// HookPayload is the JSON payload passed to hooks.
type HookPayload struct {
	Event     string          `json:"event"`
	Tool      string          `json:"tool,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Result    string          `json:"result,omitempty"`
	Session   string          `json:"session_id,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
}

// validHookEvents lists recognized hook event names.
var validHookEvents = map[string]bool{
	"pre_tool":      true,
	"post_tool":     true,
	"pre_session":   true,
	"post_session":  true,
	"pre_compact":   true,
	"post_compact":  true,
}

// ErrHookAbort is returned when a hook with on_error=abort fails.
var ErrHookAbort = fmt.Errorf("hook aborted")

// LoadHooks extracts hooks from the config.
func LoadHooks(cfg *config.Config) ([]Hook, error) {
	if len(cfg.Hooks) == 0 {
		return nil, nil
	}

	var hooks []Hook
	var errs []string
	for i, h := range cfg.Hooks {
		if h.Name == "" {
			errs = append(errs, fmt.Sprintf("hooks[%d]: name is required", i))
			continue
		}
		if !validHookEvents[h.Event] {
			errs = append(errs, fmt.Sprintf("hooks[%d] (%s): invalid event %q", i, h.Name, h.Event))
			continue
		}
		if h.Type != "shell" && h.Type != "http" {
			errs = append(errs, fmt.Sprintf("hooks[%d] (%s): type must be \"shell\" or \"http\", got %q", i, h.Name, h.Type))
			continue
		}
		if h.Type == "shell" && h.Command == "" {
			errs = append(errs, fmt.Sprintf("hooks[%d] (%s): shell hook requires command", i, h.Name))
			continue
		}
		if h.Type == "http" && h.URL == "" {
			errs = append(errs, fmt.Sprintf("hooks[%d] (%s): http hook requires url", i, h.Name))
			continue
		}

		hook := Hook{
			Name:    h.Name,
			Event:   h.Event,
			Tools:   h.Tools,
			Type:    h.Type,
			Command: h.Command,
			URL:     h.URL,
			Headers: h.Headers,
			Timeout: h.Timeout,
			OnError: h.OnError,
		}
		if hook.Timeout <= 0 {
			hook.Timeout = 10 * time.Second
		}
		if hook.OnError == "" {
			hook.OnError = "continue"
		}
		hooks = append(hooks, hook)
	}

	if len(errs) > 0 {
		return hooks, fmt.Errorf("hook config errors:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return hooks, nil
}

// RunHooks runs all hooks matching the given event and payload.
// Returns ErrHookAbort if any abort hook fails.
func RunHooks(ctx context.Context, hooks []Hook, event string, payload HookPayload, logger *slog.Logger) error {
	payload.Event = event
	payload.Timestamp = time.Now()

	for _, h := range hooks {
		if !matchesHookEvent(h, event, payload.Tool) {
			continue
		}

		var err error
		switch h.Type {
		case "shell":
			err = runShellHook(ctx, h, payload, logger)
		case "http":
			err = runHTTPHook(ctx, h, payload, logger)
		}

		if err != nil {
			if h.OnError == "abort" {
				logger.Info("hook aborted", "hook", h.Name, "event", event, "error", err)
				return fmt.Errorf("%w: hook %q failed: %s", ErrHookAbort, h.Name, err)
			}
			logger.Debug("hook failed (continuing)", "hook", h.Name, "event", event, "error", err)
		}
	}
	return nil
}

// runShellHook executes a shell hook via sh -c with the payload on stdin.
func runShellHook(ctx context.Context, h Hook, payload HookPayload, logger *slog.Logger) error {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, h.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", h.Command)
	cmd.Stdin = bytes.NewReader(payloadJSON)

	// Set environment variables.
	cmd.Env = append(os.Environ(),
		"OPENMARMUT_EVENT="+payload.Event,
		"OPENMARMUT_TOOL="+payload.Tool,
		"OPENMARMUT_SESSION="+payload.Session,
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		logger.Debug("shell hook output", "hook", h.Name, "stdout", stdout.String(), "stderr", stderr.String())
		return fmt.Errorf("command exited with error: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}

	if stdout.Len() > 0 {
		logger.Debug("shell hook stdout", "hook", h.Name, "output", stdout.String())
	}
	if stderr.Len() > 0 {
		logger.Debug("shell hook stderr", "hook", h.Name, "output", stderr.String())
	}

	return nil
}

// runHTTPHook sends a POST request with the payload to the hook URL.
func runHTTPHook(ctx context.Context, h Hook, payload HookPayload, logger *slog.Logger) error {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, h.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(payloadJSON))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Apply custom headers with env var interpolation.
	for k, v := range h.Headers {
		v = interpolateEnvVars(v)
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*64))

	if resp.StatusCode >= 400 {
		return fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	// Check for abort response.
	var abortResp struct {
		Abort  bool   `json:"abort"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal(body, &abortResp) == nil && abortResp.Abort {
		reason := abortResp.Reason
		if reason == "" {
			reason = "hook requested abort"
		}
		return fmt.Errorf("%s", reason)
	}

	logger.Debug("http hook response", "hook", h.Name, "status", resp.StatusCode)
	return nil
}

// interpolateEnvVars replaces $VAR_NAME references in a string with their
// environment variable values.
func interpolateEnvVars(s string) string {
	return os.Expand(s, os.Getenv)
}

// matchesHookEvent checks if a hook matches a specific event and optional tool.
func matchesHookEvent(h Hook, event, tool string) bool {
	if h.Event != event {
		return false
	}
	if len(h.Tools) == 0 {
		return true
	}
	for _, t := range h.Tools {
		if t == tool {
			return true
		}
	}
	return false
}

// FormatHooksList returns a human-readable list of configured hooks.
func FormatHooksList(hooks []Hook) string {
	if len(hooks) == 0 {
		return "No hooks configured."
	}

	var b strings.Builder
	for _, h := range hooks {
		fmt.Fprintf(&b, "• %s\n", h.Name)
		fmt.Fprintf(&b, "  event: %s, type: %s, on_error: %s\n", h.Event, h.Type, h.OnError)
		if len(h.Tools) > 0 {
			fmt.Fprintf(&b, "  tools: %s\n", strings.Join(h.Tools, ", "))
		} else {
			fmt.Fprintf(&b, "  tools: (all)\n")
		}
		if h.Type == "shell" {
			cmd := h.Command
			if len(cmd) > 80 {
				cmd = cmd[:80] + "..."
			}
			fmt.Fprintf(&b, "  command: %s\n", cmd)
		} else {
			fmt.Fprintf(&b, "  url: %s\n", h.URL)
		}
	}
	return b.String()
}
