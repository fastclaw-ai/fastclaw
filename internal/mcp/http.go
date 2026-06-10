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
	url       string
	headers   map[string]string
	client    *http.Client
	mu        sync.Mutex
	nextID    int
	sessionID string
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

// applyHeaders sets the common request headers (content negotiation, user
// headers with $ENV expansion, and the session ID once the server has
// assigned one during initialization).
func (c *HTTPClient) applyHeaders(httpReq *http.Request) {
	httpReq.Header.Set("Content-Type", "application/json")
	// MCP Streamable HTTP transport requires the client to advertise that
	// it accepts both a plain JSON response and an SSE stream. Servers that
	// follow the spec reject the request with HTTP 406 otherwise. Set this
	// before applying user headers so an explicit Accept override still wins.
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range c.headers {
		if strings.HasPrefix(v, "$") {
			v = os.Getenv(v[1:])
		}
		httpReq.Header.Set(k, v)
	}
	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()
	if sid != "" {
		httpReq.Header.Set("Mcp-Session-Id", sid)
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

	c.applyHeaders(httpReq)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	// Capture the server-assigned session ID. Per the spec it appears on
	// the InitializeResult response; once set, every subsequent request
	// must echo it back via the Mcp-Session-Id header.
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.mu.Lock()
		c.sessionID = sid
		c.mu.Unlock()
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	// A spec-compliant server may answer either with a plain JSON object or
	// with an SSE stream (Content-Type: text/event-stream) whose `data:`
	// lines carry the JSON-RPC message. Extract the JSON payload from SSE
	// before unmarshaling.
	payload := respBody
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "text/event-stream") {
		payload = extractSSEData(respBody)
		if payload == nil {
			return nil, fmt.Errorf("no JSON-RPC payload in SSE response")
		}
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(payload, &rpcResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	return &rpcResp, nil
}

// sendNotification posts a JSON-RPC notification (no id, no response body).
// The MCP lifecycle requires the client to send notifications/initialized
// after a successful initialize before issuing further requests.
func (c *HTTPClient) sendNotification(method string, params any) error {
	body, err := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	c.applyHeaders(httpReq)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send notification: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2<<20))
	// Spec: a notification-only POST yields 202 Accepted; some servers
	// reply 200. Anything >= 300 is a real failure.
	if resp.StatusCode >= 300 {
		return fmt.Errorf("notification %s: HTTP %d", method, resp.StatusCode)
	}
	return nil
}

// Connect runs the MCP initialization handshake: initialize, then the
// notifications/initialized notification required by the lifecycle before
// any further requests (e.g. tools/list) are accepted.
func (c *HTTPClient) Connect() error {
	if _, err := c.sendRequest("initialize", initializeParams{
		ProtocolVersion: "2024-11-05",
		ClientInfo:      clientInfo{Name: "fastclaw", Version: "0.1.0"},
	}); err != nil {
		return err
	}
	return c.sendNotification("notifications/initialized", nil)
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

// extractSSEData pulls the JSON-RPC payload out of an SSE response body.
// Per the SSE format, an event's data is one or more `data:` lines joined
// with newlines, and events are separated by blank lines. A single
// request/response exchange yields one event carrying the JSON-RPC reply;
// if a server sends several events we return the last non-empty one (the
// final response message). Returns nil when no data lines are present.
func extractSSEData(body []byte) []byte {
	var current []string
	var last []string
	flush := func() {
		if len(current) > 0 {
			last = current
			current = nil
		}
	}
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimRight(raw, "\r")
		if line == "" {
			flush()
			continue
		}
		if data, ok := strings.CutPrefix(line, "data:"); ok {
			current = append(current, strings.TrimPrefix(data, " "))
		}
	}
	flush()
	if last == nil {
		return nil
	}
	return []byte(strings.Join(last, "\n"))
}
