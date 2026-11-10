package agent

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/connectors"
)

// EndUserConnectorGate reports whether chatterUID — an end user of the
// account that owns agentID (X-Fastclaw-End-User) — may use their own
// connected accounts on that agent: it is one of THEIR agents, so its
// instructions are theirs. The gateway decides from the agent record.
type EndUserConnectorGate func(ctx context.Context, agentID, chatterUID string) bool

// ConnectorsURLParam is the turn param (set by the calling product's
// backend, never the model) naming the product's own page where an end
// user connects accounts. Their chat (an IM) can't show a connect card,
// so request_connection hands the model this link instead.
const ConnectorsURLParam = "__fastclawConnectorsURL"

// ConnectorsReturnURLParam (optional) is where the platform sends the end
// user after authorizing a link sent in their chat; defaults to
// ConnectorsURLParam. Use a page that works without signing in: the
// person opens it in their IM's browser.
const ConnectorsReturnURLParam = "__fastclawConnectorsReturnURL"

// mountConnectors puts the connectors tool on this turn's registry when
// the turn may use the chatter's connected accounts, and returns the note
// on those accounts for the system prompt ("" when nothing applies).
//
// Two cases, both a 1:1 conversation whose only reader is the accounts'
// owner, on an agent whose instructions are theirs:
//   - a web turn whose chatter owns this agent;
//   - an end user's own turn (web or API) on an agent the account marked
//     as theirs (EndUserConnectorGate), e.g. WeClaw's "my agents".
//
// IM channels, groups, cron and other people's agents never get them — an
// agent someone else set up runs on their instructions and could carry
// what it reads elsewhere (docs/connectors.md).
func (a *Agent) mountConnectors(ctx context.Context, msg bus.InboundMessage, chatterUID, sessionKey string) string {
	if a.registry == nil {
		return ""
	}
	// A previous turn's mount (bound to that turn's chatter) never
	// carries over: each turn decides afresh below.
	a.registry.Unregister(tools.ConnectorsToolName)
	if chatterUID == "" {
		return ""
	}
	if msg.Source != bus.SourceUser && msg.Source != bus.SourceConnector {
		return ""
	}
	var links *tools.ConnectLinks
	switch {
	case msg.Channel == "web" && chatterUID == a.trustOwnerID():
	case a.endUserTurnMayUseConnectors(ctx, msg, chatterUID):
		links = connectLinks(msg.Params)
	default:
		return ""
	}
	// Their own Connany when they saved one, else the deployment's.
	svc := connectors.For(ctx, chatterUID)
	if svc == nil {
		return ""
	}
	// Never hold up a turn on the connector service.
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, platforms, err := svc.Catalog(cctx, "en", false)
	if err != nil || len(platforms) == 0 {
		return ""
	}
	tools.RegisterConnectors(a.registry, svc, chatterUID, connectors.ChatRef{AgentID: a.name, SessionID: sessionKey}, platforms, links)
	return connectorAccountsNote(cctx, svc, chatterUID, platforms)
}

// endUserTurnMayUseConnectors: an end user's own direct turn on one of
// their agents. The channel is the product's (web, or its backend over the
// API relaying the person's IM chat); never a group.
func (a *Agent) endUserTurnMayUseConnectors(ctx context.Context, msg bus.InboundMessage, chatterUID string) bool {
	if a.endUserConnectors == nil || msg.PeerKind != "dm" || chatterUID == a.trustOwnerID() {
		return false
	}
	if msg.Channel != "web" && msg.Channel != "api" {
		return false
	}
	id := a.agentID
	if id == "" {
		id = a.name
	}
	return a.endUserConnectors(ctx, id, chatterUID)
}

// connectLinks: the product's pages from the turn params. Nil (cards, as
// on the web) when the product named no connect page.
func connectLinks(params map[string]any) *tools.ConnectLinks {
	page := urlParam(params, ConnectorsURLParam)
	if page == "" {
		return nil
	}
	ret := urlParam(params, ConnectorsReturnURLParam)
	if ret == "" {
		ret = page
	}
	return &tools.ConnectLinks{PageURL: page, ReturnURL: ret}
}

// urlParam is a turn param when it is an http(s) URL.
func urlParam(params map[string]any, key string) string {
	raw, _ := params[key].(string)
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return ""
	}
	return u.String()
}

// connectorAccountsNote states the person's platforms at the start of a
// turn. Waiting for the model to look didn't work in fleet: an agent whose
// memory said "I keep their todos" never called the tool and wrote "my
// todo list" into its notes, though the person meant their Todoist. ""
// (and no service call) for someone who never connected anything.
func connectorAccountsNote(ctx context.Context, svc *connectors.Service, userID string, platforms []connectors.Connector) string {
	ever, err := svc.EverConnected(ctx, userID)
	if err != nil || len(ever) == 0 {
		return ""
	}
	accounts, err := svc.Accounts(ctx, userID, false)
	if err != nil {
		return ""
	}
	title := map[string]string{}
	for _, p := range platforms {
		title[p.Name] = p.Title
	}
	byConnector := map[string][]string{}
	var order []string
	for _, acct := range accounts {
		if _, seen := byConnector[acct.Connector]; !seen {
			order = append(order, acct.Connector)
		}
		byConnector[acct.Connector] = append(byConnector[acct.Connector], fmt.Sprintf("%q", acct.Name))
	}
	var parts []string
	if len(order) > 0 {
		var live []string
		for _, c := range order {
			live = append(live, fmt.Sprintf("%s (%s)", firstNonEmpty(title[c], c), strings.Join(byConnector[c], ", ")))
		}
		parts = append(parts, "The person's connected accounts: "+strings.Join(live, "; ")+".")
	}
	var gone []string
	for _, c := range ever {
		if _, live := byConnector[c]; !live && title[c] != "" {
			gone = append(gone, title[c])
		}
	}
	if len(gone) > 0 {
		parts = append(parts, "Connected before, disconnected now: "+strings.Join(gone, ", ")+".")
	}
	parts = append(parts, `When a request is about one of these platforms — "my todo list" when they use Todoist, "my docs" when they use Notion — use the connectors tool, not your own notes. For a disconnected one, tell them it is disconnected and call request_connection. Your notes and memory are not their accounts: never answer from them as if they were.`)
	return strings.Join(parts, " ")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// HandleConnectorWake runs a turn on a web conversation for a [connector]
// note — the person finished (or cancelled) an authorization the agent
// asked for. Same session resolution as HandleWebChatStream; the note is
// runtime-authored (SourceConnector), so it never shows as a user bubble.
func (a *Agent) HandleConnectorWake(ctx context.Context, sessionID, userID, text string) string {
	channel, accountID, chatID, projectID := a.recoverWebTriple(sessionID)
	return a.HandleMessage(ctx, bus.InboundMessage{
		Channel:   channel,
		AccountID: accountID,
		ChatID:    chatID,
		ProjectID: projectID,
		UserID:    userID,
		Text:      text,
		PeerKind:  "dm",
		Source:    bus.SourceConnector,
	})
}
