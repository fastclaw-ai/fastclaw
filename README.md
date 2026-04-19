<div align="center">

# FastClaw

A lightweight AI Agent runtime written in Go.

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat&logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**Single binary - Any LLM - Multi-agent - Sandbox - Cloud-ready**

[Install](#install) - [Quick Start](#quick-start) - [Architecture](#architecture) - [Features](#features)

</div>

---

## What is FastClaw?

FastClaw is an **Agent Factory** — it creates, manages, and runs AI agents. Each agent has its own personality (SOUL.md), memory, skills, and tools. FastClaw handles the LLM communication, tool execution, sandbox isolation, and session management.

```bash
# Install and run
curl -fsSL https://raw.githubusercontent.com/fastclaw-ai/fastclaw/main/install.sh | bash
fastclaw    # Opens setup wizard at http://localhost:18953
```

## Quick Start

### 1. First Run

```bash
./fastclaw
# Opens setup wizard → configure LLM provider → creates default agent
# Admin token is generated — save it for dashboard login
```

### 2. Dashboard

Open `http://localhost:18953` and login with your admin token.

- **Agents** — Create and manage agents, each with its own personality and model
- **Skills** — Install shared skills from ClawHub or GitHub
- **Models** — Configure LLM providers (OpenAI, Anthropic, Ollama, OpenRouter, etc.)
- **Settings** — Storage, sandbox, gateway configuration

### 3. Agent Management

Click an agent to enter its management panel:

- **Chat** — Talk to the agent (debug/test)
- **Files** — Edit SOUL.md, IDENTITY.md, MEMORY.md, etc.
- **Skills** — Agent-private skills
- **Models** — Agent-specific model overrides
- **Sessions** — Conversation history

## Architecture

```
~/.fastclaw/
  fastclaw.json              # Global config (gateway, storage, providers, defaults)
  apikeys.json               # API keys for external access
  skills/                    # Shared skills (bundled + installed)
  agents/
    default/agent/           # Agent workspace
      agent.json             # Agent config (model override)
      SOUL.md                # Personality
      MEMORY.md              # Long-term memory
      sessions/              # Conversation history
      skills/                # Agent-private skills
    my-coder/agent/
      ...
```

### Storage

| Data | `storage: "file"` | `storage: "postgres"` |
|------|-------------------|-----------------------|
| Global config | File (always) | File (bootstrap) |
| Sessions | JSONL files | DB |
| Memory / SOUL.md | Files | DB |
| Agent config | Files | DB |
| Skills | Files (always) | Files (always) |

### What FastClaw Stores

| Data | Belongs to | Storage |
|------|-----------|---------|
| SOUL.md, IDENTITY.md | Agent | FastClaw |
| MEMORY.md | Agent | FastClaw |
| Skills | Agent / Global | FastClaw |
| Sessions | Agent | FastClaw |
| User accounts, billing | Application | Your app (ChatClaw, etc.) |
| Output files | Application | Your app / S3 |

## Features

### LLM Providers
- OpenAI, Anthropic, Ollama, OpenRouter, Groq, DeepSeek, Mistral, and any OpenAI-compatible API
- Per-agent model override
- Prompt cache support (RawAssistant preservation)

### Tools & Sandbox
- Built-in: exec, read_file, write_file, list_dir, web_fetch, web_search, memory_search
- E2B cloud sandbox or Docker sandbox
- MCP server support
- Plugin system (JSON-RPC subprocess)

### Skills
- Bundled skills: code-runner, image-gen, data-analysis, translation, web-search, skill-manager
- Install from [ClawHub](https://clawhub.ai) or [skills.sh](https://skills.sh)
- Agent-private or globally shared

### Memory
- MEMORY.md — long-term facts, auto-updated by heartbeat
- Session-based context with full history preservation
- Thinking/reasoning content preserved for memory extraction

### API
- OpenAI-compatible `/v1/chat/completions` (streaming)
- Web chat `/api/chat/stream` (SSE)
- Session management `/api/chat/sessions`
- Agent CRUD `/api/agents`
- Provider management `/api/config`
- Skill install `/api/skills/install` (ClawHub + GitHub)
- API key management `/v1/admin/apikeys`

## Configuration

### fastclaw.json

```json
{
  "gateway": {
    "port": 18953,
    "auth": { "token": "your-admin-token" }
  },
  "storage": {
    "type": "postgres",
    "dsn": "postgres://user:pass@localhost:5432/fastclaw?sslmode=disable",
    "autoMigrate": true
  },
  "sandbox": {
    "enabled": true,
    "backend": "e2b",
    "e2bKey": "e2b_..."
  },
  "providers": {
    "openrouter": {
      "apiKey": "sk-or-...",
      "apiBase": "https://openrouter.ai/api/v1",
      "apiType": "openai"
    }
  },
  "agents": {
    "defaults": {
      "model": "openrouter/openai/gpt-4o",
      "maxTokens": 8192,
      "temperature": 0.7
    }
  }
}
```

## Deployment

### Local
```bash
./fastclaw gateway
```

### Docker
```bash
cd deploy/docker && ./start.sh
```

### Kubernetes
```yaml
volumeMounts:
  - name: config
    mountPath: /root/.fastclaw/fastclaw.json
    subPath: fastclaw.json
env:
  - name: FASTCLAW_STORAGE_DSN
    valueFrom:
      secretKeyRef:
        name: fastclaw-db
        key: dsn
```

## Building

```bash
# Build frontend
cd web && pnpm install && pnpm build
cp -r out ../internal/setup/web

# Build binary
go build -o fastclaw ./cmd/fastclaw
```

## License

MIT
