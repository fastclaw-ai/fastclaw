// Package billing is FastClaw's side of usage metering and billing
// authorization (docs/usage-metering.md). FastClaw meters; the integrating
// app prices. Every billable thing an agent does is recorded as a usage
// event (usage_events), and an account that registered a billing hook is
// asked before an agent turn and before costly tool calls.
//
// Everything flows through context, like usage.Collector: the agent loop
// starts a Turn at the top of every turn and attaches it with WithTurn;
// tool providers, the sandbox and local agents call Record /
// AuthorizeTool with the ctx they were handed. No turn in ctx → no-op.
//
// This package only depends on internal/store so the leaf packages that
// meter (toolproviders, sandbox, agl) can import it.
package billing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"math"
	"strings"
	"sync"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// Event kinds.
const (
	KindLLM        = "llm"
	KindImage      = "image"
	KindTTS        = "tts"
	KindSearch     = "search"
	KindFetch      = "fetch"
	KindSandbox    = "sandbox"
	KindLocalAgent = "local_agent"
)

// Authorization stages and the "turn" kind.
const (
	StageTurn = "turn"
	StageTool = "tool"
	KindTurn  = "turn"
)

// Store is what metering needs from the platform store (*store.DBStore).
type Store interface {
	InsertMeteredEvent(ctx context.Context, e *store.MeteredEvent) error
	BillingTarget(ctx context.Context, userID string) (store.BillingTarget, error)
}

// Event is one billable thing to record. Who and where come from the Turn.
type Event struct {
	Kind                string
	Provider            string
	Model               string
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	Units               float64
	Unit                string
	// CostUSD is the operator's cost when known; nil = unknown.
	CostUSD *float64
	Detail  map[string]any
}

// Service records events and asks billing hooks. Safe for concurrent use;
// a nil *Service records nothing and allows everything.
type Service struct {
	st   Store
	hook *hookClient
}

// NewService returns a Service backed by st (nil st: metering disabled).
func NewService(st Store) *Service {
	if st == nil {
		return nil
	}
	return &Service{st: st, hook: sharedHookClient()}
}

var (
	hookOnce   sync.Once
	hookShared *hookClient
)

// sharedHookClient is one connection pool for every Service.
func sharedHookClient() *hookClient {
	hookOnce.Do(func() { hookShared = newHookClient() })
	return hookShared
}

// Turn is one agent turn: who ran it, where, and its id. Nested turns
// (sub-agents, message_agent, a /v1 turn authorized before the loop runs)
// share the parent's id and its authorization.
type Turn struct {
	ID         string
	UserID     string // namespace user that ran it
	AgentID    string
	SessionKey string
	Channel    string
	// Prepared marks a turn authorized before its agent loop started
	// (/v1): the loop runs it as is instead of nesting a child turn.
	Prepared bool

	svc *Service

	mu       sync.Mutex
	resolved bool
	target   store.BillingTarget
}

// NewTurn starts a turn with a fresh id. svc may be nil.
func (s *Service) NewTurn(userID, agentID, sessionKey, channel string) *Turn {
	return &Turn{ID: NewID("trn_"), UserID: userID, AgentID: agentID, SessionKey: sessionKey, Channel: channel, svc: s}
}

// Child is a nested turn that keeps t's id (and was authorized with it).
func (t *Turn) Child(svc *Service, userID, agentID, sessionKey, channel string) *Turn {
	if svc == nil {
		svc = t.svc
	}
	return &Turn{ID: t.ID, UserID: userID, AgentID: agentID, SessionKey: sessionKey, Channel: channel, svc: svc}
}

type turnKey struct{}

// WithTurn attaches t to ctx.
func WithTurn(ctx context.Context, t *Turn) context.Context {
	return context.WithValue(ctx, turnKey{}, t)
}

// TurnFrom returns the turn attached to ctx, or nil.
func TurnFrom(ctx context.Context) *Turn {
	if ctx == nil {
		return nil
	}
	t, _ := ctx.Value(turnKey{}).(*Turn)
	return t
}

// TurnID returns the id of the turn in ctx, or "".
func TurnID(ctx context.Context) string {
	if t := TurnFrom(ctx); t != nil {
		return t.ID
	}
	return ""
}

// Record records ev against the turn in ctx. No turn → nothing.
func Record(ctx context.Context, ev Event) {
	if t := TurnFrom(ctx); t != nil {
		t.Record(ctx, ev)
	}
}

// Record records ev for this turn. Never fails the caller: errors are
// logged. One insert, no network.
func (t *Turn) Record(ctx context.Context, ev Event) {
	if t == nil || t.svc == nil {
		return
	}
	t.svc.Record(ctx, Who{UserID: t.UserID, AgentID: t.AgentID, SessionKey: t.SessionKey, Channel: t.Channel, TurnID: t.ID}, ev)
}

// Who is who ran an event and where.
type Who struct {
	UserID, AgentID, SessionKey, Channel, TurnID string
}

// Record inserts one event. Errors are logged, never returned: metering
// must not break the work it meters.
func (s *Service) Record(ctx context.Context, who Who, ev Event) {
	if s == nil || s.st == nil || ev.Kind == "" {
		return
	}
	row := &store.MeteredEvent{
		UserID: who.UserID, AgentID: who.AgentID, SessionKey: who.SessionKey,
		Channel: who.Channel, TurnID: who.TurnID,
		Kind: ev.Kind, Provider: ev.Provider, Model: ev.Model,
		InputTokens: ev.InputTokens, OutputTokens: ev.OutputTokens,
		CacheReadTokens: ev.CacheReadTokens, CacheCreationTokens: ev.CacheCreationTokens,
		Units: math.Round(ev.Units*1000) / 1000, Unit: ev.Unit,
		CostUSD: ev.CostUSD, Detail: ev.Detail,
	}
	// The turn's ctx may already be cancelled (client went away) while
	// the work it paid for finished: record anyway.
	if err := s.st.InsertMeteredEvent(context.WithoutCancel(ctx), row); err != nil {
		slog.Warn("usage event record failed", "kind", ev.Kind, "agent", who.AgentID, "error", err)
	}
}

// billingTarget resolves (once per turn) who pays and their hook.
func (t *Turn) billingTarget(ctx context.Context) (store.BillingTarget, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.resolved {
		return t.target, true
	}
	target, err := t.svc.st.BillingTarget(ctx, t.UserID)
	if err != nil {
		slog.Warn("billing target lookup failed", "user", t.UserID, "error", err)
		return store.BillingTarget{}, false
	}
	t.target, t.resolved = target, true
	return target, true
}

// NewID returns prefix + 24 random hex characters.
func NewID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// SplitRef splits a "<provider>/<model>" tool-provider reference.
func SplitRef(ref string) (provider, model string) {
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}
