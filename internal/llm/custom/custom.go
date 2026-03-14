package custom

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gajaai/opencode-go/internal/llm"
)

// Provider implements llm.Provider for arbitrary OpenAI-compatible APIs.
// It uses the OpenAI chat completions wire format by default, but allows
// customization of the endpoint path and extra payload fields via PayloadConfig.
type Provider struct {
	name    string
	model   string
	apiKey  string
	baseURL string
	apiPath string
	auth    llm.AuthConfig
	headers map[string]string
	client  *http.Client
	logger  *slog.Logger
	defTemp *float64
	defMax  *int
	extra   map[string]any // Extra fields merged into the request payload.
}

// payloadConfig holds custom provider configuration parsed from PayloadConfig.
type payloadConfig struct {
	APIPath string         `json:"api_path"` // Custom API path, defaults to /v1/chat/completions.
	Extra   map[string]any `json:"extra"`    // Extra top-level fields merged into the request.
}

func init() {
	llm.RegisterType("custom", func(entry llm.ProviderEntry, logger *slog.Logger) (llm.Provider, error) {
		return New(entry, logger)
	})
}

// New creates a custom provider from a ProviderEntry.
func New(entry llm.ProviderEntry, logger *slog.Logger) (*Provider, error) {
	if entry.ModelName == "" {
		return nil, fmt.Errorf("custom.New: model is required")
	}
	if entry.EndpointURL == "" {
		return nil, fmt.Errorf("custom.New: endpoint_url is required for custom provider")
	}

	apiPath := "/v1/chat/completions"
	var extra map[string]any

	if len(entry.PayloadConfig) > 0 {
		var pc payloadConfig
		if err := json.Unmarshal(entry.PayloadConfig, &pc); err != nil {
			return nil, fmt.Errorf("custom.New: invalid payload_config: %w", err)
		}
		if pc.APIPath != "" {
			apiPath = pc.APIPath
		}
		extra = pc.Extra
	}

	return &Provider{
		name:    entry.Name,
		model:   entry.ModelName,
		apiKey:  entry.APIKey,
		baseURL: strings.TrimRight(entry.EndpointURL, "/"),
		apiPath: apiPath,
		auth:    entry.Auth,
		headers: entry.Headers,
		client:  &http.Client{},
		logger:  logger,
		defTemp: entry.Temperature,
		defMax:  entry.MaxTokens,
		extra:   extra,
	}, nil
}

func (p *Provider) Name() string  { return p.name }
func (p *Provider) Model() string { return p.model }

// Complete sends a request and streams the response using OpenAI-compatible SSE.
func (p *Provider) Complete(ctx context.Context, req llm.Request, cb llm.StreamCallback) (*llm.Response, error) {
	body, err := p.buildRequest(req)
	if err != nil {
		return nil, fmt.Errorf("custom.Complete: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+p.apiPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("custom.Complete: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	llm.ApplyAuth(httpReq, p.auth, p.apiKey)

	for k, v := range p.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("custom.Complete: %w", err)
	}
	defer resp.Body.Close()

	if err := p.checkError(resp); err != nil {
		return nil, err
	}

	return p.parseSSEStream(resp.Body, cb)
}

// --- Request Building ---

func (p *Provider) buildRequest(req llm.Request) ([]byte, error) {
	// Build an OpenAI-compatible request as a map so we can merge extra fields.
	payload := map[string]any{
		"model":  p.model,
		"stream": true,
		"stream_options": map[string]any{
			"include_usage": true,
		},
	}

	if req.MaxTokens != nil {
		payload["max_tokens"] = *req.MaxTokens
	} else if p.defMax != nil {
		payload["max_tokens"] = *p.defMax
	}

	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	} else if p.defTemp != nil {
		payload["temperature"] = *p.defTemp
	}

	// Convert messages.
	var messages []map[string]any
	for _, msg := range req.Messages {
		switch msg.Role {
		case llm.RoleSystem:
			messages = append(messages, map[string]any{
				"role":    "system",
				"content": msg.Content,
			})

		case llm.RoleUser:
			messages = append(messages, map[string]any{
				"role":    "user",
				"content": msg.Content,
			})

		case llm.RoleAssistant:
			m := map[string]any{
				"role":    "assistant",
				"content": msg.Content,
			}
			if len(msg.ToolCalls) > 0 {
				var tcs []map[string]any
				for _, tc := range msg.ToolCalls {
					tcs = append(tcs, map[string]any{
						"id":   tc.ID,
						"type": "function",
						"function": map[string]any{
							"name":      tc.Name,
							"arguments": tc.Arguments,
						},
					})
				}
				m["tool_calls"] = tcs
			}
			messages = append(messages, m)

		case llm.RoleTool:
			messages = append(messages, map[string]any{
				"role":         "tool",
				"content":      msg.Content,
				"tool_call_id": msg.ToolCallID,
			})
		}
	}
	payload["messages"] = messages

	// Convert tools.
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.Parameters,
				},
			})
		}
		payload["tools"] = tools
	}

	// Merge extra fields from PayloadConfig (don't override built-in fields).
	for k, v := range p.extra {
		if _, exists := payload[k]; !exists {
			payload[k] = v
		}
	}

	return json.Marshal(payload)
}

// --- SSE Stream Parsing (OpenAI-compatible) ---

type streamChunk struct {
	Choices []streamChoice `json:"choices"`
	Usage   *streamUsage   `json:"usage,omitempty"`
}

type streamChoice struct {
	Delta        streamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type streamDelta struct {
	Content   *string               `json:"content,omitempty"`
	ToolCalls []streamToolCallDelta `json:"tool_calls,omitempty"`
}

type streamToolCallDelta struct {
	Index    int                  `json:"index"`
	ID       string               `json:"id,omitempty"`
	Function *streamFunctionDelta `json:"function,omitempty"`
}

type streamFunctionDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type streamUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (p *Provider) parseSSEStream(body io.Reader, cb llm.StreamCallback) (*llm.Response, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	result := &llm.Response{}

	type toolAccum struct {
		id   string
		name string
		args strings.Builder
	}
	var tools []toolAccum

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		if data == "[DONE]" {
			break
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			p.logger.Debug("skipping unparseable SSE data", "data", data)
			continue
		}

		if chunk.Usage != nil {
			result.Usage.PromptTokens = chunk.Usage.PromptTokens
			result.Usage.CompletionTokens = chunk.Usage.CompletionTokens
			result.Usage.TotalTokens = chunk.Usage.TotalTokens
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]

		if choice.Delta.Content != nil && *choice.Delta.Content != "" {
			result.Content += *choice.Delta.Content
			if cb != nil {
				if err := cb(*choice.Delta.Content); err != nil {
					return nil, fmt.Errorf("custom.Complete: %w: %w", llm.ErrStreamAborted, err)
				}
			}
		}

		for _, tcd := range choice.Delta.ToolCalls {
			for tcd.Index >= len(tools) {
				tools = append(tools, toolAccum{})
			}
			if tcd.ID != "" {
				tools[tcd.Index].id = tcd.ID
			}
			if tcd.Function != nil {
				if tcd.Function.Name != "" {
					tools[tcd.Index].name = tcd.Function.Name
				}
				if tcd.Function.Arguments != "" {
					tools[tcd.Index].args.WriteString(tcd.Function.Arguments)
				}
			}
		}

		if choice.FinishReason != nil {
			result.StopReason = mapStopReason(*choice.FinishReason)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("custom.Complete: read stream: %w", err)
	}

	for _, ta := range tools {
		result.ToolCalls = append(result.ToolCalls, llm.ToolCall{
			ID:        ta.id,
			Name:      ta.name,
			Arguments: ta.args.String(),
		})
	}

	return result, nil
}

func mapStopReason(reason string) string {
	switch reason {
	case "stop":
		return "end"
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	default:
		return reason
	}
}

// --- Error Handling ---

type apiErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

func (p *Provider) checkError(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	body, _ := io.ReadAll(resp.Body)

	var ae apiErrorResponse
	_ = json.Unmarshal(body, &ae)
	msg := ae.Error.Message
	if msg == "" {
		msg = string(body)
	}

	switch resp.StatusCode {
	case 401:
		return fmt.Errorf("custom.Complete: %w: %s", llm.ErrAuthFailed, msg)
	case 429:
		return fmt.Errorf("custom.Complete: %w: %s", llm.ErrRateLimited, msg)
	case 404:
		return fmt.Errorf("custom.Complete: %w: %s", llm.ErrModelNotFound, msg)
	default:
		if resp.StatusCode >= 500 {
			return fmt.Errorf("custom.Complete: %w: HTTP %d: %s", llm.ErrServerError, resp.StatusCode, msg)
		}
		return fmt.Errorf("custom.Complete: HTTP %d: %s", resp.StatusCode, msg)
	}
}
