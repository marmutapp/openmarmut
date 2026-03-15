package anthropic

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

	"github.com/marmutapp/openmarmut/internal/llm"
)

const (
	apiVersion       = "2023-06-01"
	messagesEndpoint = "/v1/messages"
)

// Provider implements llm.Provider for the Anthropic Messages API.
type Provider struct {
	name    string
	model   string
	apiKey  string
	baseURL string
	auth    llm.AuthConfig
	headers map[string]string
	client  *http.Client
	logger  *slog.Logger
	defTemp *float64
	defMax  *int
}

func init() {
	llm.RegisterType("anthropic", func(entry llm.ProviderEntry, logger *slog.Logger) (llm.Provider, error) {
		return New(entry, logger)
	})
}

// New creates an Anthropic provider from a ProviderEntry.
func New(entry llm.ProviderEntry, logger *slog.Logger) (*Provider, error) {
	if entry.ModelName == "" {
		return nil, fmt.Errorf("anthropic.New: model is required")
	}
	if entry.APIKey == "" && entry.Auth.Type != "none" {
		return nil, fmt.Errorf("anthropic.New: %w", llm.ErrAuthFailed)
	}

	return &Provider{
		name:    entry.Name,
		model:   entry.ModelName,
		apiKey:  entry.APIKey,
		baseURL: strings.TrimRight(entry.EndpointURL, "/"),
		auth:    entry.Auth,
		headers: entry.Headers,
		client:  &http.Client{},
		logger:  logger,
		defTemp: entry.Temperature,
		defMax:  entry.MaxTokens,
	}, nil
}

func (p *Provider) Name() string  { return p.name }
func (p *Provider) Model() string { return p.model }

// Complete sends a request to the Anthropic Messages API and streams the response.
func (p *Provider) Complete(ctx context.Context, req llm.Request, cb llm.StreamCallback) (*llm.Response, error) {
	body, err := p.buildRequest(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic.Complete: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+messagesEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic.Complete: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", apiVersion)
	llm.ApplyAuth(httpReq, p.auth, p.apiKey)

	for k, v := range p.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic.Complete: %w", err)
	}
	defer resp.Body.Close()

	if err := p.checkError(resp); err != nil {
		return nil, err
	}

	return p.parseSSEStream(resp.Body, cb)
}

// --- Request Building ---

// apiRequest is the Anthropic Messages API request body.
type apiRequest struct {
	Model       string          `json:"model"`
	Messages    []apiMessage    `json:"messages"`
	System      string          `json:"system,omitempty"`
	MaxTokens   int             `json:"max_tokens"`
	Temperature *float64        `json:"temperature,omitempty"`
	Stream      bool            `json:"stream"`
	Tools       []apiToolDef    `json:"tools,omitempty"`
	Thinking    *thinkingConfig `json:"thinking,omitempty"`
}

type thinkingConfig struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

type apiMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type apiContentBlock struct {
	Type      string       `json:"type"`
	Text      string       `json:"text,omitempty"`
	ID        string       `json:"id,omitempty"`
	Name      string       `json:"name,omitempty"`
	Input     any          `json:"input,omitempty"`
	ToolUseID string       `json:"tool_use_id,omitempty"`
	Content   string       `json:"content,omitempty"`
	Source    *imageSource `json:"source,omitempty"`
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type apiToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

func (p *Provider) buildRequest(req llm.Request) ([]byte, error) {
	ar := apiRequest{
		Model:  p.model,
		Stream: true,
	}

	// Max tokens: per-request > provider default > fallback 4096.
	if req.MaxTokens != nil {
		ar.MaxTokens = *req.MaxTokens
	} else if p.defMax != nil {
		ar.MaxTokens = *p.defMax
	} else {
		ar.MaxTokens = 4096
	}

	// Temperature: per-request > provider default.
	if req.Temperature != nil {
		ar.Temperature = req.Temperature
	} else if p.defTemp != nil {
		ar.Temperature = p.defTemp
	}

	// Extended thinking: adds a thinking block with budget.
	if req.ExtendedThinking {
		budget := req.ThinkingBudget
		if budget <= 0 {
			budget = 10000
		}
		ar.Thinking = &thinkingConfig{
			Type:         "enabled",
			BudgetTokens: budget,
		}
		// Anthropic requires temperature to be unset (or 1) with thinking.
		ar.Temperature = nil
	}

	// Convert messages. System messages become the top-level system field.
	for _, msg := range req.Messages {
		switch msg.Role {
		case llm.RoleSystem:
			ar.System = msg.Content

		case llm.RoleUser:
			if len(msg.Images) > 0 {
				var blocks []apiContentBlock
				blocks = append(blocks, apiContentBlock{Type: "text", Text: msg.Content})
				for _, img := range msg.Images {
					blocks = append(blocks, apiContentBlock{
						Type: "image",
						Source: &imageSource{
							Type:      "base64",
							MediaType: img.MimeType,
							Data:      img.Data,
						},
					})
				}
				raw, _ := json.Marshal(blocks)
				ar.Messages = append(ar.Messages, apiMessage{Role: "user", Content: raw})
			} else {
				ar.Messages = append(ar.Messages, apiMessage{
					Role:    "user",
					Content: mustMarshal(msg.Content),
				})
			}

		case llm.RoleAssistant:
			if len(msg.ToolCalls) > 0 {
				// Assistant with tool calls → content blocks.
				var blocks []apiContentBlock
				if msg.Content != "" {
					blocks = append(blocks, apiContentBlock{Type: "text", Text: msg.Content})
				}
				for _, tc := range msg.ToolCalls {
					var input any
					_ = json.Unmarshal([]byte(tc.Arguments), &input)
					blocks = append(blocks, apiContentBlock{
						Type:  "tool_use",
						ID:    tc.ID,
						Name:  tc.Name,
						Input: input,
					})
				}
				raw, _ := json.Marshal(blocks)
				ar.Messages = append(ar.Messages, apiMessage{Role: "assistant", Content: raw})
			} else {
				ar.Messages = append(ar.Messages, apiMessage{
					Role:    "assistant",
					Content: mustMarshal(msg.Content),
				})
			}

		case llm.RoleTool:
			// Tool results in Anthropic are user messages with tool_result blocks.
			block := apiContentBlock{
				Type:      "tool_result",
				ToolUseID: msg.ToolCallID,
				Content:   msg.Content,
			}
			raw, _ := json.Marshal([]apiContentBlock{block})

			// Merge with previous user message if it's also a tool result,
			// or create a new user message.
			if len(ar.Messages) > 0 && ar.Messages[len(ar.Messages)-1].Role == "user" {
				// Check if previous is tool_result blocks.
				var prevBlocks []apiContentBlock
				if json.Unmarshal(ar.Messages[len(ar.Messages)-1].Content, &prevBlocks) == nil && len(prevBlocks) > 0 && prevBlocks[0].Type == "tool_result" {
					prevBlocks = append(prevBlocks, block)
					merged, _ := json.Marshal(prevBlocks)
					ar.Messages[len(ar.Messages)-1].Content = merged
					continue
				}
			}
			ar.Messages = append(ar.Messages, apiMessage{Role: "user", Content: raw})
		}
	}

	// Convert tools.
	for _, t := range req.Tools {
		ar.Tools = append(ar.Tools, apiToolDef{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Parameters,
		})
	}

	return json.Marshal(ar)
}

// mustMarshal marshals a string as a JSON string value.
func mustMarshal(s string) json.RawMessage {
	data, _ := json.Marshal(s)
	return data
}

// --- SSE Stream Parsing ---

// parseSSEStream reads the SSE event stream and accumulates the response.
func (p *Provider) parseSSEStream(body io.Reader, cb llm.StreamCallback) (*llm.Response, error) {
	scanner := bufio.NewScanner(body)
	// Allow long lines for base64-encoded content etc.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	result := &llm.Response{}

	// Accumulate tool use blocks being built.
	type toolAccum struct {
		id    string
		name  string
		input strings.Builder
	}
	var activeTools []toolAccum
	var currentToolIdx int = -1
	var inThinking bool

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		if data == "[DONE]" {
			break
		}

		var event sseEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			p.logger.Debug("skipping unparseable SSE data", "data", data)
			continue
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil && event.Message.Usage != nil {
				result.Usage.PromptTokens = event.Message.Usage.InputTokens
			}

		case "content_block_start":
			if event.ContentBlock != nil {
				switch event.ContentBlock.Type {
				case "tool_use":
					activeTools = append(activeTools, toolAccum{
						id:   event.ContentBlock.ID,
						name: event.ContentBlock.Name,
					})
					currentToolIdx = len(activeTools) - 1
				case "thinking":
					inThinking = true
				}
			}

		case "content_block_delta":
			if event.Delta != nil {
				switch event.Delta.Type {
				case "text_delta":
					result.Content += event.Delta.Text
					if cb != nil {
						if err := cb(event.Delta.Text); err != nil {
							return nil, fmt.Errorf("anthropic.Complete: %w: %w", llm.ErrStreamAborted, err)
						}
					}
				case "thinking_delta":
					result.Thinking += event.Delta.Thinking
				case "input_json_delta":
					if currentToolIdx >= 0 && currentToolIdx < len(activeTools) {
						activeTools[currentToolIdx].input.WriteString(event.Delta.PartialJSON)
					}
				}
			}

		case "content_block_stop":
			if inThinking {
				inThinking = false
			}

		case "message_delta":
			if event.Delta != nil {
				if event.Delta.StopReason != "" {
					result.StopReason = mapStopReason(event.Delta.StopReason)
				}
			}
			if event.Usage != nil {
				result.Usage.CompletionTokens = event.Usage.OutputTokens
			}

		case "message_stop":
			// End of message.
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("anthropic.Complete: read stream: %w", err)
	}

	// Convert accumulated tool calls.
	for _, ta := range activeTools {
		result.ToolCalls = append(result.ToolCalls, llm.ToolCall{
			ID:        ta.id,
			Name:      ta.name,
			Arguments: ta.input.String(),
		})
	}

	result.Usage.TotalTokens = result.Usage.PromptTokens + result.Usage.CompletionTokens

	return result, nil
}

// --- SSE Event Types ---

type sseEvent struct {
	Type         string      `json:"type"`
	Message      *sseMessage `json:"message,omitempty"`
	ContentBlock *sseCB      `json:"content_block,omitempty"`
	Delta        *sseDelta   `json:"delta,omitempty"`
	Index        int         `json:"index"`
	Usage        *sseUsage   `json:"usage,omitempty"`
}

type sseMessage struct {
	ID    string    `json:"id"`
	Model string    `json:"model"`
	Usage *sseUsage `json:"usage,omitempty"`
}

type sseUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type sseCB struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Text string `json:"text,omitempty"`
}

type sseDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

func mapStopReason(reason string) string {
	switch reason {
	case "end_turn":
		return "end"
	case "tool_use":
		return "tool_use"
	case "max_tokens":
		return "max_tokens"
	default:
		return reason
	}
}

// --- Error Handling ---

type apiError struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (p *Provider) checkError(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	body, _ := io.ReadAll(resp.Body)

	var ae apiError
	_ = json.Unmarshal(body, &ae)
	msg := ae.Error.Message
	if msg == "" {
		msg = string(body)
	}

	switch resp.StatusCode {
	case 401:
		return fmt.Errorf("anthropic.Complete: %w: %s", llm.ErrAuthFailed, msg)
	case 429:
		return fmt.Errorf("anthropic.Complete: %w: %s", llm.ErrRateLimited, msg)
	case 404:
		return fmt.Errorf("anthropic.Complete: %w: %s", llm.ErrModelNotFound, msg)
	default:
		if resp.StatusCode >= 500 {
			return fmt.Errorf("anthropic.Complete: %w: HTTP %d: %s", llm.ErrServerError, resp.StatusCode, msg)
		}
		return fmt.Errorf("anthropic.Complete: HTTP %d: %s", resp.StatusCode, msg)
	}
}
