# WeCom Channel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a production-ready WeCom Intelligent Bot channel with addressed internal-group chat, direct messages, native streaming Markdown, image/file transfer, and conversation-scoped proactive sends.

**Architecture:** Implement the official WeCom Intelligent Bot long-connection protocol inside the Go process with `gorilla/websocket`. Reuse `ChannelRecord`, channel manager, gateway routing, chatter identity, task queue, media extraction, cron targets, and dashboard primitives. Add an explicit transport-neutral stream lifecycle and a Redis stream keyed by `(wecom, Bot ID)` so only the active channel leaseholder sends replies in multi-replica deployments.

**Tech Stack:** Go, `gorilla/websocket`, standard-library crypto/HTTP/image packages, `golang.org/x/image/webp`, Redis Streams through `go-redis`, `miniredis` tests, React/Next.js, TypeScript.

---

## Execution boundary

The current `dev` worktree contains unrelated user changes. Execute in an isolated worktree on branch `codex/20260721-wecom-channel`; do not stash, reset, format, stage, or commit the current worktree. Local task-scoped commits are allowed only in the isolated worktree. Do not push, merge, deploy, delete the worktree, or touch production credentials without explicit authorization.

### Task 0: Create an isolated implementation worktree and baseline

**Files:**
- Read: `AGENTS.md`, `README.md`
- Read: `docs/superpowers/specs/2026-07-21-wecom-channel-design.md`
- Read: `~/.codex/rules/code-development.md`, `~/.codex/rules/git-security-deploy.md`
- Read: `~/.codex/rules/ui-ux-copy.md`, `~/.codex/rules/image-assets.md`

- [x] **Step 1: Re-check source state**

Run:

```bash
git status --short --branch
git rev-parse --show-toplevel
git check-ignore -q .worktrees
```

Expected: branch `dev`; current unrelated changes remain; `.worktrees` is ignored. If not ignored, add it to the existing ignore file with `apply_patch` first.

- [x] **Step 2: Create the worktree**

```bash
git worktree add .worktrees/wecom-channel -b codex/20260721-wecom-channel
```

Expected: clean `.worktrees/wecom-channel` on the new branch.

- [x] **Step 3: Baseline tests**

```bash
go test -count=1 ./internal/channels ./internal/bus ./internal/gateway ./internal/setup ./internal/agent
cd web && pnpm exec eslint src/app/agents/'[id]'/channels/page.tsx src/components/channel-icon.tsx src/lib/api.ts
```

Expected: PASS. Record pre-existing failures; do not absorb unrelated fixes.

### Task 1: Add an explicit outbound stream contract

**Files:**
- Modify: `internal/bus/bus.go`
- Modify: `internal/bus/bus_test.go`

- [x] **Step 1: Write the failing JSON round-trip test**

```go
func TestOutboundMessageStreamFieldsRoundTrip(t *testing.T) {
	want := OutboundMessage{
		Channel: "wecom", AccountID: "bot-1", ChatID: "group-1",
		ReplyToMsgID: "msg-1", Text: "partial",
		StreamID: "stream-1", StreamState: StreamUpdate,
	}
	b, err := json.Marshal(want)
	if err != nil { t.Fatal(err) }
	var got OutboundMessage
	if err := json.Unmarshal(b, &got); err != nil { t.Fatal(err) }
	if got.StreamID != want.StreamID || got.StreamState != StreamUpdate {
		t.Fatalf("round trip = %#v", got)
	}
}
```

- [x] **Step 2: Verify RED**

Run: `go test -count=1 ./internal/bus -run TestOutboundMessageStreamFieldsRoundTrip`

Expected: compile failure because the stream fields/constants do not exist.

- [x] **Step 3: Implement the contract**

```go
type StreamState string

const (
	StreamNone   StreamState = ""
	StreamStart  StreamState = "start"
	StreamUpdate StreamState = "update"
	StreamFinish StreamState = "finish"
)
```

Add to `OutboundMessage`:

```go
StreamID    string
StreamState StreamState
```

Existing channels ignore these fields and remain final-only.

- [x] **Step 4: Verify GREEN and commit**

```bash
gofmt -w internal/bus/bus.go internal/bus/bus_test.go
go test -count=1 ./internal/bus
git add internal/bus/bus.go internal/bus/bus_test.go
git commit -m "feat(bus): add outbound stream lifecycle"
```

### Task 2: Cancel persistent adapters on replace and disconnect

**Files:**
- Modify: `internal/channels/manager.go`
- Create: `internal/channels/manager_test.go`

- [x] **Step 1: Write failing lifecycle tests**

```go
type lifecycleChannel struct {
	name, account string
	started, stopped chan struct{}
}
func (c *lifecycleChannel) Name() string { return c.name }
func (c *lifecycleChannel) AccountID() string { return c.account }
func (c *lifecycleChannel) BotUsername() string { return c.account }
func (c *lifecycleChannel) Start(ctx context.Context) error {
	close(c.started); <-ctx.Done(); close(c.stopped); return nil
}
func (c *lifecycleChannel) Send(string, string) error { return nil }
func (c *lifecycleChannel) SendMessage(bus.OutboundMessage) error { return nil }
func (c *lifecycleChannel) SendTyping(string) error { return nil }
```

Add `TestManagerUnregisterCancelsRunningChannel` and `TestManagerHotReplaceCancelsPreviousChannel`, each with a one-second stop timeout.

- [x] **Step 2: Verify RED**

Run: `go test -count=1 ./internal/channels -run 'TestManager(Unregister|HotReplace)Cancels'`

Expected: timeout because current goroutines survive unregister/replacement.

- [x] **Step 3: Implement per-channel cancellation**

Add `runCancels map[string]context.CancelFunc` plus a generation map/counter. Boot and hot starts use a shared `startChannel`. Under lock, cancel the previous generation and install the new child context. On exit, remove only if the generation is still current. `Unregister` removes `channels` and `singleton`, captures the cancel under lock, unlocks, then cancels. Preserve sticky Telegram token claims.

Core shape:

```go
func (m *Manager) startChannel(parent context.Context, key string, ch Channel, singleton bool) {
	ctx, cancel := context.WithCancel(parent)
	generation := m.installRun(key, cancel)
	defer m.clearRunIfCurrent(key, generation)
	if singleton { runWithLease(ctx, ch, m.leaser, m.holderID); return }
	if err := ch.Start(ctx); err != nil {
		slog.Error("channel stopped with error", "key", key, "error", err)
	}
}
```

- [x] **Step 4: Verify and commit**

```bash
gofmt -w internal/channels/manager.go internal/channels/manager_test.go
go test -count=1 ./internal/channels
git add internal/channels/manager.go internal/channels/manager_test.go
git commit -m "fix(channels): cancel replaced persistent adapters"
```

### Task 3: Route WeCom outbound to the active Redis leaseholder

**Files:**
- Modify: `go.mod`, `go.sum`, `internal/bus/bus.go`
- Create: `internal/bus/targeted.go`, `internal/bus/targeted_test.go`

- [x] **Step 1: Add the test-only Redis fixture**

Run: `go get github.com/alicebob/miniredis/v2@v2.35.0`

Expected: dependency metadata only; production code does not import miniredis.

- [x] **Step 2: Write failing targeted tests**

Using `miniredis.RunT(t)` and a real `redis.Client`, add:

```go
func TestRedisWeComOutboundIsAccountTargeted(t *testing.T)
func TestTargetedOutboundAcksOnlyAfterHandlerSuccess(t *testing.T)
func TestTargetedOutboundClaimsPendingFromPreviousConsumer(t *testing.T)
func TestRedisNonWeComOutboundStillUsesSharedConsumer(t *testing.T)
```

Publish for `bot-a` and `bot-b`; prove isolation. Return a sentinel handler error; prove pending remains. Start a new consumer, advance miniredis past the idle threshold, and prove claim then ack after success.

- [x] **Step 3: Verify RED**

Run: `go test -count=1 ./internal/bus -run 'Test(RedisWeCom|TargetedOutbound|RedisNonWeCom)'`

Expected: compile failure because targeted APIs do not exist.

- [x] **Step 4: Implement deterministic keys and API**

```go
func targetedOutboundKey(prefix, channel, accountID string) string {
	sum := sha256.Sum256([]byte(accountID))
	return fmt.Sprintf("%s:bus:outbound:target:%s:%x", prefix, channel, sum[:12])
}
func targetedOutboundChannel(channel string) bool { return channel == "wecom" }
func (b *MessageBus) HasTargetedOutbound() bool
func (b *MessageBus) ConsumeTargetedOutbound(
	ctx context.Context, channel, accountID string,
	handle func(OutboundMessage) error,
) error
```

Redis mode routes WeCom to its target stream. Consumer creates its group, `XAUTOCLAIM`s entries idle at least 15 seconds, then `XREADGROUP`s new entries. Decode, call `handle` synchronously, and `XACK` only after nil. Keep `MAXLEN ~ 10000`. In-memory mode remains on `Manager.routeOutbound` and starts no second consumer.

- [x] **Step 5: Verify and commit**

```bash
gofmt -w internal/bus/bus.go internal/bus/targeted.go internal/bus/targeted_test.go
go test -count=1 ./internal/bus
git add go.mod go.sum internal/bus/bus.go internal/bus/targeted.go internal/bus/targeted_test.go
git commit -m "feat(bus): target wecom outbound to leaseholder"
```

Expected: PASS, proving at-least-once rather than exactly-once crash semantics.


### Task 4: Implement the authenticated WeCom WebSocket protocol core

**Files:**
- Create: `internal/channels/wecom.go`, `internal/channels/wecom_protocol.go`, `internal/channels/wecom_test.go`
- Reference: official `@wecom/aibot-node-sdk@1.0.7` protocol shapes only

- [x] **Step 1: Write failing constructor/authentication tests**

Use `httptest.Server` plus `websocket.Upgrader`:

```go
func TestNewWeComRequiresBotIDAndSecret(t *testing.T)
func TestWeComAuthenticatesWithSubscribeFrame(t *testing.T)
func TestWeComAuthenticationErrorPreservesCodeWithoutSecret(t *testing.T)
func TestWeComCredentialValidationTimesOutWithoutPersistingState(t *testing.T)
```

Assert this exact shape without printing the secret on failure:

```json
{"cmd":"aibot_subscribe","headers":{"req_id":"aibot_subscribe-test-1"},"body":{"bot_id":"bot-1","secret":"test-secret"}}
```

- [x] **Step 2: Verify RED**

Run: `go test -count=1 ./internal/channels -run 'Test(NewWeCom|WeComAuth|WeComCredential)'`

Expected: compile failure because the adapter is absent.

- [x] **Step 3: Define protocol types and redacted errors**

```go
type weComFrame struct {
	Cmd string `json:"cmd,omitempty"`
	Headers weComHeaders `json:"headers"`
	Body json.RawMessage `json:"body,omitempty"`
	ErrCode int `json:"errcode,omitempty"`
	ErrMsg string `json:"errmsg,omitempty"`
}
type weComHeaders struct { ReqID string `json:"req_id"` }
type WeComProtocolError struct { Operation string; ErrCode int; ErrMsg string }
```

Errors include operation, `errcode`, and `errmsg`, never Secret, media URL, AES key, auth JSON, or bearer-like values.

- [x] **Step 4: Implement constructor and bounded validation**

```go
type WeComOptions struct {
	BotID string
	Secret string
	AccountID string
	WSURL string
	Dialer *websocket.Dialer
	HTTPClient *http.Client
}
func NewWeCom(opts WeComOptions, mb *bus.MessageBus) (*WeCom, error)
func WeComValidateCredentials(ctx context.Context, botID, secret string) error
const weComDefaultWSURL = "wss://openws.work.weixin.qq.com"
```

Validation dials, sends `aibot_subscribe`, waits for the matching `req_id`, returns concrete protocol error, and always closes. It starts no background adapter.

- [x] **Step 5: Implement channel identity and correlation**

```go
func (w *WeCom) Name() string { return "wecom" }
func (w *WeCom) AccountID() string { return w.accountID }
func (w *WeCom) BotUsername() string { return w.botID }
func (w *WeCom) Send(chatID, text string) error
func (w *WeCom) SendTyping(chatID string) error { return nil }
```

Use one reader goroutine and a write mutex. Correlate acks by `headers.req_id`; serialize sends sharing a request ID. Bound authentication and ack waits. Context cancellation closes promptly.

- [x] **Step 6: Add lifecycle tests and behavior**

```go
func TestWeComHeartbeatUsesPingFrame(t *testing.T)
func TestWeComContextCancellationClosesSocket(t *testing.T)
func TestWeComRecoverableDisconnectReconnects(t *testing.T)
func TestWeComDisconnectedEventIsTerminal(t *testing.T)
func TestWeComRevokedCredentialsStopAuthRetry(t *testing.T)
```

Use injectable test durations. Production: 30-second heartbeat; bounded exponential reconnect with jitter; reset counters after auth. Auth rejection and competing-client `disconnected_event` are terminal. Cancel any targeted consumer when the socket closes; restart only after re-authentication.

- [x] **Step 7: Verify and commit**

```bash
gofmt -w internal/channels/wecom.go internal/channels/wecom_protocol.go internal/channels/wecom_test.go
go test -count=1 ./internal/channels -run TestWeCom
git add internal/channels/wecom.go internal/channels/wecom_protocol.go internal/channels/wecom_test.go
git commit -m "feat(channels): add wecom websocket protocol"
```

### Task 5: Map inbound text, mixed content, images, and files

**Files:**
- Modify: `internal/channels/wecom.go`, `internal/channels/wecom_test.go`
- Create: `internal/channels/wecom_media.go`, `internal/channels/wecom_media_test.go`

- [x] **Step 1: Write failing direct/group tests**

```go
func TestWeComMapsDirectText(t *testing.T)
func TestWeComMapsAddressedGroupToSharedChatWithSenderIdentity(t *testing.T)
func TestWeComIgnoresSelfMessage(t *testing.T)
func TestWeComRejectsMalformedGroupWithoutChatID(t *testing.T)
func TestWeComUsesMsgIDAndStoresReplyReqID(t *testing.T)
```

Group expectation:

```go
want := bus.InboundMessage{
	Channel: "wecom", AccountID: "bot-1", ChatID: "group-1",
	UserID: "member-7", PeerKind: "group", MessageID: "msg-9",
	Mentions: []string{"bot-1"}, SharedIdentity: false,
}
```

The callback is the authoritative addressed-bot signal. Do not add another text parser for `@`; non-addressed silence is verified in real WeCom.

- [x] **Step 2: Verify RED**

Run: `go test -count=1 ./internal/channels -run 'TestWeCom(Map|Ignore|Reject|Use)'`

- [x] **Step 3: Implement callback mapping and bounded reply refs**

Validate `msgid`, `aibotid`, `chattype`, `from.userid`, and group `chatid`, then publish. Store by `msgid`:

```go
type weComReplyRef struct {
	ReqID string
	ChatID string
	ExpiresAt time.Time
}
```

Expire/cap the map; delete after `StreamFinish` or terminal reply error. Never log raw callbacks.

- [x] **Step 4: Write failing media tests**

```go
func TestDecryptWeComMediaAES256CBC(t *testing.T)
func TestDecryptWeComMediaRejectsBadKeyAndPadding(t *testing.T)
func TestDownloadWeComMediaEnforcesHTTPSAndSizeLimit(t *testing.T)
func TestWeComMapsImageAndFileToMediaItems(t *testing.T)
func TestWeComMapsMixedTextAndImagesInOrder(t *testing.T)
func TestWeComMediaFailureRepliesWithoutPublishingInbound(t *testing.T)
func TestWeComVoiceAndVideoReturnUnsupportedError(t *testing.T)
func TestWeComMediaErrorRedactsURLAndAESKey(t *testing.T)
```

Use deterministic AES-256-CBC fixtures: IV is the first 16 decoded key bytes; PKCS#7 pads to 32 bytes.

- [x] **Step 5: Implement immediate bounded retrieval**

```go
const weComMaxInboundMediaBytes = 25 * 1024 * 1024
```

Require HTTPS without userinfo; bound redirects; `io.LimitReader(limit+1)`; decode a 32-byte AES key; decrypt AES-256-CBC with `key[:16]`; validate/remove 1–32 byte padding; sanitize `Content-Disposition`; sniff MIME; populate `bus.MediaItem`. Failure sends one concrete passive error and never invokes the model. Voice/video return unsupported.

- [x] **Step 6: Verify and commit**

```bash
gofmt -w internal/channels/wecom.go internal/channels/wecom_media.go internal/channels/wecom_test.go internal/channels/wecom_media_test.go
go test -count=1 ./internal/channels -run TestWeCom
git add internal/channels/wecom.go internal/channels/wecom_media.go internal/channels/wecom_test.go internal/channels/wecom_media_test.go
git commit -m "feat(channels): receive wecom messages and media"
```

### Task 6: Send streams, proactive Markdown, images, and files

**Files:**
- Modify: `go.mod`, `go.sum`
- Modify: `internal/channels/wecom.go`, `internal/channels/wecom_protocol.go`, `internal/channels/wecom_media.go`
- Create: `internal/channels/wecom_outbound_test.go`

- [x] **Step 1: Write failing passive-stream tests**

```go
func TestWeComStreamUsesReplyReqIDStableIDAndCumulativeContent(t *testing.T)
func TestWeComStreamFinishSetsFinishTrueAndReleasesReplyRef(t *testing.T)
func TestWeComStreamFinalAttachesJPEGAndPNGImages(t *testing.T)
func TestWeComStreamErrorDoesNotLeaveReplyOpen(t *testing.T)
```

Expected wire shape:

```json
{"cmd":"aibot_respond_msg","headers":{"req_id":"callback-req-id"},"body":{"msgtype":"stream","stream":{"id":"stable-stream-id","finish":false,"content":"cumulative Markdown"}}}
```

`msg_item` appears only on `finish:true`, maximum ten JPEG/PNG images, base64 bytes plus MD5 of pre-base64 bytes.

- [x] **Step 2: Write failing proactive/upload tests**

```go
func TestWeComProactiveMarkdownTargetsOriginalChatID(t *testing.T)
func TestWeComLongMarkdownSplitsOnUTF8Boundary(t *testing.T)
func TestWeComUploadMediaInitChunkFinish(t *testing.T)
func TestWeComOutboundFileUploadsThenSendsMediaID(t *testing.T)
func TestWeComConvertsWebPAndGIFToPNGForImageDelivery(t *testing.T)
func TestWeComSendFailureSurfacesErrCodeAndKeepsRedisEntryPending(t *testing.T)
```

Proactive uses `aibot_send_msg` with `OutboundMessage.ChatID`; no arbitrary recipient API.

- [x] **Step 3: Verify RED and add WebP decoder**

```bash
go test -count=1 ./internal/channels -run 'TestWeCom(Stream|Proactive|Long|Upload|Outbound|Converts|SendFailure)'
go get golang.org/x/image@v0.28.0
```

Import `golang.org/x/image/webp` only in conversion; use standard JPEG/PNG/GIF codecs.

- [x] **Step 4: Implement limits and upload**

```go
func splitWeComMarkdown(text string, maxBytes int) []string
func normalizeWeComImage(item bus.MediaItem) (bus.MediaItem, error)
func (w *WeCom) uploadMedia(ctx context.Context, kind string, item bus.MediaItem) (string, error)
```

Rules:
- Markdown frame max 20,480 UTF-8 bytes; never split a rune; prefer prior newline.
- Passive stream shows first chunk; remaining chunks send proactively to the same `ChatID` after finish.
- Convert decodable WebP/GIF/other rasters to PNG; corrupt input fails visibly.
- Inline final image: JPEG/PNG, at most 10 MiB each, maximum ten.
- File/overflow image: `aibot_upload_media_init`, zero-based 512 KiB chunks matching official SDK 1.0.7, then `aibot_upload_media_finish`; maximum 100 chunks.
- Send media through passive `aibot_respond_msg` or proactive `aibot_send_msg`.
- Never log base64, media IDs, media URLs, AES keys, or raw frames.

- [x] **Step 5: Wire local and Redis outbound**

After authentication:

```go
if w.bus.HasTargetedOutbound() {
	go w.bus.ConsumeTargetedOutbound(connCtx, "wecom", w.accountID, w.SendMessage)
}
```

Surface targeted-consumer setup errors through the connection loop. In-memory mode remains exclusively on `Manager.routeOutbound`.

- [x] **Step 6: Verify and commit**

```bash
gofmt -w internal/channels/wecom*.go
go test -count=1 ./internal/channels ./internal/bus
git add go.mod go.sum internal/channels/wecom.go internal/channels/wecom_protocol.go internal/channels/wecom_media.go internal/channels/wecom_outbound_test.go
git commit -m "feat(channels): send wecom streams and attachments"
```

### Task 7: Stream WeCom turns from the gateway

**Files:**
- Modify: `internal/gateway/gateway.go`
- Create: `internal/gateway/wecom_stream.go`, `internal/gateway/wecom_stream_test.go`

- [x] **Step 1: Write failing stream-pump tests**

```go
type weComStreamEmitter func(bus.OutboundMessage) error
func pumpWeComStream(
	ctx context.Context, sr *provider.StreamReader,
	base bus.OutboundMessage, interval time.Duration,
	emit weComStreamEmitter,
) (string, error)
```

Add tests for immediate `StreamStart` containing `…`, stable ID, throttled cumulative updates, exactly one finish, reader error, and cancellation:

```go
func TestPumpWeComStreamStartsImmediatelyAndUsesStableID(t *testing.T)
func TestPumpWeComStreamEmitsThrottledCumulativeUpdates(t *testing.T)
func TestPumpWeComStreamAlwaysFinishes(t *testing.T)
func TestPumpWeComStreamFinishesWithError(t *testing.T)
func TestPumpWeComStreamHonorsContextCancellation(t *testing.T)
```

- [x] **Step 2: Verify RED**

Run: `go test -count=1 ./internal/gateway -run TestPumpWeComStream`

- [x] **Step 3: Implement pump and gateway branch**

Forward `StreamReader.Next()` from a goroutine so the main loop selects chunks, a 250 ms ticker, and cancellation. Accumulate `chunk.Content`, check `sr.Err()`, and emit through the bounded bus path.

Keep attachment materialization, typing, workspace snapshot, media extraction, and every non-WeCom path unchanged:

```go
if task.Message.Channel == "wecom" {
	reply, err = g.handleWeComTaskStream(ctx, ag, task, workspaceBefore, workspaceSnapshotOK)
} else {
	reply = ag.HandleMessage(ctx, task.Message)
}
```

The WeCom handler generates one ID, starts with `ReplyToMsgID`, calls `HandleMessageStream`, emits cumulative updates, reuses `splitMediaFromReply`, `splitFilesFromReply`, and workspace fallback, then finishes once with text/media. Do not also publish the legacy one-shot final reply. Finish with a concrete error whenever a bounded send is still possible.

- [x] **Step 4: Add regressions, verify, commit**

Test non-WeCom remains final-only and WeCom media appears once.

```bash
gofmt -w internal/gateway/gateway.go internal/gateway/wecom_stream.go internal/gateway/wecom_stream_test.go
go test -count=1 ./internal/gateway -run 'Test(PumpWeCom|WeComTask|NonWeCom)'
go test -count=1 ./internal/gateway
git add internal/gateway/gateway.go internal/gateway/wecom_stream.go internal/gateway/wecom_stream_test.go
git commit -m "feat(gateway): stream agent replies to wecom"
```


### Task 8: Enforce group identity and privilege rules

**Files:**
- Modify: `internal/agent/slash.go`, `internal/agent/admin_chatter_test.go`
- Create: `internal/gateway/routing_wecom_test.go`
- Create: `internal/gateway/dedup_test.go`

- [x] **Step 1: Write failing admin tests**

```go
func TestWeComGroupChatterIsNeverAdmin(t *testing.T) {
	a := &Agent{
		ownerUserID: "u_owner",
		admins: map[string][]string{"wecom": {"member-listed", "u_owner"}},
	}
	for _, uid := range []string{"member-listed", "u_owner"} {
		msg := bus.InboundMessage{Channel: "wecom", UserID: uid, PeerKind: "group"}
		if a.isAdminChatter(msg) { t.Fatalf("wecom group %q became admin", uid) }
		if a.isTrustedTurn(msg) { t.Fatalf("wecom group %q became trusted", uid) }
	}
}
```

Also assert a WeCom DM is ordinary unless existing owner/allowlist policy grants it.

- [x] **Step 2: Verify RED and implement deny**

Run: `go test -count=1 ./internal/agent -run TestWeComGroupChatterIsNeverAdmin`

Before allowlist evaluation:

```go
if msg.Channel == "wecom" && msg.PeerKind == "group" { return false }
```

This keeps `isTrustedTurn` false while normal sandboxed tools remain available.

- [x] **Step 3: Test routing identity**

Assert two DM users get different triples; two senders in one group share `(wecom, botID, chatid)`; each turn retains its raw `UserID`; `SharedIdentity` is false. Add DM and group duplicate tests proving the existing gateway dedup rejects a repeated WeCom `msgid`/group delivery before agent execution.

- [x] **Step 4: Verify and commit**

```bash
gofmt -w internal/agent/slash.go internal/agent/admin_chatter_test.go
go test -count=1 ./internal/agent ./internal/gateway
git add internal/agent/slash.go internal/agent/admin_chatter_test.go internal/gateway/routing_wecom_test.go internal/gateway/dedup_test.go
git commit -m "fix(agent): deny admin privilege in wecom groups"
```

### Task 9: Register, persist, validate, hot-start, and disconnect bindings

**Files:**
- Modify: `internal/gateway/channels.go`
- Modify/Create: `internal/gateway/channels_test.go`
- Modify: `internal/setup/server.go`, `internal/setup/handlers_agent_channels.go`
- Create: `internal/setup/handlers_agent_channels_wecom_test.go`

- [x] **Step 1: Write failing gateway registration tests**

Cover `ChannelRecord{Type:"wecom"}` into one singleton adapter: Bot ID from `AccountID`/account key; Secret from `BotToken`/account token; disabled does not register; no migration.

Run: `go test -count=1 ./internal/gateway -run TestRegisterWeCom`

- [x] **Step 2: Implement gateway registration**

Add `wecom` to both registration switches:

```go
func registerWeComChannels(chCfg config.ChannelConfig, mb *bus.MessageBus, chanMgr *channels.Manager, hot bool) error {
	for botID, acct := range chCfg.Accounts {
		secret := acct.BotToken
		if secret == "" { secret = chCfg.BotToken }
		wc, err := channels.NewWeCom(channels.WeComOptions{
			BotID: botID, Secret: secret, AccountID: botID,
		}, mb)
		if err != nil { return err }
		registerSingleton(chanMgr, wc, hot)
	}
	return nil
}
```

- [x] **Step 3: Write failing setup API tests**

Use `newAuthTestServer`, temporary SQLite, a saved Agent, and injected validator:

```go
func TestConnectAgentWeComValidatesThenPersistsMaskedSecret(t *testing.T)
func TestConnectAgentWeComRejectsEmptyCredentials(t *testing.T)
func TestConnectAgentWeComInvalidCredentialsAreNotPersisted(t *testing.T)
func TestConnectAgentWeComRejectsDuplicateBotID(t *testing.T)
func TestConnectAgentWeComForcesSharedIdentityFalse(t *testing.T)
func TestUpdateAgentWeComRejectsSharedIdentityTrue(t *testing.T)
func TestDisconnectAgentWeComUnregistersRunningAdapter(t *testing.T)
```

Run: `go test -count=1 ./internal/setup -run 'Test(ConnectAgentWeCom|UpdateAgentWeCom|DisconnectAgentWeCom)'`

Expected: compile failure because endpoint/hook do not exist.

- [x] **Step 4: Add validator and endpoint**

Add a non-global test hook to `Server`:

```go
weComValidateCredentials func(context.Context, string, string) error
```

Production falls back to `channels.WeComValidateCredentials`. Register:

```go
mux.HandleFunc("POST /api/agents/{id}/channels/wecom", auth(s.handleConnectAgentWeCom))
```

Request/storage:

```go
type connectWeComRequest struct {
	BotID string `json:"botId"`
	Secret string `json:"secret"`
}
cc := config.ChannelConfig{
	Enabled: true,
	Accounts: map[string]config.AccountConfig{botID: {BotToken: secret}},
}
```

Require writable scope; trim/require fields; validate with bounded context; assert `(wecom, Bot ID)` uniqueness; save; explicitly force `SharedIdentity=false`; invalidate; hot-register; return `{"ok":true,"botId":"bot-1"}` in the test. Add an error-returning `tryHotRegisterChannelRecord` helper; keep the existing best-effort wrapper for older channel endpoints, but use the error-returning helper here. If hot registration fails after save, delete the new row and return its concrete error. Validation socket is closed and not reused.

- [x] **Step 5: Enforce Shared Identity server-side**

```go
if channelType == "wecom" && req.SharedIdentity != nil && *req.SharedIdentity {
	jsonResponse(w, http.StatusBadRequest, map[string]any{
		"error": "shared identity is not supported for WeCom",
	})
	return
}
```

Keep false idempotent.

- [x] **Step 6: Verify and commit**

```bash
gofmt -w internal/gateway/channels.go internal/gateway/channels_test.go internal/setup/server.go internal/setup/handlers_agent_channels.go internal/setup/handlers_agent_channels_wecom_test.go
go test -count=1 ./internal/gateway -run TestRegisterWeCom
go test -count=1 ./internal/setup -run 'Test(ConnectAgentWeCom|UpdateAgentWeCom|DisconnectAgentWeCom)'
git add internal/gateway/channels.go internal/gateway/channels_test.go internal/setup/server.go internal/setup/handlers_agent_channels.go internal/setup/handlers_agent_channels_wecom_test.go
git commit -m "feat(setup): connect wecom bots to agents"
```

Expected: list exposes Bot ID and a masked token, never Secret.

### Task 10: Add dashboard flow and official R2 brand asset

**Files:**
- Modify: `web/src/lib/api.ts`, `web/src/components/channel-icon.tsx`
- Modify: `web/src/app/agents/[id]/channels/page.tsx`
- Temporary source: `https://wwcdn.weixin.qq.com/node/wwnl/wwnl/style/images/officialImages%24h66b2153.svg`
- Temporary output: `_sandbox/wecom-channel/wecom.webp`
- Final R2 key: `brand/channels/wecom-logo-20260721.webp`

- [x] **Step 1: Prepare official asset**

The official website maps `.ww_officialImg_LogoSingle` to sprite rectangle `x=96`, `y=240`, `width=31`, `height=26`. Run:

```bash
mkdir -p _sandbox/wecom-channel
curl -fsSL 'https://wwcdn.weixin.qq.com/node/wwnl/wwnl/style/images/officialImages%24h66b2153.svg' -o _sandbox/wecom-channel/official-sprite.svg
magick -background none _sandbox/wecom-channel/official-sprite.svg -crop 31x26+96+240 +repage -resize 124x104 -define webp:lossless=true _sandbox/wecom-channel/wecom.webp
file _sandbox/wecom-channel/wecom.webp
```

Expected: a transparent WebP containing only the official WeCom mark at its original aspect ratio. Inspect visually. Do not use personal WeChat, AI imitation, SVG recreation, or CSS-drawn geometry.

- [x] **Step 2: Upload without exposing R2 credentials**

```bash
node skills/r2-uploader/scripts/upload.mjs --file _sandbox/wecom-channel/wecom.webp --key brand/channels/wecom-logo-20260721.webp
```

Expected: JSON with HTTPS public URL. Verify `Content-Type: image/webp`; put the exact returned URL in the centralized map. Do not commit temporary inputs or credentials.

- [x] **Step 3: Add typed API helper**

```ts
export async function connectAgentWeCom(
  agentId: string,
  input: { botId: string; secret: string },
): Promise<{ ok: boolean; botId?: string; error?: string }> {
  const res = await apiFetch(`/api/agents/${agentId}/channels/wecom`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
  return res.json();
}
```

Match the existing channel helpers: return the decoded body and let the dialog render its `error` field. Do not invent a second fetch/error abstraction.

- [x] **Step 4: Centralize icon and label**

Add returned URL to `ASSETS.wecom`; return `WeCom` from `channelLabel`; reuse shared `ChannelIcon` in the Channels page; remove its duplicated local map/function.

- [x] **Step 5: Implement card/dialog**

Add WeCom to the existing catalog. Dialog has Bot ID; Secret password with `autoComplete="new-password"`; disabled connect until both trimmed values exist; `Connecting…`; concrete backend error; success title `Connected`; body `The WeCom bot is connected to this agent.`; visible Bot ID; never Secret; no Shared identity; state cleared on close.

Exact help copy:

> Create an API-mode Intelligent Bot in WeCom, select Long Connection, then paste its Bot ID and Secret. No public callback URL is required.

Reuse existing components/tokens; add no literal colors or one-off inline styles.

- [x] **Step 6: Verify, clean temporary assets, commit**

```bash
cd web
pnpm exec eslint src/lib/api.ts src/components/channel-icon.tsx src/app/agents/'[id]'/channels/page.tsx
pnpm build
```

Manually check desktop/narrow sizes, password clearing, disabled/pending/error/connected states, icon, and no Shared identity. Remove only `_sandbox/wecom-channel/` after confirming R2 availability.

```bash
git add web/src/lib/api.ts web/src/components/channel-icon.tsx web/src/app/agents/'[id]'/channels/page.tsx
git commit -m "feat(web): add wecom channel setup"
```

### Task 11: Full regression, security review, and real smoke test

> Execution note (2026-07-22): focused and full Go tests, targeted race tests, TypeScript, and production web build pass. Full `pnpm lint` still reports the repository's pre-existing 25 errors and 23 warnings; the focused Channels-page result has no WeCom-introduced findings. Real-credential smoke steps remain intentionally unchecked.

**Files:**
- Verify all changed files
- Update approved design only for factual corrections
- Update this plan's checkboxes while executing

- [x] **Step 1: Focused checks**

```bash
gofmt -l internal/bus internal/channels internal/gateway internal/setup internal/agent
go test -count=1 ./internal/bus ./internal/channels ./internal/gateway ./internal/setup ./internal/agent
```

Expected: `gofmt -l` prints no changed Go file; focused tests PASS. Format only a reported feature file, never a whole unrelated directory.

- [x] **Step 2: Full checks**

```bash
go test -count=1 ./...
cd web && pnpm lint && pnpm build
```

Expected: PASS. Report baseline failures; hide nothing.

- [x] **Step 3: Security/scope review**

```bash
git diff --check
git status --short
git diff --stat dev...HEAD
git diff dev...HEAD
```

Verify no Bot Secret, AES key, media URL, raw auth frame, R2 credential/token, fixture/screenshot leak, unrelated file, or generated artifact. Verify `SharedIdentity=false`, non-WeCom final-only, ack-after-send, old-socket cancellation, and acceptance coverage.

- [ ] **Step 4: Enter real credentials only in local dashboard**

Never paste Bot ID/Secret into chat, shell history, source, env files, screenshots, fixtures, or reports.

- [ ] **Step 5: Real WeCom matrix**

1. Valid credentials connect; invalid credentials do not persist.
2. DM receives one cumulative native stream ending `finish=true`.
3. Internal group `@bot` replies; same group without `@bot` is silent.
4. Two group members share group context while retaining sender identity.
5. Inbound image/file reach agent; outbound generated WebP is image and document is file.
6. Group reminder returns only to that group; DM reminder only to that member.
7. Duplicate `msgid` does not execute twice.
8. Group member cannot use admin-only slash/host operation.
9. Restart reconnects; disconnect/reconnect closes old socket without reconnect war.

- [x] **Step 6: Commit final test/document corrections in isolated branch**

```bash
git add -u
git add docs/superpowers/specs/2026-07-21-wecom-channel-design.md docs/superpowers/plans/2026-07-21-wecom-channel.md
git commit -m "test(wecom): verify channel integration"
```

Skip if no tracked changes. Do not push, merge, deploy, remove worktree, or alter original dirty tree without new instruction.

## Final acceptance map

| Requirement | Primary proof |
|---|---|
| API-mode long connection, no callback URL | Tasks 4 and 9 |
| DM and addressed internal groups | Task 5 + smoke |
| Group-only mention behavior | Upstream contract + non-mention smoke |
| Per-DM/per-group sessions and sender identity | Task 8 |
| Native cumulative streaming | Tasks 6 and 7 |
| Images/files both directions | Tasks 5 and 6 |
| Conversation-scoped proactive sends | Tasks 3 and 6 + cron smoke |
| One active connection across replicas | Existing lease + Task 3 |
| Hot replace/disconnect closes socket | Task 2 + smoke |
| WeCom group never admin | Task 8 |
| Secret/media-value redaction | Tasks 4–6 + diff review |
| Dashboard and official icon | Task 10 |
| Existing-channel regression-free | Tasks 1, 2, 7 + full suite |
