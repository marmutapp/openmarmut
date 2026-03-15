package ollama

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
	chatPath = "/api/chat"
)

// Provider implements llm.Provider for the Ollama chat API.
// Ollama uses NDJSON streaming (not SSE) and runs locally by default.
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
	llm.RegisterType("ollama", func(entry llm.ProviderEntry, logger *slog.Logger) (llm.Provider, error) {
		return New(entry, logger)
	})
}

// New creates an Ollama provider from a ProviderEntry.
func New(entry llm.ProviderEntry, logger *slog.Logger) (*Provider, error) {
	if entry.ModelName == "" {
		return nil, fmt.Errorf("ollama.New: model is required")
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

// Complete sends a request to the Ollama /api/chat endpoint and streams the NDJSON response.
func (p *Provider) Complete(ctx context.Context, req llm.Request, cb llm.StreamCallback) (*llm.Response, error) {
	body, err := p.buildRequest(req)
	if err != nil {
		return nil, fmt.Errorf("ollama.Complete: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+chatPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama.Complete: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	llm.ApplyAuth(httpReq, p.auth, p.apiKey)

	for k, v := range p.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama.Complete: %w", err)
	}
	defer resp.Body.Close()

	if err := p.checkError(resp); err != nil {
		return nil, err
	}

	return p.parseNDJSONStream(resp.Body, cb)
}

// --- Request Building ---

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	Tools    []chatTool    `json:"tools,omitempty"`
	Options  *chatOptions  `json:"options,omitempty"`
}

type chatOptions struct {
	Temperature *float64 `json:"temperature,omitempty"`
	NumPredict  *int     `json:"num_predict,omitempty"`
}

type chatMessage struct {
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	Images    []string       `json:"images,omitempty"`
	ToolCalls []chatToolCall `json:"tool_calls,omitempty"`
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
	Function chatFunctionCall `json:"function"`
}

type chatFunctionCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (p *Provider) buildRequest(req llm.Request) ([]byte, error) {
	cr := chatRequest{
		Model:  p.model,
		Stream: true,
	}

	// Build options if temperature or max tokens are set.
	var opts chatOptions
	hasOpts := false

	if req.Temperature != nil {
		opts.Temperature = req.Temperature
		hasOpts = true
	} else if p.defTemp != nil {
		opts.Temperature = p.defTemp
		hasOpts = true
	}

	if req.MaxTokens != nil {
		opts.NumPredict = req.MaxTokens
		hasOpts = true
	} else if p.defMax != nil {
		opts.NumPredict = p.defMax
		hasOpts = true
	}

	if hasOpts {
		cr.Options = &opts
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
			cm := chatMessage{Role: "user", Content: msg.Content}
			for _, img := range msg.Images {
				cm.Images = append(cm.Images, img.Data)
			}
			cr.Messages = append(cr.Messages, cm)

		case llm.RoleAssistant:
			cm := chatMessage{
				Role:    "assistant",
				Content: msg.Content,
			}
			for _, tc := range msg.ToolCalls {
				// Parse arguments JSON string into map for Ollama format.
				var args map[string]any
				if tc.Arguments != "" {
					_ = json.Unmarshal([]byte(tc.Arguments), &args)
				}
				cm.ToolCalls = append(cm.ToolCalls, chatToolCall{
					Function: chatFunctionCall{
						Name:      tc.Name,
						Arguments: args,
					},
				})
			}
			cr.Messages = append(cr.Messages, cm)

		case llm.RoleTool:
			cr.Messages = append(cr.Messages, chatMessage{
				Role:    "tool",
				Content: msg.Content,
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

// --- NDJSON Stream Parsing ---

type streamChunk struct {
	Message         streamMessage `json:"message"`
	Done            bool          `json:"done"`
	PromptEvalCount int           `json:"prompt_eval_count,omitempty"`
	EvalCount       int           `json:"eval_count,omitempty"`
}

type streamMessage struct {
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	ToolCalls []chatToolCall `json:"tool_calls,omitempty"`
}

func (p *Provider) parseNDJSONStream(body io.Reader, cb llm.StreamCallback) (*llm.Response, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	result := &llm.Response{}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			p.logger.Debug("skipping unparseable NDJSON line", "line", line)
			continue
		}

		// Handle tool calls (come in a single chunk).
		if len(chunk.Message.ToolCalls) > 0 {
			for _, tc := range chunk.Message.ToolCalls {
				argsJSON, _ := json.Marshal(tc.Function.Arguments)
				result.ToolCalls = append(result.ToolCalls, llm.ToolCall{
					ID:        fmt.Sprintf("call_%s_%d", tc.Function.Name, len(result.ToolCalls)),
					Name:      tc.Function.Name,
					Arguments: string(argsJSON),
				})
			}
			result.StopReason = "tool_use"
		}

		// Text content.
		if chunk.Message.Content != "" {
			result.Content += chunk.Message.Content
			if cb != nil {
				if err := cb(chunk.Message.Content); err != nil {
					return nil, fmt.Errorf("ollama.Complete: %w: %w", llm.ErrStreamAborted, err)
				}
			}
		}

		// Final chunk with token counts.
		if chunk.Done {
			result.Usage.PromptTokens = chunk.PromptEvalCount
			result.Usage.CompletionTokens = chunk.EvalCount
			result.Usage.TotalTokens = chunk.PromptEvalCount + chunk.EvalCount

			if result.StopReason == "" {
				result.StopReason = "end"
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("ollama.Complete: read stream: %w", err)
	}

	return result, nil
}

// --- Error Handling ---

type apiErrorResponse struct {
	Error string `json:"error"`
}

func (p *Provider) checkError(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	body, _ := io.ReadAll(resp.Body)

	var ae apiErrorResponse
	_ = json.Unmarshal(body, &ae)
	msg := ae.Error
	if msg == "" {
		msg = string(body)
	}

	switch resp.StatusCode {
	case 401:
		return fmt.Errorf("ollama.Complete: %w: %s", llm.ErrAuthFailed, msg)
	case 429:
		return fmt.Errorf("ollama.Complete: %w: %s", llm.ErrRateLimited, msg)
	case 404:
		return fmt.Errorf("ollama.Complete: %w: %s", llm.ErrModelNotFound, msg)
	default:
		if resp.StatusCode >= 500 {
			return fmt.Errorf("ollama.Complete: %w: HTTP %d: %s", llm.ErrServerError, resp.StatusCode, msg)
		}
		return fmt.Errorf("ollama.Complete: HTTP %d: %s", resp.StatusCode, msg)
	}
}
