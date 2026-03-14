package gemini

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

// Provider implements llm.Provider for the Google Gemini API.
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
	llm.RegisterType("gemini", func(entry llm.ProviderEntry, logger *slog.Logger) (llm.Provider, error) {
		return New(entry, logger)
	})
}

// New creates a Gemini provider from a ProviderEntry.
func New(entry llm.ProviderEntry, logger *slog.Logger) (*Provider, error) {
	if entry.ModelName == "" {
		return nil, fmt.Errorf("gemini.New: model is required")
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

// Complete sends a request to the Gemini API and streams the response.
func (p *Provider) Complete(ctx context.Context, req llm.Request, cb llm.StreamCallback) (*llm.Response, error) {
	body, err := p.buildRequest(req)
	if err != nil {
		return nil, fmt.Errorf("gemini.Complete: %w", err)
	}

	// Streaming endpoint: :streamGenerateContent?alt=sse
	url := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent?alt=sse", p.baseURL, p.model)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("gemini.Complete: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	llm.ApplyAuth(httpReq, p.auth, p.apiKey)

	for k, v := range p.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini.Complete: %w", err)
	}
	defer resp.Body.Close()

	if err := p.checkError(resp); err != nil {
		return nil, err
	}

	return p.parseSSEStream(resp.Body, cb)
}

// --- Request Building ---

type apiRequest struct {
	Contents          []content        `json:"contents"`
	SystemInstruction *systemInstr     `json:"systemInstruction,omitempty"`
	GenerationConfig  *generationCfg   `json:"generationConfig,omitempty"`
	Tools             []toolDecls      `json:"tools,omitempty"`
}

type content struct {
	Role  string `json:"role"`
	Parts []part `json:"parts"`
}

type part struct {
	Text             string            `json:"text,omitempty"`
	FunctionCall     *functionCall     `json:"functionCall,omitempty"`
	FunctionResponse *functionResponse `json:"functionResponse,omitempty"`
}

type functionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type functionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type systemInstr struct {
	Parts []part `json:"parts"`
}

type generationCfg struct {
	Temperature    *float64 `json:"temperature,omitempty"`
	MaxOutputTokens *int    `json:"maxOutputTokens,omitempty"`
}

type toolDecls struct {
	FunctionDeclarations []funcDecl `json:"functionDeclarations"`
}

type funcDecl struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

func (p *Provider) buildRequest(req llm.Request) ([]byte, error) {
	ar := apiRequest{}

	// Generation config.
	var gc generationCfg
	hasGC := false
	if req.Temperature != nil {
		gc.Temperature = req.Temperature
		hasGC = true
	} else if p.defTemp != nil {
		gc.Temperature = p.defTemp
		hasGC = true
	}
	if req.MaxTokens != nil {
		gc.MaxOutputTokens = req.MaxTokens
		hasGC = true
	} else if p.defMax != nil {
		gc.MaxOutputTokens = p.defMax
		hasGC = true
	}
	if hasGC {
		ar.GenerationConfig = &gc
	}

	// Convert messages.
	for _, msg := range req.Messages {
		switch msg.Role {
		case llm.RoleSystem:
			ar.SystemInstruction = &systemInstr{
				Parts: []part{{Text: msg.Content}},
			}

		case llm.RoleUser:
			ar.Contents = append(ar.Contents, content{
				Role:  "user",
				Parts: []part{{Text: msg.Content}},
			})

		case llm.RoleAssistant:
			c := content{Role: "model"}
			if msg.Content != "" {
				c.Parts = append(c.Parts, part{Text: msg.Content})
			}
			for _, tc := range msg.ToolCalls {
				var args map[string]any
				_ = json.Unmarshal([]byte(tc.Arguments), &args)
				c.Parts = append(c.Parts, part{
					FunctionCall: &functionCall{Name: tc.Name, Args: args},
				})
			}
			ar.Contents = append(ar.Contents, c)

		case llm.RoleTool:
			// Tool results are user messages with functionResponse parts.
			var respData map[string]any
			_ = json.Unmarshal([]byte(msg.Content), &respData)
			if respData == nil {
				respData = map[string]any{"result": msg.Content}
			}

			// Try to merge with previous user message containing functionResponse.
			if len(ar.Contents) > 0 {
				last := &ar.Contents[len(ar.Contents)-1]
				if last.Role == "user" && len(last.Parts) > 0 && last.Parts[0].FunctionResponse != nil {
					last.Parts = append(last.Parts, part{
						FunctionResponse: &functionResponse{
							Name:     msg.ToolCallID,
							Response: respData,
						},
					})
					continue
				}
			}

			ar.Contents = append(ar.Contents, content{
				Role: "user",
				Parts: []part{{
					FunctionResponse: &functionResponse{
						Name:     msg.ToolCallID,
						Response: respData,
					},
				}},
			})
		}
	}

	// Convert tools.
	if len(req.Tools) > 0 {
		var decls []funcDecl
		for _, t := range req.Tools {
			decls = append(decls, funcDecl{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			})
		}
		ar.Tools = []toolDecls{{FunctionDeclarations: decls}}
	}

	return json.Marshal(ar)
}

// --- SSE Stream Parsing ---

type apiResponse struct {
	Candidates    []candidate    `json:"candidates"`
	UsageMetadata *usageMetadata `json:"usageMetadata,omitempty"`
}

type candidate struct {
	Content      content `json:"content"`
	FinishReason string  `json:"finishReason,omitempty"`
}

type usageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

func (p *Provider) parseSSEStream(body io.Reader, cb llm.StreamCallback) (*llm.Response, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	result := &llm.Response{}

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var chunk apiResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			p.logger.Debug("skipping unparseable SSE data", "data", data)
			continue
		}

		if chunk.UsageMetadata != nil {
			result.Usage.PromptTokens = chunk.UsageMetadata.PromptTokenCount
			result.Usage.CompletionTokens = chunk.UsageMetadata.CandidatesTokenCount
			result.Usage.TotalTokens = chunk.UsageMetadata.TotalTokenCount
		}

		if len(chunk.Candidates) == 0 {
			continue
		}

		cand := chunk.Candidates[0]

		for _, p2 := range cand.Content.Parts {
			if p2.Text != "" {
				result.Content += p2.Text
				if cb != nil {
					if err := cb(p2.Text); err != nil {
						return nil, fmt.Errorf("gemini.Complete: %w: %w", llm.ErrStreamAborted, err)
					}
				}
			}
			if p2.FunctionCall != nil {
				argsJSON, _ := json.Marshal(p2.FunctionCall.Args)
				result.ToolCalls = append(result.ToolCalls, llm.ToolCall{
					ID:        fmt.Sprintf("gemini_%s_%d", p2.FunctionCall.Name, len(result.ToolCalls)),
					Name:      p2.FunctionCall.Name,
					Arguments: string(argsJSON),
				})
			}
		}

		if cand.FinishReason != "" {
			result.StopReason = mapFinishReason(cand.FinishReason)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("gemini.Complete: read stream: %w", err)
	}

	// If we got tool calls, override stop reason regardless of finish reason.
	if len(result.ToolCalls) > 0 {
		result.StopReason = "tool_use"
	}
	if result.StopReason == "" {
		result.StopReason = "end"
	}

	return result, nil
}

func mapFinishReason(reason string) string {
	switch reason {
	case "STOP":
		return "end"
	case "MAX_TOKENS":
		return "max_tokens"
	default:
		return reason
	}
}

// --- Error Handling ---

type apiError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
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
	case 401, 403:
		return fmt.Errorf("gemini.Complete: %w: %s", llm.ErrAuthFailed, msg)
	case 429:
		return fmt.Errorf("gemini.Complete: %w: %s", llm.ErrRateLimited, msg)
	case 404:
		return fmt.Errorf("gemini.Complete: %w: %s", llm.ErrModelNotFound, msg)
	default:
		if resp.StatusCode >= 500 {
			return fmt.Errorf("gemini.Complete: %w: HTTP %d: %s", llm.ErrServerError, resp.StatusCode, msg)
		}
		return fmt.Errorf("gemini.Complete: HTTP %d: %s", resp.StatusCode, msg)
	}
}
