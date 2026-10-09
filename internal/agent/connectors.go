package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/connectors"
)

// mountConnectors puts the connectors tool on this turn's registry when
// the turn may use the chatter's connected accounts, and returns the note
// on those accounts for the system prompt ("" when nothing applies).
//
// Only a web turn whose chatter owns this agent: the accounts are THEIRS,
// and that conversation is the only reader of the reply. IM channels,
// groups, cron, the API and other people's agents never get them — an
// agent someone else set up runs on their instructions and could carry
// what it reads elsewhere (docs/connectors.md).
func (a *Agent) mountConnectors(ctx context.Context, msg bus.InboundMessage, chatterUID, sessionKey string) string {
	svc := connectors.Get()
	if svc == nil || a.registry == nil || msg.Channel != "web" || chatterUID == "" || chatterUID != a.trustOwnerID() {
		return ""
	}
	if msg.Source != bus.SourceUser && msg.Source != bus.SourceConnector {
		return ""
	}
	// Never hold up a turn on the connector service.
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, platforms, err := svc.Catalog(cctx, "en", false)
	if err != nil || len(platforms) == 0 {
		return ""
	}
	tools.RegisterConnectors(a.registry, svc, chatterUID, connectors.ChatRef{AgentID: a.name, SessionID: sessionKey}, platforms)
	return connectorAccountsNote(cctx, svc, chatterUID, platforms)
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
