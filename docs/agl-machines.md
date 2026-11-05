# Machines: "connect my computer" (agl)

An end user links their own computer to FastClaw with
[agl](https://github.com/thinkany-ai/agl) and chats, from WeChat (or any
IM channel bound to the agent), with a coding agent running on that
computer — Claude Code, Codex, Gemini, … — instead of an LLM.

```
product UI ──/v1/machines──▶ FastClaw ◀──wss /agl/ws/machine── agl daemon ──▶ claude / codex / …
WeChat ──iLink──▶ FastClaw WeChat channel ──▶ private agent (runtime "agl") ──agl link──▶ that machine
```

## Pieces

| Piece | Where |
| --- | --- |
| Hub (pairing, install script, machine WebSocket) | `internal/agl/hub.go`, mounted at `<FASTCLAW_PUBLIC_URL>/agl` |
| IM turn bridge (sessions, approvals, `/new`, `/stop`) | `internal/agl/bridge.go`, hooked in `internal/gateway/agl.go` (`routeDM`) |
| Runtime config validation | `internal/agl/runtime.go` |
| `/v1/machines*`, `runtime` on `/v1/agents` | `internal/api/machines.go`, `internal/api/agents.go` |
| Tables `machines`, `machine_pairings` | `internal/store/machines.go` (+ `migrationSQL`) |

## Ownership

- A machine belongs to one FastClaw user (`machines.user_id`). For a
  product's end user that is the app_user minted from
  `X-Fastclaw-End-User`.
- A `runtime: "agl"` agent is always a private agent of the same user.
  Every turn re-checks `machine.user_id == agent.user_id == the user space
  the message was routed in`, and that the machine isn't revoked; otherwise
  the chat gets a short refusal and nothing reaches the machine.
- Turns only run in 1:1 chats. When the channel knows who connected it
  (WeChat QR scanner / `/pair` sender, `channels.bound_user_id`), only that
  sender may drive the computer.
- An end-user request never sees another end user's machines or private
  agents (`/v1/*` and the `/api/agents/{id}/…` endpoints answer 404).

## Product API

All calls: `Authorization: Bearer fc_…` **and** `X-Fastclaw-End-User`
(else 400). Errors are `{"error": "<message>"}`.

| Call | Response |
| --- | --- |
| `POST /v1/machines/pairing` `{}` | `{code, installCommand, expiresAt}` — one use, 10 min, stored as sha256 |
| `POST /v1/machines/pairing/status` `{code}` | `{status: pending\|connected\|expired, machineId?}`; `connected` once the paired machine said `hello`; 404 for codes this end user didn't create |
| `GET /v1/machines` | `{machines: [{id, name, hostname, os, arch, version, online, lastSeenAt, createdAt, agents: [{id, name, available}]}]}`, newest first, revoked excluded |
| `DELETE /v1/machines/{id}` | `{ok: true}`; clears the token hash, closes the link (agl then reports "revoked" and stops retrying), agents bound to it stop running |

Agents (`/v1/agents`, existing error shape):

```jsonc
POST /v1/agents   (X-Fastclaw-End-User: user:42)
{"name": "My Mac", "owner": "end_user", "runtime": "agl",
 "runtimeConfig": {"machineId": "mch_…", "aglAgent": "claude", "cwd": "~/code/x", "access": "ask"}}
```

- `runtime: "agl"` needs owner `end_user` (body `owner` or `user`), else 400.
- `machineId` must be the end user's, not revoked; `aglAgent` non-empty and,
  when the machine reported its agents, one of them; `access` is
  `ask` (default) | `edit` | `full` | `read_only`.
- `PATCH /v1/agents/{id}` merges `runtimeConfig` (same validation).
- Responses of agl agents carry `runtime` and `runtimeConfig`; other agents
  are unchanged (no `runtime` field).

## Hub endpoints

| Endpoint | |
| --- | --- |
| `GET /agl/install.sh?code=` | agl's `assets/install.sh` with `__HUB_URL__` = `<public>/agl`, `__CONNECT_ARGS__` = `--code <code>`; invalid/used/expired code → 401 + a script that prints an error and exits 1 |
| `GET /agl/download/<target>[.sha256]` | 302 to `https://github.com/thinkany-ai/agl/releases/latest/download/agl-<target>[.sha256]` |
| `WS /agl/ws/machine?code=…&machine_id=…` | consumes the code (401 if unusable, before upgrading), sends `{"type":"paired","token"}`, stores `sha256(token)` |
| `WS /agl/ws/machine?token=…&machine_id=…` | 401 for unknown/revoked tokens; expects `hello` within 10 s, then online |
| `GET /agl/` | "computer linked" page (install.sh opens it) |

Link handling: requests are correlated by id (30 s default timeout),
events fan out to subscribers, the hub pings every 25 s and drops a link
silent for 75 s (agl pings every 20 s). Frames are capped at 8 MiB;
`hello` strings at 128 chars and 64 agents. Re-pairing the same computer
(same agl machine id, same user) keeps its `mch_` id and replaces the token.

**Single instance.** Machine sockets live in the process that accepted
them. Running several FastClaw instances needs a sticky socket holder (or
agl's HTTP link) — the seam is `Hub.Request` / `Hub.Subscribe`.

## IM turn semantics

- agl session name `fc:<agentId>:<chatId>`, created/resumed with the
  agent's `aglAgent`, `cwd`, `access` (access re-applied with
  `session.configure` when it changed).
- `text_delta` is collected until `turn_ended` (`phase: "commentary"` is
  skipped) and sent split into ≤1800-character bubbles (at most 10).
  Typing indicator every 5 s while a turn runs.
- A message during a running turn is sent with `mode: "steer"` when the
  session has the `steer` feature, else `"queue"`.
- `permission_request` → `需要确认：<title>\n回复 1 允许 / 2 本会话都允许 / 3 拒绝`;
  the next `1`/`2`/`3` in that chat answers it with `session.approve`
  (`allow` / `allow_session` / `deny`) instead of prompting. agl
  auto-denies on timeout (the chat is told).
- `/stop` aborts the running turn; `/new` closes the session so the next
  message starts a fresh one.
- Machine offline → `你的电脑当前不在线…`; link drops mid-turn → partial
  text + `你的电脑连接已断开…`.
- Web chat, cron and heartbeat turns of an agl agent are dropped (no LLM
  turn).

## Configuration

| Env | |
| --- | --- |
| `FASTCLAW_PUBLIC_URL` | Origin machines and install commands use, e.g. `https://fastclaw.example.com` (no `/agl`). Empty: the request's origin (honours `X-Forwarded-Proto/Host`). agl's installer requires HTTPS except for localhost. |

## Local test recipe (same Mac)

```sh
# 1. run FastClaw (this branch) with a public URL agl can reach
make build   # or: go build -o ./fastclaw ./cmd/fastclaw
FASTCLAW_PUBLIC_URL=http://localhost:18955 FASTCLAW_PORT=18955 ./fastclaw gateway --port 18955

# 2. as the product: get a pairing code for end user user:alice
KEY=fc_…   # an api key of the integrating account
curl -s -X POST localhost:18955/v1/machines/pairing \
  -H "Authorization: Bearer $KEY" -H "X-Fastclaw-End-User: user:alice" -d '{}'

# 3. on the computer: run installCommand from the response, or with agl
#    already installed:
agl connect http://localhost:18955/agl --code <code>
agl status            # hub: http://localhost:18955/agl · connected

# 4. machine + agent
curl -s localhost:18955/v1/machines -H "Authorization: Bearer $KEY" -H "X-Fastclaw-End-User: user:alice"
curl -s -X POST localhost:18955/v1/agents -H "Authorization: Bearer $KEY" -H "X-Fastclaw-End-User: user:alice" \
  -d '{"name":"My Mac","owner":"end_user","runtime":"agl","runtimeConfig":{"machineId":"mch_…","aglAgent":"claude"}}'

# 5. bind WeChat to it (QR) and chat
curl -s -X POST localhost:18955/api/agents/<agentId>/channels/wechat/login \
  -H "Authorization: Bearer $KEY" -H "X-Fastclaw-End-User: user:alice"
# render qrCodeImg, scan, poll …/channels/wechat/login/status?session=<sessionId>

# 6. revoke
curl -s -X DELETE localhost:18955/v1/machines/mch_… -H "Authorization: Bearer $KEY" -H "X-Fastclaw-End-User: user:alice"
agl status            # unauthorized: this machine was revoked
```

To try it without touching your normal agl setup, run a second daemon with
its own home: `AGL_HOME=/tmp/agl2 AGL_PORT=7898 agl daemon`, and prefix the
`agl connect` / `agl status` commands with the same variables.
