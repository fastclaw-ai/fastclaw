# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is FastClaw

A single-binary, multi-user AI Agent runtime in Go. Creates, manages, and runs AI agents — each with its own personality (SOUL.md), memory, skills, tools, and LLM provider. Handles LLM communication, tool execution, sandbox isolation, session management, and IM channel routing.

## Build & Dev

```bash
make build            # builds web UI (Next.js) + Go binary → bin/fastclaw
make build-web        # web UI only: cd web && pnpm install && pnpm build → internal/setup/web/
make bundle-skills    # sync repo-root skills/ → internal/agent/bundled_skills/ (go:embed requires real copy)
make test             # go test ./...
make dev              # build-web + air (hot reload)
make install          # build + install to ~/.local/bin
make release-local    # cross-compile all platforms → dist/
```

Prerequisites: Go 1.25+, Node 24+, pnpm 10. CGO is disabled (`CGO_ENABLED=0`).

Run a single test: `go test ./internal/store/ -run TestMigrate -v`

Frontend changes require `make build-web` before the Go binary reflects them.

## Architecture

### Core flow

```
IM channels / Web UI / API
        ↓
    bus.MessageBus (Inbound/Outbound)
        ↓
    gateway.Gateway (orchestrator, lazy UserSpace loading)
        ↓
    taskqueue (per-channel+chat serialization)
        ↓
    agent.Agent.HandleMessage (ReAct loop)
        ↓
    provider.Provider (LLM call via open-agent-sdk-go)
        ↓
    tools.Registry (exec, file I/O, web, memory, skills, sub-agent, cron...)
```

### Key packages (internal/)

| Package | Role |
|---------|------|
| `gateway` | Runtime orchestrator. Lazy-loads `UserSpace` per user, wires bus→taskqueue→agent. No agents loaded at boot. |
| `agent` | ReAct loop (`loop.go`, ~3200 lines), context building, tool execution, memory, goals, skills, hooks |
| `agent/tools` | Tool implementations: exec, file I/O, web_fetch/search, cron, sub-agent, goal, memory_search |
| `store` | Persistence layer — SQLite (default, WAL mode) or Postgres. Single `DBStore` with dialect-aware SQL |
| `config` | In-memory `Config` snapshot from env vars + DB. Three-layer agent config merge: defaults → agent entry → agent-file config |
| `provider` | LLM abstraction — Anthropic and OpenAI-compatible implementations |
| `api` | OpenAI-compatible HTTP surface (`/v1/chat/completions`, `/v1/agents`, `/v1/users`, WebSocket) |
| `setup` | Web UI server + admin API handlers. Embeds built Next.js static export |
| `channels` | IM adapters: Telegram, Discord, Slack, LINE, WeChat, Feishu, Web (SSE) |
| `bus` | In-process message bus connecting channels ↔ gateway ↔ agents |
| `sandbox` | Sandboxed execution: Docker, E2B, Boxlite backends |
| `cron` | Scheduler reads jobs directly from DB each tick, fires onto bus |
| `mcp` | MCP client manager (stdio + HTTP), maps external tools into agent registry |
| `plugin` | Out-of-process plugin system (JSON-RPC subprocess) |
| `scope` | Settings resolution — merges system/user/agent-scoped `configs` rows |
| `skills` | Skill installation from ClawHub, GitHub, tarballs, skills.sh |
| `taskqueue` | Bounded concurrent task queue, serializes per-(channel,chat) |
| `workspace` | Durable blob store (local FS or S3) for agent artifacts |
| `users` | User provisioning, lazy app_user minting per IM chatter, API key management |

### Database-first config

There is no `fastclaw.json`. Bootstrap settings come from `FASTCLAW_*` env vars. All runtime config (providers, channels, agent settings, defaults) lives in the `configs` DB table with `(kind, user_id, agent_id, name)` keys and layered scope-merge. Code modifying config should go through the store, not write files.

### Multi-tenant model

Every persisted row is keyed by a real `users.id`. `agents.id` is globally unique. Sessions are per-(user, agent). The gateway lazy-loads `UserSpace` per authenticated user and evicts on idle.

### Provider/tool resolution

Per-agent, falling back to shared/system defaults. Model field format: `"<providerKey>/<modelId>"` (e.g. `openai/gpt-4o`). Check `ResolvedAgent` for effective merged config.

### Web frontend

Next.js (App Router) + React + TypeScript + Tailwind CSS v4 in `web/`. Built to static export → `web/out` → embedded into Go binary via `internal/setup/web/`.

### Skills layering

Five layers: `bundled` → `managed` → `user` → `agent` → `extra`. Bundled skills (`skill-creator`, `find-skills`) are synced from `skills/` via `make bundle-skills` into `internal/agent/bundled_skills/` because `go:embed` cannot follow symlinks.

## Code conventions

- CLI uses `cobra`. Subcommands in `cmd/fastclaw/cmd_*.go`.
- Build version stamped via `-ldflags` into both `main.*` and `internal/buildinfo.*` — keep them in sync.
- `store.ErrNotFound` is the sentinel for missing records.
- `config.WithUserID` / `config.UserIDFromContext` for request-scoped user identity.
- Agent prompt mode (`PromptModeAgent` / `Chatbot` / `Customize`) controls system prompt shape and exposed tools.
- Channel adapters share `SplitMessageMarker` (`<|split|>`) for multi-bubble replies and `FlattenMarkdownTables` for IM compatibility.
- Hot-reload: CLI writes send `SIGHUP` to running gateway for cache invalidation.
