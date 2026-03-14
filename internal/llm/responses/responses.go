package responses

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/gajaai/opencode-go/internal/llm"
)

const (
	responsesPath = "/v1/responses"
)

// Provider implements llm.Provider for the OpenAI Responses API.
// This is the newer API used by o3, o4-mini, GPT-4o, and Codex.
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
	llm.RegisterType("openai-responses", func(entry llm.ProviderEntry, logger *slog.Logger) (llm.Provider, error) {
		return New(entry, logger)
	})
}

// New creates an OpenAI Responses API provider from a ProviderEntry.
func New(entry llm.ProviderEntry, logger *slog.Logger) (*Provider, error) {
	if entry.ModelName == "" {
		return nil, fmt.Errorf("responses.New: model is required")
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

// Complete sends a request to the OpenAI Responses API and streams the response.
func (p *Provider) Complete(ctx context.Context, req llm.Request, cb llm.StreamCallback) (*llm.Response, error) {
	body, err := p.buildRequest(req)
	if err != nil {
		return nil, fmt.Errorf("responses.Complete: %w", err)
	}

	reqURL := p.baseURL + responsesPath
	if hasPath(p.baseURL) {
		reqURL = p.baseURL
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("responses.Complete: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	llm.ApplyAuth(httpReq, p.auth, p.apiKey)

	for k, v := range p.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("responses.Complete: %w", err)
	}
	defer resp.Body.Close()

	if err := p.checkError(resp); err != nil {
		return nil, err
	}

	return p.parseSSEStream(resp.Body, cb)
}

// hasPath reports whether the endpoint URL contains a non-empty path component.
// Bare hosts like "https://api.openai.com" return false; URLs with paths like
// "https://example.com/openai/responses?api-version=..." return true.
func hasPath(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	return u.Path != "" && u.Path != "/"
}

// --- Request Building ---

type apiRequest struct {
	Model           string     `json:"model"`
	Input           any        `json:"input"`
	Instructions    string     `json:"instructions,omitempty"`
	Temperature     *float64   `json:"temperature,omitempty"`
	MaxOutputTokens *int       `json:"max_output_tokens,omitempty"`
	Stream          bool       `json:"stream"`
	Tools           []apiTool  `json:"tools,omitempty"`
}

type inputItem struct {
	Type       string `json:"type"`
	Role       string `json:"role,omitempty"`
	Content    string `json:"content,omitempty"`
	CallID     string `json:"call_id,omitempty"`
	Output     string `json:"output,omitempty"`
	ID         string `json:"id,omitempty"`
	Name       string `json:"name,omitempty"`
	Arguments  string `json:"arguments,omitempty"`
}

type apiTool struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

func (p *Provider) buildRequest(req llm.Request) ([]byte, error) {
	ar := apiRequest{
		Model:  p.model,
		Stream: true,
	}

	if req.MaxTokens != nil {
		ar.MaxOutputTokens = req.MaxTokens
	} else if p.defMax != nil {
		ar.MaxOutputTokens = p.defMax
	}

	if req.Temperature != nil {
		ar.Temperature = req.Temperature
	} else if p.defTemp != nil {
		ar.Temperature = p.defTemp
	}

	var items []inputItem
	for _, msg := range req.Messages {
		switch msg.Role {
		case llm.RoleSystem:
			ar.Instructions = msg.Content

		case llm.RoleUser:
			items = append(items, inputItem{
				Type:    "message",
				Role:    "user",
				Content: msg.Content,
			})

		case llm.RoleAssistant:
			// Only emit a message item if there's actual text content.
			// The Responses API rejects empty content on message items.
			if msg.Content != "" {
				items = append(items, inputItem{
					Type:    "message",
					Role:    "assistant",
					Content: msg.Content,
				})
			}
			for _, tc := range msg.ToolCalls {
				items = append(items, inputItem{
					Type:      "function_call",
					CallID:    tc.ID,
					Name:      tc.Name,
					Arguments: tc.Arguments,
				})
			}

		case llm.RoleTool:
			items = append(items, inputItem{
				Type:   "function_call_output",
				CallID: msg.ToolCallID,
				Output: msg.Content,
			})
		}
	}

	if len(items) == 1 && items[0].Type == "message" && items[0].Role == "user" {
		ar.Input = items[0].Content
	} else {
		ar.Input = items
	}

	for _, t := range req.Tools {
		ar.Tools = append(ar.Tools, apiTool{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
		})
	}

	return json.Marshal(ar)
}

// --- SSE Stream Parsing ---

type sseEvent struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta,omitempty"`
	Item     json.RawMessage `json:"item,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
}

type responseObject struct {
	ID     string       `json:"id"`
	Output []outputItem `json:"output"`
	Usage  *apiUsage    `json:"usage,omitempty"`
}

type outputItem struct {
	Type    string        `json:"type"`
	ID      string        `json:"id,omitempty"`
	Name    string        `json:"name,omitempty"`
	Content []contentPart `json:"content,omitempty"`
	// For function_call items:
	CallID    string `json:"call_id,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type apiUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

func (p *Provider) parseSSEStream(body io.Reader, cb llm.StreamCallback) (*llm.Response, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	result := &llm.Response{}

	// Accumulate function call arguments by item ID.
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

		var event sseEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			p.logger.Debug("skipping unparseable SSE data", "data", data)
			continue
		}

		switch event.Type {
		case "response.output_text.delta":
			if event.Delta != "" {
				result.Content += event.Delta
				if cb != nil {
					if err := cb(event.Delta); err != nil {
						return nil, fmt.Errorf("responses.Complete: %w: %w", llm.ErrStreamAborted, err)
					}
				}
			}

		case "response.function_call_arguments.delta":
			if len(tools) > 0 {
				tools[len(tools)-1].args.WriteString(event.Delta)
			}

		case "response.output_item.added":
			var item outputItem
			if json.Unmarshal(event.Item, &item) == nil && item.Type == "function_call" {
				tools = append(tools, toolAccum{
					id:   item.CallID,
					name: item.Name,
				})
			}

		case "response.completed":
			var respObj responseObject
			if json.Unmarshal(event.Response, &respObj) == nil {
				if respObj.Usage != nil {
					result.Usage.PromptTokens = respObj.Usage.InputTokens
					result.Usage.CompletionTokens = respObj.Usage.OutputTokens
					result.Usage.TotalTokens = respObj.Usage.TotalTokens
				}
			}

		case "response.failed":
			return nil, fmt.Errorf("responses.Complete: server reported failure")
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("responses.Complete: read stream: %w", err)
	}

	for _, ta := range tools {
		result.ToolCalls = append(result.ToolCalls, llm.ToolCall{
			ID:        ta.id,
			Name:      ta.name,
			Arguments: ta.args.String(),
		})
	}

	if len(result.ToolCalls) > 0 {
		result.StopReason = "tool_use"
	} else {
		result.StopReason = "end"
	}

	return result, nil
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
		return fmt.Errorf("responses.Complete: %w: %s", llm.ErrAuthFailed, msg)
	case 429:
		return fmt.Errorf("responses.Complete: %w: %s", llm.ErrRateLimited, msg)
	case 404:
		return fmt.Errorf("responses.Complete: %w: %s", llm.ErrModelNotFound, msg)
	default:
		if resp.StatusCode >= 500 {
			return fmt.Errorf("responses.Complete: %w: HTTP %d: %s", llm.ErrServerError, resp.StatusCode, msg)
		}
		return fmt.Errorf("responses.Complete: HTTP %d: %s", resp.StatusCode, msg)
	}
}
