# WeCom Channel Design

**Date:** 2026-07-21

**Status:** Approved in conversation; ready for implementation planning

**Channel identifier:** `wecom`

**User-facing name:** WeCom

## 1. Outcome

Add WeCom as a first-class FastClaw channel using the WeCom Intelligent Bot API in long-connection mode. A WeCom bot binds to one FastClaw agent and supports:

- Direct-message conversations.
- Internal group conversations triggered only when a member mentions the bot.
- Native streaming Markdown replies.
- Inbound and outbound text, images, and files.
- Scheduled and asynchronous messages sent back only to the conversation where the task was created.
- Per-member identity with group users restricted to non-admin capabilities.

The integration must remain part of the Go binary. It must not require a Node.js sidecar, a public webhook, or a third-party channel plugin.

## 2. Confirmed Product Decisions

| Area | Decision |
|---|---|
| WeCom product | Intelligent Bot created in API mode |
| Transport | Outbound WebSocket long connection |
| Credentials | Bot ID and Secret |
| Binding | One WeCom bot to one FastClaw agent |
| Direct messages | Every supported user message triggers the agent |
| Group messages | Only messages that mention the bot trigger the agent |
| Group context | One shared FastClaw session per WeCom group `chatid` |
| Sender identity | Preserve the sender's WeCom `userid` and resolve it to a FastClaw chatter |
| Proactive messages | Only send back to the original DM or group where the task was created |
| Message types | Text, image, and file in both directions |
| Deferred types | Voice and video |
| Reply UX | Native WeCom streaming Markdown; media appended when the stream finishes |
| Access | Every member in the bot's WeCom visibility scope may use it |
| Group privilege | Group messages never receive FastClaw admin privilege |
| Shared identity | Always disabled for WeCom bindings |
| Supported groups | WeCom internal groups only |

## 3. Non-goals

The first release will not include:

- The legacy WeCom self-built application flow using Corp ID, Agent ID, callback Token, or EncodingAESKey.
- Notification-only group robot webhooks.
- Public callback/Webhook mode for Intelligent Bots.
- Customer/external group support.
- Arbitrary employee, department, tag, or group selection for proactive messages.
- Multiple FastClaw agents behind one WeCom bot.
- Agent switching commands inside a WeCom conversation.
- Voice or video messages.
- Contact-directory synchronization.
- Conversation-history import from WeCom.
- A separate WeCom admin allowlist UI.

## 4. Why Long Connection

Three approaches were considered:

1. **WeCom Intelligent Bot long connection — selected.** It supports bidirectional direct and group conversations, native streaming replies, proactive sends, and requires only Bot ID and Secret. It works for local and cloud FastClaw deployments without exposing an inbound URL.
2. **Group robot webhook — rejected.** It is appropriate for notifications but not a complete interactive FastClaw channel.
3. **Self-built WeCom application — rejected for this scope.** It adds Corp ID, Agent ID, application Secret, public HTTPS callbacks, Token, EncodingAESKey, and a larger permissions surface without improving the requested chat experience.

## 5. Architecture

### 5.1 Adapter

Add a Go adapter under `internal/channels` that implements the existing `channels.Channel` interface. It will use the already-installed `github.com/gorilla/websocket` dependency and Go standard-library crypto packages. No new runtime dependency is required.

The adapter owns:

- WebSocket connection and authentication.
- Heartbeats and reconnect backoff.
- Protocol frame correlation by `req_id`.
- Inbound message parsing and media retrieval.
- Native stream lifecycle and proactive message sending.
- Media upload and `media_id` handling.
- Sensitive-value redaction.

The adapter reports:

- `Name() == "wecom"`
- `AccountID() == Bot ID`
- `BotUsername() == Bot ID`

Using Bot ID as the bot username gives existing group-mention routing a stable value. WeCom only delivers addressed Intelligent Bot group messages; the adapter synthesizes a mention containing Bot ID so the existing binding resolver can select the one agent bound to that bot.

### 5.2 Connection lifecycle

The adapter connects to the official default endpoint:

```text
wss://openws.work.weixin.qq.com
```

It authenticates with an `aibot_subscribe` frame containing Bot ID and Secret, sends protocol heartbeats, and reads message and event frames until its context is cancelled.

WeCom is registered through the existing singleton-channel path. In multi-replica deployments, the existing channel lease ensures only the lease holder opens the WebSocket for a given Bot ID.

Because WeCom replies and proactive sends must use that authenticated WebSocket, Redis-backed deployments must not deliver WeCom outbound messages through the existing shared outbound consumer group: that group may choose a replica that does not hold the channel lease. Instead, publish WeCom outbound messages to a durable stream keyed by `(channel, Bot ID)`. The active adapter consumes that targeted stream only after it acquires the channel lease and acknowledges an entry only after WeCom accepts the send. A new lease holder claims pending entries before reading new ones. In-memory, single-instance deployments keep the existing local outbound path.

The channel manager needs a scoped lifecycle correction: replacing or unregistering a running channel must cancel that channel's context before removing it from the routing table. This prevents a replaced WeCom bot, and existing hot-replaced persistent channels, from leaving an old background connection alive.

### 5.3 Storage

Reuse `store.ChannelRecord` and the current channel configuration shape:

- `Type`: `wecom`
- `AccountID`: Bot ID
- `BotToken`: Secret
- `Enabled`: `true`
- `SharedIdentity`: `false`
- `AgentID`: selected FastClaw agent
- `UserID`: agent owner

No database migration or new credential table is required. Existing masking rules must ensure list and detail APIs never return the Secret in plaintext.

## 6. API and Dashboard

### 6.1 Connect endpoint

Add:

```http
POST /api/agents/{agentId}/channels/wecom
Content-Type: application/json

{
  "botId": "...",
  "secret": "..."
}
```

Success:

```json
{
  "ok": true,
  "botId": "..."
}
```

The handler must:

1. Require ownership or existing super-admin authorization for the agent.
2. Reject empty Bot ID or Secret.
3. Establish a bounded WebSocket authentication attempt.
4. Return the real WeCom `errcode` and `errmsg` when authentication fails.
5. Save the binding only after successful authentication.
6. Force `SharedIdentity=false`, regardless of request content.
7. Close any old adapter for the same binding before hot-starting the replacement.

If validation succeeds but persistence fails, close the validation connection and return the store error. Do not leave an untracked bot connection running.

### 6.2 Disconnect

Reuse the existing generic channel disconnect endpoint. Disconnecting removes only the FastClaw binding and closes its running WebSocket; it does not delete the robot in WeCom.

### 6.3 Channel card and dialog

Add a **WeCom** card to Agent → Channels and reuse the existing channel-card and dialog components.

The dialog contains only:

- `Bot ID`
- `Secret` as a password field
- `Cancel`
- `Connect` / `Connecting…`

Final English description:

> Create an API-mode Intelligent Bot in WeCom, select Long Connection, then paste its Bot ID and Secret. No public callback URL is required.

Success state:

- Heading: `Connected`
- Body: `The WeCom bot is connected to this agent.`
- Show Bot ID, because it is an identifier rather than a secret.
- Never repopulate or reveal Secret.

The WeCom card must not show a Shared identity toggle. The backend remains authoritative and rejects any attempt to enable it.

### 6.4 Brand asset

Use an official WeCom brand icon, not the existing personal WeChat icon and not an AI-generated imitation. During implementation, obtain the icon from an official WeCom property, preserve its brand geometry, convert the prepared asset to WebP, upload it to the project's R2 channel-asset path, and keep the final URL in the existing centralized channel-icon map. The feature must not ship until the official WeCom asset is in place.

## 7. Inbound Message Flow

### 7.1 Common processing

For each `aibot_msg_callback` frame:

1. Validate required protocol fields.
2. Ignore messages from the bot itself.
3. Use WeCom `msgid` as FastClaw `MessageID`.
4. Store the short-lived `msgid -> req_id` correlation required for replies.
5. Build a `bus.InboundMessage`.
6. Publish it to the existing message bus.

Gateway deduplication remains authoritative. The adapter may suppress duplicate frames early, but it must not replace the existing gateway dedup layer.

### 7.2 Direct messages

Map:

- `Channel = "wecom"`
- `AccountID = Bot ID`
- `ChatID = from.userid`
- `UserID = from.userid`
- `PeerKind = "dm"`
- `MessageID = msgid`

Direct-message sessions are isolated by user.

### 7.3 Group messages

Map:

- `Channel = "wecom"`
- `AccountID = Bot ID`
- `ChatID = chatid`
- `UserID = from.userid`
- `PeerKind = "group"`
- `MessageID = msgid`
- `Mentions = [Bot ID]`

The adapter treats the WeCom addressed-bot callback as the mention signal. It must not attempt to process arbitrary group traffic. FastClaw then routes the message through the existing group binding path to the single agent bound to the Bot ID.

Every group member shares the session identified by `(wecom, Bot ID, chatid)`, while each turn retains the resolved chatter identity for attribution and per-user memory rules.

### 7.4 Text

Forward text content after removing only the platform's bot-addressing marker when it is present. Do not strip other mentions or rewrite user content.

### 7.5 Images and files

WeCom media download URLs are short-lived and may be encrypted. The adapter must download the bytes immediately, enforce the existing FastClaw inbound-media size boundary, decrypt according to the WeCom `aeskey` contract, determine a safe filename/content type, and publish a `bus.MediaItem`.

On download, size, integrity, or decryption failure:

- Do not send corrupt bytes to the model.
- Return an explicit attachment-processing error to the originating conversation.
- Log the error without the temporary URL or AES key.

Voice and video callbacks are acknowledged but reported as unsupported rather than silently interpreted as text.

## 8. Streaming Reply Flow

WeCom native streaming uses `aibot_respond_msg`, a stable stream ID, cumulative Markdown content, and a final frame with `finish=true`.

The existing IM task path calls `HandleMessage` and publishes only the final result. For `wecom`, reuse `Agent.HandleMessageStream` instead:

1. Open a WeCom stream immediately for the inbound `req_id` so a long-running tool call does not leave the user without feedback.
2. Consume provider chunks from `HandleMessageStream`.
3. Accumulate content because WeCom stream updates replace content rather than append deltas.
4. Throttle outbound updates to avoid one WebSocket frame per token.
5. Reuse one generated stream ID for every update associated with the inbound `msgid`.
6. On completion, run the existing reply media extraction against the accumulated final text.
7. Send the final cumulative Markdown with `finish=true` and attach supported image items.
8. Upload remaining files and send them to the same conversation after the final stream frame.

The transport-neutral bus message should carry an explicit stream lifecycle rather than overloading empty text or `EditMsgID`. A small enum/string field such as `start`, `update`, and `finish`, plus a stable stream ID, is sufficient. Channels that do not support streaming ignore these fields and retain their current final-message behavior.

If the turn fails, finish the stream with a clear error. Do not leave the WeCom client displaying a permanently active stream.

## 9. Proactive Messages

Scheduled reminders, task completion notices, cron output, and other runtime-originated messages use `aibot_send_msg` and do not require an inbound `req_id`.

Targets are limited to the persisted originating conversation:

- DM: `chatid = member userid`
- Group: `chatid = group chatid`

The integration does not expose arbitrary recipient selection. Existing cron records already preserve channel, account ID, chat ID, agent ID, and owner ID, so no scheduler schema change is required.

Proactive text uses a complete Markdown message. Proactive images and files use the WeCom media upload flow and the returned temporary `media_id`.

## 10. Identity and Security

WeCom bindings always use `SharedIdentity=false`. The gateway resolves raw WeCom `userid` values into stable FastClaw chatter users through its existing channel identity path.

Policy:

- Any member included in the bot's WeCom visibility scope may converse with it.
- Group turns are always non-admin.
- Group users cannot read identity files, modify agent configuration, or use host-level privileged capabilities.
- Normal answering and tools allowed by the agent's current sandbox/policy remain available.
- Host execution remains governed by existing deploy-mode and sandbox policy.

Bot Secret, media AES keys, temporary media URLs, raw authentication frames, and bearer-like protocol values must never appear in logs, API responses, tests, screenshots, or committed fixtures.

## 11. Failure Handling

| Failure | Required behavior |
|---|---|
| Invalid Bot ID or Secret | Do not save; return WeCom `errcode` and `errmsg` |
| Authentication timeout | Do not save; return the timeout cause |
| Network disconnect | Reconnect with exponential backoff and jitter |
| Context cancellation | Close socket promptly and release channel lease |
| Revoked credentials | Stop pointless authentication retries and report the permanent error |
| Another client replaces the connection | Stop and expose the connection conflict; do not create a reconnect war |
| Duplicate `msgid` | Ignore before agent execution |
| Invalid frame | Reject/log the concrete parse error without secrets |
| Media download/decrypt failure | Do not invoke the model with corrupt media; reply with a concrete error |
| Stream update failure | Finish or fail the stream explicitly; do not claim success |
| Proactive send failure | Return/log the protocol error; do not record a successful delivery |
| Store failure after validation | Close the candidate socket and leave the existing binding unchanged |

Reconnect must use bounded exponential backoff. A permanent authentication error and a server `disconnected_event` caused by a competing connection are terminal for that adapter instance until the user reconnects or FastClaw reloads the binding.

## 12. Testing Strategy

Implementation follows TDD: add the smallest failing test for each behavior, then implement the minimum change that passes it.

### 12.1 Protocol and adapter tests

Use a local mock WebSocket server and deterministic fixtures to cover:

- Subscription frame contains Bot ID and Secret but test output redacts Secret.
- Successful and failed authentication.
- Heartbeat behavior.
- Context cancellation closes the socket.
- Recoverable disconnect reconnects with bounded backoff.
- Competing-client disconnect becomes terminal.
- Direct text mapping.
- Group mapping with synthesized Bot ID mention.
- Non-addressed group traffic does not enter the agent path.
- Stable message ID and gateway dedup.
- Image/file download, size limit, integrity check, and AES decryption.
- Voice/video unsupported response.
- Stream ID stability, cumulative content, throttled updates, and final `finish=true`.
- Final image and file delivery.
- Proactive DM and group targets.
- No secret, AES key, or temporary media URL in errors or logs.

### 12.2 Gateway, store, and lifecycle tests

Cover:

- WeCom record decoding and registration as a singleton channel.
- Binding stores Bot ID/Secret in existing fields with `SharedIdentity=false`.
- List API masks Secret.
- Invalid credentials are not persisted.
- Hot replacement and disconnect cancel the old adapter.
- Group chatter never receives admin privilege.
- DM and group sessions resolve to the expected triples.
- Cron output retains the originating WeCom target.
- Existing channel tests continue to pass.

### 12.3 Dashboard tests and checks

Cover:

- WeCom card opens the correct dialog.
- Connect is disabled until Bot ID and Secret are present.
- Secret uses a password input and is cleared when the dialog closes.
- Backend errors are displayed rather than replaced with a generic success state.
- Connected state shows Bot ID but never Secret.
- No Shared identity control is rendered for WeCom.
- Disconnect invokes the existing generic endpoint.

Run the project's available Go tests, web lint/type checks, and production build after implementation.

### 12.4 Real WeCom smoke test

With user-supplied credentials entered directly in the local dashboard:

1. Connect the bot and confirm authenticated state.
2. Send a DM and receive a streaming Markdown reply.
3. Add the bot to an internal group and mention it.
4. Confirm a group message without a bot mention remains silent.
5. Send an image and a file to the bot.
6. Receive an image and a file from the bot.
7. Create a scheduled reminder in a group and confirm it returns only to that group.
8. Restart FastClaw and confirm the bot reconnects.
9. Verify a duplicate callback does not run twice.
10. Verify a group member cannot perform an admin-only operation.

## 13. Acceptance Criteria

The feature is complete when all of the following are true:

1. Valid Bot ID and Secret create a connected WeCom binding without a public URL.
2. Invalid credentials are not stored and expose the concrete WeCom error.
3. Direct text, image, and file messages reach the bound agent.
4. Internal group mentions reach the bound agent; non-mentions do not.
5. Direct sessions are per member and group sessions are per `chatid`.
6. The agent receives stable sender identity for every turn.
7. Replies update one native WeCom stream and finish cleanly.
8. Outbound images and files reach the originating conversation.
9. Scheduled/asynchronous messages return only to the conversation that created them.
10. A repeated `msgid` cannot execute the agent twice.
11. Multi-replica deployments open only one connection per Bot ID.
12. Replacing or deleting a binding closes the old WebSocket.
13. Group chatters cannot gain admin privilege.
14. Credentials and media secrets never leak through UI, API, logs, or fixtures.
15. Existing channels and builds do not regress.

## 14. User-provided Items for Real Integration Testing

The user needs to prepare:

- A WeCom Intelligent Bot created in API mode with Long Connection selected.
- Bot ID.
- Secret, entered directly into the local FastClaw dashboard and not sent in chat.
- A WeCom member inside the bot's visibility scope.
- An internal WeCom group containing that member and the bot.

No Corp ID, Agent ID, public callback URL, callback Token, or EncodingAESKey is needed.

## 15. References

- WeCom Intelligent Bot documentation: <https://developer.work.weixin.qq.com/document/path/101463>
- Official WeCom AI Bot Node SDK package: <https://www.npmjs.com/package/@wecom/aibot-node-sdk>
- Default long-connection endpoint documented by the SDK: `wss://openws.work.weixin.qq.com`
