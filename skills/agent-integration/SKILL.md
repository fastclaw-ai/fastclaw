---
name: agent-integration
description: Integrate an application's backend with FastClaw cloud agents over the FastClaw API. Use when a project needs to chat with one or more existing FastClaw agents (agent-scope API key), or to create, update, delete and chat with agents on demand (user-scope API key) — e.g. "connect this app to FastClaw", "call my FastClaw agent from the backend", "let each user create their own agent".
---

# FastClaw Agent Integration

FastClaw is an agent runtime. It stores agent configuration, runs the agent
loop with its tools and sandbox, keeps sessions and memory, and meters usage.
**Your application** owns everything about its users: sign-in, which user may
use which agent, chat records, billing. Your backend calls FastClaw; FastClaw
never calls your app back and never knows your users.

Follow this guide over guesses from other FastClaw code (the dashboard's
`/api/*` endpoints are internal and change without notice). Integrations use
`/v1/*` only.

## 1. Configure the connection first

The integration needs two values. Never hardcode them; read them on the
server side from configuration (environment variables or the app's secret
store):

| Setting | What it is | Where the user finds it |
|---|---|---|
| `FASTCLAW_BASE_URL` | The FastClaw address, e.g. `https://claw.example.com` (no trailing slash). | The address the user opens the FastClaw console at. The console's **Integration** page shows it. |
| `FASTCLAW_API_KEY` | A FastClaw API key (`fc_...`), sent as `Authorization: Bearer <key>`. | FastClaw console → **API Keys** → **Add API key**. The token is shown once. |

If either is missing, **stop and ask the user** for it — tell them where to
find it (table above) and which key type the integration needs (section 2).
Add both to the project's environment template (`.env.example` or similar)
without real values.

Then verify the connection:

```bash
curl -s "$FASTCLAW_BASE_URL/v1/agents" -H "Authorization: Bearer $FASTCLAW_API_KEY"
```

A `200` lists the agents the key may use; `401 unauthorized` means a wrong or
revoked key.

**Never expose the key to browsers or mobile apps.** Clients talk to your
backend; your backend talks to FastClaw.

## 2. Pick the scenario

| | A. Fixed agents | B. Manage agents |
|---|---|---|
| Use when | The app talks to agents that already exist in FastClaw — one or several (e.g. an image-editing agent, a support bot). | The app creates agents itself, e.g. one per user or per workspace, and manages them over time. |
| Key type | **Agent** key, granted exactly those agents. | **User** key. |
| Can | List and read its granted agents, update their settings, chat with them. | Create, list, read, update and delete agents; chat with any agent of the account. |
| Cannot | Create or delete agents; see any other agent. | — |

A user key reaches **every** agent of its FastClaw account. For scenario B,
use a FastClaw account dedicated to this integration (and one per
environment, e.g. `myapp-prod` / `myapp-dev`) so the key can't touch agents
used elsewhere.

Whichever scenario: **your app must check "may this user use this agent?"
before every call.** FastClaw only isolates accounts and API keys from each
other.

## 3. Scenario A — chat with fixed agents

1. The user creates the agent(s) in the FastClaw console and issues an
   **Agent** API key granted to them.
2. Find the agents the key may use:

   ```bash
   curl -s "$FASTCLAW_BASE_URL/v1/agents" -H "Authorization: Bearer $FASTCLAW_API_KEY"
   ```

   ```json
   { "agents": [ { "id": "agt_2834a7a1e660d509b83d", "display_name": "Image Editor", "description": "…", "model": "…", "metadata": {}, "created_at": "…", "updated_at": "…" } ], "has_more": false }
   ```

3. Keep the agent ids in configuration (e.g. `FASTCLAW_AGENT_ID`, or a map
   from your feature to an agent id when there are several). With a single
   granted agent, chat requests may omit `agent_id`; name it anyway once the
   app may use more than one.
4. Chat (section 5).

## 4. Scenario B — manage agents

An agent belongs either to the key's FastClaw account or to one of your
users:

| | Platform agent | Private agent |
|---|---|---|
| Owner | The account | One end-user (your user id, see `user` in section 5) |
| Create with | No end-user (or `"owner": "account"`) | `"user": "<your user id>"` in the body, or `"owner": "end_user"` with the `X-Fastclaw-End-User` header |
| Who may chat | Every end-user of the account; each gets private history and memory | Its owner only |
| Reached by requests naming end-user X | Yes | Only when X is the owner; any other end-user gets `404 agent_not_found` |
| Talks to (agent-to-agent messages, groups) | The account's other platform agents | Only its owner's other private agents |

Make an agent private when your user edits it themselves, it acts for them
outside a chat (schedules, IM), or it must work with their other agents.
Otherwise keep users on a platform agent: their data is already separate.
FastClaw enforces who owns a private agent; you still decide which of your
users may use which platform agent.

The header alone never makes an agent private, so a backend that sends
`X-Fastclaw-End-User` on every call keeps creating platform agents.

### Create — `POST /v1/agents`

```bash
curl -s "$FASTCLAW_BASE_URL/v1/agents" \
  -H "Authorization: Bearer $FASTCLAW_API_KEY" -H "Content-Type: application/json" \
  -d '{
    "name": "Weekly Report Assistant",
    "description": "Summarizes the team every Friday",
    "instructions": "You write concise weekly reports.",
    "model": "anthropic/claude-sonnet-5-5",
    "metadata": { "app_user": "user-123" }
  }'
```

```json
{ "agent": { "id": "agt_2834a7a1e660d509b83d", "display_name": "Weekly Report Assistant", "name": "agt_2834a7a1e660d509b83d", "description": "Summarizes the team every Friday", "model": "anthropic/claude-sonnet-5-5", "metadata": { "app_user": "user-123" }, "created_at": "2026-10-04T08:00:00Z", "updated_at": "2026-10-04T08:00:00Z" } }
```

- Only `name` is required. `instructions` becomes the agent's SOUL.md
  (persona). `model` is `<provider>/<model>`; omit it for the account default.
- `metadata`: your own string key/values — at most 16 keys, keys ≤ 64
  characters, values ≤ 512. Stored and filterable, never used for
  authorization.
- Show `display_name` to people. `name` equals `id` (kept for old clients).
- `user` + `owner` choose the owner (table above). `owner` is `"account"` or
  `"end_user"`; with `user` set it defaults to `"end_user"`.
  `"owner": "end_user"` without an end-user is `400`.
- `external_ref` (≤ 255 characters, unique per owner) makes create
  idempotent: creating again with the same ref returns the existing agent,
  unchanged, with `"created": false` — even when two requests race. Use it to
  create a user's agent lazily on first use, e.g. `myapp:bot:<user id>`.
- Every agent view carries `owner` (`"account"` | `"end_user"`), and
  `end_user` (your user id) on private agents, plus `external_ref` when set.

### List — `GET /v1/agents`

Newest first. Query: `limit` (1–100; omit for all), `cursor` (the previous
page's `next_cursor`), `metadata[key]=value` filters (ANDed; also
`metadata.key=value`), `owner`:

- With an end-user (`X-Fastclaw-End-User`): the platform agents plus that
  user's private agents by default; `owner=end_user` lists only theirs,
  `owner=account` only the platform agents.
- Without one: the platform agents by default; `owner=end_user` lists every
  private agent of all your users, `owner=all` both.

```bash
curl -s "$FASTCLAW_BASE_URL/v1/agents?limit=20&metadata%5Bapp_user%5D=user-123" \
  -H "Authorization: Bearer $FASTCLAW_API_KEY"
```

### Read — `GET /v1/agents/{id}`

### Update — `PATCH /v1/agents/{id}`

Any of `name`, `description`, `instructions`, `model`, `metadata`; omitted
fields are unchanged. `model: ""` clears the override. `metadata` merges; a key
set to `null` or `""` is removed.

### Identity files — `PUT /v1/agents/{id}/system-files/{name}`

Body `{"content": "..."}`. `name` is one of `SOUL.md`, `IDENTITY.md`,
`AGENTS.md`, `BOOTSTRAP.md`, `TOOLS.md`, `HEARTBEAT.md`, `KNOWLEDGE.md`
(max 256 KB). Shared by every end-user of the agent.

### Delete — `DELETE /v1/agents/{id}`

A second delete returns `404 agent_not_found`; treat it as "already deleted".
Also remove your own mapping.

Read, update, identity files and delete work on a private agent with the
owner's `X-Fastclaw-End-User`, or with no end-user at all: the account's key
manages every private agent (support, migrations). Another end-user gets
`404 agent_not_found`.

Agent keys may also read and update (PATCH, identity files) the agents granted
to them; only user keys create and delete. Agent keys never reach private
agents.

## 5. Chat (both scenarios) — `POST /v1/chat/completions`

OpenAI-compatible, with FastClaw extensions.

```bash
curl -sN "$FASTCLAW_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $FASTCLAW_API_KEY" -H "Content-Type: application/json" \
  -H "X-Fastclaw-Session-Key: myapp:conv_42" \
  -d '{
    "agent_id": "agt_2834a7a1e660d509b83d",
    "user": "user-123",
    "stream": true,
    "messages": [{ "role": "user", "content": "Summarize today'\''s discussion" }]
  }'
```

- **`agent_id`** (or header `X-Fastclaw-Agent-ID`; the body wins) picks the
  agent and is strict: an agent the key may not use is `404 agent_not_found`
  — never a different agent. Omitted, FastClaw uses the key's only agent (or
  the account's only agent). A private agent only chats with its owner as
  `user`; without an end-user the call is `400 invalid_request`, with another
  end-user `404 agent_not_found`.
- **`X-Fastclaw-Session-Key`** selects the conversation history. Always send
  it, deterministic per conversation and prefixed with your app:
  `myapp:<conversation>[:<topic>]`. Omitted, every call starts a new session.
- **`user`** (or header `X-Fastclaw-End-User`; the body wins) is your user's
  stable id (not an email or display name). It keeps that user's session
  history and personal memory (USER.md / MEMORY.md) private to them. Omit it
  when all callers should share the agent's memory (e.g. a user's own agent,
  or a group chat).
- **`params`**: per-turn structured context shown to the agent, not
  persisted. In group chats send the current speaker as
  `"params": {"speaker": {"id": "user-456", "name": "Bob"}}`.
- **Files**: `images` (image URLs or data URLs, shown to vision models) and
  `attachments` (`[{"url", "name"}]`, any file type) land in the agent's
  workspace for this turn. A message's `content` may also be an array of
  parts in the OpenAI or Anthropic shape — `{"type": "text", "text"}`,
  `{"type": "image_url", "image_url": {"url"}}`,
  `{"type": "image", "source": {"type": "base64", "media_type", "data"}}` —
  and its images are treated like `images`.
- **`project_id`** (optional) files the conversation under one of the
  agent's projects so it shares that project's workspace.
- Only the last `user` message is sent to the agent — FastClaw keeps the
  history itself, keyed by the session key. Don't resend old turns.

Streaming returns OpenAI SSE chunks
(`data: {"choices":[{"delta":{"content":"..."}}]}` … `data: [DONE]`);
non-streaming returns an OpenAI `chat.completion` (`choices[0].message.content`).
The request's `model` (`<provider>/<model>`) runs this turn on that model
when the agent can reach its provider — a provider configured for the
account or the agent. Anything else (a bare name such as `gpt-4o`, which
OpenAI SDKs require, or an unknown provider) is ignored and the agent's own
model answers. The response's `model` names the model that actually ran.
To change an agent's model for every turn, `PATCH` it instead.

A stream follows the turn as it runs: the agent's text arrives while it is
written, including what it says between steps, and each tool call is
reported on a chunk with an empty delta, once when it starts and once when
it finishes:
`data: {"choices":[{"delta":{}}],"tool":{"id":"call_1","name":"exec","status":"running"}}`
(`status` then `done`). `name` is the agent's tool (`exec`, `load_skill`,
`read_file`, …), so show it as a progress step in your own words. The
streamed text adds up to the non-streaming reply; separate messages in a
reply are joined by a blank line.

A turn that fails — the agent's model API returns an error, or the model
gives no usable answer — is an error, not a reply: non-streaming answers
`502` with `{"error": {"type": "server_error", "code": "agent_error",
"message": "..."}}`; a stream (already `200`) ends with that `error` object
on its final chunk instead of `files`. Treat either as "nothing was
produced": don't charge for it, let the user retry, and show `message`
only in details — it is the upstream error, not text for end-users.

`usage` (non-streaming: top level; streaming: on the final chunk) sums every
model call the agent made for this turn, tool loops included:
`prompt_tokens` / `completion_tokens` / `total_tokens` as in OpenAI
(`prompt_tokens` includes cached input, also in
`prompt_tokens_details.cached_tokens`), plus `input_tokens` (uncached),
`cache_read_tokens`, `cache_creation_tokens` and `model_calls`, and
`cost_usd`: what the turn cost in USD, each model call priced at the unit
price of the model it actually ran on (a turn can call several — a
per-request `model`, context compaction, sub-agents). `cost_usd` is always
present; it is `null` when any call's model has no price set — treat that
as unknown, never as 0. FastClaw reports cost only: what you charge your
users (credits, markup, plans) is up to you. Background work that finishes
after the reply (memory extraction, skill learning) isn't in the turn's
`usage`; it shows up as its own usage events.

API conversations are never trusted with the FastClaw host: the agent's
tools (shell, scripts) run in FastClaw's sandbox. If the FastClaw operator
hasn't configured one, the agent can still answer but can't run commands.

### Files the agent produces

Files the agent creates or changes during a turn — e.g. an edited image —
come back with the reply in `files` (non-streaming: top level; streaming:
on the final chunk, the one with `finish_reason`). Files the reply links as
`/workspace/<path>` come first; your own uploads are not echoed back.

```json
{
  "choices": [{ "message": { "role": "assistant", "content": "Done: ![edited](/workspace/edited.png)" } }],
  "files": [
    {
      "name": "edited.png",
      "path": "edited.png",
      "size": 77490,
      "content_type": "image/png",
      "url": "https://claw.example.com/v1/agents/agt_…/sessions/myapp%3Aconv_42/files/edited.png"
    }
  ]
}
```

- Download `url` with the same `Authorization` header — and the same
  `X-Fastclaw-End-User` when the conversation had one: end-users can only
  read their own conversations' files. It is
  `GET /v1/agents/{agent_id}/sessions/{session_key}/files/{path}`
  (`?project_id=` for project conversations).
- Send `"return_files": "inline"` to also get each file's bytes as a
  `data_url` (files up to 10 MB, 25 MB per reply) — simplest for one image.
- Don't rely on the agent to put files anywhere else: FastClaw instructs it
  not to upload your users' files to third-party services.

## 6. Usage and quotas

- `GET /v1/usage` — per-day token usage. Query: `days` (1–90, default 30),
  `agent_id`, `end_user` (your user id), `scope=app` (the whole account, all
  end-users). Each daily row has `agentId` and `endUser`; roll up by them to
  bill your users.
- `PUT|GET|DELETE /v1/quota` — monthly token / request limits for an end-user
  (`user_id` from `POST /v1/users` with `{"external_id": "<your user id>"}`).

## 7. Errors

Every `/v1` error is `{"error": {"type", "code", "message"}}`. Branch on
`code`:

| Status | `code` | Meaning |
|---|---|---|
| 400 | `invalid_request` | Bad body, missing messages, metadata over limits, bad cursor, a private agent chatted with without its owner as `user`. |
| 401 | `unauthorized` | Missing or invalid API key. |
| 403 | `forbidden` | The key may not do this (e.g. an agent key creating agents). |
| 403 | `agent_quota_exceeded` | The account reached its agent quota. |
| 404 | `agent_not_found` | No such agent for this key — or another end-user's private agent. |
| 402 | `payment_required` | The FastClaw account is on billing hold (its balance ran out). Tell your user the service is paused; retrying won't help until it's topped up. |
| 404 | `project_not_found` | Unknown `project_id`. |
| 429 | `rate_limited` | Slow down and retry with backoff. |
| 503 | `not_configured` | That feature isn't enabled on this FastClaw. |

## 8. Checklist

- [ ] `FASTCLAW_BASE_URL` and `FASTCLAW_API_KEY` read from server-side
      configuration, documented in the env template, verified with
      `GET /v1/agents`.
- [ ] Right key type for the scenario (agent key for fixed agents, user key on
      a dedicated account for managed agents).
- [ ] The app checks the user's access to an agent before every call.
- [ ] Deterministic `X-Fastclaw-Session-Key` per conversation; `user` set when
      a user's history must stay private.
- [ ] Per-user agents created as private agents (`user` on create) with an
      `external_ref`, and chatted with as their owner.
- [ ] Streaming parsed as SSE; errors handled by `error.code`.
- [ ] Produced files taken from `files` (download `url` or `return_files: "inline"`).
- [ ] No FastClaw key or URL in client-side code.
