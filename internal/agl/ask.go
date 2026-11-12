package agl

import (
	"context"
	"fmt"
	"time"
)

// ApprovalAsk asks the person in a chat to approve one action of a tool
// that runs on the server — a built-in tool held on their computer, or a
// write on one of their connected accounts (connectors). It shares the
// chat's single open request with run_local_agent, so every surface that
// answers those (the web chat's buttons, a 1/2/3 reply in an IM, POST
// /v1/local-agents/approval) answers this one too.
type ApprovalAsk struct {
	Chat   ToolChat
	UserID string // the only person who may answer
	// Agent and Machine are display names on the card ("Notion", "").
	Agent   string
	Machine string
	Title   string // what it asks permission for; clipped to 300 characters
	// Notify, when set, receives the request and its outcome (web chat,
	// /v1 stream); nil sends IM text to Chat instead.
	Notify func(ToolNotice)
	// Wait caps how long it waits for the answer (default 10 minutes).
	Wait time.Duration
}

// Decisions Ask returns besides those of Decide.
const DecisionTimeout = "timeout"

// errApprovalBusy: the chat already has an open request.
var errApprovalBusy = &ToolError{Msg: "Another approval is still waiting for the person's answer in this chat. Wait for it, then try again."}

// Ask posts the request in the chat and waits for the person's decision:
// allow, allow_session, deny, or DecisionTimeout (already reported to the
// chat). An error means nothing was asked or the turn ended while waiting.
func (t *Tools) Ask(ctx context.Context, a ApprovalAsk) (string, error) {
	if t == nil {
		return "", &ToolError{Msg: "Approvals are not available on this server."}
	}
	if a.Chat == (ToolChat{}) || a.UserID == "" {
		return "", &ToolError{Msg: "There is no chat to ask the person in."}
	}
	title := clipText(a.Title, 300)
	answer := make(chan string, 1)
	p := &toolApproval{userID: a.UserID, chat: a.Chat, title: title, agent: a.Agent, machine: a.Machine, notify: a.Notify, answer: answer}
	key := a.Chat.key()
	t.mu.Lock()
	if t.pending[key] != nil {
		t.mu.Unlock()
		return "", errApprovalBusy
	}
	t.pending[key] = p
	t.mu.Unlock()
	drop := func() {
		t.mu.Lock()
		if t.pending[key] == p {
			delete(t.pending, key)
		}
		t.mu.Unlock()
	}
	if a.Notify != nil {
		a.Notify(ToolNotice{Kind: NoticeApproval, Agent: a.Agent, Machine: a.Machine, Text: title})
	} else {
		t.send(a.Chat, fmt.Sprintf(MsgApproval, title))
	}
	wait := a.Wait
	if wait <= 0 {
		wait = builtinApprovalWait
	}
	timer := time.NewTimer(max(wait, time.Second))
	defer timer.Stop()
	select {
	case decision := <-answer:
		return decision, nil
	case <-timer.C:
		drop()
		if a.Notify != nil {
			a.Notify(ToolNotice{Kind: NoticeApprovalResolved, Agent: a.Agent, Machine: a.Machine, Decision: DecisionTimeout})
		} else {
			t.send(a.Chat, MsgApprovalTimeout)
		}
		return DecisionTimeout, nil
	case <-ctx.Done():
		drop()
		return "", ctx.Err()
	}
}
