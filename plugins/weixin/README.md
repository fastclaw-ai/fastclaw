# FastClaw WeChat Channel Plugin (ilink)

A channel plugin for [FastClaw](https://github.com/fastclaw-ai/fastclaw) that connects WeChat users via the ilink bot API.

## Features

- **Long-poll messaging** — Real-time message receiving via ilink `getUpdates`
- **Text messages** — Full text send/receive with markdown stripping for WeChat
- **Voice transcription** — Automatically uses WeChat's voice-to-text
- **Quoted replies** — Preserves quoted message context
- **Multi-account** — Supports multiple WeChat bot accounts simultaneously
- **Session persistence** — Survives gateway restarts (sync buf + context tokens)
- **Auto-reconnect** — Exponential backoff on failures, session expiry handling

## Installation

### Manual

```bash
# Copy plugin to FastClaw plugins directory
mkdir -p ~/.fastclaw/plugins/weixin
cp plugin.json plugin.mjs ~/.fastclaw/plugins/weixin/
```

### Via FastClaw CLI

```bash
fastclaw plugin install /path/to/weixin
```

## Configuration

Add to `~/.fastclaw/fastclaw.json`:

```json
{
  "plugins": {
    "enabled": true,
    "entries": {
      "weixin": {
        "enabled": true,
        "config": {
          "accountsDir": "~/.fastclaw/weixin/accounts"
        }
      }
    }
  },
  "bindings": [
    {
      "agentId": "your-agent",
      "match": { "channel": "weixin" }
    }
  ]
}
```

## Account Setup

Accounts are created via the login page (QR scan). Each account is stored as a JSON file:

```
~/.fastclaw/weixin/accounts/
├── accounts.json          # ["account-id-1", "account-id-2"]
├── account-id-1.json      # {"token": "...", "baseUrl": "...", "userId": "..."}
└── account-id-2.json
```

Alternatively, use the FastClaw web dashboard to scan the QR code and add accounts.

## Protocol

This plugin communicates with FastClaw via JSON-RPC 2.0 over stdin/stdout:

### Inbound (plugin → FastClaw)

```json
{
  "jsonrpc": "2.0",
  "method": "message.inbound",
  "params": {
    "channel": "weixin",
    "chatId": "user-id@im.wechat",
    "userId": "user-id@im.wechat",
    "text": "Hello!",
    "peerKind": "dm"
  }
}
```

### Outbound (FastClaw → plugin)

```json
{
  "jsonrpc": "2.0",
  "method": "channel.send",
  "params": {
    "chatId": "user-id@im.wechat",
    "text": "Hi there!"
  },
  "id": 1
}
```

## Data Storage

```
~/.fastclaw/weixin/
├── accounts/              # Account credentials (from login page)
│   ├── accounts.json
│   └── {accountId}.json
└── data/                  # Runtime state (auto-created)
    ├── {accountId}.sync-buf
    └── {accountId}.context-tokens.json
```

## Requirements

- Node.js >= 22
- FastClaw >= v0.9.0

## License

MIT
