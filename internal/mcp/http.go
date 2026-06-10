package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// HTTPClient implements the MCP client for HTTP (Streamable HTTP) servers.
type HTTPClient struct {
	url     string
	headers map[string]string
	client  *http.Client
	mu      sync.Mutex
	nextID  int
}

// NewHTTPClient creates a new HTTP MCP client.
func NewHTTPClient(url string, headers map[string]string) *HTTPClient {
	return &HTTPClient{
		url:     url,
		headers: maps.Clone(headers),
		client:  &http.Client{Timeout: 15 * time.Second},
		nextID:  1,
	}
}

func (c *HTTPClient) sendRequest(method string, params any) (*jsonRPCResponse, error) {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	c.mu.Unlock()

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range c.headers {
		if strings.HasPrefix(v, "$") {
			v = os.Getenv(v[1:])
		}
		httpReq.Header.Set(k, v)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	return &rpcResp, nil
}

// Connect initializes the connection with the MCP server.
func (c *HTTPClient) Connect() error {
	_, err := c.sendRequest("initialize", initializeParams{
		ProtocolVersion: "2024-11-05",
		ClientInfo:      clientInfo{Name: "fastclaw", Version: "0.1.0"},
	})
	return err
}

// ListTools returns the list of tools available on the MCP server.
func (c *HTTPClient) ListTools() ([]ToolDef, error) {
	resp, err := c.sendRequest("tools/list", struct{}{})
	if err != nil {
		return nil, err
	}

	var result toolsListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("parse tools list: %w", err)
	}

	return result.Tools, nil
}

// CallTool calls a tool on the MCP server.
func (c *HTTPClient) CallTool(name string, args json.RawMessage) (string, error) {
	resp, err := c.sendRequest("tools/call", toolCallParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		return "", err
	}

	var result toolCallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return "", fmt.Errorf("parse tool result: %w", err)
	}

	var texts []string
	for _, c := range result.Content {
		if c.Type == "text" {
			texts = append(texts, c.Text)
		}
	}
	return strings.Join(texts, "\n"), nil
}

// Close is a no-op for HTTP clients.
func (c *HTTPClient) Close() error {
	return nil
}
