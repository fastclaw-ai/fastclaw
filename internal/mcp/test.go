package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/fastclaw-ai/fastclaw/internal/buildinfo"
	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// TestResult reports the outcome of a one-shot MCP connection check.
type TestResult struct {
	OK        bool   `json:"ok"`
	ToolCount int    `json:"toolCount"`
	Error     string `json:"error,omitempty"`
}

// TestConnection performs a one-shot initialize + tools/list handshake
// against an HTTP MCP server and reports how many tools it exposes. It
// never starts a subprocess: stdio servers are rejected up front because
// a dashboard dry-run shouldn't spawn local processes. The whole probe is
// bounded by ctx so a hung server can't block the request goroutine.
func TestConnection(ctx context.Context, cfg config.MCPServerConfig) TestResult {
	switch cfg.Type {
	case "http":
		if buildinfo.IsHostedDeploy() && isBlockedHostedHTTPMCPURL(cfg.URL) {
			return TestResult{Error: "hosted deployments cannot connect MCP servers to localhost, private, or link-local addresses"}
		}
	case "stdio":
		return TestResult{Error: "connection test is only available for HTTP MCP servers"}
	default:
		return TestResult{Error: fmt.Sprintf("unknown MCP server type %q", cfg.Type)}
	}

	type probeOut struct {
		tools int
		err   error
	}
	done := make(chan probeOut, 1)
	go func() {
		client := NewHTTPClient(cfg.URL, cfg.Headers)
		defer client.Close()
		if err := client.Connect(); err != nil {
			done <- probeOut{err: err}
			return
		}
		tools, err := client.ListTools()
		if err != nil {
			done <- probeOut{err: err}
			return
		}
		done <- probeOut{tools: len(tools)}
	}()

	select {
	case <-ctx.Done():
		return TestResult{Error: contextError(ctx.Err())}
	case out := <-done:
		if out.err != nil {
			return TestResult{Error: out.err.Error()}
		}
		return TestResult{OK: true, ToolCount: out.tools}
	}
}

func contextError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "connection timed out"
	}
	return err.Error()
}
