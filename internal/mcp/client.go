package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Sentinel errors.
var (
	ErrNotConnected    = errors.New("mcp: not connected")
	ErrConnectFailed   = errors.New("mcp: connection failed")
	ErrInitFailed      = errors.New("mcp: initialization failed")
	ErrToolNotFound    = errors.New("mcp: tool not found")
	ErrToolCallFailed  = errors.New("mcp: tool call failed")
	ErrTransportClosed = errors.New("mcp: transport closed")
)

// MCPTool describes a tool exposed by an MCP server.
type MCPTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"` // JSON Schema for parameters
}

// MCPClient connects to a single MCP server.
type MCPClient struct {
	Name      string   // User-assigned name (e.g., "github").
	ServerURL string   // For SSE transport: base URL.
	Transport string   // "sse" or "stdio".
	Command   string   // For stdio: command to run.
	Args      []string // For stdio: command arguments.
	Env       []string // For stdio: extra environment variables.

	mu        sync.Mutex
	tools     []MCPTool
	connected bool
	transport mcpTransport
	reqID     atomic.Int64
}

// MCPServerConfig is the YAML-serializable configuration for an MCP server.
type MCPServerConfig struct {
	Name      string   `yaml:"name"`
	Transport string   `yaml:"transport"` // "sse" or "stdio"
	URL       string   `yaml:"url"`       // For SSE transport.
	Command   string   `yaml:"command"`   // For stdio transport.
	Args      []string `yaml:"args"`      // For stdio transport.
	Env       []string `yaml:"env"`       // Extra environment variables.
}

// NewMCPClient creates a new MCP client from config.
func NewMCPClient(cfg MCPServerConfig) (*MCPClient, error) {
	if cfg.Name == "" {
		return nil, fmt.Errorf("mcp.NewMCPClient: name is required")
	}
	if cfg.Transport != "sse" && cfg.Transport != "stdio" {
		return nil, fmt.Errorf("mcp.NewMCPClient(%s): transport must be \"sse\" or \"stdio\", got %q", cfg.Name, cfg.Transport)
	}
	if cfg.Transport == "sse" && cfg.URL == "" {
		return nil, fmt.Errorf("mcp.NewMCPClient(%s): url is required for SSE transport", cfg.Name)
	}
	if cfg.Transport == "stdio" && cfg.Command == "" {
		return nil, fmt.Errorf("mcp.NewMCPClient(%s): command is required for stdio transport", cfg.Name)
	}

	return &MCPClient{
		Name:      cfg.Name,
		ServerURL: cfg.URL,
		Transport: cfg.Transport,
		Command:   cfg.Command,
		Args:      cfg.Args,
		Env:       cfg.Env,
	}, nil
}

// Connect establishes the connection and performs the MCP initialization handshake.
func (c *MCPClient) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.connected {
		return nil
	}

	var t mcpTransport
	var err error
	switch c.Transport {
	case "sse":
		t, err = newSSETransport(ctx, c.ServerURL)
	case "stdio":
		t, err = newStdioTransport(ctx, c.Command, c.Args, c.Env)
	default:
		return fmt.Errorf("mcp.Connect(%s): unknown transport %q", c.Name, c.Transport)
	}
	if err != nil {
		return fmt.Errorf("mcp.Connect(%s): %w: %w", c.Name, ErrConnectFailed, err)
	}

	c.transport = t

	// Send initialize request.
	initResult, err := c.sendRequest(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "openmarmut",
			"version": "1.0.0",
		},
	})
	if err != nil {
		t.Close()
		return fmt.Errorf("mcp.Connect(%s): %w: %w", c.Name, ErrInitFailed, err)
	}

	// Verify server responded with valid result.
	if initResult == nil {
		t.Close()
		return fmt.Errorf("mcp.Connect(%s): %w: empty init response", c.Name, ErrInitFailed)
	}

	// Send initialized notification (no response expected).
	if err := c.sendNotification("initialized", nil); err != nil {
		t.Close()
		return fmt.Errorf("mcp.Connect(%s): initialized notification: %w", c.Name, err)
	}

	c.connected = true
	return nil
}

// ListTools discovers available tools from the MCP server.
func (c *MCPClient) ListTools(ctx context.Context) ([]MCPTool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return nil, fmt.Errorf("mcp.ListTools(%s): %w", c.Name, ErrNotConnected)
	}

	result, err := c.sendRequest(ctx, "tools/list", nil)
	if err != nil {
		return nil, fmt.Errorf("mcp.ListTools(%s): %w", c.Name, err)
	}

	var resp struct {
		Tools []MCPTool `json:"tools"`
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("mcp.ListTools(%s): marshal result: %w", c.Name, err)
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("mcp.ListTools(%s): parse tools: %w", c.Name, err)
	}

	c.tools = resp.Tools
	return resp.Tools, nil
}

// CallTool invokes a tool on the MCP server.
func (c *MCPClient) CallTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return "", fmt.Errorf("mcp.CallTool(%s, %s): %w", c.Name, name, ErrNotConnected)
	}

	params := map[string]any{
		"name": name,
	}
	if args != nil && string(args) != "null" && string(args) != "{}" {
		var parsed any
		if err := json.Unmarshal(args, &parsed); err != nil {
			return "", fmt.Errorf("mcp.CallTool(%s, %s): parse args: %w", c.Name, name, err)
		}
		params["arguments"] = parsed
	}

	result, err := c.sendRequest(ctx, "tools/call", params)
	if err != nil {
		return "", fmt.Errorf("mcp.CallTool(%s, %s): %w: %w", c.Name, name, ErrToolCallFailed, err)
	}

	// Parse the result — MCP returns { content: [{type: "text", text: "..."}] }
	raw, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("mcp.CallTool(%s, %s): marshal result: %w", c.Name, name, err)
	}

	var callResult struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &callResult); err != nil {
		// Fall back to raw JSON if not standard format.
		return string(raw), nil
	}

	var texts []string
	for _, c := range callResult.Content {
		if c.Type == "text" || c.Type == "" {
			texts = append(texts, c.Text)
		}
	}

	output := strings.Join(texts, "\n")
	if callResult.IsError {
		return "", fmt.Errorf("mcp.CallTool(%s, %s): server error: %s", c.Name, name, output)
	}

	if output == "" {
		return string(raw), nil
	}
	return output, nil
}

// Tools returns the cached list of discovered tools.
func (c *MCPClient) Tools() []MCPTool {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]MCPTool, len(c.tools))
	copy(cp, c.tools)
	return cp
}

// Connected returns whether the client is connected.
func (c *MCPClient) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// Close disconnects from the MCP server.
func (c *MCPClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return nil
	}
	c.connected = false
	c.tools = nil
	if c.transport != nil {
		return c.transport.Close()
	}
	return nil
}

// --- JSON-RPC 2.0 protocol ---

type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *jsonRPCError) Error() string {
	return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message)
}

func (c *MCPClient) sendRequest(ctx context.Context, method string, params any) (any, error) {
	id := c.reqID.Add(1)
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	resp, err := c.transport.Send(ctx, req)
	if err != nil {
		return nil, err
	}

	if resp.Error != nil {
		return nil, resp.Error
	}

	var result any
	if resp.Result != nil {
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			return nil, fmt.Errorf("unmarshal result: %w", err)
		}
	}
	return result, nil
}

func (c *MCPClient) sendNotification(method string, params any) error {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	return c.transport.SendNotification(req)
}

// --- Transport abstraction ---

type mcpTransport interface {
	Send(ctx context.Context, req jsonRPCRequest) (*jsonRPCResponse, error)
	SendNotification(req jsonRPCRequest) error
	Close() error
}

// --- SSE Transport ---

type sseTransport struct {
	baseURL    string
	messageURL string
	client     *http.Client
	cancel     context.CancelFunc
	mu         sync.Mutex
	pending    map[int64]chan *jsonRPCResponse
}

func newSSETransport(ctx context.Context, baseURL string) (*sseTransport, error) {
	t := &sseTransport{
		baseURL: baseURL,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		pending: make(map[int64]chan *jsonRPCResponse),
	}

	// Connect to SSE endpoint and discover the message endpoint.
	sseCtx, cancel := context.WithCancel(ctx)
	t.cancel = cancel

	msgURLCh := make(chan string, 1)
	errCh := make(chan error, 1)

	go func() {
		req, err := http.NewRequestWithContext(sseCtx, http.MethodGet, baseURL, nil)
		if err != nil {
			errCh <- err
			return
		}
		req.Header.Set("Accept", "text/event-stream")

		resp, err := t.client.Do(req)
		if err != nil {
			errCh <- err
			return
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			errCh <- fmt.Errorf("SSE connect: status %d", resp.StatusCode)
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		foundEndpoint := false
		var eventType string

		for scanner.Scan() {
			line := scanner.Text()

			if strings.HasPrefix(line, "event:") {
				eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
				continue
			}

			if strings.HasPrefix(line, "data:") {
				data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))

				if eventType == "endpoint" || !foundEndpoint {
					// First data line or endpoint event: this is the message URL.
					if !foundEndpoint {
						messageURL := data
						// If relative URL, resolve against baseURL.
						if strings.HasPrefix(messageURL, "/") {
							// Extract scheme+host from baseURL.
							parts := strings.SplitN(baseURL, "://", 2)
							if len(parts) == 2 {
								hostPart := parts[1]
								slashIdx := strings.Index(hostPart, "/")
								if slashIdx > 0 {
									hostPart = hostPart[:slashIdx]
								}
								messageURL = parts[0] + "://" + hostPart + messageURL
							}
						}
						msgURLCh <- messageURL
						foundEndpoint = true
					}
					eventType = ""
					continue
				}

				if eventType == "message" || foundEndpoint {
					// Parse JSON-RPC response.
					var resp jsonRPCResponse
					if err := json.Unmarshal([]byte(data), &resp); err == nil && resp.ID > 0 {
						t.mu.Lock()
						ch, ok := t.pending[resp.ID]
						t.mu.Unlock()
						if ok {
							ch <- &resp
						}
					}
				}
				eventType = ""
			}
		}
		resp.Body.Close()
	}()

	// Wait for message URL or error or timeout.
	select {
	case msgURL := <-msgURLCh:
		t.messageURL = msgURL
		return t, nil
	case err := <-errCh:
		cancel()
		return nil, err
	case <-time.After(10 * time.Second):
		cancel()
		return nil, fmt.Errorf("SSE: timeout waiting for endpoint")
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}
}

func (t *sseTransport) Send(ctx context.Context, req jsonRPCRequest) (*jsonRPCResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("sseTransport.Send: marshal: %w", err)
	}

	ch := make(chan *jsonRPCResponse, 1)
	t.mu.Lock()
	t.pending[req.ID] = ch
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		delete(t.pending, req.ID)
		t.mu.Unlock()
	}()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.messageURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("sseTransport.Send: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sseTransport.Send: HTTP: %w", err)
	}
	httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK && httpResp.StatusCode != http.StatusAccepted && httpResp.StatusCode != http.StatusNoContent {
		return nil, fmt.Errorf("sseTransport.Send: status %d", httpResp.StatusCode)
	}

	// Wait for response via SSE.
	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("sseTransport.Send: timeout")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *sseTransport) SendNotification(req jsonRPCRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("sseTransport.SendNotification: marshal: %w", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, t.messageURL, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("sseTransport.SendNotification: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("sseTransport.SendNotification: HTTP: %w", err)
	}
	resp.Body.Close()
	return nil
}

func (t *sseTransport) Close() error {
	if t.cancel != nil {
		t.cancel()
	}
	return nil
}

// --- Stdio Transport ---

type stdioTransport struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	scanner *bufio.Scanner
	mu      sync.Mutex
}

func newStdioTransport(_ context.Context, command string, args, env []string) (*stdioTransport, error) {
	cmd := exec.Command(command, args...)
	if len(env) > 0 {
		cmd.Env = append(cmd.Environ(), env...)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdioTransport: stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("stdioTransport: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("stdioTransport: start %s: %w", command, err)
	}

	return &stdioTransport{
		cmd:     cmd,
		stdin:   stdin,
		scanner: bufio.NewScanner(stdout),
	}, nil
}

func (t *stdioTransport) Send(_ context.Context, req jsonRPCRequest) (*jsonRPCResponse, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("stdioTransport.Send: marshal: %w", err)
	}
	body = append(body, '\n')

	if _, err := t.stdin.Write(body); err != nil {
		return nil, fmt.Errorf("stdioTransport.Send: write: %w", err)
	}

	// Read response lines until we get one matching our request ID.
	for t.scanner.Scan() {
		line := strings.TrimSpace(t.scanner.Text())
		if line == "" {
			continue
		}
		var resp jsonRPCResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			continue // Skip non-JSON lines.
		}
		if resp.ID == req.ID {
			return &resp, nil
		}
		// Not our response — could be a notification. Skip it.
	}

	if err := t.scanner.Err(); err != nil {
		return nil, fmt.Errorf("stdioTransport.Send: read: %w", err)
	}
	return nil, ErrTransportClosed
}

func (t *stdioTransport) SendNotification(req jsonRPCRequest) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("stdioTransport.SendNotification: marshal: %w", err)
	}
	body = append(body, '\n')

	if _, err := t.stdin.Write(body); err != nil {
		return fmt.Errorf("stdioTransport.SendNotification: write: %w", err)
	}
	return nil
}

func (t *stdioTransport) Close() error {
	t.stdin.Close()
	return t.cmd.Process.Kill()
}

// --- Manager for multiple MCP clients ---

// Manager manages connections to multiple MCP servers.
type Manager struct {
	mu      sync.Mutex
	clients map[string]*MCPClient
}

// NewManager creates a new MCP manager.
func NewManager() *Manager {
	return &Manager{
		clients: make(map[string]*MCPClient),
	}
}

// ConnectAll connects to all configured MCP servers.
// Returns errors for servers that failed to connect, but continues connecting others.
func (m *Manager) ConnectAll(ctx context.Context, configs []MCPServerConfig) []error {
	var errs []error
	for _, cfg := range configs {
		client, err := NewMCPClient(cfg)
		if err != nil {
			errs = append(errs, err)
			continue
		}

		if err := client.Connect(ctx); err != nil {
			errs = append(errs, err)
			continue
		}

		// Discover tools.
		if _, err := client.ListTools(ctx); err != nil {
			errs = append(errs, fmt.Errorf("mcp.Manager: %s: list tools: %w", cfg.Name, err))
			client.Close()
			continue
		}

		m.mu.Lock()
		m.clients[cfg.Name] = client
		m.mu.Unlock()
	}
	return errs
}

// Client returns a connected MCP client by name.
func (m *Manager) Client(name string) *MCPClient {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.clients[name]
}

// AllTools returns all tools from all connected servers, prefixed with server name.
// Format: "mcp_<server>_<tool>".
func (m *Manager) AllTools() map[string]struct {
	Client *MCPClient
	Tool   MCPTool
} {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make(map[string]struct {
		Client *MCPClient
		Tool   MCPTool
	})
	for _, client := range m.clients {
		for _, tool := range client.Tools() {
			prefixedName := fmt.Sprintf("mcp_%s_%s", client.Name, tool.Name)
			result[prefixedName] = struct {
				Client *MCPClient
				Tool   MCPTool
			}{Client: client, Tool: tool}
		}
	}
	return result
}

// Clients returns all connected clients.
func (m *Manager) Clients() []*MCPClient {
	m.mu.Lock()
	defer m.mu.Unlock()
	var clients []*MCPClient
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	return clients
}

// CloseAll disconnects all clients.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.clients {
		c.Close()
	}
	m.clients = make(map[string]*MCPClient)
}
