package usermcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// A minimal Streamable HTTP MCP client for servers a person added
// themselves. Unlike internal/mcp (servers an operator configured), every
// address here comes from a user, so:
//   - it dials only public addresses, checked again at dial time, never
//     through a proxy, and follows no redirects;
//   - header values are sent as given (no $ENV expansion);
//   - responses are size-capped.

const (
	protocolVersion = "2025-03-26"
	maxBody         = 4 << 20
	requestTimeout  = 30 * time.Second
)

// ToolDef is one tool a server offers.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	Annotations *struct {
		ReadOnlyHint *bool `json:"readOnlyHint,omitempty"`
	} `json:"annotations,omitempty"`
}

// ReadOnly: the server says the tool doesn't change anything.
func (t ToolDef) ReadOnly() bool {
	return t.Annotations != nil && t.Annotations.ReadOnlyHint != nil && *t.Annotations.ReadOnlyHint
}

// httpClient is shared by every session (a var so tests can reach a local
// server, which the public-address guard refuses).
var httpClient = newPublicHTTPClient()

func newPublicHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil // a proxy would dial for us, skipping the check
	tr.DialContext = publicDial
	return &http.Client{
		Transport: tr,
		// A redirect could point anywhere; the address was checked, not where it sends us.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// publicDial resolves the host, refuses any non-public address and dials
// the address it checked, so DNS can't swap one in between.
func publicDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("usermcp: no addresses for %s", host)
	}
	for _, ip := range ips {
		if blockedAddr(ip.IP) {
			return nil, fmt.Errorf("usermcp: blocked address %s for %s", ip.IP, host)
		}
	}
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	return d.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}

// blockedAddr: loopback, private, link-local, multicast, unspecified and
// CGNAT addresses (same list as web_fetch's and connectors' guards).
func blockedAddr(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 0 || (ip4[0] == 100 && ip4[1]&0xc0 == 0x40) {
			return true
		}
	}
	return false
}

// session is one initialized conversation with a server.
type session struct {
	url     string
	headers map[string]string
	id      string // Mcp-Session-Id, when the server issues one
	nextID  atomic.Int64
}

func dial(ctx context.Context, url string, headers map[string]string) (*session, error) {
	s := &session{url: url, headers: headers}
	_, err := s.call(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "fastclaw", "version": "1"},
	})
	if err != nil {
		return nil, err
	}
	// Servers wait for this before serving requests; a failure is theirs to report on the next call.
	_ = s.notify(ctx, "notifications/initialized")
	return s, nil
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

func (s *session) post(ctx context.Context, body any) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	if s.id != "" {
		req.Header.Set("Mcp-Session-Id", s.id)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Code: "unreachable", Detail: err.Error()}
	}
	return resp, nil
}

func statusError(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &Error{Code: "unauthorized", Detail: resp.Status}
	}
	return &Error{Code: "not_mcp", Detail: resp.Status}
}

func (s *session) notify(ctx context.Context, method string) error {
	resp, err := s.post(ctx, map[string]any{"jsonrpc": "2.0", "method": method})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode >= 300 {
		return statusError(resp)
	}
	return nil
}

// call sends one request and waits for its response (a JSON body, or the
// matching message of an SSE stream).
func (s *session) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	id := s.nextID.Add(1)
	resp, err := s.post(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp)
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" && method == "initialize" {
		s.id = sid
	}
	body := io.LimitReader(resp.Body, maxBody)
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	var msg *rpcResponse
	if ct == "text/event-stream" {
		msg, err = readSSE(body, id)
	} else {
		msg = &rpcResponse{}
		err = json.NewDecoder(body).Decode(msg)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Code: "not_mcp", Detail: err.Error()}
	}
	if msg.Error != nil {
		return nil, &Error{Code: "rpc_error", Detail: msg.Error.Message}
	}
	return msg.Result, nil
}

// readSSE returns the response to request id from an event stream,
// skipping the server's notifications and requests.
func readSSE(r io.Reader, id int64) (*rpcResponse, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxBody)
	var data strings.Builder
	flush := func() *rpcResponse {
		defer data.Reset()
		if data.Len() == 0 {
			return nil
		}
		var msg rpcResponse
		if json.Unmarshal([]byte(data.String()), &msg) != nil {
			return nil
		}
		var got int64
		if json.Unmarshal(msg.ID, &got) != nil || got != id {
			return nil
		}
		return &msg
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if msg := flush(); msg != nil {
				return msg, nil
			}
			continue
		}
		if v, ok := strings.CutPrefix(line, "data:"); ok {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(v, " "))
		}
	}
	if msg := flush(); msg != nil {
		return msg, nil
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("stream ended without a response")
}

const maxTools = 200

func (s *session) listTools(ctx context.Context) ([]ToolDef, error) {
	var out []ToolDef
	cursor := ""
	for page := 0; page < 10 && len(out) < maxTools; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := s.call(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var res struct {
			Tools      []ToolDef `json:"tools"`
			NextCursor string    `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, &Error{Code: "not_mcp", Detail: err.Error()}
		}
		for _, t := range res.Tools {
			if t.Name != "" {
				out = append(out, t)
			}
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	if len(out) > maxTools {
		out = out[:maxTools]
	}
	return out, nil
}

// MaxResultChars caps what a tool call hands the model.
const MaxResultChars = 20000

func (s *session) callTool(ctx context.Context, name string, args map[string]any) (string, error) {
	if args == nil {
		args = map[string]any{}
	}
	raw, err := s.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", err
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", &Error{Code: "not_mcp", Detail: err.Error()}
	}
	var parts []string
	for _, c := range res.Content {
		switch c.Type {
		case "text":
			parts = append(parts, c.Text)
		case "":
		default:
			parts = append(parts, "["+c.Type+" content omitted]")
		}
	}
	text := strings.Join(parts, "\n")
	if strings.TrimSpace(text) == "" && len(res.StructuredContent) > 0 {
		text = string(res.StructuredContent)
	}
	if r := []rune(text); len(r) > MaxResultChars {
		text = string(r[:MaxResultChars]) + "\n…(cut off)"
	}
	if res.IsError {
		return "", &Error{Code: "tool_error", Detail: text}
	}
	return text, nil
}
