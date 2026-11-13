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

// MCPSignInRedirectParam is the turn param (set by the calling product's
// backend, never the model) naming the product's OAuth redirect URI for
// custom MCP servers: a person who hasn't signed in to an OAuth server
// offered on this agent gets a sign-in link in the chat, and the product
// finishes it there (/api/mcp-servers/oauth/finish). MCPClientNameParam
// (optional) is the name it registers with those servers.
const (
	MCPSignInRedirectParam = "__fastclawMCPRedirectURI"
	MCPClientNameParam     = "__fastclawMCPClientName"
)

// mountCustomMCP puts the custom_mcp tool on this turn's registry when
// there are custom MCP servers for it — the chatter's own (on every agent
// they chat with) plus the ones this agent's maker offers on it — and
// this turn may use them (the same turns as the chatter's connected
// accounts: ownAccountsTurn). Returns the system-prompt note naming them
// ("" otherwise).
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
	id := a.agentID
	if id == "" {
		id = a.name
	}
	lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	mine, _ := svc.List(lctx, chatterUID)
	offered, _ := svc.AgentServers(lctx, id)
	servers := mergeServers(mine, offered)
	if len(servers) == 0 {
		return ""
	}
	var signIn *tools.MCPSignIn
	if redirect := urlParam(msg.Params, MCPSignInRedirectParam); redirect != "" {
		name, _ := msg.Params[MCPClientNameParam].(string)
		signIn = &tools.MCPSignIn{RedirectURI: redirect, ClientName: name}
	}
	tools.RegisterCustomMCP(a.registry, svc, chatterUID, servers, a.connectorApprovals(ctx, chatterUID), signIn)
	var ready, own []string
	for _, s := range servers {
		if s.OAuth != nil {
			own = append(own, fmt.Sprintf("%q", s.Name))
		} else {
			ready = append(ready, fmt.Sprintf("%q", s.Name))
		}
	}
	note := "MCP servers available here"
	if len(ready) > 0 {
		note += ": " + strings.Join(ready, ", ")
	}
	if len(own) > 0 {
		if len(ready) > 0 {
			note += "; and"
		}
		note += " ones the person uses with their own account (they sign in the first time; the tool gives you the link): " + strings.Join(own, ", ")
	}
	return note + ". When a request is about one of them, or needs what it offers, use the custom_mcp tool (list_tools first)."
}

// mergeServers is the chatter's own servers, then the agent's offered ones
// they don't already have. The model names servers by name, so an offered
// one whose name the chatter already uses gets a numbered name.
func mergeServers(own, offered []usermcp.Server) []usermcp.Server {
	out := make([]usermcp.Server, 0, len(own)+len(offered))
	ids := map[string]bool{}
	names := map[string]bool{}
	add := func(s usermcp.Server) {
		if ids[s.ID] {
			return
		}
		ids[s.ID] = true
		name := s.Name
		for n := 2; names[strings.ToLower(name)]; n++ {
			name = fmt.Sprintf("%s (%d)", s.Name, n)
		}
		names[strings.ToLower(name)] = true
		s.Name = name
		out = append(out, s)
	}
	for _, s := range own {
		add(s)
	}
	for _, s := range offered {
		add(s)
	}
	return out
}
