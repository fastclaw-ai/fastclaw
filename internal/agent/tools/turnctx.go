package tools

import "context"

// turnctx carries the per-turn identity + session state that tool
// implementations need to scope their side effects (which workspace
// path, which chatter's USER.md row, which chat to route a cron replay
// to, …). It is threaded through context.Context instead of mutating
// shared fields on *Registry, so two concurrent turns on the same agent
// (e.g. a public agent serving two chatters) cannot race on that state.
//
// Mirrors the ctx-tagging convention already used by
// internal/store/chatterctx.go and internal/sandbox/userctx.go: an
// unexported struct-typed key, WithTurnContext as the writer (no-op on
// nil), FromContext as the reader (nil when unset → tools fall back to
// zero values, preserving legacy behavior).
//
// Boot-stable identity (agent owner, skills roots, sandbox config, the
// workspace store handles) stays on *Registry and is NOT duplicated
// here — those never change across turns. Only the values that bindSession
// used to write per-turn belong in TurnContext.

// TurnContext is the per-turn snapshot of identity + scoping. All fields
// are read-only once constructed; build one at the top of a turn and
// attach it to ctx via WithTurnContext.
type TurnContext struct {
	// SessionID is the channel-level chat identifier (msg.ChatID). Used as
	// the session segment of workspace.Store paths so each chat's files
	// are isolated.
	SessionID string
	// ProjectID scopes workspace.Store calls to a project folder when
	// non-empty, overriding the session scope.
	ProjectID string
	// CodingRootScope drops the session segment from workspace scoping so
	// file tools address the project ROOT (the tree a coding-agent dev
	// server serves). Only on for agents with a project runtime.
	CodingRootScope bool
	// CodingSubdir redirects file-tool paths into this subfolder of the
	// scope workspace (the folder a project runtime scaffolds its app
	// into). Empty disables the redirect.
	CodingSubdir string
	// Channel / AccountID / ChatID name the bus address of the in-flight
	// turn. create_cron_job stamps them onto persisted rows so a fired
	// reminder routes back to the originating web/IM thread.
	Channel    string
	AccountID  string
	ChatID     string
	// GoalSessionKey is the durable session.Session key goal tools use to
	// address their rows. Distinct from SessionID (the channel chatID).
	GoalSessionKey string
	// ChatterUserID is the resolved per-turn chatter (IM per-sender
	// app_user, or the UserSpace owner for direct web chats). Per-user
	// files (USER.md / MEMORY.md) route here.
	ChatterUserID string
	// CallerIsAdmin marks this turn's chatter as the agent owner / channel
	// admin. File tools gate identity-file (SOUL.md, …) ops on it.
	CallerIsAdmin bool
}

type turnCtxKey struct{}

// WithTurnContext returns ctx tagged with tc. A nil tc is a no-op so the
// caller doesn't have to nil-check before wrapping — tools then see
// FromContext()==nil and fall back to zero values.
func WithTurnContext(ctx context.Context, tc *TurnContext) context.Context {
	if tc == nil {
		return ctx
	}
	return context.WithValue(ctx, turnCtxKey{}, tc)
}

// FromContext returns the TurnContext attached by WithTurnContext, or nil
// when none is set (background ctx, boot/admin paths, legacy tests). Tool
// implementations must tolerate nil — read fields through turnOrZero,
// never by dereferencing the returned pointer directly.
func FromContext(ctx context.Context) *TurnContext {
	if ctx == nil {
		return nil
	}
	v, _ := ctx.Value(turnCtxKey{}).(*TurnContext)
	return v
}

// turnOrZero returns a pointer to the ctx's TurnContext, or a pointer to
// a zero-valued TurnContext when none is attached. The zero value is the
// correct "no per-turn state" fallback for every field (empty strings,
// false bools) — it matches what a boot/admin path with no chat in flight
// should see. Always returns non-nil so callers can do
// `tc := turnOrZero(ctx); tc.SessionID` without nil-guarding.
func turnOrZero(ctx context.Context) *TurnContext {
	if tc := FromContext(ctx); tc != nil {
		return tc
	}
	return &TurnContext{}
}

// OwnerKeyFor folds the (agentID, chatterUserID, sessionKey) identity
// triple into a single deterministic string used to isolate background
// shells (shellManager.Start/Get). It mirrors the scoping
// workspace.Store uses, so a shell started in one chat can only be
// observed by a later tool call from the same chat/chatter.
//
// Empty agentID means "no agent context" (shouldn't happen in a real
// turn) — the returned key is still well-formed, just less specific.
// Callers that genuinely have no chat tenancy (boot, admin reload, some
// tests) pass "" to opt out of isolation entirely at the Get site.
func OwnerKeyFor(agentID, chatterUserID, sessionKey string) string {
	// A separator that can't appear in any of the three id spaces:
	// agentIDs/chatter ids are "ag_…"/"u_…" and sessionKeys are "s-…",
	// none contain a NUL. NUL also can't be typed via a guessed bash_id,
	// so it doubles as a trivial injection guard.
	return agentID + "\x00" + chatterUserID + "\x00" + sessionKey
}
