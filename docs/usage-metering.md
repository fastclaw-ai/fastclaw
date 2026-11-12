# Usage metering and billing authorization

FastClaw **meters**; the app that integrates it **prices**. FastClaw never
decides what a user pays. It records every billable thing an agent did as a
usage event, exports those events to the account that pays for them, and —
when the account registers a billing hook — asks that hook before work that
costs real money starts.

```
agent turn ──▶ usage events (llm, image, tts, search, fetch, sandbox, local_agent, skill)
                 │                                     ▲
                 ▼                                     │ allow / deny
     GET /v1/usage/events (pull, cursor)     POST <hook url> (authorize)
                 │                                     │
                 └──────────▶ the app (prices, debits, stops) ◀┘
```

## 1. Usage events

One row per billable thing, in table `usage_events` (both dialects).

| kind          | what                                  | quantity                                   | provider / model                       |
|---------------|---------------------------------------|--------------------------------------------|----------------------------------------|
| `llm`         | one model call (main loop, sub-agents, compaction, memory, …) | `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_creation_tokens` (images sent to the model are already in `input_tokens`) | provider id / model id |
| `image`       | `image_gen` tool                      | `units` = images returned, `unit` = `image`; `detail.size`, `detail.quality` when known | image provider (`openai`, `fal`, …) / model |
| `tts`         | `tts` tool                            | `units` = characters synthesized, `unit` = `char` | tts provider / voice model |
| `search`      | `web_search` tool                     | `units` = 1, `unit` = `call`               | search provider (`brave`, `exa`, …) |
| `fetch`       | `web_fetch` tool                      | `units` = 1, `unit` = `call`               | fetch provider (`jina`, `firecrawl`, `direct`) |
| `sandbox`     | one sandbox command (`exec` and file ops in a sandbox) | `units` = wall seconds (3 decimals), `unit` = `second` | backend (`e2b`, `docker`, `boxlite`) / `""` |
| `local_agent` | one `run_local_agent` / `generate_image` run on the end user's own computer (agl) | `units` = 1, `unit` = `run`; `detail.duration_ms`, `detail.machine` | `agl` / the local agent (`codex`, `claude`, …) |
| `skill`       | one command that ran a script of a skill with configured secrets (API keys) — the skill spends the operator's money at a third-party API FastClaw can't see | `units` = 1, `unit` = `run`; `detail.script` (path under the skill's dir, for reports) | the skill's name / `""` |

FastClaw doesn't know what a skill's API charges, and doesn't try: every
skill depends on different APIs priced differently. It counts runs, and the
app prices a run per skill (a flat average is fine). Skills without
configured secrets aren't metered.

A run is a script under the skill's directory being **executed** — as the
command (`/skills/x/bin/q`) or an interpreter's script (`python
/skills/x/a.py`, `uv run …`, `bash -c "…"` is read in turn). Only then does
the command get the skill's secrets. Reading the skill (`cat SKILL.md`,
`ls`, `grep`, `sed -n` on a script, `python -c` inline code) gets no
secrets and costs nothing. Since secrets and metering go together, a
command FastClaw misreads can only miss its keys, never use them unmetered.
`units` = the simple commands that run the skill's scripts (`a.py && b.py`
is 2), counted whatever the exit status (the command is the model's: a
failing tail can't make it free); a loop inside one is still one.

Only successful work is recorded (a failed provider call records nothing;
a failed model call that still reported usage records it). `cost_usd` is
what it cost the operator when known (model unit prices; tool providers:
null for now) — for margin reports, not for pricing.

Every event carries who and where: the namespace user that ran it, the
paying account, the end user (`X-Fastclaw-End-User` external id) when the
user is an app user, `agent_id`, `session_key`, `channel` (`web`, `api`,
`wechat`, `cron`, `task`, …) and `turn_id` (one id per agent turn, shared by
every event of that turn; `""` when outside a turn).

### Pull — `GET /v1/usage/events?after=<id>&limit=<n>`

Auth: an account API key. Returns the events the **calling account** pays
for (its own usage and its end users'), oldest first. `limit` 1–1000,
default 500. Events are exported once they are a few seconds old, so a
reader moving its cursor never skips one.

```jsonc
{
  "events": [
    {
      "id": 1042,
      "created_at": "2026-11-07T08:00:00Z",
      "kind": "llm",                 // see the table above
      "provider": "openrouter",
      "model": "autojev/fast",
      "input_tokens": 9213, "output_tokens": 77,
      "cache_read_tokens": 0, "cache_creation_tokens": 0,
      "units": 0, "unit": "",
      "cost_usd": null,             // null: unknown, never "free"
      "detail": {},                 // small, kind-specific
      "end_user": "user:42",        // "" for the account's own usage
      "agent_id": "agt_…",
      "session_key": "imc:acc_…:o9cq…",
      "channel": "api",
      "turn_id": "trn_…"
    }
  ],
  "next_after": 1042,
  "has_more": false
}
```

`id` is unique and increasing: store `next_after` as the cursor in the same
transaction as the debits it caused, and a crash replays instead of skipping.

The final chunk of a streamed `/v1/chat/completions` turn also carries
`turn_id` (top level), so the caller can link that turn's events.

## 2. Billing hook (authorization)

### Register — `PUT /v1/billing/hook`

```jsonc
{ "url": "https://app.example.com/api/fastclaw/authorize", "secret": "<random, ≥ 32 chars>" }
```

Auth: an account (user or admin) API key; agent-scoped keys and console
sessions get 403. One hook per account; `{"url": ""}` removes it.
`GET /v1/billing/hook` → `{"url", "secret_set": true}` (the secret is never
returned; it is stored as is — there is no at-rest encryption yet). The URL
must be `https` and resolve to a public address, checked at registration and
on every connection, redirects not followed — unless the deployment sets
`FASTCLAW_BILLING_HOOK_ALLOW_PRIVATE=true` (local development: also allows
`http://`).

### Authorize — FastClaw → `POST <url>`

```http
POST <url>
Content-Type: application/json
X-Fastclaw-Signature: t=<unix seconds>,v1=<hex hmac-sha256(secret, t + "." + body)>
```

```jsonc
{
  "id": "authz_…",
  "stage": "turn",            // "turn": before an agent turn; "tool": before a costly tool call
  "kind": "turn",             // "turn" | "image" | "tts" | "sandbox" | "skill"
  "provider": "fal", "model": "flux-pro",
  "estimate": { "units": 2, "unit": "image" },   // tools only
  "end_user": "user:42", "agent_id": "agt_…", "session_key": "…",
  "channel": "cron", "turn_id": "trn_…"
}
```

Answer `200 {"allow": true}` or `200 {"allow": false, "message": "…"}`
within 5 s. Verify the signature and reject an old `t`.

When it's asked, for accounts with a hook:

- **Before every agent turn** on every channel (web, IM, cron, tasks, /v1,
  …). Denied: the turn doesn't run; the user sees `message` (`/v1` returns
  `402 payment_required` with it).
- **Before costly tool calls**: `image_gen`, `tts`, starting a sandbox
  command, and running a keyed skill's script (`estimate` = its runs, `provider`
  the skill's name). Denied: the tool returns `message` to the model as its error, so
  the agent can tell the user. Cheap calls (model calls, search, fetch) are
  not asked about individually.

Sub-agents and `message_agent` share the outer turn's `turn_id` and aren't
asked again. A sandbox command's estimate is its timeout in seconds.

If the hook can't be reached or answers anything else, FastClaw **allows**
the work and logs a warning (billing outages don't stop chats); set
`FASTCLAW_BILLING_HOOK_FAIL_CLOSED=true` to deny instead.
