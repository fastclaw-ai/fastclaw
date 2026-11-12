# Connectors

People connect their own third-party accounts (Notion, GitHub, Linear, …)
in Console → Connectors, and their agents read those accounts when they chat
with them on the web. Built on the Connany
connector service, adapted from fleet's connectors
(`apps/web/docs/connectors-spec.md`).

Status: reads, and **writes the person approves call by call**; **1:1 chats with your own agents** (web, and a product's end users over `/v1`).

## Configuration

A super admin sets it in **System → Tools → Connectors**: the Connany address
and project API key. Saving checks them against the service and applies at
once — no restart. The key stays on the server: the form only ever sees its
last four characters.

Alternatively, environment variables (used when nothing is saved in the UI):

| Env | |
| --- | --- |
| `FASTCLAW_CONNANY_URL` | Connany's origin. HTTPS, or HTTP on `localhost` for development. |
| `FASTCLAW_CONNANY_API_KEY` | The project key. Scrubbed from the process environment after boot. |

For `make dev`, the env vars can go in an untracked `.env.dev`.

Each person then connects their own accounts in **Console → Connectors**
(below Skills in the console sidebar).

### A person's own Connany key

Like models and skills, a person may bring their own: in **Console → Tools
→ Connectors** (settings only; the accounts stay in Console → Connectors),
**Use my own key** takes a Connany address and project key (checked
against the service before it is saved, `PUT /api/connectors-config`). Their
accounts then live in their Connany project, and their agents use it;
without one they use the deployment's service. So connectors work for
someone who brings a key even when the deployment configured none — with
neither, the page asks for a key and agents get no tool.

- The address must be a public `https://` origin: every address the host
  resolves to is checked (again at dial time) against loopback, private,
  link-local and CGNAT ranges, so the setting can't reach the server's own
  network.
- The key is stored per user (a `setting` config row in their scope) and
  never leaves the server; the form sees its last four characters.
- Accounts don't move between projects: switching keys shows the accounts
  of the project now in use.
- Rate limits are per project key: one key cooling down after a 429 doesn't
  hold up the others.
- A product's end users (app_users, `X-Fastclaw-End-User`) use the product
  account's own key, never one of their own (`connectors.For`, source
  `account`), else the deployment's. Their accounts still belong to them
  alone (subject = their app_user id).

## Ownership

- Connany only knows an opaque `external_user_id`, the **subject**:
  `fastclaw:user:<userId>` (`connectors.Subject`). Stable forever — changing it
  orphans every connection.
- The subject comes only from the authenticated session (HTTP) or the turn's
  chatter (agent). Never from a request body, URL or the model. Connany checks
  that every connection / session id belongs to the subject.
- Connany holds the credentials, connections and tools. FastClaw stores only
  `connector_accounts` (local name + default per account) and
  `connector_requests` (authorizations, and which conversation to wake).

## Where accounts are usable

The `connectors` tool is mounted (`Agent.mountConnectors`) only on:

- a **web** turn whose **chatter owns the agent**; or
- an app **end user's** own 1:1 turn (web or `/v1` API, `X-Fastclaw-End-User`)
  on an agent of the app's account marked as theirs (`endUserOwner`, see
  `gateway.endUserConnectorGate`). Their subject is their own app_user id,
  so accounts are theirs alone, usable across all of their agents — but not
  on the app's other public agents, whose instructions are someone else's.
  Exception: agents the app marks `endUserConnectors: true`
  (`PUT /api/agents/{id}` or `/v1`, account callers only) — its own official
  agents, whose instructions it vouches for — take every end user's
  accounts. WeClaw marks the agents its staff created. The app
  passes its own connect page as the turn param `__fastclawConnectorsURL`
  (hidden from the model's client-parameters note), and optionally
  `__fastclawConnectorsReturnURL`. The tool's request actions then mint a
  one-time authorization link (no ChatRef: an IM turn can't be woken) for
  the model to send in the 1:1 chat, returning to that URL; if minting
  fails, they return the connect page with `?connector=<name>`. On the
  app's own page, authorization returns there (`returnUrl` on
  `POST /api/connector-authorizations`, end-user callers only).

Not in IM channels, groups, cron, or on someone else's agent — there the
reader of the reply, or the author of the agent's instructions, isn't the
account's owner. The tool is unregistered at the start of every turn, so a
mount never carries over to the next turn.

## The tool

One tool, several actions; the model names platforms and account **names**,
never ids:

| action | |
| --- | --- |
| `accounts` | the person's accounts (name, status, needs_access, default), plus platforms connected before but disconnected now |
| `list_tools` | search an account's tools with their input_schema and `read_only` (only the read-only ones when the turn can't ask for approval) |
| `call_tool` | run a tool: a read-only one at once, one that changes data only after the person approves it (see Writes) — whether a tool only reads is Connany's current definition, not the model's word |
| `request_connection` | put a connect / reconnect card in the conversation |
| `request_access` | put a "grant access" card (platforms with a separate resource step, e.g. GitHub) |

Default account: the stored one while connected, else the only connected
account; several and none chosen → no default (the model must ask).

Each turn of someone who has connected anything also gets a system note
listing their accounts — without it, an agent took "my todo list" to mean its
own notes rather than the person's Todoist.

## Writes

A tool that changes data (Connany's `read_only: false`) never runs on the
model's call alone: the person approves each call in the chat, while the
turn waits.

- Offered only when the turn can ask: it streams to the person who owns the
  accounts — the dashboard chat, or a `/v1` stream with `local_agents` and
  an explicit session key (e.g. WeClaw relaying WeChat). Elsewhere the tool
  lists read-only tools only, as before.
- The request uses run_local_agent's channel (`agl.Tools.Ask`): one open
  request per chat; the web chat shows buttons, a `/v1` caller gets a
  `local_agent` chunk `{"kind":"approval"}` and answers with
  `POST /v1/local-agents/approval` (WeClaw: the person replies 1/2/3).
- The request names the platform, the account, the tool and the exact input
  (clipped to 300 characters). The tool definition is read from Connany when
  the call is made, so the model can't call a write a read.
- `allow` runs it once; `allow_session` also lets that tool on that account
  run without asking in this chat for 12 hours (in memory, per process);
  `deny`, or no answer in 10 minutes, runs nothing and tells the model so.

## Cards and waking

1. `request_connection` / `request_access` record a request and return tool
   metadata `connectorRequest` (rendered as a card, persisted with the tool
   message) and `endsTurn` (the loop ends the turn after that tool round, so the
   model can't claim anything happened yet).
2. Clicking the card opens `/connectors/open` in a new tab, which mints the
   one-use link and redirects. Links are never stored or shown in the chat.
3. Connany sends the browser back to `/connectors/done?connany_session_id=…`.
   That id is only a pointer: the server looks it up among the person's own
   requests and asks Connany how it ended.
4. On success (or "Not now" on the card), the conversation is woken once
   (`notified_at` guard) with a `[connector] …` note as a `connector`-source
   input — a runtime message, never shown as a user bubble — and a turn runs
   like a typed message (`Server.wakeConnectorChat`).

## Not yet

- Writes where the turn can't ask (IM channels, cron), custom MCP servers.
- IM channels, groups, API end users.
- Polling Connany's event stream: a revocation on the platform is noticed on
  the next call.
- Multi-replica: caches and the 429 cooldown are per process.
