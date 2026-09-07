package session

import (
	"bufio"
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// Session holds the message history for one conversation thread within
// a (channel, accountID, chatID) triple. session_key is the per-session
// opaque id; the triple identifies "where" the conversation lives.
type Session struct {
	mu               sync.Mutex
	Messages         []provider.Message
	LastConsolidated int // index of last consolidated message
	filePath         string
	snapshot         []provider.Message // undo snapshot
	store            SessionStore
	userID           string
	agentID          string
	sessionKey       string
	channel          string
	accountID        string
	chatID           string
	// projectID, when non-empty, is stamped on every SaveSession write
	// for this session. Set on the FIRST turn of a brand-new chat that
	// arrived with a project hint (URL `?project=<pid>`); for existing
	// rows it's read back via Manager.Get and late-bound here so the
	// next save preserves it.
	projectID string
	// chatterUserID is the per-turn conversation participant — distinct
	// from userID (UserSpace owner = channel binder) whenever an IM
	// channel routes a per-sender app_user into a channel-owner
	// UserSpace. Set per-turn by the agent loop via SetChatter so the
	// ctx() embeds it for DBStore session writes (sessions.chatter_user_id /
	// session_messages.chatter_user_id / session_events.chatter_user_id).
	// Empty when the caller hasn't bound a chatter — writes leave the
	// column '' and readers fall back to user_id.
	chatterUserID string
	// provider and model are stamped onto assistant messages by
	// Append so session_messages rows record which LLM produced them.
	// Set per-turn by the agent loop via SetProviderModel.
	provider string
	model    string

	// Steering: turnDepth counts in-flight HandleMessage turns for this
	// session (a counter, not a bool, so re-entrant/overlapping turns
	// don't strand the active flag). steerBuf holds user messages that
	// arrived mid-turn; the running ReAct loop drains them between tool
	// iterations. Both are guarded by mu. getByKey never touches these,
	// so a Manager.Get reload (which overwrites Messages) can't clobber a
	// pending steer.
	turnDepth int
	steerBuf  []provider.Message
}

// SessionKey returns the opaque session_key this Session is bound to.
// Exposed so per-turn plumbing (e.g. the tool registry binding for
// goal-scoped tools) can address the right row without re-resolving
// the (channel, account, chat) quadruple every time.
func (s *Session) SessionKey() string { return s.sessionKey }

// ctx returns a context tagged with this Session's user so the store layer
// can scope SQL by user_id. Falls back to context.Background() when no
// user is set; the store will then default to config.DefaultUserID.
//
// Also embeds the per-turn chatter (when set) so DBStore session writes
// (sessions.chatter_user_id / session_messages.chatter_user_id /
// session_events.chatter_user_id) can record the actual conversation
// participant. user_id stays = UserSpace owner; chatter is the
// additional dimension. Both tags are independent — empty chatter
// just leaves the column ”.
func (s *Session) ctx() context.Context {
	ctx := context.Background()
	if s.userID != "" {
		ctx = config.WithUserID(ctx, s.userID)
	}
	if s.chatterUserID != "" {
		ctx = store.WithChatterUserID(ctx, s.chatterUserID)
	}
	return ctx
}

// SetChatter binds the per-turn conversation participant to this
// Session so the next Append / SaveSession write stamps the
// chatter_user_id column. Called by the agent loop at the top of each
// turn from the resolved chatterUID. Passing "" clears it (the next
// write goes back to ” which readers fall back to user_id for).
func (s *Session) SetChatter(uid string) {
	s.mu.Lock()
	s.chatterUserID = uid
	s.mu.Unlock()
}

// SetProviderModel binds the current LLM provider and model to this
// Session so Append stamps them onto assistant messages. Called by the
// agent loop alongside SetChatter.
func (s *Session) SetProviderModel(prov, mdl string) {
	s.mu.Lock()
	s.provider = prov
	s.model = mdl
	s.mu.Unlock()
}

// Manager manages sessions for one (user, agent). Sessions are keyed
// internally by an opaque session_key; the (channel, accountID, chatID)
// triple is what callers use to address "the conversation thread the
// user is in right now". The active session for that triple is the
// most recently updated row — `/new` mints a fresh one to start over.
//
// SessionStore is the optional persistence interface (DB-backed in
// production; nil in file-only mode for single-binary dev installs).
//
// Two parallel persistence shapes:
//   - GetSession / SaveSession operate on the LLM-facing working set
//     (post-compaction). This is what the agent loop reads/writes every
//     turn.
//   - AppendMessage / ListMessages operate on the append-only per-turn
//     archive (session_messages table). Compaction never touches it, so
//     UI history / audit reads see the original conversation regardless
//     of how many times the working set has been pruned/summarized.
type SessionStore interface {
	GetSession(ctx context.Context, agentID, sessionKey string) ([]provider.Message, error)
	SaveSession(ctx context.Context, agentID, sessionKey, channel, accountID, chatID, projectID string, messages []provider.Message) error
	AppendMessage(ctx context.Context, agentID, sessionKey string, msg provider.Message) error
	ListMessages(ctx context.Context, agentID, sessionKey string) ([]provider.Message, error)
	ListWebSessions(ctx context.Context, agentID string) ([]WebSession, error)
	DeleteSession(ctx context.Context, agentID, sessionKey string) error
	RenameSession(ctx context.Context, agentID, sessionKey, title string) error
	// MoveSession reassigns a session to a different project (or
	// detaches when projectID is ""). Used by the sidebar drag-and-drop
	// affordance; workspace file migration is the caller's job.
	MoveSession(ctx context.Context, agentID, sessionKey, projectID string) error
	// ResolveActiveSessionKey returns the most recent session_key for the
	// (channel, accountID, chatID) triple, or empty string if none.
	ResolveActiveSessionKey(ctx context.Context, agentID, channel, accountID, chatID string) (string, error)
	// LookupSessionTriple is the inverse — given a session_key, return
	// the conversation it belongs to. Returns ("","","",nil) when the
	// session doesn't exist (manager treats that as "not yet stored").
	LookupSessionTriple(ctx context.Context, agentID, sessionKey string) (channel, accountID, chatID string, err error)
	// LookupSessionProject returns the project_id stamped on the session
	// row, or "" for loose chats. Used by the agent runtime to thread
	// project context onto inbound messages so the workspace store and
	// sandbox both route to projects/<pid>/.
	LookupSessionProject(ctx context.Context, agentID, sessionKey string) (string, error)
}

type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	dataDir  string
	store    SessionStore
	userID   string
	agentID  string
}

func NewManager(dataDir string) *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
		dataDir:  dataDir,
	}
}

// NewManagerWithStoreForUser is the user-scoped constructor. Caller MUST
// supply a real user_id resolved from auth. We log and keep the Manager
// alive on empty input so a bad request cannot crash the whole gateway;
// downstream store calls will fail closed under the empty owner.
func NewManagerWithStoreForUser(dataDir string, st SessionStore, userID, agentID string) *Manager {
	if userID == "" {
		fmt.Fprintf(os.Stderr, "session.NewManagerWithStoreForUser: empty userID for agent %q\n", agentID)
	}
	return &Manager{
		sessions: make(map[string]*Session),
		dataDir:  dataDir,
		store:    st,
		userID:   userID,
		agentID:  agentID,
	}
}

// ctx returns a context tagged with this Manager's user for store calls.
func (m *Manager) ctx() context.Context {
	if m.userID == "" {
		return context.Background()
	}
	return config.WithUserID(context.Background(), m.userID)
}

// generateSessionKey mints an opaque session_key for a fresh
// conversation thread. The same generator is used regardless of channel
// — `s-<unix_ms>-<rand>`. The (channel, accountID, chatID) triple is
// stored alongside in dedicated columns; the literal session_key string
// no longer encodes channel info, so a `/new` command in IM can mint a
// second key under the same triple without colliding.
func generateSessionKey() string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	var rand6 [6]byte
	if _, err := cryptorand.Read(rand6[:]); err != nil {
		// fall back to time-derived bytes — collision is extremely
		// unlikely once the timestamp prefix is in play
		now := time.Now().UnixNano()
		for i := range rand6 {
			rand6[i] = byte(now >> (i * 8))
		}
	}
	suffix := make([]byte, len(rand6))
	for i, b := range rand6 {
		suffix[i] = alphabet[int(b)%len(alphabet)]
	}
	return fmt.Sprintf("s-%d-%s", time.Now().UnixMilli(), suffix)
}

// resolveOrMintKey picks the active session_key for (channel,
// accountID, chatID) from the store, or mints a fresh one when nothing
// exists yet (the very first message in a conversation). Pre-existing
// rows from before the channel-triple migration may carry a key like
// `web_<sid>` or `wechat_<openid>` — they're matched by the backfilled
// triple, not by parsing the key, so the legacy format keeps working.
//
// New-row mint policy:
//   - web: session_key == chatID. Web's chatID *is* the per-conversation
//     identifier (the frontend generates one per "+New chat") so making
//     it equal the session_key keeps the URL `?session=` token stable
//     across reloads — no "URL changed after first message" surprises.
//   - everywhere else: mint an opaque `s-<unix_ms>-<rand>`. IM channels
//     reuse one chatID (the user's openid / chat_id) across many
//     sessions, so the session_key has to be independent for `/new` to
//     produce a sibling row.
func (m *Manager) resolveOrMintKey(channel, accountID, chatID string) string {
	if m.store != nil {
		if k, err := m.store.ResolveActiveSessionKey(m.ctx(), m.agentID, channel, accountID, chatID); err == nil && k != "" {
			return k
		}
	}
	if channel == "web" && chatID != "" {
		return chatID
	}
	return generateSessionKey()
}

// Get returns or creates the active session for the (channel, accountID,
// chatID) triple. The session_key is resolved server-side rather than
// derived from the inputs — see resolveOrMintKey.
//
// projectID is the "this chat belongs to project X" hint from the chat
// request (URL `?project=<pid>`). It only matters on first save: if the
// session row already has project_id stored, that wins; if the row is
// brand new, this hint is what gets persisted.
//
// In multi-replica deployments (store-backed mode), every Get() reloads
// Messages from the store so a request served by pod B sees writes made
// by pod A. Without this, each pod's in-memory cache drifts away from
// Postgres: the first refresh after a cross-pod write returns whichever
// pod-local snapshot happened to be warm. We deliberately overwrite
// Messages on the cached Session rather than re-creating the struct so
// transient fields (snapshot, LastConsolidated) survive.
//
// File-backed mode stays cache-first since there's only one process.
func (m *Manager) Get(channel, accountID, chatID, projectID string) *Session {
	key := m.resolveOrMintKey(channel, accountID, chatID)
	return m.getByKey(key, channel, accountID, chatID, projectID)
}

// GetByKey loads a specific session by its session_key. Used when the
// caller already has a key in hand (e.g. web history fetch from a URL
// `?session=…`) and wants to bypass the active-session lookup.
func (m *Manager) GetByKey(sessionKey string) *Session {
	return m.getByKey(sessionKey, "", "", "", "")
}

// LookupSessionProject returns the project_id of a session row (or ""
// if loose / not yet stored). Used by the agent runtime to populate
// InboundMessage.ProjectID so workspace IO routes to projects/<pid>/.
func (m *Manager) LookupSessionProject(sessionKey string) string {
	if m.store == nil || sessionKey == "" {
		return ""
	}
	pid, err := m.store.LookupSessionProject(m.ctx(), m.agentID, sessionKey)
	if err != nil {
		return ""
	}
	return pid
}

// LookupSessionTriple forwards to the store's session_key → triple
// lookup. Returns ("","","",nil) when the row doesn't exist, mirroring
// the SessionStore implementation. Callers should use SessionExists
// first if they need to distinguish "no row" from "row with empty
// triple" (e.g. file-backed dev mode where the store is nil).
func (m *Manager) LookupSessionTriple(sessionKey string) (channel, accountID, chatID string, err error) {
	if m.store == nil {
		return "", "", "", nil
	}
	return m.store.LookupSessionTriple(m.ctx(), m.agentID, sessionKey)
}

// SessionExists reports whether a session row already exists under the
// given session_key. Used by agent-side URL resolvers: a `?session=…`
// token can be either a canonical session_key or a legacy web chat_id,
// and the lookup needs a cheap way to tell which.
func (m *Manager) SessionExists(sessionKey string) bool {
	if m.store == nil {
		// File-backed mode has no negative-lookup primitive — assume
		// yes so the legacy chat_id fallback isn't preferred over the
		// caller's intent. The follow-up GetByKey will load whatever's
		// on disk (empty file → empty Session, harmless).
		return true
	}
	msgs, err := m.store.GetSession(m.ctx(), m.agentID, sessionKey)
	return err == nil && msgs != nil
}

// ResolveSessionKey turns a URL token (`?session=…`) into the
// canonical session_key. Accepts either:
//   - a session_key directly (the ID surfaced by ListWebSessions)
//   - a legacy web chat_id (older URLs and the frontend's freshly-
//     generated id on the *first* turn of a "+New chat")
//
// Returns the input unchanged when nothing matches — callers' downstream
// load/save will then create the row, which is correct for brand-new
// web chats where the URL token is the about-to-exist session_key.
func (m *Manager) ResolveSessionKey(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	if m.SessionExists(sessionID) {
		return sessionID
	}
	if m.store != nil {
		if k, err := m.store.ResolveActiveSessionKey(m.ctx(), m.agentID, "web", "", sessionID); err == nil && k != "" {
			return k
		}
	}
	return sessionID
}

// OpenNewSession mints a brand new session under the same (channel,
// accountID, chatID) triple and returns its session_key. The next Get
// for that triple will pick it up (it has the freshest updated_at).
// Used by IM `/new` / `/reset` commands and any future "start new
// conversation" UI affordance.
func (m *Manager) OpenNewSession(channel, accountID, chatID string) string {
	key := generateSessionKey()
	if m.store != nil {
		// Persist an empty row immediately so the active-session lookup
		// for the next inbound message resolves to this key, not the
		// previous (still-newer-than-not-existing) row. IM `/new` is
		// always a loose chat (project_id=""); project chats are
		// minted lazily by the chat handler on first message.
		_ = m.store.SaveSession(m.ctx(), m.agentID, key, channel, accountID, chatID, "", nil)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := &Session{
		filePath:   filepath.Join(m.dataDir, key+".jsonl"),
		store:      m.store,
		userID:     m.userID,
		agentID:    m.agentID,
		sessionKey: key,
		channel:    channel,
		accountID:  accountID,
		chatID:     chatID,
	}
	m.sessions[key] = s
	return key
}

func (m *Manager) getByKey(key, channel, accountID, chatID, projectID string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	if s, ok := m.sessions[key]; ok {
		if m.store != nil {
			if msgs, err := m.store.GetSession(m.ctx(), m.agentID, key); err == nil {
				s.mu.Lock()
				s.Messages = msgs
				s.mu.Unlock()
			}
		}
		// Late-bind the triple + project on cached entries created via
		// GetByKey or earlier hint-less paths. Once stamped, project_id
		// on the persisted row is authoritative — we only ever fill in
		// the empty case so a hint mismatch can't overwrite the truth.
		if channel != "" || projectID != "" {
			s.mu.Lock()
			if s.channel == "" && channel != "" {
				s.channel, s.accountID, s.chatID = channel, accountID, chatID
			}
			if s.projectID == "" && projectID != "" {
				s.projectID = projectID
			}
			s.mu.Unlock()
		}
		return s
	}

	filePath := filepath.Join(m.dataDir, key+".jsonl")

	s := &Session{
		filePath:   filePath,
		store:      m.store,
		userID:     m.userID,
		agentID:    m.agentID,
		sessionKey: key,
		channel:    channel,
		accountID:  accountID,
		chatID:     chatID,
		projectID:  projectID,
	}

	// Load from store (DB) if available, otherwise from file
	if m.store != nil {
		msgs, err := m.store.GetSession(m.ctx(), m.agentID, key)
		if err == nil && len(msgs) > 0 {
			s.Messages = msgs
		}
	} else {
		s.load()
	}

	m.sessions[key] = s
	return s
}

// Append adds a message to the session and persists it.
//
// Store-backed mode writes to TWO places:
//   - SaveSession overwrites the LLM-facing working set in the sessions
//     table (the array the agent loop reads next turn);
//   - AppendMessage inserts the new turn into session_messages, the
//     append-only archive that survives compaction.
//
// The archive write is best-effort (logged on failure but not surfaced)
// — losing one archive row is recoverable from the working set, and we
// don't want history to silently drop chat replies if the audit table
// hiccups.
// Key returns the opaque session_key this Session is bound to.
// Exposed so callers that need to tag external records by session
// (e.g. usage metering's per-session token rollup) don't have to
// reach into the struct.
func (s *Session) Key() string { return s.sessionKey }

func (s *Session) Append(msg provider.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Auto-set timestamp if not provided
	if msg.Timestamp == 0 {
		msg.Timestamp = time.Now().UnixMilli()
	}
	// Stamp provider/model on assistant messages so the archive
	// records which LLM produced each response.
	if msg.Role == "assistant" && msg.Provider == "" && s.provider != "" {
		msg.Provider = s.provider
		msg.Model = s.model
	}

	s.Messages = append(s.Messages, msg)

	if s.store != nil {
		s.store.SaveSession(s.ctx(), s.agentID, s.sessionKey, s.channel, s.accountID, s.chatID, s.projectID, s.Messages)
		if err := s.store.AppendMessage(s.ctx(), s.agentID, s.sessionKey, msg); err != nil {
			fmt.Fprintf(os.Stderr, "session archive append error: %v\n", err)
		}
	} else {
		s.appendToFile(msg)
	}
}

// ArchivedMessages returns the full append-only history for this session.
// Falls back to the in-memory working set when no store is configured or
// the archive is empty (e.g. file-backed mode, or a session created
// before the archive table existed).
func (s *Session) ArchivedMessages() []provider.Message {
	s.mu.Lock()
	store := s.store
	agentID := s.agentID
	sessionKey := s.sessionKey
	s.mu.Unlock()
	if store == nil {
		return s.GetMessages()
	}
	msgs, err := store.ListMessages(s.ctx(), agentID, sessionKey)
	if err != nil || len(msgs) == 0 {
		return s.GetMessages()
	}
	return msgs
}

// GetMessages returns a copy of all messages.
func (s *Session) GetMessages() []provider.Message {
	s.mu.Lock()
	defer s.mu.Unlock()

	msgs := make([]provider.Message, len(s.Messages))
	copy(msgs, s.Messages)
	return msgs
}

// BeginTurn marks a HandleMessage turn as in-flight for this session.
// Paired with EndTurn. Steering messages are only accepted while at
// least one turn is active.
func (s *Session) BeginTurn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnDepth++
}

// EndTurn marks a turn as finished. When the last in-flight turn ends it
// returns any steer messages still buffered (the end-of-turn race: a
// message pushed after the loop's final drain). Callers redispatch the
// leftovers as a fresh turn.
func (s *Session) EndTurn() []provider.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turnDepth > 0 {
		s.turnDepth--
	}
	if s.turnDepth > 0 || len(s.steerBuf) == 0 {
		return nil
	}
	leftover := s.steerBuf
	s.steerBuf = nil
	return leftover
}

// PushSteerIfActive buffers a steering message iff a turn is currently
// in-flight. Returns false when no turn is active, so the caller can
// fall back to dispatching the message as a normal new turn. The return
// value is the single source of truth — there is deliberately no
// separate "is running" probe to race against.
func (s *Session) PushSteerIfActive(msg provider.Message) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turnDepth == 0 {
		return false
	}
	s.steerBuf = append(s.steerBuf, msg)
	return true
}

// DrainSteer atomically returns and clears the buffered steer messages.
// The running loop calls this between tool iterations.
func (s *Session) DrainSteer() []provider.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.steerBuf) == 0 {
		return nil
	}
	drained := s.steerBuf
	s.steerBuf = nil
	return drained
}

// UnconsolidatedCount returns the number of messages since last consolidation.
func (s *Session) UnconsolidatedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.Messages) - s.LastConsolidated
}

// MarkConsolidated updates the consolidation pointer.
func (s *Session) MarkConsolidated(index int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastConsolidated = index
}

// ReplaceMessages replaces all session messages with the given list.
// This is used after context compaction to trim the session.
func (s *Session) ReplaceMessages(msgs []provider.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Messages = make([]provider.Message, len(msgs))
	copy(s.Messages, msgs)
	s.LastConsolidated = 0

	if s.store != nil {
		s.store.SaveSession(s.ctx(), s.agentID, s.sessionKey, s.channel, s.accountID, s.chatID, s.projectID, s.Messages)
	} else {
		s.rewriteFile()
	}
}

// Clear resets the session messages.
func (s *Session) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Messages = nil
	s.LastConsolidated = 0
	if s.store != nil {
		s.store.DeleteSession(s.ctx(), s.agentID, s.sessionKey)
	} else {
		os.Remove(s.filePath)
	}
}

func (s *Session) load() {
	f, err := os.Open(s.filePath)
	if err != nil {
		return // file doesn't exist yet
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var msg provider.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		s.Messages = append(s.Messages, msg)
	}
}

func (s *Session) rewriteFile() {
	dir := filepath.Dir(s.filePath)
	os.MkdirAll(dir, 0o755)

	f, err := os.Create(s.filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "session rewrite error: %v\n", err)
		return
	}
	defer f.Close()

	for _, msg := range s.Messages {
		data, err := json.Marshal(msg)
		if err != nil {
			continue
		}
		f.Write(data)
		f.Write([]byte("\n"))
	}
}

func (s *Session) appendToFile(msg provider.Message) {
	dir := filepath.Dir(s.filePath)
	os.MkdirAll(dir, 0o755)

	f, err := os.OpenFile(s.filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "session persist error: %v\n", err)
		return
	}
	defer f.Close()

	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	f.Write(data)
	f.Write([]byte("\n"))
}

// WebSession holds metadata for one chat session surfaced to the
// dashboard. Despite the historical name it now spans every channel —
// the Channel field tells callers which one to render the icon for.
//
// ID is the session_key (the row's PK), not the chat_id. Older URLs
// pointing at a chat_id still resolve via the agent-side fallback
// (ResolveSessionKey) so existing bookmarks don't break.
type WebSession struct {
	ID        string `json:"id"`
	Channel   string `json:"channel,omitempty"`
	AccountID string `json:"accountId,omitempty"`
	ChatID    string `json:"chatId,omitempty"`
	// ProjectID groups this chat under a per-(user, agent) project
	// folder. Empty = loose chat. Surfaced so the sidebar can section
	// chats by project.
	ProjectID string `json:"projectId,omitempty"`
	Title     string `json:"title"`
	Preview   string `json:"preview"`
	// LastMessage is the most recent user-visible user/assistant message,
	// independent of Preview (which intentionally remains the opening user
	// turn and is used as the default conversation title).
	LastMessage   string `json:"lastMessage,omitempty"`
	LastMessageAt int64  `json:"lastMessageAt,omitempty"` // unix ms
	CreatedAt     int64  `json:"createdAt"`               // unix ms
	UpdatedAt     int64  `json:"updatedAt"`               // unix ms
	// ThumbnailURL is the first image_url attached to the FIRST user
	// turn of the session, surfaced so the sidebar can show "image +
	// text" instead of just the text label for multimodal chats.
	// Empty for sessions whose opening message had no image.
	ThumbnailURL string `json:"thumbnailUrl,omitempty"`
	// ChatterUserID is the actual conversation participant. Differs
	// from user_id when an IM sender is resolved to a per-sender
	// app_user under the channel owner's UserSpace.
	ChatterUserID string `json:"chatterUserId,omitempty"`
}

// ListWebSessions scans session files for web chat sessions and returns
// a list with id, title, preview, and timestamps.
func (m *Manager) ListWebSessions() []WebSession {
	if m.store != nil {
		sessions, err := m.store.ListWebSessions(m.ctx(), m.agentID)
		if err == nil {
			return sessions
		}
	}
	pattern := filepath.Join(m.dataDir, "web_*.jsonl")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}

	var sessions []WebSession
	for _, f := range files {
		base := filepath.Base(f)
		// "web_<sessionId>.jsonl" -> "<sessionId>"
		sessionId := strings.TrimPrefix(base, "web_")
		sessionId = strings.TrimSuffix(sessionId, ".jsonl")

		info, err := os.Stat(f)
		if err != nil {
			continue
		}

		// Read the first user message as the conversation preview, while also
		// tracking the latest user-visible message for contact-list summaries.
		preview := ""
		thumb := ""
		lastMessage := ""
		var lastMessageAt int64
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(fh)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			// Multimodal user turns store text inside content_parts and
			// leave content empty — read both shapes so the preview
			// doesn't latch onto a later plain message and mislabel the
			// session, and pull the first image_url so the sidebar can
			// surface a thumbnail.
			var msg struct {
				Role         string                 `json:"role"`
				Content      string                 `json:"content"`
				ContentParts []provider.ContentPart `json:"content_parts"`
				Timestamp    int64                  `json:"timestamp"`
				Origin       string                 `json:"origin"`
			}
			if json.Unmarshal(scanner.Bytes(), &msg) != nil || msg.Origin != provider.OriginUser {
				continue
			}
			if msg.Role != "user" && msg.Role != "assistant" {
				continue
			}

			text := strings.TrimSpace(msg.Content)
			img := ""
			if msg.Role == "user" && text == "" {
				var parts []string
				for _, p := range msg.ContentParts {
					if p.Type == "text" && p.Text != "" {
						parts = append(parts, p.Text)
					}
				}
				text = strings.Join(parts, "\n")
			}
			if msg.Role == "user" {
				text = provider.StripAttachedPrefix(text)
				for _, p := range msg.ContentParts {
					if p.Type == "image_url" && p.ImageURL != nil && p.ImageURL.URL != "" {
						img = p.ImageURL.URL
						break
					}
				}
			}
			if text == "" && img == "" {
				continue
			}

			messagePreview := compactMessagePreview(text)
			if messagePreview == "" {
				messagePreview = "[image]"
			}
			lastMessage = messagePreview
			lastMessageAt = msg.Timestamp

			if msg.Role == "user" && preview == "" {
				preview = messagePreview
				thumb = img
			}
		}
		fh.Close()

		if preview == "" {
			continue // skip empty sessions
		}
		if lastMessage == "" {
			lastMessage = preview
		}
		if lastMessageAt == 0 {
			lastMessageAt = info.ModTime().UnixMilli()
		}

		// Read title from metadata file, fallback to preview
		title := m.readSessionTitle(sessionId)
		if title == "" {
			title = preview
			if len(title) > 60 {
				title = title[:60] + "..."
			}
		}

		sessions = append(sessions, WebSession{
			ID:            sessionId,
			Title:         title,
			Preview:       preview,
			LastMessage:   lastMessage,
			LastMessageAt: lastMessageAt,
			ThumbnailURL:  thumb,
			CreatedAt:     info.ModTime().UnixMilli(),
			UpdatedAt:     info.ModTime().UnixMilli(),
		})
	}

	// Sort by updatedAt descending (newest first)
	for i := 0; i < len(sessions); i++ {
		for j := i + 1; j < len(sessions); j++ {
			if sessions[j].UpdatedAt > sessions[i].UpdatedAt {
				sessions[i], sessions[j] = sessions[j], sessions[i]
			}
		}
	}

	return sessions
}

// resolveWebSessionKey maps a web sessionId (the URL `?session=` token,
// which is the conversation's chat_id) to its current session_key. New
// rows have an opaque session_key (different from chat_id); legacy rows
// still carry the `web_<sid>` form. Falls back to the legacy literal
// when no row exists yet so file-backed mode and brand-new sessions
// don't error on rename/delete.
func (m *Manager) resolveWebSessionKey(sessionId string) string {
	if m.store != nil {
		if k, err := m.store.ResolveActiveSessionKey(m.ctx(), m.agentID, "web", "", sessionId); err == nil && k != "" {
			return k
		}
	}
	return "web_" + sessionId
}

// DeleteSessionByID resolves a URL token (session_key or legacy web
// chat_id) and deletes the matching session. Channel-agnostic — used
// by the dashboard to delete any-channel chats.
func (m *Manager) DeleteSessionByID(sessionId string) error {
	key := m.ResolveSessionKey(sessionId)
	m.mu.Lock()
	delete(m.sessions, key)
	m.mu.Unlock()
	if m.store != nil {
		return m.store.DeleteSession(m.ctx(), m.agentID, key)
	}
	// File-backed mode only had a "web_<sid>" filename convention; non-
	// web sessions don't reach this path in dev mode, so the legacy
	// fallback in DeleteWebSession is sufficient.
	return m.DeleteWebSession(sessionId)
}

// RenameSessionByID resolves a URL token and renames the matching
// session.
func (m *Manager) RenameSessionByID(sessionId, title string) error {
	key := m.ResolveSessionKey(sessionId)
	if m.store != nil {
		return m.store.RenameSession(m.ctx(), m.agentID, key, title)
	}
	return m.RenameWebSession(sessionId, title)
}

// MoveSessionByID reassigns a session to a different project (or
// detaches when projectID is ""). Resolves either a session_key or a
// legacy web chat_id. Drops the in-memory cache entry so the next
// Get re-loads the row with the freshly-stamped project_id — without
// this drop, an open chat would keep saving with the old project_id
// even after the sidebar shows it under a new project.
//
// File-backed mode is a no-op (no project concept) — callers that
// only run dev mode shouldn't reach this path.
func (m *Manager) MoveSessionByID(sessionId, projectID string) error {
	key := m.ResolveSessionKey(sessionId)
	m.mu.Lock()
	if s, ok := m.sessions[key]; ok {
		s.mu.Lock()
		s.projectID = projectID
		s.mu.Unlock()
	}
	m.mu.Unlock()
	if m.store != nil {
		return m.store.MoveSession(m.ctx(), m.agentID, key, projectID)
	}
	return nil
}

// DeleteWebSession removes a web chat session file and its metadata.
func (m *Manager) DeleteWebSession(sessionId string) error {
	key := m.resolveWebSessionKey(sessionId)

	// Remove from in-memory cache
	m.mu.Lock()
	delete(m.sessions, key)
	m.mu.Unlock()

	if m.store != nil {
		return m.store.DeleteSession(m.ctx(), m.agentID, key)
	}

	safeId := strings.ReplaceAll(sessionId, "/", "_")
	safeId = strings.ReplaceAll(safeId, "..", "_")
	sessionFile := filepath.Join(m.dataDir, "web_"+safeId+".jsonl")
	metaFile := filepath.Join(m.dataDir, "web_"+safeId+".meta.json")
	os.Remove(metaFile)
	return os.Remove(sessionFile)
}

// RenameWebSession sets a custom title for a web chat session.
func (m *Manager) RenameWebSession(sessionId, title string) error {
	if m.store != nil {
		return m.store.RenameSession(m.ctx(), m.agentID, m.resolveWebSessionKey(sessionId), title)
	}

	safeId := strings.ReplaceAll(sessionId, "/", "_")
	safeId = strings.ReplaceAll(safeId, "..", "_")
	metaFile := filepath.Join(m.dataDir, "web_"+safeId+".meta.json")
	data, _ := json.Marshal(map[string]string{"title": title})
	return os.WriteFile(metaFile, data, 0o644)
}

// readSessionTitle reads the title from a session metadata file.
func (m *Manager) readSessionTitle(sessionId string) string {
	safeId := strings.ReplaceAll(sessionId, "/", "_")
	safeId = strings.ReplaceAll(safeId, "..", "_")

	metaFile := filepath.Join(m.dataDir, "web_"+safeId+".meta.json")
	data, err := os.ReadFile(metaFile)
	if err != nil {
		return ""
	}
	var meta struct {
		Title string `json:"title"`
	}
	json.Unmarshal(data, &meta)
	return meta.Title
}

// Snapshot saves the current message list as a restore point (for undo).
func (s *Session) Snapshot() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot = make([]provider.Message, len(s.Messages))
	copy(s.snapshot, s.Messages)
}

// Undo restores the last snapshot. Returns false if no snapshot exists.
func (s *Session) Undo() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot == nil {
		return false
	}
	s.Messages = make([]provider.Message, len(s.snapshot))
	copy(s.Messages, s.snapshot)
	s.snapshot = nil
	s.LastConsolidated = 0
	s.rewriteFile()
	return true
}

// HasSnapshot returns true if an undo snapshot exists.
func (s *Session) HasSnapshot() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot != nil
}
