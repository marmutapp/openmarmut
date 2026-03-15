package openai

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

	"github.com/gajaai/openmarmut-go/internal/llm"
)

const (
	chatCompletionsPath = "/v1/chat/completions"
)

// Provider implements llm.Provider for the OpenAI Chat Completions API.
// This covers OpenAI, Azure OpenAI, Groq, Together, Fireworks, vLLM,
// and any endpoint speaking the OpenAI wire format.
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
	llm.RegisterType("openai", func(entry llm.ProviderEntry, logger *slog.Logger) (llm.Provider, error) {
		return New(entry, logger)
	})
}

// New creates an OpenAI wire format provider from a ProviderEntry.
func New(entry llm.ProviderEntry, logger *slog.Logger) (*Provider, error) {
	if entry.ModelName == "" {
		return nil, fmt.Errorf("openai.New: model is required")
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

// Complete sends a request to the OpenAI Chat Completions API and streams the response.
func (p *Provider) Complete(ctx context.Context, req llm.Request, cb llm.StreamCallback) (*llm.Response, error) {
	body, err := p.buildRequest(req)
	if err != nil {
		return nil, fmt.Errorf("openai.Complete: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+chatCompletionsPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai.Complete: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	llm.ApplyAuth(httpReq, p.auth, p.apiKey)

	for k, v := range p.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai.Complete: %w", err)
	}
	defer resp.Body.Close()

	if err := p.checkError(resp); err != nil {
		return nil, err
	}

	return p.parseSSEStream(resp.Body, cb)
}

// --- Request Building ---

type chatRequest struct {
	Model            string        `json:"model"`
	Messages         []chatMessage `json:"messages"`
	Temperature      *float64      `json:"temperature,omitempty"`
	MaxTokens        *int          `json:"max_tokens,omitempty"`
	Stream           bool          `json:"stream"`
	StreamOptions    *streamOpts   `json:"stream_options,omitempty"`
	Tools            []chatTool    `json:"tools,omitempty"`
	ReasoningEffort  string        `json:"reasoning_effort,omitempty"`
}

type streamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

// contentPart is used in OpenAI's multimodal content array.
type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatFunctionCall `json:"function"`
}

type chatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func (p *Provider) buildRequest(req llm.Request) ([]byte, error) {
	cr := chatRequest{
		Model:         p.model,
		Stream:        true,
		StreamOptions: &streamOpts{IncludeUsage: true},
	}

	// Max tokens: per-request > provider default.
	if req.MaxTokens != nil {
		cr.MaxTokens = req.MaxTokens
	} else if p.defMax != nil {
		cr.MaxTokens = p.defMax
	}

	// Temperature: per-request > provider default.
	if req.Temperature != nil {
		cr.Temperature = req.Temperature
	} else if p.defTemp != nil {
		cr.Temperature = p.defTemp
	}

	// Extended thinking: for o-series models, use reasoning_effort.
	if req.ExtendedThinking {
		cr.ReasoningEffort = budgetToEffort(req.ThinkingBudget)
	}

	// Convert messages.
	for _, msg := range req.Messages {
		switch msg.Role {
		case llm.RoleSystem:
			cr.Messages = append(cr.Messages, chatMessage{
				Role:    "system",
				Content: msg.Content,
			})

		case llm.RoleUser:
			if len(msg.Images) > 0 {
				parts := []contentPart{{Type: "text", Text: msg.Content}}
				for _, img := range msg.Images {
					parts = append(parts, contentPart{
						Type: "image_url",
						ImageURL: &imageURL{
							URL: "data:" + img.MimeType + ";base64," + img.Data,
						},
					})
				}
				cr.Messages = append(cr.Messages, chatMessage{Role: "user", Content: parts})
			} else {
				cr.Messages = append(cr.Messages, chatMessage{
					Role:    "user",
					Content: msg.Content,
				})
			}

		case llm.RoleAssistant:
			cm := chatMessage{
				Role:    "assistant",
				Content: msg.Content,
			}
			for _, tc := range msg.ToolCalls {
				cm.ToolCalls = append(cm.ToolCalls, chatToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: chatFunctionCall{
						Name:      tc.Name,
						Arguments: tc.Arguments,
					},
				})
			}
			cr.Messages = append(cr.Messages, cm)

		case llm.RoleTool:
			cr.Messages = append(cr.Messages, chatMessage{
				Role:       "tool",
				Content:    msg.Content,
				ToolCallID: msg.ToolCallID,
			})
		}
	}

	// Convert tools.
	for _, t := range req.Tools {
		cr.Tools = append(cr.Tools, chatTool{
			Type: "function",
			Function: chatFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}

	return json.Marshal(cr)
}

// --- SSE Stream Parsing ---

type streamChunk struct {
	ID      string         `json:"id"`
	Choices []streamChoice `json:"choices"`
	Usage   *streamUsage   `json:"usage,omitempty"`
}

type streamChoice struct {
	Index        int         `json:"index"`
	Delta        streamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type streamDelta struct {
	Role      string                `json:"role,omitempty"`
	Content   *string               `json:"content,omitempty"`
	ToolCalls []streamToolCallDelta `json:"tool_calls,omitempty"`
}

type streamToolCallDelta struct {
	Index    int                  `json:"index"`
	ID       string               `json:"id,omitempty"`
	Type     string               `json:"type,omitempty"`
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

	// Accumulate tool calls by index. OpenAI sends tool call deltas with an
	// index field; the first delta for a tool includes id+name, subsequent
	// deltas only contain argument fragments.
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

		// Handle usage (typically in the final chunk when stream_options.include_usage is set).
		if chunk.Usage != nil {
			result.Usage.PromptTokens = chunk.Usage.PromptTokens
			result.Usage.CompletionTokens = chunk.Usage.CompletionTokens
			result.Usage.TotalTokens = chunk.Usage.TotalTokens
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]

		// Text content delta.
		if choice.Delta.Content != nil && *choice.Delta.Content != "" {
			result.Content += *choice.Delta.Content
			if cb != nil {
				if err := cb(*choice.Delta.Content); err != nil {
					return nil, fmt.Errorf("openai.Complete: %w: %w", llm.ErrStreamAborted, err)
				}
			}
		}

		// Tool call deltas.
		for _, tcd := range choice.Delta.ToolCalls {
			// Grow the tools slice if needed.
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

		// Finish reason.
		if choice.FinishReason != nil {
			result.StopReason = mapStopReason(*choice.FinishReason)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("openai.Complete: read stream: %w", err)
	}

	// Convert accumulated tool calls.
	for _, ta := range tools {
		result.ToolCalls = append(result.ToolCalls, llm.ToolCall{
			ID:        ta.id,
			Name:      ta.name,
			Arguments: ta.args.String(),
		})
	}

	return result, nil
}

// budgetToEffort maps a thinking budget to OpenAI reasoning_effort level.
func budgetToEffort(budget int) string {
	switch {
	case budget <= 0:
		return "medium"
	case budget <= 5000:
		return "low"
	case budget <= 20000:
		return "medium"
	default:
		return "high"
	}
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
		return fmt.Errorf("openai.Complete: %w: %s", llm.ErrAuthFailed, msg)
	case 429:
		return fmt.Errorf("openai.Complete: %w: %s", llm.ErrRateLimited, msg)
	case 404:
		return fmt.Errorf("openai.Complete: %w: %s", llm.ErrModelNotFound, msg)
	default:
		if resp.StatusCode >= 500 {
			return fmt.Errorf("openai.Complete: %w: HTTP %d: %s", llm.ErrServerError, resp.StatusCode, msg)
		}
		return fmt.Errorf("openai.Complete: HTTP %d: %s", resp.StatusCode, msg)
	}
}
