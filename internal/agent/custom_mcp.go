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
// this agent offers custom MCP servers (whoever made it chose them) and
// this turn may use them (the same turns as the chatter's connected
// accounts: ownAccountsTurn), and returns the system-prompt note naming
// them ("" otherwise).
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
	servers, err := svc.AgentServers(lctx, id)
	if err != nil || len(servers) == 0 {
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
	note := "This agent offers MCP servers"
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
