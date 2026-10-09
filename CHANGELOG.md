# Changelog

Notable changes to FastClaw. Items marked **BREAKING** require operator
action on upgrade — read those notes before deploying.

## [Unreleased]

### Added — connectors (Notion, GitHub, Linear, …)

People connect their own accounts in Console → Connectors and agents they
own can read from them in web chats, through the Connany connector service
(configured in System → Tools → Connectors). Read-only for now; never
available in IM, groups, cron or on someone else's agent. See
`docs/connectors.md`.

### Added — chat list working and unread states

Agent and group rows in the chat list show a spinner while any of their
chats is running — including IM and cron turns — and a red dot for unread
replies.

### Changed — long conversations open fast

Chat history pages are read directly from the database instead of loading
the whole conversation for every page, so very long chats open as fast as
short ones. A New chat button now sits next to Share in the chat header.

### Added — agent configuration export / import

Settings → Advanced exports an agent's identity files (SOUL.md, AGENTS.md,
…), prompt mode and its own skills as a ZIP, and imports one — a FastClaw
export or a plain workspace folder — to replace them after a preview.
Model keys, MCP servers, channels, chats and personal memory are never
included.

### Changed — turns survive flaky model APIs

LLM requests now retry up to 6 times (2s → 30s backoff) on timeouts,
dropped connections, 429 and 5xx, instead of ending the turn on the first
response-header timeout; the header timeout is raised from 60s to 180s.
Tool output over 64 KB is trimmed in context and saved in full under
`.tool-outputs/`. Ordinary tool calls no longer hit a fixed 20-iteration
cap — only repeated failures and no-progress loops stop a turn.

### Changed — host-mode commands run in the session workspace

`exec` on the host now starts in the chat's session folder, so generated
files show up in that chat's workspace and links. Links to files an older
version wrote at the agent root still resolve.

### Fixed

- DeepSeek thinking mode no longer fails with "The reasoning_content in the
  thinking mode must be passed back to the API" when a step had no
  reasoning (e.g. behind a routing model).
- Clicking Send no longer immediately stops the run it started.
- Photos sent over IM channels are saved to the workspace, and files an
  agent links from outside the session folder are delivered to IM chats.
- Relative image and HTML links in replies open the workspace file.

### Changed — **BREAKING**: IM channels must be paired before they answer

Connecting a bot proves you control the bot, not which chat account is
you. Channels now have an owner: the console shows a one-time code, and
sending `/pair <code>` to the bot from your own IM account pairs it. Until
then the bot answers every message with a "not paired" notice and routes
nothing to the agent. Only the paired account gets owner (host) access;
everyone else can still chat once the channel is paired.

**On upgrade:** existing WeChat channels are paired automatically with the
account that scanned their QR code. **Every other existing channel stops
replying until its owner pairs it** from the agent's Channels page.
Channels connected by QR scan (WeChat, Feishu, WhatsApp) and iMessage are
paired at connect time.

### Changed — **BREAKING**: LINE requires the channel secret

The LINE webhook used to skip signature verification when no channel
secret was stored, so anyone who knew the bot's userId could post forged
messages. The secret is now required to connect, and the webhook rejects
every event when it's missing. **LINE channels connected without a secret
must be disconnected and reconnected with it.**

### Added — new IM channels

- **WeCom (企业微信):** smart bots over a long connection (no public URL).
  The connect dialog creates and connects a bot by scanning a QR code;
  pasting a Bot ID + Secret still works. Shows WeCom's native "thinking"
  state while the agent works.
- **WhatsApp:** link a number as a companion device by scanning a QR code,
  like WhatsApp Web. Unofficial protocol (whatsmeow) — automated use can
  get a number banned, so use a dedicated number. Device keys live in
  `$FASTCLAW_HOME/whatsapp.db`.
- **iMessage (macOS, admins only):** the Mac fastclaw runs on answers
  direct messages to its Apple ID. Needs Full Disk Access and Automation →
  Messages.

### Improved — channels

- **LINE:** group chats (replies when @-mentioned), inbound images, video,
  audio and files, outbound images (served from `/api/line/media/…`) and
  file links, the loading animation in 1:1 chats, and up to five messages
  per reply. LINE still needs a public HTTPS address for its webhook.
- **Feishu:** the connect dialog creates and connects a bot by scanning a
  QR code; a "Typing" reaction shows on the user's message while the agent
  works.

### Changed — `make dev` uses its own data directory

`make dev` now runs with `FASTCLAW_HOME=~/.fastclaw-dev` on port 18955, so
it never touches the installed release's database. The PID file, logs,
`fastclaw session export` and `fastclaw daemon install` now honor
`FASTCLAW_HOME` too, and a daemon installed for a non-default home gets its
own service name.

### Changed — `/v1/chat/completions` no longer falls back to another agent

Naming an agent (`agent_id` in the body or `X-Fastclaw-Agent-ID`) that
doesn't exist, or that the API key may not use, now returns
`404` with `error.code = "agent_not_found"`. Previously FastClaw silently
answered with the default (or an arbitrary) agent. Requests that don't name
an agent still use the default agent. Scripts that passed a stale or wrong
agent ID will now get a 404 instead of a reply from some other agent.

### Added — billing hooks for hosted deployments

FastClaw stays free of prices and payments; these generic primitives let an
external billing system (e.g. a hosted FastClaw Cloud) charge for usage. See
[docs/billing-hooks.md](docs/billing-hooks.md).

- **Per-turn usage in `/v1/chat/completions`:** `usage` now reports real
  token counts — every model call of the turn, tool loops included — with
  cache read / write split out and `model_calls`. Streaming responses carry
  it on the final chunk. Previously it was always zero.
- **Usage export:** `GET /api/admin/usage/events?after=<id>` returns model
  calls after a cursor, each with the paying `account_id` (end-users and IM
  chatters roll up to their account).
- **Billing hold:** `PUT /api/admin/users/{id}/billing-hold` pauses an
  account. Its agents refuse new turns on every channel, and `/v1` chat
  returns `402` with `error.code = "payment_required"`.
- **Login links:** `POST /api/admin/users/{id}/login-link` mints a
  single-use, short-lived URL that signs a browser into the console as that
  account.

### Added — runtime API for integrating apps

- **Agent management on `/v1`:** `POST/GET/PATCH/DELETE /v1/agents[/{id}]`
  and `PUT /v1/agents/{id}/system-files/{name}`. Agents belong to the API
  key's account — the tenant. `GET /v1/agents` now returns `display_name`,
  `description`, `metadata` and timestamps, reads from the database, and
  supports `limit`/`cursor` pagination and `metadata[key]=value` filters.
  `name` still equals `id` for compatibility.
- **End-user is a data namespace, not an identity switch:** with
  `X-Fastclaw-End-User` or the chat body field `user`, the agent is resolved
  from the account — so its agents are now usable for its end-users (this
  previously returned 404) — while session history, USER.md and personal
  memory stay per end-user under the existing app_user records. No data
  migration.
- **`params.speaker`** (`{"id", "name"}`) tells the agent who is speaking in a
  group conversation; it is per-turn context and never persisted.
- **`project_id`** on `/v1/chat/completions` files the conversation under one
  of the agent's projects.
- **`GET /v1/usage`** accepts `agent_id`, `end_user` and `scope=app`; daily
  rows include `userId` and `endUser`.
- **Unified `/v1` errors:** `{"error": {"type", "code", "message"}}` with a
  stable `code` (including 401s and rate limits).
- **`agent` keys** default to their only granted agent when a request
  names none.
- **Integration guide for coding agents:** the skill
  `skills/agent-integration/SKILL.md` is the integration guide — configuring
  `FASTCLAW_BASE_URL` / `FASTCLAW_API_KEY`, agent-scope keys for fixed
  agents, user-scope keys to create and manage agents, chat, usage and
  errors. Every FastClaw serves it at `/skills/agent-integration/SKILL.md` (public). The
  console's new **Integration** page links it and builds a ready-to-paste
  prompt for the coding agent wiring another app to FastClaw. It replaces
  `skills/fastclaw-api-integration`; `docs/upstream-api.md` now points to it.
- **Files from `/v1` conversations:** files the agent creates or changes
  in a turn come back with the reply in `files` (top level, or on the final
  streaming chunk) with a download URL —
  `GET /v1/agents/{id}/sessions/{session_key}/files/{path}`, limited to the
  caller's namespace (an end-user only reads their own conversations).
  `"return_files": "inline"` adds small files as data URLs. Agents are told
  never to upload users' files to third-party hosts to produce a link.
- **Multimodal message content:** `/v1/chat/completions` accepts a
  message `content` given as an array of parts — OpenAI `text` /
  `image_url` and Anthropic `image` (base64) — so OpenAI- or
  Anthropic-shaped clients can send images unchanged; the last user
  message's images are handled like the `images` field.
- **On-demand agent loading:** accounts with more than 50 agents
  (`FASTCLAW_EAGER_AGENT_LIMIT`) load agents on first use and drop idle ones.
  Agents bound to IM channels or with enabled cron jobs are still loaded at
  startup and never dropped, and cron/channel/webhook dispatch attaches any
  agent it needs. Smaller accounts load exactly as before.

### Added — personal skills

Every account can now install skills for itself from `/console/skills`,
with the same search / install / zip upload / configure / remove flow as an
Agent's Skills page. They live in `~/.fastclaw/users/<uid>/skills/` (and the
object store) and load in all of that account's conversations, with any
agent. Skills installed for the whole deployment are listed as shared;
accounts can set their own credentials for them. API: `scope: "user"` on
`POST /api/skills/install`, `?scope=user` on `GET /api/skills`,
`POST /api/skills/upload` and `DELETE /api/skills/{name}`.

### Changed — management pages moved under `/console`

The web UI now has two areas: the chat (`/agents/<id>/chat/…`,
`/teams/…`, unchanged) and the console at `/console` for managing agents,
models, skills, tools, plugins, channels, cron and API keys. Per-agent
configuration pages moved from `/agents/<id>/<tab>` to
`/console/agents/<id>/<tab>`; `/overview` is now `/console`. Old URLs
(including `/agents/?manage=1`) redirect to their new location.

The console sidebar is one flat list of the account's own pages —
Overview, Agents, Models, Skills, API Keys — for every account.
`/console/models` and `/console/skills` show the caller's own (user-level)
configuration.

Super_admins manage the deployment from `/admin`, with its own sidebar: Users, Chats, Token Usage,
Models, Skills, Tools and About. These pages left the Settings dialog,
which now holds only personal preferences (Account, General).
`/console/tools` and `/tools` redirect to `/admin/tools/`.

A narrow rail on the far left switches between the areas: the FastClaw
logo on top, then Chat, Console, Admin (super_admins only), and at the
bottom the signed-in account (menu: Settings, Log out). Each area's sidebar
is titled with its name. Each area reopens
the page the user last had open there. After sign-in users return to the
area they were last in (their last conversation by default). Accounts
without agents land on `/console/agents/` to create one.

### Fixed

- **Security: chatters of an agent attached into their own space were
  treated as its operator.** API end-users (`X-Fastclaw-End-User` / `user`),
  public-link visitors and admins chatting with someone else's agent got
  operator trust — on self-hosted installs that meant host-shell `exec` and
  host file access. Operator trust now follows the agent's real owner
  (`agents.user_id`); everyone else is a guest whose commands run in the
  sandbox (or are refused without one).
- The agent owner's session file panel now shows files of conversations
  that belong to other users (e.g. an app's end-users over the API).

- `GET /v1/usage?user_id=` and the `/v1/quota` endpoints accepted any user
  ID. They now only accept the key's own account or one of its end-users
  (platform-admin keys excepted), returning `403 forbidden` otherwise.
- **Cron jobs fired ~hours late on Postgres when the server ran in a
  non-UTC timezone.** The `cron_jobs` time columns were declared
  `TIMESTAMP WITHOUT TIME ZONE`. A Go `time.Time` carrying a non-UTC
  offset (e.g. `Asia/Shanghai`) was written with its offset silently
  dropped, so a Beijing "09:00" job was stored as `09:00` and read back
  as `09:00 UTC` = `17:00 Beijing` — firing 8h late (or, in the opposite
  direction, never matching `next_run <= now()` at all). SQLite was
  unaffected. The columns are now `TIMESTAMPTZ`, which preserves the
  instant across write/read regardless of offset or session `TimeZone`.

### BREAKING — cron schedule state is reset on upgrade (Postgres only)

When a Postgres deployment runs the schema migration for the first time,
**every existing row in `cron_jobs` is deleted.** This is deliberate:
the stored `next_run` wall-clocks already carry the wrong timezone, so
converting them would freeze the bug into the new column type. A clean
reschedule is the correct recovery.

- **What is lost:** pending scheduled jobs (recurring `cron`, `interval`,
  and not-yet-fired `once` reminders).
- **What is NOT lost:** chat history (`sessions`, `session_messages`),
  agent identity files, provider/channel config — none of these are
  touched.
- **Operator action required:** after upgrading, any recurring schedule
  a user relies on (e.g. "every day at 9am") must be recreated by asking
  the agent again, or via the dashboard's Scheduler tab. The original
  `create_cron_job` tool calls are still visible in chat history and can
  serve as a reference for what to rebuild.
- **Visibility:** the gateway logs a single
  `level=WARN msg="resetting cron schedule state for timestamptz migration …"`
  line with the row count before wiping, so operators can tell from the
  upgrade log whether any jobs were affected.
- **SQLite deployments:** unaffected — the migration is skipped entirely.
- **Idempotent:** re-running the migration (e.g. on every daemon boot)
  is a no-op once the columns are already `timestamptz`.
