package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// E2B API: https://e2b.dev/docs
// Sandbox creation: POST https://api.e2b.dev/sandboxes
// Command execution: Connect protocol via envd on the sandbox

const e2bBaseURL = "https://api.e2b.dev"
const e2bEnvdPort = "49983"

// E2BExecutor implements Executor using E2B hosted sandboxes.
type E2BExecutor struct {
	apiKey      string
	sandboxID   string
	accessToken string
	client      *http.Client
}

func newE2BExecutor(ctx context.Context, apiKey, template string, timeout time.Duration) (*E2BExecutor, error) {
	if template == "" {
		template = "base"
	}
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}

	client := &http.Client{Timeout: 60 * time.Second}

	body, _ := json.Marshal(map[string]interface{}{
		"templateID": template,
		"timeout":    int(timeout.Seconds()),
	})
	req, err := http.NewRequestWithContext(ctx, "POST", e2bBaseURL+"/sandboxes", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("e2b create sandbox: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("e2b create sandbox: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		SandboxID       string `json:"sandboxID"`
		EnvdAccessToken string `json:"envdAccessToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("e2b parse response: %w", err)
	}

	slog.Info("e2b sandbox created", "sandboxID", result.SandboxID, "template", template)

	return &E2BExecutor{
		apiKey:      apiKey,
		sandboxID:   result.SandboxID,
		accessToken: result.EnvdAccessToken,
		client:      client,
	}, nil
}

func (e *E2BExecutor) envdURL() string {
	return fmt.Sprintf("https://%s-%s.e2b.app", e2bEnvdPort, e.sandboxID)
}

// recreate destroys the current sandbox and creates a new one.
func (e *E2BExecutor) recreate(ctx context.Context) error {
	slog.Info("e2b sandbox expired, recreating", "oldSandboxID", e.sandboxID)
	newEx, err := newE2BExecutor(ctx, e.apiKey, "base", 30*time.Minute)
	if err != nil {
		return err
	}
	e.sandboxID = newEx.sandboxID
	e.accessToken = newEx.accessToken
	return nil
}

// isSandboxGone checks if the error indicates the sandbox was destroyed.
func isSandboxGone(statusCode int) bool {
	return statusCode == 502 || statusCode == 404
}

// connectEnvelope wraps JSON payload in Connect protocol envelope framing.
// Format: [1 byte flags][4 bytes big-endian length][payload]
func connectEnvelope(payload []byte) []byte {
	buf := make([]byte, 5+len(payload))
	buf[0] = 0 // flags: no compression, not end of stream
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(payload)))
	copy(buf[5:], payload)
	return buf
}

// parseConnectStream reads Connect protocol streaming response.
// Each frame: [1 byte flags][4 bytes length][payload]
func parseConnectStream(data []byte) []json.RawMessage {
	var messages []json.RawMessage
	for len(data) >= 5 {
		flags := data[0]
		length := binary.BigEndian.Uint32(data[1:5])
		data = data[5:]
		if uint32(len(data)) < length {
			break
		}
		payload := data[:length]
		data = data[length:]

		// flags & 0x02 = end_stream (trailer), skip it
		if flags&0x02 != 0 {
			continue
		}
		messages = append(messages, json.RawMessage(payload))
	}
	return messages
}

func (e *E2BExecutor) Exec(ctx context.Context, command string, timeout time.Duration) (string, error) {
	result, err := e.execOnce(ctx, command, timeout)
	if err != nil && strings.Contains(err.Error(), "HTTP 502") || strings.Contains(fmt.Sprint(err), "HTTP 404") {
		if rerr := e.recreate(ctx); rerr != nil {
			return "", fmt.Errorf("sandbox recreate failed: %w (original: %v)", rerr, err)
		}
		return e.execOnce(ctx, command, timeout)
	}
	return result, err
}

func (e *E2BExecutor) execOnce(ctx context.Context, command string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"process": map[string]interface{}{
			"cmd":  "/bin/bash",
			"args": []string{"-c", command},
		},
	})

	enveloped := connectEnvelope(payload)

	reqURL := e.envdURL() + "/process.Process/Start"
	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, bytes.NewReader(enveloped))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/connect+json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Connect-Timeout-Ms", fmt.Sprintf("%d", int(timeout.Milliseconds())))
	if e.accessToken != "" {
		req.Header.Set("X-Access-Token", e.accessToken)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("e2b exec: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("e2b exec HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Parse Connect streaming response frames
	frames := parseConnectStream(body)

	var stdout, stderr strings.Builder
	exitCode := 0
	exited := false

	for _, frame := range frames {
		// E2B response format: {"event":{"data":{"stdout":"base64..."}}}
		var msg struct {
			Event struct {
				Start *struct {
					Pid int `json:"pid"`
				} `json:"start,omitempty"`
				Data *struct {
					Stdout string `json:"stdout,omitempty"` // base64 encoded
					Stderr string `json:"stderr,omitempty"` // base64 encoded
				} `json:"data,omitempty"`
				End *struct {
					Exited bool   `json:"exited"`
					Status string `json:"status"` // "exit status 0"
				} `json:"end,omitempty"`
			} `json:"event"`
		}
		if json.Unmarshal(frame, &msg) != nil {
			continue
		}
		if msg.Event.Data != nil {
			if msg.Event.Data.Stdout != "" {
				if decoded, err := base64.StdEncoding.DecodeString(msg.Event.Data.Stdout); err == nil {
					stdout.Write(decoded)
				}
			}
			if msg.Event.Data.Stderr != "" {
				if decoded, err := base64.StdEncoding.DecodeString(msg.Event.Data.Stderr); err == nil {
					stderr.Write(decoded)
				}
			}
		}
		if msg.Event.End != nil {
			exited = msg.Event.End.Exited
			// Parse "exit status N" to get exit code
			if strings.HasPrefix(msg.Event.End.Status, "exit status ") {
				fmt.Sscanf(msg.Event.End.Status, "exit status %d", &exitCode)
			}
		}
	}

	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}
	output = strings.TrimSpace(output)

	slog.Info("e2b exec completed", "sandboxID", e.sandboxID, "exitCode", exitCode, "exited", exited, "outputLen", len(output))

	if exitCode != 0 {
		if output == "" {
			output = fmt.Sprintf("Process exited with code %d", exitCode)
		}
		return output, fmt.Errorf("exit code %d", exitCode)
	}
	return output, nil
}

func (e *E2BExecutor) ReadFile(ctx context.Context, path string) (string, error) {
	result, err := e.readFileOnce(ctx, path)
	if err != nil && (strings.Contains(err.Error(), "HTTP 502") || strings.Contains(err.Error(), "HTTP 404")) {
		if rerr := e.recreate(ctx); rerr != nil {
			return "", rerr
		}
		return e.readFileOnce(ctx, path)
	}
	return result, err
}

func (e *E2BExecutor) readFileOnce(ctx context.Context, path string) (string, error) {
	reqURL := fmt.Sprintf("%s/files?path=%s", e.envdURL(), url.QueryEscape(path))
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return "", err
	}
	if e.accessToken != "" {
		req.Header.Set("X-Access-Token", e.accessToken)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("e2b read: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("e2b read HTTP %d: %s", resp.StatusCode, string(body))
	}
	return string(body), nil
}

func (e *E2BExecutor) WriteFile(ctx context.Context, path, content string) (string, error) {
	result, err := e.writeFileOnce(ctx, path, content)
	if err != nil && (strings.Contains(err.Error(), "HTTP 502") || strings.Contains(err.Error(), "HTTP 404")) {
		if rerr := e.recreate(ctx); rerr != nil {
			return "", rerr
		}
		return e.writeFileOnce(ctx, path, content)
	}
	return result, err
}

func (e *E2BExecutor) writeFileOnce(ctx context.Context, path, content string) (string, error) {
	reqURL := fmt.Sprintf("%s/files?path=%s", e.envdURL(), url.QueryEscape(path))
	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, strings.NewReader(content))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if e.accessToken != "" {
		req.Header.Set("X-Access-Token", e.accessToken)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("e2b write: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("e2b write HTTP %d: %s", resp.StatusCode, string(body))
	}
	return fmt.Sprintf("Wrote %d bytes to %s", len(content), path), nil
}

func (e *E2BExecutor) ListDir(ctx context.Context, path string) (string, error) {
	// Use exec to list directory since the files API doesn't have a list endpoint
	return e.Exec(ctx, fmt.Sprintf("ls -la %s", path), 10*time.Second)
}

func (e *E2BExecutor) Close() error {
	req, _ := http.NewRequest("DELETE",
		fmt.Sprintf("%s/sandboxes/%s", e2bBaseURL, e.sandboxID), nil)
	req.Header.Set("X-API-Key", e.apiKey)
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	slog.Info("e2b sandbox closed", "sandboxID", e.sandboxID)
	return nil
}

// E2BExecutorPool manages per-user E2B sandboxes.
type E2BExecutorPool struct {
	mu        sync.Mutex
	executors map[string]*E2BExecutor
	apiKey    string
	template  string
	timeout   time.Duration
}

func NewE2BExecutorPool(apiKey, template string, timeout time.Duration) *E2BExecutorPool {
	return &E2BExecutorPool{
		executors: make(map[string]*E2BExecutor),
		apiKey:    apiKey,
		template:  template,
		timeout:   timeout,
	}
}

func (p *E2BExecutorPool) Get(ctx context.Context, userID string) (Executor, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ex, ok := p.executors[userID]; ok {
		return ex, nil
	}
	ex, err := newE2BExecutor(ctx, p.apiKey, p.template, p.timeout)
	if err != nil {
		return nil, err
	}
	p.executors[userID] = ex
	return ex, nil
}

func (p *E2BExecutorPool) Release(userID string) error {
	p.mu.Lock()
	ex, ok := p.executors[userID]
	delete(p.executors, userID)
	p.mu.Unlock()
	if ok {
		return ex.Close()
	}
	return nil
}

func (p *E2BExecutorPool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ex := range p.executors {
		ex.Close()
	}
	p.executors = make(map[string]*E2BExecutor)
}

var (
	_ Executor     = (*E2BExecutor)(nil)
	_ ExecutorPool = (*E2BExecutorPool)(nil)
)
