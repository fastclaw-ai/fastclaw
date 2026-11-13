package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/usermcp"
)

// mountCustomMCP puts the custom_mcp tool on this turn's registry when the
// chatter added MCP servers of their own and this turn may use them (the
// same turns as their connected accounts: ownAccountsTurn), and returns
// the system-prompt note naming them ("" otherwise).
func (a *Agent) mountCustomMCP(ctx context.Context, msg bus.InboundMessage, chatterUID string) string {
	if a.registry == nil {
		return ""
	}
	// A previous turn's mount (bound to that turn's chatter) never carries over.
	a.registry.Unregister(tools.CustomMCPToolName)
	if _, ok := a.ownAccountsTurn(ctx, msg, chatterUID); !ok {
		return ""
	}
	svc := usermcp.Get()
	if svc == nil {
		return ""
	}
	lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	servers, err := svc.List(lctx, chatterUID)
	if err != nil || len(servers) == 0 {
		return ""
	}
	tools.RegisterCustomMCP(a.registry, svc, chatterUID, servers, a.connectorApprovals(ctx, chatterUID))
	names := make([]string, 0, len(servers))
	for _, s := range servers {
		names = append(names, fmt.Sprintf("%q", s.Name))
	}
	return "The person added their own MCP servers: " + strings.Join(names, ", ") +
		". When a request is about one of them, or needs what it offers, use the custom_mcp tool (list_tools first)."
}
