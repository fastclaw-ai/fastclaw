package agl

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// "2 本会话都允许" must mean every later request in that agl session is
// allowed. Agents don't do that themselves: Claude Code's stdio prompt
// asks again for each new command, and agl answers allow_session there
// like a one-off allow. So Fastclaw remembers the grant per agl session
// (one machine, one local agent, one chat) and answers the session's
// later requests itself, without asking the chat again.

const (
	grantTTL       = 24 * time.Hour
	maxGrants      = 10000
	autoApproveFor = 30 * time.Second
)

type sessionGrants struct {
	mu sync.Mutex
	at map[string]time.Time // machineID + "\x00" + sessionID → granted at
}

func newSessionGrants() *sessionGrants { return &sessionGrants{at: map[string]time.Time{}} }

func grantKey(machineID, sessionID string) string { return machineID + "\x00" + sessionID }

// note records decision for the session; only allow_session grants.
func (g *sessionGrants) note(machineID, sessionID, decision string) {
	if decision != "allow_session" || machineID == "" || sessionID == "" {
		return
	}
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.at) >= maxGrants {
		for k, t := range g.at {
			if now.Sub(t) > grantTTL || len(g.at) >= maxGrants {
				delete(g.at, k)
			}
		}
	}
	g.at[grantKey(machineID, sessionID)] = now
}

func (g *sessionGrants) has(machineID, sessionID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	t, ok := g.at[grantKey(machineID, sessionID)]
	if ok && time.Since(t) > grantTTL {
		delete(g.at, grantKey(machineID, sessionID))
		return false
	}
	return ok
}

// autoApprove answers a request in a granted session. It runs off the
// event loop, so the loop keeps draining events meanwhile.
func autoApprove(link Link, machineID, sessionID, requestID string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), autoApproveFor)
		defer cancel()
		if err := approve(ctx, link, machineID, &pendingApproval{sessionID: sessionID, requestID: requestID}, "allow_session"); err != nil {
			slog.Warn("agl: session auto-approve failed", "machine", machineID, "session", sessionID, "error", err)
		}
	}()
}
