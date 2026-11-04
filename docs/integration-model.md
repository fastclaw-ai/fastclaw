# Integration model: accounts, app users and agents

Status: **proposal** (2026-11-03). Describes how a product built on FastClaw
(WorkAny, a personal-agent app, a multi-agent app) should map its users and
agents onto FastClaw, and the FastClaw changes that model needs. Sections 1–4
are the model; section 5 is today's state; sections 6–9 are the changes.

## 1. Three layers

| Layer | Is | Owns |
| --- | --- | --- |
| **Account** (`user`, role `user`) | One integrating product × one environment (`workany-prod`, `workany-dev`) | API keys, model providers, platform agents and templates, account-wide usage and rate limits |
| **App user** (`user`, role `app_user`) | One end user of that product, minted by the product's key from `X-Fastclaw-End-User` | Their data namespace: sessions, USER.md / MEMORY.md, workspace files, cron jobs, personal skills, connector accounts, quota — and (new, §6) their own agents |
| **Agent** | One configuration: persona, model, tools, skills, IM bot identity | Belongs to the account (**platform agent**) or to one app user (**private agent**) |

Rules of thumb:

- **One account per product per environment.** Never share an account between
  products: a key reaches every agent of its account, and usage can't be split
  afterwards.
- **An agent is split off when its configuration differs, not when its data
  must be isolated.** Data isolation is the app-user layer's job.
- **Isolation sits on the boundary FastClaw already enforces (the owner),**
  never on a sub-boundary every feature has to remember to filter by.

## 2. Platform agents and private agents

"Shared" means the configuration is shared. Data is always per chatter.

| | Platform agent (owner = account) | Private agent (owner = app user) |
| --- | --- | --- |
| Who may chat | Every app user of the account (the product decides who actually gets to — e.g. paid plans) | Its owner only |
| Who edits config | The account (the product) only | The owner; the product may manage it on their behalf |
| Sessions, memory, files, cron | Per chatter, private to each app user | The owner's |
| Connectors | The chatter's own accounts, in 1:1 turns (new, §8) | The owner's accounts (today's rule) |
| Collaboration (`message_agent`, groups) | Not reachable from app users' agents | Only with the same owner's other agents |
| Quota / hold | Per chatter | Per owner |

A private agent is usually **derived from a platform template** (§9): the owner
may override fields; fields they never touched follow the template.

## 3. Choosing a shape

| Product | Accounts | Agents |
| --- | --- | --- |
| Tool chat + a personal bot (WorkAny, ChatGPT + Dot) | 1 | 1 platform agent for task chats + 1 private bot per user who opens it (≤ n+1) |
| Pure personal agent (one agent per person) | 1 | n private agents |
| Many agents per person that collaborate (Grok-bot style) | 1 | Σkᵢ private agents (+ platform templates) |
| Feature agent (support bot, image editor) | 1 | 1 platform agent |

Make an agent private when any of these holds:

1. The **user** edits its persona, model or skills.
2. It **acts on the user's own accounts** in contexts beyond a 1:1 chat
   (cron, IM, proactive messages).
3. It has **its own IM bot identity** (e.g. a Feishu bot created by the user's
   QR scan — one bot account = one agent).
4. It must **collaborate** with the user's other agents.

Otherwise keep the user on a platform agent as a chatter.

Separate accounts per end user are only warranted when that end user is a
FastClaw customer in their own right: they log into the console, bring their
own model keys, or need FastClaw-enforced billing for themselves. A product
that sells to companies that each need their own admins and billing needs an
organisation layer between account and app user — out of scope here.

## 4. Who starts a turn

| Started by | Examples | Product in the loop today? |
| --- | --- | --- |
| The product | Web / app chat via `/v1/chat/completions` | Yes: it authenticates, checks entitlement and balance, then calls |
| FastClaw | IM messages, cron, heartbeat, connector wake-ups | **No** — the product neither authorises nor learns about these turns until it polls usage |

Every FastClaw-started turn needs the product's say before it runs (§7) and
reports to the product after (§10). Transport stays in FastClaw: long
connections, media, typing indicators, retries and dedupe are already here,
and products on short-lived runtimes (Cloudflare Workers ~30 s after the
response) can't carry a long agent turn themselves.

## 5. Today

| Capability | State | Where |
| --- | --- | --- |
| App user minted per end user, data namespace switched to it | ✅ | `internal/auth/auth.go:300-324` |
| Private agents owned by app users (`agents.user_id` = the app_user); `/v1` resolves platform agents + the end user's own, other end users' get `404 agent_not_found` | ✅ (§6) | `internal/api/agents.go:81` (`agentAccessible`) |
| `POST /v1/agents` owner | Account by default; the app user with body `user` or `owner: "end_user"` (the header alone keeps creating platform agents); `external_ref` idempotent per owner (unique index) | `internal/api/agents.go:260`, `internal/store/database.go:1141,2747` |
| `GET /v1/agents` | With an end user: platform + their private agents; without: platform only, `owner=end_user\|all` for every private agent. Each tagged `owner` / `end_user` | `internal/api/agents.go:413` |
| Product key without end user on a private agent | Manages it (read, update, files, delete); chat is `400` — turns run only in the owner's namespace | `internal/api/agents.go:179` |
| `message_agent` lookup | Target always owned by the space user; in an app user's space the calling agent must be too (private ↔ same owner only; platform agents there reach none) | `internal/gateway/userspace.go:1439` |
| Groups / team topics | A private agent joins only its owner's groups. `/api/*` doesn't reach private agents yet (key ACL is the account's agents) | `internal/setup/handlers_team_chat.go:81` |
| IM channels, cron, connectors of a private agent | Owned by the app user: run in the app user's space (owner = `agents.user_id`). No `/v1` API attaches channels to private agents yet | `internal/gateway/userspace.go:381` |
| Per-chatter USER.md / MEMORY.md | ✅ keyed by (agent, chatter) | `internal/api/openai.go:446`, `internal/agent/context.go:287` |
| Per-session workspace, private sandbox mount | ✅ | `internal/workspace/localfs.go:52`, `internal/sandbox/docker_executor.go:205` |
| Per-chatter cron | ✅ `ChatterID` | `internal/cron/scheduler.go:80` |
| Per-user skills shared across that user's agents | ✅ (to verify for app users over `/v1`) | `internal/agent/skills.go:198,266` |
| Connectors | Web 1:1 turns of the agent's **owner** only; not `/v1`, IM, cron | `docs/connectors.md` |
| Usage events with `account_id`, `end_user`, `cost_usd` | ✅ pull only; each event costed at its model's unit price (`null` when unpriced) | `docs/billing-hooks.md` §1, `internal/agent/cost.go` |
| Per-end-user monthly quota | ✅ `/v1/quota` | `internal/api/billing.go:150` |
| Billing hold | Account-wide only — holding it stops every end user | `internal/api/openai.go:354`, `internal/agent/loop.go:747` |
| Pre-turn admission for FastClaw-started turns | ❌ none | — |
| IM sender → end user mapping | ❌ IM chatters are `channel_user`s unrelated to the product's end users | — |
| Agent templates / inheritance | ❌ (public agents can be copied, not followed) | — |
| Request `model` on `/v1` | Runs the turn when it is `<provider>/<model>` with that provider reachable by the agent; otherwise ignored. The response names the model that ran | `internal/api/openai.go:484`, `internal/agent/turn_scope.go:47` |

## 6. Private agents owned by app users

- `POST /v1/agents` with an end user named (`X-Fastclaw-End-User` / `user`)
  creates the agent **owned by that app user**. Without one, behaviour is
  unchanged (platform agent).
- Resolution: a request carrying an end user may reach the account's
  platform agents **and** that app user's private agents — never another app
  user's. `404 agent_not_found` otherwise, same as today.
- `GET /v1/agents` with an end user lists platform agents + their private
  ones, each tagged `owner: "account" | "end_user"`.
- The product's key without an end user can manage every private agent of the
  account (support, migrations, template rollouts).
- `message_agent`, groups and team topics run in the owner's user space, so a
  private agent only reaches its owner's other agents. Platform agents are
  not addressable from private agents.
- IM channels, cron and connectors attached to a private agent belong to its
  owner.
- Idempotent create: `external_ref` on create (e.g. `workany:bot:<uid>`)
  returns the existing agent instead of a duplicate, so products can create
  lazily on first use without their own lock.

## 7. Admission hook (FastClaw-started turns)

Configured per account: `admission_url` + signing secret. Called before every
turn FastClaw starts on that account's agents (IM, cron, heartbeat, connector
wake). Not called for `/v1` turns — the product already decided.

```http
POST <admission_url>
X-Fastclaw-Signature: t=<unix>,v1=<hex hmac-sha256(secret, t + "." + body)>

{
  "id": "adm_…",                       // idempotency key
  "trigger": "im" | "cron" | "heartbeat" | "connector",
  "agent_id": "agt_…",
  "agent_owner": { "kind": "account" } | { "kind": "end_user", "end_user": "u1" },
  "end_user": "u1",                    // when FastClaw already knows the chatter
  "channel": { "type": "feishu", "bot_id": "cli_…", "chat_type": "p2p",
               "sender_external_id": "ou_…" },   // IM only
  "text": "绑定 ABCD2345"              // IM only, first 2 KB
}
```

Response (within 3 s):

```jsonc
{ "action": "run",   "end_user": "u1", "session_key": "workany:u1:bot" }  // session_key optional
{ "action": "reply", "text": "请先在网页上绑定…" }                       // answer once, don't run
{ "action": "drop" }
```

- **Identity.** An IM sender FastClaw can't map is sent with no `end_user`;
  the product answers `run` with the end user (e.g. after redeeming a bind code
  in `text`) or `reply` with instructions. FastClaw stores
  (`channel`, `bot_id`, `sender_external_id`) → `end_user` and sends it on
  later calls. `DELETE /v1/channel-identities/{…}` removes a mapping.
- **Entitlement and balance** are the product's call: `reply` carries the
  "top up" / "not on your plan" message in the product's words.
- **Failure is closed:** timeout, non-2xx or bad body → no turn, the chatter
  gets FastClaw's generic "service unavailable" line.
- Without an `admission_url`, FastClaw keeps today's behaviour (run, chatter =
  `channel_user`), so existing deployments are unaffected.

## 8. Connectors over `/v1` and on platform agents

Today's rule guards two things: the reader of the reply must be the
connection's owner, and the agent's instructions must not come from someone
else who could steer the model into reading the owner's data. Extend the
mounting condition to:

- the turn is 1:1 (web or `/v1`; never groups, shared sessions, IM groups), and
- the connections used are the **chatter's** own (subject = the chatter's
  user id, as now), and
- the agent is owned by the chatter **or** is a platform agent of the account
  that minted the chatter.

Cards and wake-ups (`request_connection`, `/connectors/done`) need a product
URL to send the end user to: `connector_return_url` per account, with the
one-use link minted by FastClaw as today.

## 9. Templates

- A platform agent can be marked `template`.
- `POST /v1/agents {"template_id": "…"}` creates a derived agent storing only
  overrides; unset fields (SOUL.md, skills, model, tool policy) resolve from
  the template at load time.
- Template edits apply to every derived agent on its next turn.
  `GET /v1/agents/{id}?resolved=1` shows the effective config.
- Private agents never edit their template; "reset to default" clears an
  override.

## 10. Billing

| Stage | Mechanism | Change |
| --- | --- | --- |
| Before | `/v1` turns: the product checks before calling. FastClaw-started turns: the admission hook (§7) | New hook |
| After | `turn.completed` pushed to `events_url` (per account, signed like §7): `{ id, account_id, end_user, agent_id, session_key, trigger, usage{…}, model_calls, cost_usd, error?, created_at }`. `id` equals the usage event id so pushed and pulled events dedupe. Failed turns (`agent_error`) carry `error` and aren't charged | New push; the existing pull stays for reconciliation |
| Stop | Per-end-user hold: `PUT /v1/end-users/{external_id}/hold` checked next to the account hold, on every channel. `/v1/quota` keeps working as a token/request ceiling | New; today only the account can be held |

The same `turn.completed` event lets the product refresh its UI when a turn
ran in IM, instead of polling session history.

### Cost, not price

FastClaw reports what a turn **cost** in USD; the product decides what it
**charges** (credits, markup, plans, balance). Only FastClaw can cost a turn
correctly: one turn can call several models (fallback, compaction, memory
extraction, sub-agents), and the product only sees the turn's totals.

- **Unit prices** live on the provider's model entry — the existing
  `ModelEntry.cost` (USD per 1M tokens: `input`, `output`, `cacheRead`,
  `cacheWrite`), editable in the console with the provider. A built-in
  default table may fill models the admin left blank.
- **Per call:** when a model call is recorded (`usage.Collector.Add`, the
  `token_usage_log` row), cost it with the unit price of the model that
  actually ran. A call whose model has no price makes the turn's cost
  unknown, never 0.
- **Reported as** `usage.cost_usd` (number, or `null` when unknown) on the
  final `/v1/chat/completions` chunk and the non-streaming response; and
  `cost_usd` on each usage event (pull) and on `turn.completed` (push).
  Store it on `token_usage_log` so pulled and pushed numbers agree.
- FastClaw still never holds balances or converts to credits.

**Status:** implemented for pull and `/v1` (`internal/agent/cost.go`,
`internal/usage/pricing.go`). Every metered call — turn model, per-request
`model`, compaction, `delegate_task` sub-agents, group coordination,
memory extraction, skill learning — writes its own `cost_usd`. Memory
extraction and skill learning run after the reply, so they are separate
events outside the turn's `usage`. When `turn.completed` lands it must
carry `usage.cost_usd` from the same collector, which sums exactly the
turn's `token_usage_log` rows. There is no model fallback chain today;
when one is added it only has to pass the model that ran to `meterTokens`.

## 11. Order of work

1. **Private agents** (§6) — unblocks personal bots and multi-agent products.
2. **Per-end-user hold** (§10) — without it a shared account can't stop one
   user.
3. **Admission hook + channel identities** (§7) — IM and cron become billable
   and attributable.
4. **`turn.completed` push** (§10).
5. **Connectors over `/v1`** (§8).
6. **Templates** (§9) — needed before private agents scale past a handful of
   rollouts.

## 12. Open questions

- Cost of an idle private agent (config rows, workspace prefix, channel
  connections) at 10k–100k per account — measure before §6 ships.
- ~~Request `model` on `/v1`~~ — decided: honoured per turn when the agent can
  reach the provider, ignored otherwise (§5).
- Should per-chatter USER.md be shared across all agents of one app user
  (a "who you are" profile) while MEMORY.md stays per agent? WorkAny's
  task chats (platform agent) and bot (private agent) would then know the
  same person.
- Admission-hook latency budget for IM: is 3 s right when Feishu redelivers
  after ~3 s and FastClaw already acknowledged the event?
- Organisation layer for products that sell to companies (§3).
