# Coding-Agent Project Runtime

This document is the integration contract for the **coding-agent runtime**:
the layer that lets fastclaw scaffold a project from a template, run its
dev server in a long-lived sandbox, and hand back a live preview URL. The
upstream SaaS shell drives everything through the HTTP API below — it
never touches the sandbox, the LLM, or the filesystem directly.

## Mental model: two layers, one project

| Layer | Owns | Table | Lifecycle |
|-------|------|-------|-----------|
| **Project** (pre-existing) | the source tree (a shared workspace folder) + chat grouping | `projects` | created by the user, persists |
| **Project Runtime** (new) | the *running instance* of that tree: a long-lived dev-server container + preview URL | `project_runtimes` | booted on demand, evictable |

A runtime is 1:1 with a project, keyed by the same `(user_id, agent_id,
project_id)`. The two are separate tables on purpose: dropping every
`project_runtimes` row degrades gracefully to "no previews" and never
touches chat history or workspace files. The existing project feature is
byte-for-byte unchanged.

**Why the preview reflects agent edits live:** the runtime container and
the agent's per-turn sandbox bind-mount the *same* host directory
(`workspaces/<agent>/projects/<pid>/`). When the agent writes a file
during a turn, the dev server in the runtime container sees it instantly
and HMR reloads. No file sync — the bind mount is the channel.

## HTTP API

All endpoints are under the existing per-agent auth (`requireAgentReadable`
for reads, `requireWritable` for mutations). Ownership is scoped to the
caller's `user_id`, identical to `/projects`.

If the deployment hasn't wired a runtime manager (non-docker sandbox
backend, or `SetRuntimeManager` never called), every endpoint returns
`503 {"error":"project runtime not enabled on this deployment"}`.

### Runtime record shape

```jsonc
{
  "projectId":   "proj_ab12…",
  "templateRef": "shipany-tanstack",
  "status":      "running",        // none|scaffolding|starting|running|sleeping|crashed
  "devPort":     3000,             // container-internal dev server port
  "hostPort":    49210,            // published host port (0 when sleeping)
  "previewUrl":  "https://proj_ab12.preview.example.com",
  "gitRef":      "abc123",         // last snapshot commit (for revert)
  "lastError":   "",               // populated on status=crashed
  "createdAt":   "2026-06-12T…",
  "updatedAt":   "2026-06-12T…"
}
```

### Endpoints

| Method & path | Purpose | Body | Returns |
|---|---|---|---|
| `GET /api/agents/{id}/projects/{pid}/runtime` | current state | — | runtime record, or `404` if none |
| `POST /api/agents/{id}/projects/{pid}/runtime/up` | provision + boot (idempotent) | `{"templateRef":"shipany-tanstack"}` (required on first boot, ignored after) | runtime record (`status:running`) |
| `POST /api/agents/{id}/projects/{pid}/runtime/sleep` | stop container, keep files | — | `{"ok":true,"status":"sleeping"}` |
| `POST /api/agents/{id}/projects/{pid}/runtime/wake` | re-boot a sleeping runtime | — | runtime record |
| `POST /api/agents/{id}/projects/{pid}/runtime/exec` | run one coding-tool command in the live project sandbox | `{"command":"pnpm build","timeoutSeconds":120}` | `{"output":"…","truncated":false}` |
| `DELETE /api/agents/{id}/projects/{pid}/runtime` | tear down container + forget runtime (files kept) | — | `{"ok":true}` |
| `GET /api/agents/{id}/projects/{pid}/preview` | preview URL + status only | — | `{"previewUrl":…,"status":…}` |
| `GET /api/agents/{id}/projects/{pid}/runtime/logs?tail=200` | dev-server log tail | — | `{"logs":"…"}` |

`up` and `wake` may take minutes (scaffold + `pnpm install`); the handler
allows a 10-minute deadline. The SaaS should show a "building…" state and
poll `GET …/runtime` (or `…/preview`) until `status` is `running` or
`crashed`.

`exec` requires the same write access and effective-user project ownership as
the other mutating endpoints. It never falls back to host execution, caps a
request and response at 1 MiB, and clamps `timeoutSeconds` to 120. A non-zero
command returns `409` with both `error` and any captured `output`.

### Typical SaaS flow ("make me an X")

1. `POST /api/agents/{id}/projects` → create the project (existing API), get `pid`.
2. Send the build instruction via the existing chat API (`/api/chat/stream`)
   with `projectId=pid`. The coding-agent persona customizes the template.
3. `POST …/{pid}/runtime/up` with `templateRef` → boots the dev server.
4. Poll `GET …/{pid}/preview` until `status=running`, then iframe `previewUrl`.
5. Further edits: just send more chat turns. HMR reflects them; no re-up needed.
6. Idle: `POST …/sleep` to free compute; `POST …/wake` when the user returns.

## Using it from fastclaw's own web chat (no SaaS shell)

The runtime is also wired into the agent loop as two tools, so you can
dogfood the whole loop in fastclaw's built-in web chat:

- `start_app_preview` — scaffolds the project from the template (first
  call), boots the dev server, returns the preview URL.
- `app_preview_logs` — tails the dev-server log to debug a bad edit.

These tools appear **only** on agents that have a runtime wired
(`SetProjectRuntime`), so ordinary agents are unaffected. When present,
the system-prompt guidance flips from "don't start dev servers" to "use
`start_app_preview` for web-app projects."

**Where the app is homed.** `start_app_preview` works in any chat:

- **Inside a project** → the app is homed at the project root
  (`projects/<pid>/`), shared and persistent across the project's chats.
  A coding agent's file tools address that root (not a per-chat subdir),
  so its edits land where the dev server serves them and HMR reloads.
- **In a loose chat** (no project) → the app is homed in the chat's own
  workspace (`sessions/<sid>/`), which is exactly where the agent's edits
  already go. Great for one-off demos; the app lives with that chat.

So you do **not** need to pre-create a project — it's an optional upgrade
for persistence/sharing. Plain agents (no runtime wired) are unaffected
and keep per-chat isolation.

### Dogfood steps

1. Run fastclaw with the docker sandbox backend and a template source
   (see env vars below). For a local template checkout:
   ```
   FASTCLAW_SHIPANY_TEMPLATE_DIR=/Users/you/code/shipany-tanstack
   ```
   The sandbox image still needs node + pnpm.
2. Open any chat (a project chat for a persistent app, or just a new
   loose chat for a quick demo).
3. Say e.g. *"用 shipany 模板做个 AI 抠图落地页"*. The agent calls
   `start_app_preview` (scaffold + boot), edits the template's copy/theme,
   and replies with a preview URL. Leave `FASTCLAW_PREVIEW_BASE` empty and
   it's `http://127.0.0.1:<port>` — open it directly.
4. Keep chatting to iterate; HMR reflects edits live.

> Note: the agent's `exec` tool still cwd's into the per-chat sandbox
> subdir, so the runtime (not the agent) owns build/install/serve. The
> agent edits files; the dev server rebuilds. If the agent needs to run a
> project command itself it should `cd /workspace` first.

## Server wiring

`cmd/fastclaw/main.go` constructs the manager and registers the
`shipany-tanstack` template when a home dir resolves. It's active for the
docker sandbox backend; other backends leave the endpoints at `503`.

Template commands are env-overridable so fastclaw stays template-agnostic:

| Env var | Default | Meaning |
|---|---|---|
| `FASTCLAW_PREVIEW_BASE` | _(empty)_ | Preview URL template. Empty → `http://127.0.0.1:<hostPort>` (local). Set to `https://{project}.preview.example.com` for the wildcard gateway (the `{project}` token is replaced with the project id). |
| `FASTCLAW_SHIPANY_SCAFFOLD` | `if [ -d /template ]; then cp -a /template/. /workspace/; fi; cd /workspace && (pnpm install \|\| npm install)` | Shell run once in `/workspace` when it's empty. Populates the source tree + installs deps. |
| `FASTCLAW_SHIPANY_DEV` | `pnpm dev --host 0.0.0.0 --port 3000` | Shell that starts the dev server bound to `0.0.0.0:3000`. |
| `FASTCLAW_SHIPANY_TEMPLATE_DIR` | _(empty)_ | Host dir bind-mounted read-only at `/template` in the runtime container (option C). Set to a local checkout (e.g. `~/code/shipany-tanstack`) to scaffold from disk — no image bake, no git clone. The default scaffold's `cp -a /template/.` then works. |

To add another template (e.g. a Next.js starter), call
`rtMgr.RegisterTemplate("my-template", coderuntime.TemplateSpec{…})` —
nothing in the runtime is ShipAny-specific. The **first** registered ref is
the default the preview tool uses when the agent omits one, so registering
more templates never changes an existing deployment's default. A second
template ships out of the box — `vite-react`, a plain Vite + React + TS
starter that self-scaffolds with `npm create vite` (no baked `/template`, no
source fetch) — as a worked example of multi-template support.

## Sandbox backends (docker vs e2b/boxlite)

The preview path depends on `FASTCLAW_SANDBOX_BACKEND`:

- **docker (default)** — the runtime owns a dedicated long-lived container
  per project, publishes the dev port to a host port, and the agent's edits
  reach the dev server through a shared host bind mount. Unchanged.
- **e2b / boxlite (cloud, no host mount)** — the runtime runs the dev server
  inside the **same pooled sandbox the agent writes files to** (so HMR works
  with no bind mount) and exposes the port via the backend's own URL scheme
  (e2b: `https://<port>-<sandboxID>.e2b.app`). Coding writes route to
  `workspace.Store`; for a remote-workspace backend they're additionally
  mirrored into the live sandbox so the dev server sees them.

### Cloud template provisioning (where `/template` comes from)

The scaffold needs the template source at `/template` in the sandbox. On a
cloud backend there is no host bind mount, so pick one of:

1. **Bake into the sandbox image / e2b template (recommended).** Build the
   e2b template (or docker image) with the toolchain (node/pnpm + camoufox
   for copyweb) **and** the template at `/template` — ideally with a warm
   pnpm store so scaffold installs offline and fast. Rebuild only when deps
   change. Set the e2b template id via the `e2bTemplate` setting (falls back
   to `FASTCLAW_SANDBOX_IMAGE`).
2. **Pull source at scaffold time (image stays stable).** Override
   `FASTCLAW_SHIPANY_SCAFFOLD` to `curl` a pinned tarball from object storage
   (R2/S3, via a short-lived presigned URL — no long-lived creds in a sandbox
   that runs LLM code) into `/workspace`, then `pnpm install --offline`
   against a warm store baked in the image. Decouples template content from
   the image; only dep changes need an image rebuild. Prefer this over
   `git clone` for private templates (no token to exfiltrate, pinned snapshot).
3. **Upload from the fastclaw host.** When `FASTCLAW_SHIPANY_TEMPLATE_DIR`
   points at a checkout on the fastclaw server, the e2b path uploads it into
   the sandbox `/template` (`TemplateProvisioner.ProvisionDir`). Convenient
   for local e2b testing; costs a per-cold-sandbox upload.

**Multi-template, one image:** same-stack templates (e.g. several ShipAny
variants) share ONE backend image and differ only by `ScaffoldCmd` / source —
you do **not** need an e2b image per template. A genuinely different stack
(Python vs Node) is the only reason to vary the image; `TemplateSpec.Image`
pins a per-template image on the **docker** path (the e2b path uses the one
pool image, so per-template e2b images would need a pool per-project override
— not wired yet).

### Sandbox image requirements

The runtime reuses the sandbox image (`FASTCLAW_SANDBOX_IMAGE`). For the
ShipAny template that image must have **node + pnpm** and the template
source baked at `/template` (so the default scaffold's `cp -a /template/.`
works). Alternatively override `FASTCLAW_SHIPANY_SCAFFOLD` to `git clone`
the template instead.

## Preview gateway (deployment-side, NOT in this repo)

The runtime publishes the dev port to `127.0.0.1:<hostPort>` on the host —
deliberately **not** `0.0.0.0`, because the container runs LLM-generated
code and must never be directly reachable. Turning `hostPort` into a
shareable URL is a reverse proxy you deploy alongside fastclaw:

```
*.preview.example.com
        │  (wildcard DNS + wildcard TLS, e.g. Caddy / Traefik)
        ▼
   preview gateway  ──looks up subdomain (= project_id) in project_runtimes──▶ 127.0.0.1:<hostPort>
```

Gateway responsibilities:

- **Subdomain → host port.** Resolve `proj_ab12.preview.example.com` to the
  `host_port` of that project's `project_runtimes` row (query the same DB,
  or add a small internal lookup endpoint).
- **Wildcard TLS** for `*.preview.example.com` (Let's Encrypt DNS-01).
- **WebSocket passthrough** — Vite HMR runs over WS. Without it, edits
  won't hot-reload. The template must also advertise the *published host
  port* in its HMR config (`server.hmr.clientPort`), since inside the
  container the dev server only knows port 3000.

Set `FASTCLAW_PREVIEW_BASE=https://{project}.preview.example.com` so the
runtime records gateway-shaped URLs; the gateway does the port mapping.

For local development leave `FASTCLAW_PREVIEW_BASE` empty and hit
`http://127.0.0.1:<hostPort>` directly — no gateway needed.

## What's intentionally left to the integrator

- **Git snapshot / revert.** The record carries `gitRef`; wiring a
  `git commit` after each turn and a `/git/revert` endpoint is a thin
  follow-up using `Manager.Exec` (it runs commands in the runtime
  container). Not built yet.
- **Idle auto-sleep.** `ListAllProjectRuntimes` exists for a sweeper, but
  the background eviction loop for runtimes is not wired (the per-turn
  pool has its own; this is the long-lived layer). Add a ticker that
  `Sleep`s runtimes idle past a TTL.
- **Deploy.** "Ship to production" reuses the template's own deploy skill
  (e.g. ShipAny's `deploy-cloudflare`) via a chat turn or `Manager.Exec`.
