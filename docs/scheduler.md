# Scheduled tasks for API products (trig)

An agent creates scheduled tasks with `create_cron_job` ("remind me at 9",
"every Monday…"). In FastClaw's own channels (web, the IM bots it runs) the
built-in scheduler fires them back into that channel. A product that drives
its chats through the API (`/v1/chat/completions`) owns the chat — WeChat
over imc, its own app — so FastClaw can't deliver into it. Such a product
registers a scheduler: a [trig](https://github.com/thinkany-ai/trig) app.

```
user: "每天 8 点提醒我…"
  → agent: create_cron_job            (an API turn of an account with a scheduler)
  → FastClaw: PUT <trig>/v1/jobs/fc/<agent>/<chatter>/<id>   payload below
  … when due …
  → trig: signed job.fired webhook → the product
  → product: runs a turn in the same session (X-Fastclaw-Session-Key = payload.sessionKey)
             with payload.message, and sends the reply into its chat
```

## Register

With the account API key (the one whose end users chat):

```
PUT /v1/scheduler   {"url": "https://trig.example.com", "api_key": "trig_…"}
GET /v1/scheduler   {"url": "…", "key_set": true}          (the key is never returned)
PUT /v1/scheduler   {"url": ""}                             removes it
```

The URL must be public https; FastClaw checks the key against trig
(`GET /v1/app`) before saving. The trig app's webhook points at the product.

## What the product receives

trig's `job.fired` event (see trig's docs/api.md) with this `payload`:

```jsonc
{
  "v": 1, "source": "fastclaw.cron",
  "id": "t1a2b3c4d5e6",            // the id the agent sees (list / delete_cron_job)
  "agentId": "agt_…",
  "chatter": "u_…",                // FastClaw user of the end user
  "sessionKey": "myapp:conv_42",   // the X-Fastclaw-Session-Key of the turn that created it
  "name": "喝水提醒",
  "message": "提醒用户喝水…",       // instructions the agent wrote for itself
  "type": "once" | "cron" | "interval",
  "schedule": "0 8 * * *", "timezone": "Asia/Shanghai"
}
```

Run the turn as usual with `X-Fastclaw-End-User` / `x-fastclaw-agent-id` /
`x-fastclaw-session-key` from your own records for that session (check the
chat is still bound to that agent), send `message` as the user message, and
say in `params` that it's a scheduled task firing, not the person writing.
Delete a job whose chat is gone (`DELETE /v1/jobs/{key}` on trig).

## Behavior

- Only turns on the `api` channel use the scheduler; anything else (and
  accounts without one) keeps the built-in scheduler.
- Job keys are `fc/<agentId>/<chatter>/<id>`: `list_cron_jobs` and
  `delete_cron_job` see only the calling chatter's jobs on that agent.
- At most 30 active jobs per chatter per agent; intervals ≥ 1m.
- `once` → trig `at`; `cron` → trig `cron` in the chatter's timezone;
  `interval` → trig `every`. Missed fires (trig down) come once, late; the
  product decides whether a late one is still worth sending.
