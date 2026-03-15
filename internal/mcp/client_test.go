package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockMCPServer creates an httptest server that implements a minimal MCP SSE server.
func mockMCPServer(t *testing.T) *httptest.Server {
	t.Helper()

	var mu sync.Mutex
	sseClients := make(map[int]chan []byte)
	clientID := 0

	mux := http.NewServeMux()

	// SSE endpoint — client connects here and receives events.
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		mu.Lock()
		id := clientID
		clientID++
		ch := make(chan []byte, 10)
		sseClients[id] = ch
		mu.Unlock()

		// Send endpoint event.
		fmt.Fprintf(w, "event: endpoint\ndata: /message\n\n")
		flusher.Flush()

		// Send responses via SSE.
		for {
			select {
			case data := <-ch:
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(data))
				flusher.Flush()
			case <-r.Context().Done():
				mu.Lock()
				delete(sseClients, id)
				mu.Unlock()
				return
			}
		}
	})

	// Message endpoint — client sends JSON-RPC requests here.
	mux.HandleFunc("/message", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "parse json", http.StatusBadRequest)
			return
		}

		// Handle different methods.
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "test-server",
					"version": "1.0.0",
				},
			}
		case "tools/list":
			result = map[string]any{
				"tools": []map[string]any{
					{
						"name":        "get_weather",
						"description": "Get current weather for a location",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"location": map[string]any{
									"type":        "string",
									"description": "City name",
								},
							},
							"required": []string{"location"},
						},
					},
					{
						"name":        "search",
						"description": "Search for information",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"query": map[string]any{
									"type":        "string",
									"description": "Search query",
								},
							},
							"required": []string{"query"},
						},
					},
				},
			}
		case "tools/call":
			params, _ := json.Marshal(req.Params)
			var callParams struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(params, &callParams)

			switch callParams.Name {
			case "get_weather":
				loc := "unknown"
				if l, ok := callParams.Arguments["location"]; ok {
					loc = fmt.Sprintf("%v", l)
				}
				result = map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": fmt.Sprintf("Weather in %s: Sunny, 22°C", loc)},
					},
				}
			case "error_tool":
				result = map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": "Something went wrong"},
					},
					"isError": true,
				}
			default:
				result = map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": "OK"},
					},
				}
			}
		case "initialized":
			// Notification — no response needed.
			w.WriteHeader(http.StatusNoContent)
			return
		default:
			w.WriteHeader(http.StatusOK)
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &jsonRPCError{
					Code:    -32601,
					Message: "Method not found",
				},
			}
			respBytes, _ := json.Marshal(resp)
			// Broadcast to all SSE clients.
			mu.Lock()
			for _, ch := range sseClients {
				ch <- respBytes
			}
			mu.Unlock()
			return
		}

		resp := jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
		}
		resultBytes, _ := json.Marshal(result)
		resp.Result = resultBytes

		respBytes, _ := json.Marshal(resp)

		// Send response via SSE.
		mu.Lock()
		for _, ch := range sseClients {
			ch <- respBytes
		}
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
	})

	return httptest.NewServer(mux)
}

func TestNewMCPClient_Valid(t *testing.T) {
	client, err := NewMCPClient(MCPServerConfig{
		Name:      "test",
		Transport: "sse",
		URL:       "http://localhost:3001/sse",
	})
	require.NoError(t, err)
	assert.Equal(t, "test", client.Name)
	assert.Equal(t, "sse", client.Transport)
	assert.Equal(t, "http://localhost:3001/sse", client.ServerURL)
	assert.False(t, client.Connected())
}

func TestNewMCPClient_Stdio(t *testing.T) {
	client, err := NewMCPClient(MCPServerConfig{
		Name:      "local",
		Transport: "stdio",
		Command:   "echo",
		Args:      []string{"hello"},
	})
	require.NoError(t, err)
	assert.Equal(t, "local", client.Name)
	assert.Equal(t, "stdio", client.Transport)
}

func TestNewMCPClient_MissingName(t *testing.T) {
	_, err := NewMCPClient(MCPServerConfig{
		Transport: "sse",
		URL:       "http://localhost:3001/sse",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestNewMCPClient_InvalidTransport(t *testing.T) {
	_, err := NewMCPClient(MCPServerConfig{
		Name:      "test",
		Transport: "websocket",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transport must be")
}

func TestNewMCPClient_SSEMissingURL(t *testing.T) {
	_, err := NewMCPClient(MCPServerConfig{
		Name:      "test",
		Transport: "sse",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "url is required")
}

func TestNewMCPClient_StdioMissingCommand(t *testing.T) {
	_, err := NewMCPClient(MCPServerConfig{
		Name:      "test",
		Transport: "stdio",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "command is required")
}

func TestSSEConnect_ListTools(t *testing.T) {
	server := mockMCPServer(t)
	defer server.Close()

	client, err := NewMCPClient(MCPServerConfig{
		Name:      "test",
		Transport: "sse",
		URL:       server.URL + "/sse",
	})
	require.NoError(t, err)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.Connect(ctx)
	require.NoError(t, err)
	assert.True(t, client.Connected())

	tools, err := client.ListTools(ctx)
	require.NoError(t, err)
	assert.Len(t, tools, 2)

	assert.Equal(t, "get_weather", tools[0].Name)
	assert.Equal(t, "Get current weather for a location", tools[0].Description)
	assert.NotNil(t, tools[0].InputSchema)

	assert.Equal(t, "search", tools[1].Name)
}

func TestSSECallTool(t *testing.T) {
	server := mockMCPServer(t)
	defer server.Close()

	client, err := NewMCPClient(MCPServerConfig{
		Name:      "test",
		Transport: "sse",
		URL:       server.URL + "/sse",
	})
	require.NoError(t, err)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, client.Connect(ctx))
	_, err = client.ListTools(ctx)
	require.NoError(t, err)

	result, err := client.CallTool(ctx, "get_weather", json.RawMessage(`{"location":"Tokyo"}`))
	require.NoError(t, err)
	assert.Contains(t, result, "Tokyo")
	assert.Contains(t, result, "Sunny")
}

func TestSSECallTool_Error(t *testing.T) {
	server := mockMCPServer(t)
	defer server.Close()

	client, err := NewMCPClient(MCPServerConfig{
		Name:      "test",
		Transport: "sse",
		URL:       server.URL + "/sse",
	})
	require.NoError(t, err)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, client.Connect(ctx))

	_, err = client.CallTool(ctx, "error_tool", json.RawMessage(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server error")
}

func TestCallTool_NotConnected(t *testing.T) {
	client, err := NewMCPClient(MCPServerConfig{
		Name:      "test",
		Transport: "sse",
		URL:       "http://localhost:9999/sse",
	})
	require.NoError(t, err)

	_, err = client.CallTool(context.Background(), "test", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotConnected)
}

func TestListTools_NotConnected(t *testing.T) {
	client, err := NewMCPClient(MCPServerConfig{
		Name:      "test",
		Transport: "sse",
		URL:       "http://localhost:9999/sse",
	})
	require.NoError(t, err)

	_, err = client.ListTools(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotConnected)
}

func TestClose(t *testing.T) {
	server := mockMCPServer(t)
	defer server.Close()

	client, err := NewMCPClient(MCPServerConfig{
		Name:      "test",
		Transport: "sse",
		URL:       server.URL + "/sse",
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, client.Connect(ctx))
	assert.True(t, client.Connected())

	require.NoError(t, client.Close())
	assert.False(t, client.Connected())
	assert.Empty(t, client.Tools())
}

func TestClose_NotConnected(t *testing.T) {
	client, err := NewMCPClient(MCPServerConfig{
		Name:      "test",
		Transport: "sse",
		URL:       "http://localhost:9999/sse",
	})
	require.NoError(t, err)

	// Close should not error when not connected.
	require.NoError(t, client.Close())
}

func TestTools_Cached(t *testing.T) {
	server := mockMCPServer(t)
	defer server.Close()

	client, err := NewMCPClient(MCPServerConfig{
		Name:      "test",
		Transport: "sse",
		URL:       server.URL + "/sse",
	})
	require.NoError(t, err)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, client.Connect(ctx))
	_, err = client.ListTools(ctx)
	require.NoError(t, err)

	// Tools() returns cached copy.
	tools := client.Tools()
	assert.Len(t, tools, 2)

	// Modifying the copy shouldn't affect cached tools.
	tools[0].Name = "modified"
	assert.Equal(t, "get_weather", client.Tools()[0].Name)
}

func TestManager_ConnectAll(t *testing.T) {
	server := mockMCPServer(t)
	defer server.Close()

	mgr := NewManager()
	errs := mgr.ConnectAll(context.Background(), []MCPServerConfig{
		{
			Name:      "server1",
			Transport: "sse",
			URL:       server.URL + "/sse",
		},
	})
	assert.Empty(t, errs)

	client := mgr.Client("server1")
	require.NotNil(t, client)
	assert.True(t, client.Connected())
	assert.Len(t, client.Tools(), 2)

	mgr.CloseAll()
	assert.Nil(t, mgr.Client("server1"))
}

func TestManager_AllTools(t *testing.T) {
	server := mockMCPServer(t)
	defer server.Close()

	mgr := NewManager()
	errs := mgr.ConnectAll(context.Background(), []MCPServerConfig{
		{
			Name:      "myserver",
			Transport: "sse",
			URL:       server.URL + "/sse",
		},
	})
	assert.Empty(t, errs)
	defer mgr.CloseAll()

	allTools := mgr.AllTools()
	assert.Len(t, allTools, 2)

	// Check prefixed names.
	_, hasWeather := allTools["mcp_myserver_get_weather"]
	_, hasSearch := allTools["mcp_myserver_search"]
	assert.True(t, hasWeather)
	assert.True(t, hasSearch)
}

func TestManager_ConnectAll_BadServer(t *testing.T) {
	mgr := NewManager()
	errs := mgr.ConnectAll(context.Background(), []MCPServerConfig{
		{
			Name:      "bad",
			Transport: "sse",
			URL:       "http://localhost:1/sse",
		},
	})
	assert.NotEmpty(t, errs)
	assert.Nil(t, mgr.Client("bad"))
}

func TestManager_ConnectAll_InvalidConfig(t *testing.T) {
	mgr := NewManager()
	errs := mgr.ConnectAll(context.Background(), []MCPServerConfig{
		{
			Name:      "",
			Transport: "sse",
			URL:       "http://localhost/sse",
		},
	})
	assert.NotEmpty(t, errs)
}

func TestManager_Clients(t *testing.T) {
	server := mockMCPServer(t)
	defer server.Close()

	mgr := NewManager()
	errs := mgr.ConnectAll(context.Background(), []MCPServerConfig{
		{
			Name:      "s1",
			Transport: "sse",
			URL:       server.URL + "/sse",
		},
	})
	assert.Empty(t, errs)
	defer mgr.CloseAll()

	clients := mgr.Clients()
	assert.Len(t, clients, 1)
	assert.Equal(t, "s1", clients[0].Name)
}

func TestConnect_AlreadyConnected(t *testing.T) {
	server := mockMCPServer(t)
	defer server.Close()

	client, err := NewMCPClient(MCPServerConfig{
		Name:      "test",
		Transport: "sse",
		URL:       server.URL + "/sse",
	})
	require.NoError(t, err)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, client.Connect(ctx))
	// Second connect should be a no-op.
	require.NoError(t, client.Connect(ctx))
	assert.True(t, client.Connected())
}

func TestCallTool_NullArgs(t *testing.T) {
	server := mockMCPServer(t)
	defer server.Close()

	client, err := NewMCPClient(MCPServerConfig{
		Name:      "test",
		Transport: "sse",
		URL:       server.URL + "/sse",
	})
	require.NoError(t, err)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, client.Connect(ctx))

	// Call with nil args.
	result, err := client.CallTool(ctx, "search", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, result)
}

func TestCallTool_EmptyArgs(t *testing.T) {
	server := mockMCPServer(t)
	defer server.Close()

	client, err := NewMCPClient(MCPServerConfig{
		Name:      "test",
		Transport: "sse",
		URL:       server.URL + "/sse",
	})
	require.NoError(t, err)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, client.Connect(ctx))

	// Call with empty JSON object.
	result, err := client.CallTool(ctx, "search", json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.NotEmpty(t, result)
}

// --- JSON-RPC error test ---

func TestJSONRPCError_String(t *testing.T) {
	err := &jsonRPCError{Code: -32601, Message: "Method not found"}
	assert.Contains(t, err.Error(), "-32601")
	assert.Contains(t, err.Error(), "Method not found")
}

// --- MCPServerConfig test ---

func TestMCPServerConfig_SSE(t *testing.T) {
	cfg := MCPServerConfig{
		Name:      "github",
		Transport: "sse",
		URL:       "http://localhost:3001/sse",
	}
	assert.Equal(t, "github", cfg.Name)
	assert.Equal(t, "sse", cfg.Transport)
	assert.Equal(t, "http://localhost:3001/sse", cfg.URL)
}

func TestMCPServerConfig_Stdio(t *testing.T) {
	cfg := MCPServerConfig{
		Name:      "fs",
		Transport: "stdio",
		Command:   "npx",
		Args:      []string{"-y", "@anthropic-ai/mcp-server-filesystem", "/home"},
	}
	assert.Equal(t, "fs", cfg.Name)
	assert.Equal(t, "stdio", cfg.Transport)
	assert.Equal(t, "npx", cfg.Command)
	assert.Len(t, cfg.Args, 3)
}

// --- SSE URL resolution test ---

func TestSSETransport_RelativeURL(t *testing.T) {
	server := mockMCPServer(t)
	defer server.Close()

	client, err := NewMCPClient(MCPServerConfig{
		Name:      "test",
		Transport: "sse",
		URL:       server.URL + "/sse",
	})
	require.NoError(t, err)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The mock server sends a relative URL "/message" which should be resolved.
	require.NoError(t, client.Connect(ctx))
	assert.True(t, client.Connected())

	// Verify we can make requests (proves URL resolution worked).
	tools, err := client.ListTools(ctx)
	require.NoError(t, err)
	assert.Len(t, tools, 2)
}

// --- Concurrency test ---

func TestConnect_Concurrent(t *testing.T) {
	server := mockMCPServer(t)
	defer server.Close()

	// Multiple concurrent connect/list/call cycles shouldn't panic.
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			client, err := NewMCPClient(MCPServerConfig{
				Name:      fmt.Sprintf("test-%d", i),
				Transport: "sse",
				URL:       server.URL + "/sse",
			})
			if err != nil {
				return
			}
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			if err := client.Connect(ctx); err != nil {
				return
			}

			if _, err := client.ListTools(ctx); err != nil {
				return
			}
		}(i)
	}
	wg.Wait()
}

// --- MCPTool JSON test ---

func TestMCPTool_JSON(t *testing.T) {
	toolJSON := `{"name":"test_tool","description":"A test tool","inputSchema":{"type":"object","properties":{"arg":{"type":"string"}}}}`
	var tool MCPTool
	err := json.Unmarshal([]byte(toolJSON), &tool)
	require.NoError(t, err)
	assert.Equal(t, "test_tool", tool.Name)
	assert.Equal(t, "A test tool", tool.Description)
	assert.NotNil(t, tool.InputSchema)

	// Verify schema can be re-parsed.
	var schema map[string]any
	require.NoError(t, json.Unmarshal(tool.InputSchema, &schema))
	assert.Equal(t, "object", schema["type"])
}

// --- SSE connect to non-existent server ---

func TestSSEConnect_Timeout(t *testing.T) {
	// Use a server that won't respond with SSE events.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Just hang — don't send endpoint event.
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	}))
	defer server.Close()

	client, err := NewMCPClient(MCPServerConfig{
		Name:      "test",
		Transport: "sse",
		URL:       server.URL + "/sse",
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	err = client.Connect(ctx)
	require.Error(t, err)
	// Should fail due to context timeout or SSE timeout.
	assert.True(t, strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "context"))
}
