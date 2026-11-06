# Machines: "connect my computer" (agl)

An end user links their own computer to FastClaw with
[agl](https://github.com/thinkany-ai/agl). Then, from WeChat (or any IM
channel), the coding agents on that computer — Claude Code, Codex,
Gemini, … — are reachable two ways:

- **Tool mode (main path).** The user chats with a normal LLM agent (e.g.
  a platform persona). When a request needs their computer, the LLM calls
  `run_local_agent`, which runs one turn of a local coding agent through
  agl, and uses the answer in its own reply. See [Tool mode](#tool-mode).
- **Passthrough (optional).** A private agent with `runtime: "agl"`: every
  IM message goes straight to one local coding agent, no LLM in between.

```
product UI ──/v1/machines──▶ FastClaw ◀──wss /agl/ws/machine── agl daemon ──▶ claude / codex / …
WeChat ──▶ LLM agent ──run_local_agent──▶ agl link ──▶ the CHATTER's machine        (tool mode)
web chat ─▶ LLM agent ──run_local_agent──▶ agl link ──▶ the logged-in person's machine (tool mode)
WeChat ──▶ private agent (runtime "agl") ──agl link──▶ that machine                 (passthrough)
```

## Pieces

| Piece | Where |
| --- | --- |
| Hub (pairing, install script, machine WebSocket) | `internal/agl/hub.go`, mounted at `<FASTCLAW_PUBLIC_URL>/agl` |
| IM turn bridge (sessions, approvals, `/new`, `/stop`) | `internal/agl/bridge.go`, hooked in `internal/gateway/agl.go` (`routeDM`) |
| Tool mode runner (machine choice, runs, approvals) | `internal/agl/tools.go`; tools `internal/agent/tools/local_agents.go`, mounted in `internal/agent/local_agents.go`; approval interception + eligibility in `internal/gateway/agl.go` |
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

**Web chat.** The dashboard's streaming chat (`POST /api/chat/stream`)
marks its turn with the logged-in person (`agent.ContextWithLocalAgentsUser`);
the agent mounts the same tools for them when they have a machine with
`allowAgents` (never for team chat, `message_agent`, the non-streaming
`/api/chat`, groups or shared sessions). The run's chat is
`agl.WebToolChat(user, agent, streamSession)`. Instead of IM text, the
run streams `local_agent` events — `{kind: progress|approval|approval_resolved|done,
agent, machine, text?, decision?, error?}` — and the page shows a card with
Allow / Allow for this session / Deny buttons:

| Call | |
| --- | --- |
| `GET /api/chat/local-agent-approval?agentId=&sessionId=` | `{pending:false}` or `{pending:true, agent, machine, title}` (a page opened mid-run) |
| `POST /api/chat/local-agent-approval` `{agentId, sessionId, decision}` | answers the open request (`allow` / `allow_session` / `deny`); 404 when there is none for this person |

The old `local-coding-agents` skill (exec of the gateway host's `codex` /
`claude`) is gone; installed as-shipped copies are removed on startup
(`retiredBundledSkills`). Self-hosted users link the host itself instead.

## Product API

Callers: an integrating product — `Authorization: Bearer fc_…` **and**
`X-Fastclaw-End-User` (else 400) — or the dashboard (console → Devices)
with the logged-in user's session cookie, acting for that user (403 while a
super_admin browses as someone else). Errors are `{"error": "<message>"}`.

| Call | Response |
| --- | --- |
| `POST /v1/machines/pairing` `{}` | `{code, installCommand, expiresAt}` — one use, 10 min, stored as sha256 |
| `POST /v1/machines/pairing/status` `{code}` | `{status: pending\|connected\|expired, machineId?}`; `connected` once the paired machine said `hello`; 404 for codes this end user didn't create |
| `GET /v1/machines` | `{machines: [Machine]}`, newest first, revoked excluded |
| `PATCH /v1/machines/{id}` | body any of `{name, allowAgents, defaultAccess, defaultCwd}` → the updated Machine; 404 unless it is this end user's active machine; 400 on invalid values |
| `DELETE /v1/machines/{id}` | `{ok: true}`; clears the token hash, closes the link (agl then reports "revoked" and stops retrying), agents bound to it stop running |
| `POST /v1/machines/{id}/update` | the computer's agl updates itself to the newest release (`daemon.update` with `if_idle`, so a running task is never ended): `{updated, current, latest, busy?}`; 409 offline, or `{code: "manual_update"}` for agl < 0.3.5 (run `agl update` on the computer) |
| `POST /v1/machines/{id}/agents/refresh` | re-detects the local agents now (`agents.list`), stores them, returns the Machine; 409 when offline |
| `GET /v1/machines/{id}/agents/{agent}/models` | `{models: [{id, name?, description?, default?, efforts?[]}]}` (`agents.models`) |
| `POST /v1/machines/{id}/agents/{agent}/test` `{model?, effort?}` | runs the agent read-only in a throwaway session with "Reply with just: OK" (uses a little of its own quota): `{durationMs, reply, model?}`; 409 offline, 502 when it fails |

Machine:

```jsonc
{"id": "mch_…", "name": "MacBook", "hostname": "MacBook-Pro", "os": "macos", "arch": "arm64",
 "version": "0.3.3", "online": true, "lastSeenAt": "…", "createdAt": "…",
 "agents": [{"id": "claude", "name": "Claude Code", "available": true,
   "status": "ready",                       // ready | desktop-only | not-found (agl)
   "path": "/Users/me/.local/bin/claude", "version": "2.1.318 (Claude Code)",
   "features": ["models", "effort", …], "installCommand": "", "guide": "",
   "enabled": true, "model": "", "effort": ""}],  // the owner's choices (PATCH agents)
 "latestVersion": "0.3.5", // newest agl release ("" until known; checked hourly)
 "updateAvailable": true,  // version < latestVersion
 "remoteUpdate": false,    // agl ≥ 0.3.5: POST …/update works; older: `agl update` by hand
 "allowAgents": true,      // LLM agents may dispatch work here (tool mode); default true
 "defaultAccess": "ask",   // read_only | ask | edit | full; default "ask"
 "defaultCwd": ""}         // working dir for tool runs; "" = agl default (home), "~/" expanded by agl
```

`name` follows the hostname until it is set with PATCH (1–64 characters,
one line); later hellos keep a chosen name. `defaultCwd` ≤ 1024 bytes.

PATCH also takes `agents: {<agentId>: {enabled?, model?, effort?}}`, merged
into the stored per-agent settings (`machines.agent_settings`). A disabled
agent is left out of `local_agents` / `run_local_agent`; `model` / `effort`
("" = the agent's default) are passed to `session.create` for new tool
sessions. The executable and startup arguments stay agl's to decide on the
computer (`AGL_<ID>_BIN`, `custom_agents` in `~/.agl/config.json`): agl has
no remote method to change them.

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
| `GET /agl/install.sh?code=` | agl's `assets/install.sh` with `__HUB_URL__` = the code's hub address, `__CONNECT_ARGS__` = `--code <code> --private`; a code made for another address → invalid/used/expired code → 401 + a script that prints an error and exits 1 |
| `GET /agl/download/<target>[.sha256]` | 302 to `https://github.com/thinkany-ai/agl/releases/latest/download/agl-<target>[.sha256]` |
| `WS /agl/ws/machine?code=…&machine_id=…` | consumes the code (401 if unusable, before upgrading), sends `{"type":"paired","token"}`, stores `sha256(token)` |
| `WS /agl/ws/machine?token=…&machine_id=…` | 401 for unknown/revoked tokens; expects `hello` within 10 s, then online |
| `GET /agl/` | static "computer linked" page (nothing opens it automatically; the app shows the link via pairing status) |

Link handling: requests are correlated by id (30 s default timeout),
events fan out to subscribers, the hub pings every 25 s and drops a link
silent for 75 s (agl pings every 20 s). Frames are capped at 8 MiB;
`hello` strings at 128 chars and 64 agents. Re-pairing the same computer
(same agl machine id, same user) keeps its `mch_` id and replaces the token.

**One hub address per product.** agl keeps one link per hub URL on a
computer, and re-connecting a URL replaces that link. So `/v1/machines/pairing`
hands each product (api key account) its own address,
`<public>/agl/p/<scope>` with `scope = sha256("agl-hub-scope:" + account)[:16]`
(`agl.ProductScope`); every endpoint above also lives there. A pairing
code and the machine it pairs are bound to that address (`hub_scope`):
codes pair, and tokens connect, only there. Consequences:

- Two products on the same FastClaw linked to one computer get two links
  that don't touch each other.
- The same product, a second account, same computer: agl replaces the
  first account's link, so pairing revokes the first account's record
  (its token is gone from the computer anyway) instead of leaving it
  "offline" forever.
- The same account pairing again at a new address keeps the `mch_` id
  and moves it there; the old link then gets 401 and agl drops it.
- Machines paired before this (scope `""`) keep using `/agl`.

`--private` keeps each product out of the sessions the person runs
locally (`agl run`, desktop apps); a product sees only the sessions it
starts.

**Single instance.** Machine sockets live in the process that accepted
them. Running several FastClaw instances needs a sticky socket holder (or
agl's HTTP link) — the seam is `Hub.Request` / `Hub.Subscribe`.

## Tool mode

Tools offered to LLM agents (platform and private agents alike):

| Tool | Params | Result |
| --- | --- | --- |
| `local_agents` | none | `{"computers": [{id, name, online, access, agents: [{id, name}]}]}` — the chatter's machines with `allowAgents`, available agents only |
| `run_local_agent` | `agent` (required, e.g. `claude`), `task` (required, a self-contained instruction), `machine` (name or id), `cwd` | `[<agent name> on <machine>]\n<final answer>`; failures are tool errors with a short message for the LLM to relay |

**When they are offered.** Only on a turn where the gateway stamped
`MachineUserID` (`machineUserFor`): a 1:1 message typed by a person (not
cron/heartbeat/sub-agent, not a bot, not web, not a shared-identity
session, never a group), in an IM channel whose `channels.user_id` is the
user space the message was routed to, and — when the channel knows who
connected it (`bound_user_id`) — only from that sender. `MachineUserID`
is that user space: **the chatter's own account, never the agent's
owner.** The agent then mounts the tools only if that user has ≥1 active
machine with `allowAgents`, and adds one system line naming them
(`The person has linked their own computer(s): MacBook (online: claude,
codex); …`). Anyone else's turn is unchanged. Runtime `agl` agents never
reach the LLM, so they never see the tools.

**A run.** Ownership is re-read at call time (`ListMachines(user)`,
`allowAgents`, not revoked). Machine: the one named (`machine` = id or
name), else the first online one that has the agent; offline / missing
agent / not allowed → tool error. agl session `fc:tool:<machineId>:<agentId>:<chatHash>`
(hash of channel+account+chat), so follow-ups in that chat keep the local
agent's context; created/resumed with access = `defaultAccess`, cwd =
`cwd` or `defaultCwd`. The prompt is sent with `mode: "queue"`; the tool
waits for its `turn_ended`, keeping only non-commentary `text_delta`,
capped at 8 KB with a truncation note. Outcome `failed` / `aborted` →
tool error (with partial output). Limit 15 min, and at most 80% of what
is left of the turn's deadline (the IM task timeout,
`task_timeout_sec`, default 300 s — raise it for long jobs); on timeout
or when the turn is cancelled, `session.abort` is sent.

**While it runs.** Typing indicator every 5 s. `permission_request` →
`需要确认：<title>\n回复 1 允许 / 2 本会话都允许 / 3 拒绝` in the chat. After
20 s without an answer (and no pending approval) one notice:
`<agent name> 正在你的电脑上处理…`. Approval timeouts are reported like in
passthrough.

**Approval replies.** `routeDM` first calls `interceptToolApproval`:
when the chat has an open tool-run permission request, a `1`/`2`/`3`
from the bound sender (same user space as the run) is answered with
`session.approve` (`allow` / `allow_session` / `deny`) and acknowledged,
and the message goes no further — no new turn, no steering into the
turn that is blocked in the tool call. Anything else routes as usual.
Requests live in memory (one per chat); after a restart the run's turn is
gone anyway.

### Sending files back (`send_local_file`)

Mounted with the two tools above. `send_local_file {path, machine?}` reads
one file from the chatter's machine with agl `fs.read` (allowed on any hub
link access), stores it in the turn's session workspace and returns
`/workspace/<name>`. The reply then carries `![](/workspace/<name>)`
(image → photo) or `[name](/workspace/<name>)` (document → attachment),
which the gateway already uploads to the IM channel.

- Only deliverable kinds: png/jpg/gif/webp, pdf, docx/xlsx/pptx,
  txt/md/csv, zip, mp4, mp3; at most 20 MB.
- Never a path with a segment starting with `.` (`~/.ssh`, `.git`, …).
- SVG is refused with a hint: chat apps don't render it, so the model asks
  the coding agent to export a PNG first.
- Relative paths resolve against the machine's default cwd.

## IM turn semantics (passthrough agents)

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
| `FASTCLAW_PUBLIC_URL` | Origin machines and install commands use, e.g. `https://fastclaw.example.com` (no `/agl`). Empty: the request's origin (honours `X-Forwarded-Proto/Host`). agl's installer requires HTTPS except for localhost. **Set it in production**: a product's backend calls `/v1/machines/pairing` (and may relay `install.sh`) over an internal address, which would otherwise end up in the install command and the script's hub URL, where users' computers can't reach it. Deploy configs: `deploy/docker/docker-compose.yml`, `deploy/k8s/fastclaw.yaml`, helm `publicUrl`. Local `make dev`: put it in `.env.dev`. |

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
