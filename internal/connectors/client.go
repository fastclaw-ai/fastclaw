// Package connectors lets a person connect their own third-party accounts
// (Notion, GitHub, Linear, …) and lets their agent read them, through the
// Connany connector service. Adapted from fleet's connectors
// (apps/web/docs/connectors-spec.md); see docs/connectors.md.
//
// Connany holds the third-party credentials, connections and MCP tools;
// fastclaw stores no third-party token. Its project key lives only in the
// server process: it never reaches a browser, the sandbox or the model.
package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// ServiceError is a non-2xx answer from Connany.
type ServiceError struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
}

func (e *ServiceError) Error() string {
	return fmt.Sprintf("connany %d %s: %s", e.Status, e.Code, e.Message)
}

// Connector is one platform the administrator enabled.
type Connector struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Category    string `json:"category,omitempty"`
	Description string `json:"description,omitempty"`
	AvatarURL   string `json:"avatar_url"`
}

// Category groups connectors in display order.
type Category struct {
	Name  string `json:"name"`
	Title string `json:"title"`
}

// Connection is one account a person connected.
type Connection struct {
	ID               string `json:"id"`
	ExternalUserID   string `json:"external_user_id"`
	Connector        string `json:"connector"`
	Status           string `json:"status"` // connected | reauth_required | revoked
	NeedsAccess      bool   `json:"needs_access"`
	RevocationStatus string `json:"revocation_status"`
	Identity         struct {
		AccountName   string `json:"account_name"`
		WorkspaceName string `json:"workspace_name"`
	} `json:"identity"`
}

// Session is one authorization attempt.
type Session struct {
	ID           string `json:"id"`
	Connector    string `json:"connector"`
	Status       string `json:"status"` // pending | authorizing | processing | connected | error | expired
	ExpiresAt    string `json:"expires_at"`
	ConnectURL   string `json:"connect_url"`
	ConnectionID string `json:"connection_id"`
	ErrorCode    string `json:"error_code"`
}

// AccessGrant is a resource an account shares beyond its sign-in (for
// GitHub: an organization or repository the app was installed on).
type AccessGrant struct {
	ID        any    `json:"id"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Selection string `json:"selection"`
	Suspended bool   `json:"suspended"`
	ManageURL string `json:"manage_url"`
}

// AccessListing lists an account's grants and where to add more.
type AccessListing struct {
	AddURL string        `json:"add_url"`
	Total  int           `json:"total"`
	Data   []AccessGrant `json:"data"`
}

// ToolDefinition is one MCP tool of a connection.
type ToolDefinition struct {
	Name        string         `json:"name"`
	Connector   string         `json:"connector"`
	Description string         `json:"description"`
	ReadOnly    bool           `json:"read_only"`
	InputSchema map[string]any `json:"input_schema"`
}

// Client talks to Connany's HTTP API (/v1).
type Client struct {
	base   string
	apiKey string
	http   *http.Client
}

// NewClient validates the base URL: an HTTPS origin, or HTTP on this
// machine for local development.
func NewClient(baseURL, apiKey string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("connectors: base URL must be a bare origin")
	}
	local := u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")
	if u.Scheme != "https" && !local {
		return nil, errors.New("connectors: base URL must be HTTPS (or HTTP on localhost)")
	}
	return &Client{
		base:   u.Scheme + "://" + u.Host,
		apiKey: apiKey,
		http: &http.Client{
			Timeout: 60 * time.Second,
			// Never follow a redirect with the project key on it.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// One cooldown per process: Connany limits the whole project (120
// requests a minute), so after a 429 every caller waits.
var (
	coolMu    sync.Mutex
	coolUntil time.Time
)

// ErrRateLimited is returned while the project is cooling down.
var ErrRateLimited = errors.New("connectors: rate limited")

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	coolMu.Lock()
	cooling := time.Now().Before(coolUntil)
	coolMu.Unlock()
	if cooling {
		return ErrRateLimited
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("connectors: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusTooManyRequests {
		secs, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		if secs <= 0 {
			secs = 60
		}
		coolMu.Lock()
		coolUntil = time.Now().Add(time.Duration(secs) * time.Second)
		coolMu.Unlock()
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error struct {
				Code    string         `json:"code"`
				Message string         `json:"message"`
				Details map[string]any `json:"details"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		code := e.Error.Code
		if code == "" {
			code = "request_failed"
		}
		return &ServiceError{Status: resp.StatusCode, Code: code, Message: e.Error.Message, Details: e.Error.Details}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &ServiceError{Status: resp.StatusCode, Code: "invalid_response", Message: "invalid response"}
	}
	return nil
}

func q(pairs ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			v.Set(pairs[i], pairs[i+1])
		}
	}
	return v.Encode()
}

// Connectors lists the enabled connectors; descriptions and category
// titles come in lang (a BCP-47 tag, falling back to English).
func (c *Client) Connectors(ctx context.Context, lang string) ([]Category, []Connector, error) {
	var out struct {
		Categories []Category  `json:"categories"`
		Data       []Connector `json:"data"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/connectors?"+q("lang", lang), nil, &out)
	return out.Categories, out.Data, err
}

// Connections lists a subject's accounts (all pages).
func (c *Client) Connections(ctx context.Context, subject string) ([]Connection, error) {
	var all []Connection
	after := ""
	for i := 0; i < 10; i++ {
		var page struct {
			Data       []Connection `json:"data"`
			NextCursor string       `json:"next_cursor"`
		}
		if err := c.do(ctx, http.MethodGet, "/v1/connections?"+q("external_user_id", subject, "limit", "100", "after", after), nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Data...)
		if page.NextCursor == "" || page.NextCursor == after {
			break
		}
		after = page.NextCursor
	}
	return all, nil
}

// CreateSession starts connecting a new account.
func (c *Client) CreateSession(ctx context.Context, connector, subject, returnURL string) (*Session, error) {
	var s Session
	err := c.do(ctx, http.MethodPost, "/v1/connectors/"+url.PathEscape(connector)+"/sessions",
		map[string]string{"external_user_id": subject, "return_url": returnURL}, &s)
	return &s, err
}

// GetSession reads how an authorization went.
func (c *Client) GetSession(ctx context.Context, connector, id, subject string) (*Session, error) {
	var s Session
	err := c.do(ctx, http.MethodGet, "/v1/connectors/"+url.PathEscape(connector)+"/sessions/"+url.PathEscape(id)+"?"+q("external_user_id", subject), nil, &s)
	return &s, err
}

// Reconnect re-authorizes an existing account.
func (c *Client) Reconnect(ctx context.Context, connectionID, subject, returnURL string) (*Session, error) {
	var s Session
	err := c.do(ctx, http.MethodPost, "/v1/connections/"+url.PathEscape(connectionID)+"/reconnect",
		map[string]string{"external_user_id": subject, "return_url": returnURL}, &s)
	return &s, err
}

// Disconnect revokes and removes an account.
func (c *Client) Disconnect(ctx context.Context, connectionID, subject string) (*Connection, error) {
	var conn Connection
	err := c.do(ctx, http.MethodDelete, "/v1/connections/"+url.PathEscape(connectionID)+"?"+q("external_user_id", subject), nil, &conn)
	return &conn, err
}

// Check verifies an account still works.
func (c *Client) Check(ctx context.Context, connectionID, subject string) (int, error) {
	var out struct {
		ToolCount int `json:"tool_count"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/connections/"+url.PathEscape(connectionID)+"/check",
		map[string]string{"external_user_id": subject}, &out)
	return out.ToolCount, err
}

// Access lists the resources an account shares.
func (c *Client) Access(ctx context.Context, connectionID, subject string) (*AccessListing, error) {
	var out AccessListing
	err := c.do(ctx, http.MethodGet, "/v1/connections/"+url.PathEscape(connectionID)+"/access?"+q("external_user_id", subject, "page", "1", "limit", "20"), nil, &out)
	return &out, err
}

// ListTools searches a connection's tools.
func (c *Client) ListTools(ctx context.Context, connectionID, subject, query string, offset, limit int, readOnly bool) ([]ToolDefinition, *int, error) {
	params := []string{"external_user_id", subject, "limit", strconv.Itoa(limit)}
	if query != "" {
		params = append(params, "query", query)
	}
	if offset > 0 {
		params = append(params, "offset", strconv.Itoa(offset))
	}
	if readOnly {
		params = append(params, "read_only", "true")
	}
	var out struct {
		Data       []ToolDefinition `json:"data"`
		NextOffset *int             `json:"next_offset"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/connections/"+url.PathEscape(connectionID)+"/tools?"+q(params...), nil, &out)
	return out.Data, out.NextOffset, err
}

// CallTool runs a tool on a connection.
func (c *Client) CallTool(ctx context.Context, connectionID, subject, tool string, input map[string]any) (json.RawMessage, error) {
	if input == nil {
		input = map[string]any{}
	}
	var out struct {
		Data json.RawMessage `json:"data"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/connections/"+url.PathEscape(connectionID)+"/tools/"+url.PathEscape(tool)+"/call",
		map[string]any{"external_user_id": subject, "input": input}, &out)
	return out.Data, err
}

// Avatar fetches a connector's icon (proxied so browsers never contact
// the service directly).
func (c *Client) Avatar(ctx context.Context, avatarURL string) ([]byte, string, error) {
	u, err := url.Parse(avatarURL)
	if err != nil {
		return nil, "", err
	}
	if !u.IsAbs() {
		u, err = url.Parse(c.base + avatarURL)
		if err != nil {
			return nil, "", err
		}
	}
	// Only the service's own origin: it's the one we trust with the key.
	if u.Scheme+"://"+u.Host != c.base {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, "", err
		}
		resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
		if err != nil {
			return nil, "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, "", fmt.Errorf("avatar HTTP %d", resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return data, resp.Header.Get("Content-Type"), err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("avatar HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return data, resp.Header.Get("Content-Type"), err
}
