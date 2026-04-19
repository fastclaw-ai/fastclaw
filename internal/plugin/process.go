package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Process manages a plugin subprocess and its JSON-RPC communication.
type Process struct {
	manifest *Manifest
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   *bufio.Scanner
	stderr   io.ReadCloser

	mu        sync.Mutex
	nextID    atomic.Int64
	pending   map[int]chan *Response
	onNotify  func(Notification)
	running   bool
	cancelFn  context.CancelFunc
}

// NewProcess creates a new plugin process from a manifest.
func NewProcess(m *Manifest) *Process {
	p := &Process{
		manifest: m,
		pending:  make(map[int]chan *Response),
	}
	p.nextID.Store(1)
	return p
}

// SetNotifyHandler sets the handler for notifications from the plugin.
func (p *Process) SetNotifyHandler(fn func(Notification)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onNotify = fn
}

// Start launches the plugin subprocess.
func (p *Process) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		return nil
	}

	childCtx, cancel := context.WithCancel(ctx)
	p.cancelFn = cancel

	parts := strings.Fields(p.manifest.Command)
	if len(parts) == 0 {
		cancel()
		return fmt.Errorf("plugin %s: empty command", p.manifest.ID)
	}

	cmd := exec.CommandContext(childCtx, parts[0], parts[1:]...)
	cmd.Dir = p.manifest.Dir

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("plugin %s: stdin pipe: %w", p.manifest.ID, err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("plugin %s: stdout pipe: %w", p.manifest.ID, err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("plugin %s: stderr pipe: %w", p.manifest.ID, err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("plugin %s: start: %w", p.manifest.ID, err)
	}

	p.cmd = cmd
	p.stdin = stdin
	p.stdout = bufio.NewScanner(stdoutPipe)
	p.stdout.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB buffer
	p.stderr = stderrPipe
	p.running = true

	// Read stdout for JSON-RPC messages
	go p.readLoop()
	// Log stderr
	go p.logStderr()

	return nil
}

// Call sends a JSON-RPC request and waits for the response.
func (p *Process) Call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	id := int(p.nextID.Add(1))

	req, err := newRequest(method, params, id)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	ch := make(chan *Response, 1)
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return nil, fmt.Errorf("plugin %s: not running", p.manifest.ID)
	}
	p.pending[id] = ch
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
	}()

	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')

	p.mu.Lock()
	_, err = p.stdin.Write(data)
	p.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("plugin %s: write: %w", p.manifest.ID, err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

// Notify sends a JSON-RPC notification (no response expected) to the plugin.
func (p *Process) Notify(method string, params interface{}) error {
	req, err := newRequest(method, params, 0)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}
	// Notifications have no ID in JSON-RPC 2.0; we use a minimal struct.
	notif := struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}{
		JSONRPC: "2.0",
		Method:  method,
		Params:  req.Params,
	}

	data, err := json.Marshal(notif)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.running {
		return fmt.Errorf("plugin %s: not running", p.manifest.ID)
	}
	_, err = p.stdin.Write(data)
	return err
}

// Stop gracefully shuts down the plugin process.
func (p *Process) Stop(timeout time.Duration) {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()

	// Try graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	_, _ = p.Call(ctx, MethodShutdown, nil)

	p.mu.Lock()
	p.running = false
	if p.cancelFn != nil {
		p.cancelFn()
	}
	// Close stdin to signal EOF to the child
	if p.stdin != nil {
		p.stdin.Close()
	}
	p.mu.Unlock()

	// Wait for process exit with timeout
	done := make(chan struct{})
	go func() {
		if p.cmd != nil && p.cmd.Process != nil {
			p.cmd.Wait()
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(timeout):
		if p.cmd != nil && p.cmd.Process != nil {
			p.cmd.Process.Kill()
		}
	}

	// Cancel any pending calls
	p.mu.Lock()
	for id, ch := range p.pending {
		ch <- &Response{Error: &RPCError{Code: -1, Message: "plugin stopped"}}
		delete(p.pending, id)
	}
	p.mu.Unlock()
}

// IsRunning returns whether the process is alive.
func (p *Process) IsRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

func (p *Process) readLoop() {
	defer func() {
		p.mu.Lock()
		p.running = false
		p.mu.Unlock()
	}()

	for p.stdout.Scan() {
		line := p.stdout.Bytes()
		if len(line) == 0 {
			continue
		}

		// Try to parse as response (has "id" field)
		var msg struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *RPCError       `json:"error"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			slog.Warn("plugin: invalid JSON from stdout", "plugin", p.manifest.ID, "line", string(line))
			continue
		}

		if msg.ID != nil {
			// It's a response
			p.mu.Lock()
			ch, ok := p.pending[*msg.ID]
			p.mu.Unlock()
			if ok {
				ch <- &Response{
					JSONRPC: "2.0",
					Result:  msg.Result,
					Error:   msg.Error,
					ID:      *msg.ID,
				}
			}
		} else if msg.Method != "" {
			// It's a notification
			p.mu.Lock()
			fn := p.onNotify
			p.mu.Unlock()
			if fn != nil {
				fn(Notification{
					JSONRPC: "2.0",
					Method:  msg.Method,
					Params:  msg.Params,
				})
			}
		}
	}

	if err := p.stdout.Err(); err != nil {
		slog.Debug("plugin stdout closed", "plugin", p.manifest.ID, "error", err)
	}
}

func (p *Process) logStderr() {
	scanner := bufio.NewScanner(p.stderr)
	for scanner.Scan() {
		slog.Info("plugin stderr", "plugin", p.manifest.ID, "line", scanner.Text())
	}
}
