// Package agl links users' own computers to Fastclaw through agl
// (https://github.com/thinkany-ai/agl) and runs private agents' turns on
// them instead of an LLM. See docs/agl-machines.md.
//
// The hub is a port of agl's reference hub (src/hub.rs), mounted at
// <FASTCLAW_PUBLIC_URL>/agl:
//
//   - GET /agl/install.sh?code=…        installer for a one-time pairing code
//   - GET /agl/download/<target>[.sha256] 302 to the agl GitHub release
//   - WS  /agl/ws/machine?code=…|token=…&machine_id=…  machines dial in here
//
// The same endpoints also live under /agl/p/<scope>/ — one hub address
// per integrating product (ProductScope). agl keeps one link per hub URL
// on a computer, so two products sharing this Fastclaw must not hand out
// the same URL, or linking the computer to one would replace the other's
// link. A machine and its pairing code are bound to the address they
// were made for.
//
// Pairing: the product asks for a one-time code (10 min, only its hash
// is stored); the install command carries it; the machine connects with
// ?code=, receives its own token (`paired`, only sha256(token) is
// stored against the code's user), and reconnects with ?token= and a
// `hello`. Revoking clears the hash and closes the socket, so the next
// upgrade fails with 401 and agl stops retrying.
//
// Single instance: machine sockets live in this process. A multi-instance
// deployment needs either agl's HTTP link (`--transport http`, stateless,
// queue in the DB) or a sticky socket holder that other instances reach
// through Redis — the seam is Hub.Request / Hub.Subscribe, which would
// publish to the instance holding the socket (see internal/rediscoord for
// the lease primitive such a holder would use).
package agl

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

//go:embed install.sh
var installScript string

const (
	// PairingTTL is how long a pairing code stays valid.
	PairingTTL = 10 * time.Minute
	// DefaultReleaseURL is where /agl/download redirects.
	DefaultReleaseURL = "https://github.com/thinkany-ai/agl/releases/latest/download"
	// MountPath is where the hub lives under the public URL.
	MountPath = "/agl"

	helloTimeout   = 10 * time.Second
	pingInterval   = 25 * time.Second
	readTimeout    = 75 * time.Second // agl pings every 20s
	writeTimeout   = 10 * time.Second
	requestTimeout = 30 * time.Second
	touchInterval  = time.Minute

	// maxMessageBytes caps one frame from a machine. tool_end events can
	// carry images (≤4 MB base64'd); anything bigger drops the link.
	maxMessageBytes = 8 << 20
	maxAgents       = 64
	maxFieldLen     = 128
	subscriberBuf   = 1024
)

// ErrOffline is returned by Request when the machine has no live link.
var ErrOffline = errors.New("machine is offline")

// Event is one event a machine relayed for a session, or the synthetic
// Disconnected marker sent to subscribers when the link drops.
type Event struct {
	SessionID    string
	Seq          uint64
	Event        AgentEvent
	Disconnected bool
}

// AgentEvent is the subset of agl's event fields Fastclaw reads.
type AgentEvent struct {
	Kind       string   `json:"kind"`
	TurnID     string   `json:"turn_id,omitempty"`
	IntoTurnID string   `json:"into_turn_id,omitempty"`
	Text       string   `json:"text,omitempty"`
	Phase      string   `json:"phase,omitempty"`
	RequestID  string   `json:"request_id,omitempty"`
	Tool       string   `json:"tool,omitempty"`
	Title      string   `json:"title,omitempty"`
	Options    []string `json:"options,omitempty"`
	Decision   string   `json:"decision,omitempty"`
	By         string   `json:"by,omitempty"`
	// DecidedBy (permission_request, agl ≥ 0.3.4): agl answers it itself
	// ("policy" | "session"); never ask the chat.
	DecidedBy string `json:"decided_by,omitempty"`
	Outcome   string `json:"outcome,omitempty"`
	Error     string `json:"error,omitempty"`
	Message   string `json:"message,omitempty"`
	Status    string `json:"status,omitempty"`
	// tool_start: the tool and its arguments.
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// wireMsg is one agl link frame (protocol.md "Framing").
type wireMsg struct {
	Type      string          `json:"type"`
	ID        string          `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    any             `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *string         `json:"error,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Seq       uint64          `json:"seq,omitempty"`
	Event     json.RawMessage `json:"event,omitempty"`
	Machine   *helloMachine   `json:"machine,omitempty"`
	Token     string          `json:"token,omitempty"`
}

type helloMachine struct {
	MachineID string      `json:"machine_id"`
	Hostname  string      `json:"hostname"`
	OS        string      `json:"os"`
	Arch      string      `json:"arch"`
	Version   string      `json:"version"`
	Protocol  int         `json:"protocol"`
	Agents    []agentInfo `json:"agents"`
	StartedAt uint64      `json:"started_at"`
}

// agentInfo is agl's AgentInfo (hello, agents.list), the fields we keep.
type agentInfo struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Available bool     `json:"available"`
	Features  []string `json:"features"`
	Path      string   `json:"path"`
	Version   string   `json:"version"`
	Status    string   `json:"status"`
	// What the agent can do beyond code (agl ≥ 0.3.6).
	Capabilities []string `json:"capabilities"`
	Install      struct {
		InstallCommand string `json:"install_command"`
		Guide          string `json:"guide"`
	} `json:"install"`
}

// hiddenAgents are agl agents FastClaw doesn't offer: never stored, listed
// or dispatched to.
var hiddenAgents = map[string]bool{"droid": true} // Factory Droid

// HiddenAgent reports whether agent id is one FastClaw doesn't offer.
func HiddenAgent(id string) bool { return hiddenAgents[id] }

// machineAgents trims agl's agent list to what we store.
func machineAgents(list []agentInfo) []store.MachineAgent {
	out := make([]store.MachineAgent, 0, len(list))
	for i, a := range list {
		if i >= maxAgents {
			break
		}
		id := clip(a.ID)
		if id == "" || HiddenAgent(id) {
			continue
		}
		name := clip(a.Name)
		if name == "" {
			name = id
		}
		var feats []string
		for j, f := range a.Features {
			if j >= 16 {
				break
			}
			feats = append(feats, clip(f))
		}
		var caps []string
		for j, c := range a.Capabilities {
			if j >= 16 {
				break
			}
			caps = append(caps, clip(c))
		}
		status := clip(a.Status)
		if status == "" { // agl ≤ 0.2.0
			status = "not-found"
			if a.Available {
				status = "ready"
			}
		}
		out = append(out, store.MachineAgent{ID: id, Name: name, Available: a.Available, Features: feats,
			Path: clipN(a.Path, 1024), Version: clip(a.Version), Status: status,
			InstallCommand: clipN(a.Install.InstallCommand, 1024), Guide: clipN(a.Install.Guide, 512), Capabilities: caps})
	}
	return out
}

type response struct {
	result json.RawMessage
	err    error
}

// conn is one live machine link.
type conn struct {
	hub       *Hub
	machineID string
	userID    string
	ws        *websocket.Conn
	send      chan []byte
	closed    chan struct{}
	closeOnce sync.Once

	mu      sync.Mutex
	pending map[string]chan response
}

func (c *conn) close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		_ = c.ws.Close()
		c.mu.Lock()
		for id, ch := range c.pending {
			ch <- response{err: ErrOffline}
			delete(c.pending, id)
		}
		c.mu.Unlock()
	})
}

// Hub holds the machine links of this process.
type Hub struct {
	store      store.MachineStore
	publicURL  string
	releaseURL string
	latest     *latestRelease // newest agl release, for "update available"
	now        func() time.Time
	upgrader   websocket.Upgrader

	counter atomic.Uint64

	mu    sync.Mutex
	conns map[string]*conn // machine id → live link
	subs  map[string]map[uint64]chan Event
}

// NewHub returns a hub backed by st. publicURL is FASTCLAW_PUBLIC_URL
// (scheme://host[:port], no /agl); empty falls back to the request origin.
func NewHub(st store.MachineStore, publicURL string) *Hub {
	return &Hub{
		store:      st,
		publicURL:  strings.TrimRight(strings.TrimSpace(publicURL), "/"),
		releaseURL: DefaultReleaseURL,
		latest:     newLatestRelease(DefaultLatestURL),
		now:        func() time.Time { return time.Now().UTC() },
		upgrader: websocket.Upgrader{
			// Machines are not browsers; auth is the code / token.
			CheckOrigin: func(*http.Request) bool { return true },
		},
		conns: map[string]*conn{},
		subs:  map[string]map[uint64]chan Event{},
	}
}

// SetReleaseURL points /agl/download at a mirror of agl's release assets
// (FASTCLAW_AGL_RELEASE_URL), e.g. a COS/OSS bucket for machines that can't
// reach GitHub. The mirror must serve agl-<target> and agl-<target>.sha256.
// Empty keeps DefaultReleaseURL.
func (h *Hub) SetReleaseURL(u string) {
	if u = strings.TrimRight(strings.TrimSpace(u), "/"); u != "" {
		h.releaseURL = u
	}
}

// RegisterRoutes mounts the hub's endpoints under /agl and under each
// product's /agl/p/{scope}.
func (h *Hub) RegisterRoutes(mux *http.ServeMux) {
	for _, base := range []string{MountPath, MountPath + "/p/{scope}"} {
		mux.HandleFunc("GET "+base+"/{$}", h.handleIndex)
		mux.HandleFunc("GET "+base+"/install.sh", h.handleInstallScript)
		mux.HandleFunc("GET "+base+"/download/{target}", h.handleDownload)
		mux.HandleFunc("GET "+base+"/ws/machine", h.handleMachineWS)
	}
}

// scopeRE is what a product scope looks like (ProductScope).
var scopeRE = regexp.MustCompile(`^[0-9a-f]{16}$`)

// ProductScope is the hub scope of an integrating product's account:
// its machines pair through <public>/agl/p/<scope>. Derived (not the
// raw account id) so the URL doesn't reveal internal ids.
func ProductScope(accountID string) string {
	if accountID == "" {
		return ""
	}
	return sha256Hex("agl-hub-scope:" + accountID)[:16]
}

// requestScope is the scope of the hub address r came in on; ok=false
// for a malformed one.
func requestScope(r *http.Request) (string, bool) {
	sc := r.PathValue("scope")
	if sc == "" {
		return "", true
	}
	return sc, scopeRE.MatchString(sc)
}

// PublicURL is FASTCLAW_PUBLIC_URL, or the origin r was addressed to.
func (h *Hub) PublicURL(r *http.Request) string {
	if h.publicURL != "" {
		return h.publicURL
	}
	if r == nil {
		return "http://localhost"
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = strings.TrimSpace(strings.Split(p, ",")[0])
	}
	host := r.Host
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
		host = strings.TrimSpace(strings.Split(fh, ",")[0])
	}
	return scheme + "://" + host
}

// HubURL is the URL agl connects to: <public URL>/agl.
func (h *Hub) HubURL(r *http.Request) string { return h.ScopedHubURL(r, "") }

// ScopedHubURL is the hub URL of scope: <public URL>/agl/p/<scope>, or
// /agl for "".
func (h *Hub) ScopedHubURL(r *http.Request, scope string) string {
	if scope == "" {
		return h.PublicURL(r) + MountPath
	}
	return h.PublicURL(r) + MountPath + "/p/" + scope
}

// InstallCommand is the one-liner a user runs to link a machine.
func (h *Hub) InstallCommand(r *http.Request, code string) string {
	return h.ScopedInstallCommand(r, code, "")
}

// ScopedInstallCommand is InstallCommand for a code made with scope.
func (h *Hub) ScopedInstallCommand(r *http.Request, code, scope string) string {
	return h.InstallCommandWithAccess(r, code, scope, "")
}

// InstallCommandWithAccess is ScopedInstallCommand linking with access
// (LinkAccess; "" or ask = agl's default). The person sees it in the
// command they run: the hub can't raise a link's access afterwards.
func (h *Hub) InstallCommandWithAccess(r *http.Request, code, scope, access string) string {
	q := "code=" + code
	if a, ok := LinkAccess(access); ok && a != AccessAsk {
		q += "&access=" + a
	}
	return fmt.Sprintf(`curl -fsSL "%s/install.sh?%s" | sh`, h.ScopedHubURL(r, scope), q)
}

// LinkAccess normalizes an access level for `agl connect --access`; ""
// means agl's default (ask). ok is false for anything else.
func LinkAccess(a string) (string, bool) {
	switch a = strings.TrimSpace(a); a {
	case "":
		return AccessAsk, true
	case AccessAsk, AccessEdit, AccessFull, AccessReadOnly:
		return a, true
	}
	return "", false
}

// --- pairing ---

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// codeRE is what a pairing code looks like; anything else is refused
// before touching the store.
var codeRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// CreatePairing mints a one-time code for userID on the /agl hub.
func (h *Hub) CreatePairing(ctx context.Context, userID string) (code string, expiresAt time.Time, err error) {
	return h.CreateScopedPairing(ctx, userID, "")
}

// CreateScopedPairing mints a one-time code for userID whose machine
// links to the hub address of scope (ProductScope; "" = /agl).
func (h *Hub) CreateScopedPairing(ctx context.Context, userID, scope string) (code string, expiresAt time.Time, err error) {
	if scope != "" && !scopeRE.MatchString(scope) {
		return "", time.Time{}, fmt.Errorf("agl: bad hub scope %q", scope)
	}
	code, err = randomHex(16)
	if err != nil {
		return "", time.Time{}, err
	}
	now := h.now()
	expiresAt = now.Add(PairingTTL)
	if err := h.store.CreateMachinePairing(ctx, &store.MachinePairingRecord{
		CodeHash: sha256Hex(code), UserID: userID, ExpiresAt: expiresAt, CreatedAt: now, Scope: scope,
	}); err != nil {
		return "", time.Time{}, err
	}
	return code, expiresAt, nil
}

// Pairing statuses reported by PairingStatus.
const (
	PairingPending   = "pending"
	PairingConnected = "connected"
	PairingExpired   = "expired"
)

// ErrPairingNotFound: no such code for this user.
var ErrPairingNotFound = errors.New("pairing code not found")

// PairingStatus reports a code userID created: connected once the
// machine it paired has said hello, expired after its 10 minutes
// otherwise, pending before.
func (h *Hub) PairingStatus(ctx context.Context, userID, code string) (status, machineID string, err error) {
	code = strings.TrimSpace(code)
	if !codeRE.MatchString(code) {
		return "", "", ErrPairingNotFound
	}
	p, err := h.store.GetMachinePairing(ctx, sha256Hex(code))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", "", ErrPairingNotFound
		}
		return "", "", err
	}
	if p.UserID != userID {
		return "", "", ErrPairingNotFound
	}
	if p.MachineID != "" {
		if m, err := h.store.GetMachine(ctx, p.MachineID); err == nil && !m.Revoked() && (!m.LastSeenAt.IsZero() || h.Online(m.ID)) {
			return PairingConnected, m.ID, nil
		}
	}
	if !h.now().Before(p.ExpiresAt) {
		return PairingExpired, "", nil
	}
	return PairingPending, "", nil
}

// --- HTTP endpoints ---

func (h *Hub) handleIndex(w http.ResponseWriter, r *http.Request) {
	// A plain landing page for anyone who opens <hub>/ by hand; the web
	// app that made the pairing code shows the link itself (pairing/status).
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!doctype html><html lang="zh"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>电脑已连接</title>
<style>body{font-family:-apple-system,system-ui,sans-serif;max-width:32rem;margin:15vh auto;padding:0 16px;color:#222;background:#fff}@media(prefers-color-scheme:dark){body{color:#eee;background:#111}}</style></head>
<body><h1>电脑已连接</h1><p>这台电脑已经通过 agl 连接。回到网页即可继续，可以关闭此页面。</p><p>This computer is linked through agl. You can close this page.</p></body></html>`)
}

func (h *Hub) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	valid, scope := false, ""
	if pathScope, ok := requestScope(r); ok && codeRE.MatchString(code) {
		if p, err := h.store.GetMachinePairing(r.Context(), sha256Hex(code)); err == nil && p.UsedAt.IsZero() && h.now().Before(p.ExpiresAt) && p.Scope == pathScope {
			valid, scope = true, p.Scope
		}
	}
	if !valid {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, "#!/bin/sh\necho 'agl: this install link is invalid or has expired; copy a new one from the web page' >&2\nexit 1\n")
		return
	}
	script := strings.ReplaceAll(installScript, "__HUB_URL__", h.ScopedHubURL(r, scope))
	// --private: this hub sees only the sessions it starts itself, not
	// the ones the person runs locally (agl run, desktop apps) — those
	// are none of a web app's business, and with several apps linked to
	// one computer each would otherwise see them all.
	args := "--code " + code + " --private"
	// access: what the person chose on the web page; it decides which of
	// agl's built-in tools (read/write/edit/exec) this hub may call.
	if a, ok := LinkAccess(r.URL.Query().Get("access")); ok && a != AccessAsk {
		args += " --access " + a
	}
	script = strings.ReplaceAll(script, "__CONNECT_ARGS__", args)
	fmt.Fprint(w, script)
}

var targetRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (h *Hub) handleDownload(w http.ResponseWriter, r *http.Request) {
	target := r.PathValue("target")
	suffix := ""
	if t, ok := strings.CutSuffix(target, ".sha256"); ok {
		target, suffix = t, ".sha256"
	}
	if !targetRE.MatchString(target) {
		http.Error(w, "bad target", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("%s/agl-%s%s", h.releaseURL, target, suffix), http.StatusFound)
}

func (h *Hub) handleMachineWS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	aglID := q.Get("machine_id")
	if aglID == "" || len(aglID) > 64 {
		http.Error(w, "machine_id required", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	scope, ok := requestScope(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if code := q.Get("code"); code != "" {
		// Pairing: validate and consume the code BEFORE upgrading, so a
		// bad code is a plain 401 (agl treats it as final). A code only
		// pairs at the hub address it was made for, so agl stores the
		// link under that URL.
		if !codeRE.MatchString(code) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if p, err := h.store.GetMachinePairing(ctx, sha256Hex(code)); err != nil || p.Scope != scope {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		p, err := h.store.ConsumeMachinePairing(ctx, sha256Hex(code), h.now())
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ws, err := h.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		h.pair(context.WithoutCancel(ctx), ws, p, aglID)
		return
	}
	token := q.Get("token")
	if token == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	m, err := h.store.GetMachineByTokenHash(ctx, sha256Hex(token))
	if err != nil || m.Revoked() || m.AglMachineID != aglID || m.HubScope != scope {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ws, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	h.serve(ws, m)
}

// pair issues the machine its own token for a consumed code.
func (h *Hub) pair(ctx context.Context, ws *websocket.Conn, p *store.MachinePairingRecord, aglID string) {
	defer ws.Close()
	token, err := randomHex(32)
	if err != nil {
		return
	}
	token = "aglm_" + token
	hash := sha256Hex(token)
	// The same computer paired again by the same user keeps its id (and
	// the agents bound to it); its old token stops working. Its scope
	// moves to the code's: a link made at the old address then gets 401
	// and agl drops it.
	machineID := ""
	if prev, err := h.store.FindActiveMachine(ctx, p.UserID, aglID); err == nil && prev != nil {
		if err := h.store.SetMachineToken(ctx, prev.ID, hash, p.Scope); err != nil {
			slog.Warn("agl: re-pair failed", "machine", prev.ID, "error", err)
			return
		}
		machineID = prev.ID
		h.disconnect(prev.ID)
	} else {
		id, err := randomHex(10)
		if err != nil {
			return
		}
		machineID = "mch_" + id
		if err := h.store.CreateMachine(ctx, &store.MachineRecord{
			ID: machineID, UserID: p.UserID, AglMachineID: aglID, TokenHash: hash, CreatedAt: h.now(),
			AllowAgents: true, DefaultAccess: AccessAsk, HubScope: p.Scope,
		}); err != nil {
			slog.Warn("agl: create machine failed", "error", err)
			return
		}
	}
	h.revokeReplaced(ctx, machineID, aglID, p.Scope)
	if err := h.store.SetMachinePairingMachine(ctx, p.CodeHash, machineID); err != nil {
		slog.Warn("agl: record pairing failed", "machine", machineID, "error", err)
	}
	blob, _ := json.Marshal(wireMsg{Type: "paired", Token: token})
	_ = ws.SetWriteDeadline(time.Now().Add(writeTimeout))
	_ = ws.WriteMessage(websocket.TextMessage, blob)
	_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	slog.Info("agl: machine paired", "machine", machineID, "user", p.UserID)
}

// revokeReplaced revokes other accounts' records of the same computer at
// the same hub address. agl keeps one link per hub URL, so pairing there
// for another account replaced theirs on the computer: their record
// could never come online again, and would only sit there "offline".
func (h *Hub) revokeReplaced(ctx context.Context, keepID, aglID, scope string) {
	others, err := h.store.ListActiveMachinesByAglID(ctx, aglID)
	if err != nil {
		slog.Warn("agl: list machines for re-pair failed", "agl_machine", aglID, "error", err)
		return
	}
	for i := range others {
		m := &others[i]
		if m.ID == keepID || m.HubScope != scope {
			continue
		}
		if err := h.store.RevokeMachine(ctx, m.ID, h.now()); err != nil && !errors.Is(err, store.ErrNotFound) {
			slog.Warn("agl: revoke replaced machine failed", "machine", m.ID, "error", err)
			continue
		}
		h.disconnect(m.ID)
		slog.Info("agl: machine linked to another account; revoked the old record", "machine", m.ID, "user", m.UserID, "by", keepID)
	}
}

// clip trims untrusted strings from a machine.
func clip(s string) string { return clipN(s, maxFieldLen) }

// clipN trims s to at most n bytes of valid UTF-8.
func clipN(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// serve runs an authenticated machine link until it ends.
func (h *Hub) serve(ws *websocket.Conn, m *store.MachineRecord) {
	ws.SetReadLimit(maxMessageBytes)
	_ = ws.SetReadDeadline(time.Now().Add(helloTimeout))
	_, data, err := ws.ReadMessage()
	if err != nil {
		ws.Close()
		return
	}
	var hello wireMsg
	if json.Unmarshal(data, &hello) != nil || hello.Type != "hello" || hello.Machine == nil {
		ws.Close()
		return
	}
	ctx := context.Background()
	h.applyHello(ctx, m, hello.Machine)

	c := &conn{
		hub: h, machineID: m.ID, userID: m.UserID, ws: ws,
		send: make(chan []byte, 64), closed: make(chan struct{}),
		pending: map[string]chan response{},
	}
	h.mu.Lock()
	old := h.conns[m.ID]
	h.conns[m.ID] = c
	h.mu.Unlock()
	if old != nil {
		old.close()
	}
	slog.Info("agl: machine connected", "machine", m.ID, "host", m.Hostname, "version", m.Version)

	go c.writeLoop()
	c.readLoop()

	c.close()
	h.mu.Lock()
	if h.conns[m.ID] == c {
		delete(h.conns, m.ID)
	}
	h.mu.Unlock()
	_ = h.store.TouchMachine(ctx, m.ID, h.now())
	h.fanout(m.ID, Event{Disconnected: true})
	slog.Info("agl: machine disconnected", "machine", m.ID)
}

func (h *Hub) applyHello(ctx context.Context, m *store.MachineRecord, hm *helloMachine) {
	// The name follows the hostname until the user renames the machine
	// (PATCH /v1/machines/{id}).
	host := clip(hm.Hostname)
	if m.Name == "" || m.Name == m.Hostname {
		m.Name = host
	}
	m.Hostname = host
	m.OS = clip(hm.OS)
	if m.OS == "darwin" {
		m.OS = "macos"
	}
	m.Arch = clip(hm.Arch)
	m.Version = clip(hm.Version)
	m.Agents = machineAgents(hm.Agents)
	m.LastSeenAt = h.now()
	if err := h.store.UpdateMachineHello(ctx, m); err != nil {
		slog.Warn("agl: save hello failed", "machine", m.ID, "error", err)
	}
}

func (c *conn) writeLoop() {
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-c.closed:
			return
		case msg := <-c.send:
			_ = c.ws.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := c.ws.WriteMessage(websocket.TextMessage, msg); err != nil {
				c.close()
				return
			}
		case <-ping.C:
			if err := c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)); err != nil {
				c.close()
				return
			}
		}
	}
}

func (c *conn) readLoop() {
	h := c.hub
	extend := func() { _ = c.ws.SetReadDeadline(time.Now().Add(readTimeout)) }
	extend()
	c.ws.SetPongHandler(func(string) error { extend(); return nil })
	c.ws.SetPingHandler(func(data string) error {
		extend()
		err := c.ws.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(writeTimeout))
		if errors.Is(err, websocket.ErrCloseSent) {
			return nil
		}
		return err
	})
	lastTouch := time.Now()
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		extend()
		if time.Since(lastTouch) > touchInterval {
			lastTouch = time.Now()
			_ = h.store.TouchMachine(context.Background(), c.machineID, h.now())
		}
		var msg wireMsg
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "response":
			c.mu.Lock()
			ch := c.pending[msg.ID]
			delete(c.pending, msg.ID)
			c.mu.Unlock()
			if ch != nil {
				res := response{result: msg.Result}
				if msg.Error != nil {
					res.err = errors.New(clipError(*msg.Error))
				}
				ch <- res
			}
		case "event":
			var ev AgentEvent
			if msg.SessionID == "" || json.Unmarshal(msg.Event, &ev) != nil || ev.Kind == "" {
				continue
			}
			h.fanout(c.machineID, Event{SessionID: msg.SessionID, Seq: msg.Seq, Event: ev})
		case "hello":
			if msg.Machine != nil {
				if m, err := h.store.GetMachine(context.Background(), c.machineID); err == nil {
					h.applyHello(context.Background(), m, msg.Machine)
				}
			}
		}
	}
}

func clipError(s string) string {
	if len(s) > 500 {
		s = s[:500]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

// Online reports whether machineID has a live link.
func (h *Hub) Online(machineID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.conns[machineID]
	return ok
}

func (h *Hub) disconnect(machineID string) {
	h.mu.Lock()
	c := h.conns[machineID]
	delete(h.conns, machineID)
	h.mu.Unlock()
	if c != nil {
		_ = c.ws.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "revoked"), time.Now().Add(time.Second))
		c.close()
	}
}

// Revoke deletes machineID's token hash and closes its link. The next
// upgrade with the old token fails with 401.
func (h *Hub) Revoke(ctx context.Context, machineID string) error {
	if err := h.store.RevokeMachine(ctx, machineID, h.now()); err != nil {
		return err
	}
	h.disconnect(machineID)
	return nil
}

// Request sends method to machineID and waits for its response.
func (h *Hub) Request(ctx context.Context, machineID, method string, params any) (json.RawMessage, error) {
	h.mu.Lock()
	c := h.conns[machineID]
	h.mu.Unlock()
	if c == nil {
		return nil, ErrOffline
	}
	id := fmt.Sprintf("fc%d", h.counter.Add(1))
	ch := make(chan response, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	blob, err := json.Marshal(wireMsg{Type: "request", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, requestTimeout)
		defer cancel()
	}
	select {
	case c.send <- blob:
	case <-c.closed:
		return nil, ErrOffline
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case res := <-ch:
		return res.result, res.err
	case <-c.closed:
		return nil, ErrOffline
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Subscribe receives machineID's events until cancel is called. A slow
// subscriber loses events rather than stalling the link.
func (h *Hub) Subscribe(machineID string) (<-chan Event, func()) {
	id := h.counter.Add(1)
	ch := make(chan Event, subscriberBuf)
	h.mu.Lock()
	if h.subs[machineID] == nil {
		h.subs[machineID] = map[uint64]chan Event{}
	}
	h.subs[machineID][id] = ch
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs[machineID], id)
			if len(h.subs[machineID]) == 0 {
				delete(h.subs, machineID)
			}
			h.mu.Unlock()
		})
	}
}

func (h *Hub) fanout(machineID string, ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs[machineID] {
		select {
		case ch <- ev:
		default:
			slog.Warn("agl: subscriber lagging, event dropped", "machine", machineID, "kind", ev.Event.Kind)
		}
	}
}
