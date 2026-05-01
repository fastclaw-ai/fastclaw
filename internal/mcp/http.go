package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
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
		headers: expandHeaders(headers),
		client:  &http.Client{},
		nextID:  1,
	}
}

func expandHeaders(headers map[string]string) map[string]string {
	expanded := make(map[string]string, len(headers))
	for k, v := range headers {
		if strings.HasPrefix(v, "$") {
			expanded[k] = os.Getenv(v[1:])
		} else {
			expanded[k] = v
		}
	}
	return expanded
}

func (c *HTTPClient) sendRequest(method string, params interface{}) (*jsonRPCResponse, error) {
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

	httpReq, err := http.NewRequest("POST", c.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range c.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	respBody = extractSSEPayload(respBody)

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	return &rpcResp, nil
}

// extractSSEPayload extracts JSON from SSE response format.
// Handles both plain JSON and SSE (event:/data:) responses.
func extractSSEPayload(body []byte) []byte {
	s := strings.TrimSpace(string(body))
	if len(s) > 0 && s[0] == '{' {
		return body
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimPrefix(line, "data:")
			payload = strings.TrimSpace(payload)
			if payload != "" {
				return []byte(payload)
			}
		}
	}
	return body
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
