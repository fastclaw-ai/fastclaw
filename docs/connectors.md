# Connectors

People connect their own third-party accounts (Notion, GitHub, Linear, …)
in Console → Connectors, and their agents read those accounts when they chat
with them on the web. Built on the Connany
connector service, adapted from fleet's connectors
(`apps/web/docs/connectors-spec.md`).

Status: first version — **read-only**, **web 1:1 chats with your own agents**.

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

Neither → connectors are off: the Connectors page shows "not available", agents get no
tool. For `make dev`, the env vars can go in an untracked `.env.dev`.

Each person then connects their own accounts in **Console → Connectors**
(below Skills in the console sidebar).

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

The `connectors` tool is mounted only on a **web** turn whose **chatter owns
the agent** (`Agent.mountConnectors`). Not in IM channels, groups, cron, the
`/v1` API, or on someone else's agent — there the reader of the reply, or the
author of the agent's instructions, isn't the account's owner.

## The tool

One tool, several actions; the model names platforms and account **names**,
never ids:

| action | |
| --- | --- |
| `accounts` | the person's accounts (name, status, needs_access, default), plus platforms connected before but disconnected now |
| `list_tools` | search an account's read-only tools with their input_schema |
| `call_tool` | run a read-only tool — whether a tool only reads is Connany's current definition, not the model's word |
| `request_connection` | put a connect / reconnect card in the conversation |
| `request_access` | put a "grant access" card (platforms with a separate resource step, e.g. GitHub) |

Default account: the stored one while connected, else the only connected
account; several and none chosen → no default (the model must ask).

Each turn of someone who has connected anything also gets a system note
listing their accounts — without it, an agent took "my todo list" to mean its
own notes rather than the person's Todoist.

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

- Writes (approval cards, as in fleet §6), custom MCP servers.
- IM channels, groups, API end users.
- Polling Connany's event stream: a revocation on the platform is noticed on
  the next call.
- Multi-replica: caches and the 429 cooldown are per process.
