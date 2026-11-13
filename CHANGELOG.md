# Changelog

Notable changes to FastClaw. Items marked **BREAKING** require operator
action on upgrade — read those notes before deploying.

## [Unreleased]

### Changed — custom MCP servers are offered on agents; OAuth is per person

**BREAKING (API):** a custom MCP server is no longer mounted on its adder's
own turns automatically. Whoever made an agent offers their servers on it
(`GET/PUT /api/agents/{id}/mcp-servers`), and everyone chatting with that
agent may use them there. A header server runs on its adder's headers; an
OAuth server is now added without signing in (`POST /api/mcp-servers` no
longer answers `oauth_required`; optional `redirectUri` registers the
client up front), and each person signs in with their own account from a
link the agent sends in the chat (turn param `__fastclawMCPRedirectURI`).
`oauth/start` takes only `{serverId, redirectUri}`; `oauth/finish` with the
account's own key and no end user finishes it for the end user who started
it. The list shows `signIn` and `agentIds` instead of `needsReauth`. Tables
`user_mcp_tokens` and `user_mcp_agent_servers` are created at boot, and an
adder's tokens saved on a server row move to their own sign-in.

An app's end users may now use their own connected accounts on **any** of
the app's agents in their 1:1 chats, not only their own and the ones marked
`endUserConnectors` (which no longer matters).

### Added — OAuth for custom MCP servers

A custom MCP server that asks for OAuth (401 with a `resource_metadata`
challenge, MCP authorization spec) is now added through the authorization
code flow: `POST /api/mcp-servers` answers `oauth_required`, then
`POST /api/mcp-servers/oauth/start` (dynamic client registration + PKCE,
the caller's `redirectUri`) and `POST /api/mcp-servers/oauth/finish`
(`{state, code}`). Tokens are kept with the server and refreshed; a server
whose refresh is refused shows `needsReauth`. Adds the column
`user_mcp_servers.oauth` and the table `user_mcp_oauth_pending` at boot.

### Added — custom MCP servers per person

People can add their own Streamable HTTP MCP servers (`GET/POST
/api/mcp-servers`, `PATCH`/`DELETE /api/mcp-servers/{id}`, end users included).
A server is checked as MCP before it is saved, belongs only to the person
who added it, and is offered to their own turns as the `custom_mcp` tool,
on the same turns as their connected accounts. Tools the server doesn't
mark read-only run only after the person approves them in the chat. New
table `user_mcp_servers` is created at boot. See `docs/connectors.md`.

### Added — macOS privacy prompts name the product, not agl

`POST /v1/machines/pairing` takes an optional `appName` (the product's
display name), stored per product hub scope. Commands from `computer_exec`
and local agent sessions on those computers pass it to agl as `app_name`,
so macOS asks "WeClaw wants to access Downloads" instead of "agl …",
also on computers linked earlier. New install commands carry
`--app-name` when the name is one word. File reads through agl's daemon
(`computer_read`, `send_local_file`) are still attributed to agl.

### Changed — agents pick direct computer tools or the local coding agent

`computer_read/write/edit/exec` (agl's built-in tools) are now mounted on
every linked computer with agl ≥ 0.3.14, not only those without a coding
agent. The model chooses per request: quick look-ups and a command or two
run directly, substantial or multi-step work goes to `run_local_agent`,
and a call the link doesn't allow is handed to the coding agent. With agl
≥ 0.3.15, writes and commands that aren't read-only are approved by the
person in the chat (1/2/3 or the web card) instead of being refused, so
the default `ask` link can do more than read. See `docs/agl-machines.md`.

### Added — force the sandbox from the admin UI

Admin → Tools → Runtime → Sandbox has a "强制在沙盒执行" toggle
(`sandbox.enforce`): every exec runs in the sandbox, never on the host.
It takes effect on save, no restart. Same effect as
`FASTCLAW_SANDBOX_ENFORCE=1`, which (like hosted deploys) still enforces
regardless of the toggle.

### Fixed — BoxLite sandboxes failed to start

BoxLite Cloud started rejecting the `auto_remove` create field, so every
box failed with `property auto_remove should not exist`. A cold image
pull that outlasts BoxLite's start wait (HTTP 408) is now waited out
instead of failing.

### Fixed — cropped browser screenshots in the sandbox image

`camoufox-cli screenshot` captured only the top-left 1280×720 of pages
laid out at the fingerprinted window width. Fixed in the sandbox image,
now published as `thinkany/fastclaw-sandbox:20261112` and used by default.
BoxLite Cloud never re-pulls a moved `:latest`, so the default is now a
dated tag; if Admin → Tools → Runtime pins `thinkany/fastclaw-sandbox`
(or `:latest`), clear the field or set the dated tag.

### Fixed — sandbox settings page showed stale values

The sandbox is deployment-wide but was also copied into users' own
settings whenever they saved anything, and those copies shadowed the
system value on the settings page. It is now system-only, and clearing an
optional field (image, URL) on the page actually clears it.

### Added — connectors for a product's end users

A product calling FastClaw for its end users (`X-Fastclaw-End-User`) can
let each of them connect their own Notion, GitHub, … accounts. The accounts
are theirs alone and usable on the agents marked as theirs
(`endUserOwner`), never on the product's public agents. End users run on
the product account's own Connany key, else the deployment's. In an IM
chat (`__fastclawConnectorsURL` turn param), the agent sends a one-time
authorization link instead of a connect card; the platform returns the
person to `__fastclawConnectorsReturnURL`. `POST
/api/connector-authorizations` takes `returnUrl` for end users. See
`docs/connectors.md` and the agent-integration skill.

### Added — agl download mirror

`FASTCLAW_AGL_RELEASE_URL` points `/agl/download` at a mirror of agl's
release assets, for computers that can't reach GitHub.

### Fixed — tool-call markup in streamed replies

A model that writes its tool calls as text (DeepSeek-style `<｜DSML｜ …>`)
no longer streams that markup: API callers relaying the stream (e.g. into
WeChat) showed it as a message, and the web chat until the round ended.

### Added — bring your own Connany key

Console → Tools → Connectors takes a person's own Connany address and
project key (public HTTPS only, checked before saving). Their accounts and
agents use it before the deployment's connector service, so connectors work
for them even when the deployment configured none. See
`docs/connectors.md`.

### Added — Console → Tools

A new Console → Tools page (below Connectors), laid out like Admin → Tools,
holds a person's own tool settings: used first, with the deployment's tools
as the fallback. Its first entry is which Connany holds their connector
accounts; the accounts themselves stay in Console → Connectors.

### Added — one-click agl updates

Console → Devices marks a computer whose agl is behind the newest release
(looked up hourly) and updates it with one click (`POST
/v1/machines/{id}/update`); a running task is never ended, the update waits
until the computer is idle. Needs agl 0.3.5 on the computer: older ones
show `agl update` to run there once.

### Added — connect your own computer (agl)

Console → Devices links a person's own Mac or Linux computer with
[agl](https://github.com/thinkany-ai/agl): one install command, then the
agents they chat with — in the web chat or a 1:1 IM chat — can hand tasks
to the coding agents on it (Claude Code, Codex, Gemini, Kimi, Grok, …)
with `run_local_agent`, and bring files back with `send_local_file`. When
the coding agent asks to run a command or edit a file, the web chat shows
Allow / Allow for this session / Deny buttons (IM chats reply 1/2/3).
Per computer: whether agents may use it, the permissions runs start with
and the default folder; per local agent: on/off, model and reasoning
effort, and a test run. Integrating products get the same through
`/v1/machines` with `X-Fastclaw-End-User`. Only the computer's owner can
see or use it. See `docs/agl-machines.md`.

### Removed — `local-coding-agents` skill

The bundled skill that ran the gateway host's `codex` / `claude` CLIs with
`exec` is gone; its unmodified installed copies are deleted on startup (an
edited copy is kept). On a self-hosted install, link the host itself in
Console → Devices to keep using its coding agents.

### Changed — BoxLite defaults to BoxLite Cloud

The BoxLite sandbox now defaults to `https://api.boxlite.ai/v1` with no
path prefix, and its image is optional (`thinkany/fastclaw-sandbox:latest`
when empty; any public registry image works). A URL set to the old
`api.dev.boxlite.ai` host keeps its `default` prefix. The unused
`FASTCLAW_SANDBOX_BOXLITE_CLIENT_ID` setting was removed.

### Added — per-turn cost in USD

Each model call is now costed at the unit price of the model it actually
ran on (USD per 1M tokens: input, output, cache read, cache write).
`/v1/chat/completions` reports the turn's total as `usage.cost_usd` on the
non-streaming response and the final stream chunk, and
`GET /api/admin/usage/events` carries `cost_usd` on every event (a new
`token_usage_log.cost_usd` column, added on upgrade). When any call's model
has no price the cost is `null` — unknown, never 0; rows from before the
upgrade read as `null` too. Set prices per model in Console → Models →
provider; well-known Anthropic and OpenAI models fall back to built-in list
prices when left blank, and a price you enter always wins. FastClaw still
only reports cost — it doesn't hold balances or convert to credits.

Context compaction, sub-agent (`delegate_task`) calls, memory extraction
and skill learning are now metered too; before, only the main loop's calls
were recorded.

### Changed — API streams the turn as it runs

`stream: true` on `/v1/chat/completions` now runs the same agent loop as the
web chat: the agent's text arrives as it is written, including what it says
between steps, and each tool call is reported on a chunk with an empty
delta and a top-level `tool` object (`{"id", "name", "status": "running" |
"done"}`). The final answer costs one model call instead of two. Separate
messages in a reply are joined by a blank line instead of a literal
`<|split|>` marker.

### Changed — failed API turns are errors

When the agent's model API fails, or the model gives no usable answer, a
non-streaming request now answers `502` with `{"error": {"code":
"agent_error", ...}}` instead of a `200` apology, and a stream ends with
that `error` object on its final chunk. Integrators that treated every
`200` as a delivered answer should handle it (don't charge, offer a retry).
The reason is also saved in the conversation, so the console shows why a
turn failed.

### Added — `/model` in `fastclaw chat`

The terminal chat switches the current agent's model with
`/model [provider/model|default]`, listing the same models as the console,
and its footer keeps the key hints on one line.

### Fixed

- A reply cut off at the model's output limit (usually one huge tool call)
  is dropped and retried with a note to work in smaller pieces, instead of
  running half-written arguments or ending the turn empty.
- DeepSeek thinking mode behind a routing model: a "reasoning_content must
  be passed back" 400 is retried with it, and later calls send it up front.
- Pictures in API conversations and project chats show in the console —
  a reply's file links are resolved against the conversation's own folder.
  Pictures the agent links instead of embedding are shown inline, and an
  image beside text no longer causes a hydration error.
- An agent's model dropdown no longer mixes in models from an inherited
  provider of the same name.

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
Pictures in replies are shown at a bounded size and open full size on
click.

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
