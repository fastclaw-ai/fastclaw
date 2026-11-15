// Package trig is a small client for trig (github.com/thinkany-ai/trig),
// the scheduler an account can register for the cron jobs its API turns
// create. trig keeps the job and, when it's due, posts a signed webhook to
// the account's product, which runs the turn and delivers the reply — the
// product owns the chat (WeChat over imc, …), so FastClaw can't.
package trig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/billing"
)

const timeout = 10 * time.Second

// Schedule is a trig schedule: kind "cron" (5 fields, in Timezone), "at"
// (RFC 3339) or "every" (Go duration ≥ 1m).
type Schedule struct {
	Kind     string `json:"kind"`
	Expr     string `json:"expr"`
	Timezone string `json:"timezone,omitempty"`
}

// Job is a trig job as the API returns it.
type Job struct {
	Key         string          `json:"key"`
	Schedule    Schedule        `json:"schedule"`
	Payload     json.RawMessage `json:"payload"`
	Status      string          `json:"status"`
	NextRunAt   *time.Time      `json:"nextRunAt"`
	LastFiredAt *time.Time      `json:"lastFiredAt"`
	FireCount   int             `json:"fireCount"`
	CreatedAt   time.Time       `json:"createdAt"`
}

// PutJob is the body of PUT /v1/jobs/{key}.
type PutJob struct {
	Schedule Schedule `json:"schedule"`
	Payload  any      `json:"payload,omitempty"`
	Missed   string   `json:"missed,omitempty"`
}

// Error is a non-2xx answer from trig.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("trig: %s (%s)", e.Message, e.Code)
	}
	return fmt.Sprintf("trig: HTTP %d", e.Status)
}

// IsNotFound reports a 404 from trig.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// Client talks to one trig deployment with one app's API key.
type Client struct {
	base string
	key  string
	http *http.Client
}

var sharedHTTP = &http.Client{
	Timeout: timeout,
	Transport: &http.Transport{
		// The URL is the account's choice: public addresses only, checked
		// at dial time (billing's SSRF guard).
		DialContext:           billing.GuardedDial,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		IdleConnTimeout:       60 * time.Second,
		MaxIdleConnsPerHost:   4,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// New returns a client for the trig at base (its origin, e.g.
// https://trig.example.com) with an app API key.
func New(base, key string) *Client {
	return &Client{base: strings.TrimRight(strings.TrimSpace(base), "/"), key: key, http: sharedHTTP}
}

// WithHTTP swaps the HTTP client (tests).
func (c *Client) WithHTTP(h *http.Client) *Client {
	cp := *c
	cp.http = h
	return &cp
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	if err := billing.CheckScheme(c.base); err != nil {
		return fmt.Errorf("trig url: %w", err)
	}
	var rd io.Reader
	if body != nil {
		blob, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(blob)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("User-Agent", "FastClaw-Scheduler/1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	blob, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(blob, &e)
		return &Error{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(blob, out); err != nil {
		return fmt.Errorf("trig: bad response: %w", err)
	}
	return nil
}

func jobPath(key string) string { return "/v1/jobs/" + url.PathEscape(key) }

// Put creates or replaces the job at key.
func (c *Client) Put(ctx context.Context, key string, j PutJob) (*Job, error) {
	var out struct {
		Job Job `json:"job"`
	}
	if err := c.do(ctx, http.MethodPut, jobPath(key), j, &out); err != nil {
		return nil, err
	}
	return &out.Job, nil
}

// Get returns the job at key.
func (c *Client) Get(ctx context.Context, key string) (*Job, error) {
	var out struct {
		Job Job `json:"job"`
	}
	if err := c.do(ctx, http.MethodGet, jobPath(key), nil, &out); err != nil {
		return nil, err
	}
	return &out.Job, nil
}

// Delete removes the job at key; false when there was none.
func (c *Client) Delete(ctx context.Context, key string) (bool, error) {
	var out struct {
		Deleted bool `json:"deleted"`
	}
	if err := c.do(ctx, http.MethodDelete, jobPath(key), nil, &out); err != nil {
		return false, err
	}
	return out.Deleted, nil
}

// List returns up to limit jobs whose key starts with prefix (one page).
func (c *Client) List(ctx context.Context, prefix string, limit int) ([]Job, error) {
	q := url.Values{"prefix": {prefix}, "limit": {fmt.Sprint(limit)}}
	var out struct {
		Jobs []Job `json:"jobs"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/jobs?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return out.Jobs, nil
}

// Ping checks the URL and key: GET /v1/app.
func (c *Client) Ping(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/v1/app", nil, nil)
}
