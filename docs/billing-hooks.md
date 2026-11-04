# Billing hooks

FastClaw doesn't hold balances or take payments. It reports what each
model call **cost** in USD at the model's unit price; what to **charge**
(credits, markup, plans) is the billing system's call. A hosted
deployment that charges for usage runs its own billing system next to
FastClaw and connects through four primitives. All admin routes take a
platform-admin credential: a `super_admin` session or an **admin** API key
(`Authorization: Bearer <key>`).

## Model

- One FastClaw **account** (user) per paying customer. Create it with
  `POST /api/users`, and issue its API keys with
  `POST /api/users/{id}/apikeys`.
- The billing system keeps the mapping customer ↔ FastClaw account id, its
  own price list (what it charges) and the balance.
- Unit prices (USD per 1M tokens: input, output, cache read, cache write)
  are set on each provider model in Console → Models. Well-known models
  fall back to a built-in list price when left blank; a model with no
  price reports `cost_usd: null`.

## 1. Pull usage — `GET /api/admin/usage/events`

Query: `after` (cursor, default 0), `limit` (1–1000, default 500).

```jsonc
{
  "events": [
    {
      "id": 1042,
      "account_id": "u_…",          // who pays
      "user_id": "u_…",             // the namespace that made the call
      "end_user": "alice",          // the integrating app's user id, when any
      "agent_id": "agt_…",
      "session_key": "myapp:conv_42",
      "provider": "anthropic",
      "model": "claude-sonnet-5-5",
      "input_tokens": 1200,
      "output_tokens": 340,
      "cache_read_tokens": 8000,
      "cache_creation_tokens": 0,
      "duration_ms": 0,
      "channel": "",
      "cost_usd": 0.01284,          // null when the model has no price
      "created_at": "2026-10-05T08:00:00Z"
    }
  ],
  "next_after": 1042,
  "has_more": false
}
```

Poll on a schedule: price each event (from `cost_usd`, or from the tokens
if you price them yourself), debit `account_id`, then store
`next_after` as the cursor — in the same transaction as the debits, so a
crash replays rather than skips. `id` is unique, so it also works as an
idempotency key. Events show up a few seconds after the call; polling never
skips one.

`cost_usd` is null for a model with no unit price and for rows recorded
before cost tracking — treat it as unknown, never as free.

`account_id` rolls end-users (`X-Fastclaw-End-User`) and IM chatters up to
the account that owns them.

## 2. Hold an account — `PUT /api/admin/users/{id}/billing-hold`

```jsonc
{ "hold": true, "reason": "balance exhausted" }
```

While held, the account's agents refuse new turns on every channel (web,
IM, cron, `/v1`). `/v1/chat/completions` returns
`402 {"error": {"code": "payment_required", …}}`. Chatters see a generic
"service paused" message; `reason` is only visible to admins
(`GET …/billing-hold`). Send `{"hold": false}` after a top-up.

Since usage is exported after the fact, an account can run a little past
zero before its hold lands. Hold at a small positive threshold if that
matters.

## 3. Sign users in — `POST /api/admin/users/{id}/login-link`

Body (optional): `{"redirect": "/console/", "ttl_seconds": 300}`.
Returns `{"url": "https://<fastclaw>/auth/login-link?token=…", "expires_at": …}`.

Redirect the customer's browser to `url`: it signs them in as the account
and lands on `redirect` (a path on FastClaw). Links work once and expire
after `ttl_seconds` (default 5 minutes, max 30). Only console accounts
(`user`, `super_admin`) can get one.

## 4. Per-turn usage for integrators

`/v1/chat/completions` responses carry the turn's `usage` (see the
[agent-integration skill](../skills/agent-integration/SKILL.md)), so apps
built on FastClaw can attribute cost to their own users.
