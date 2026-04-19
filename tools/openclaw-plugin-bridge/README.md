# openclaw-proxy

Bridge OpenClaw TypeScript plugins to FastClaw's JSON-RPC protocol.

## How it works

```
FastClaw Gateway ←→ JSON-RPC (stdin/stdout) ←→ openclaw-proxy ←→ OpenClaw Plugin (JS/TS)
```

The proxy loads an OpenClaw plugin, captures its `register()` calls (tools, channels, etc.), and exposes them via FastClaw's JSON-RPC protocol.

## Supported capabilities

| OpenClaw API | FastClaw RPC | Status |
|---|---|---|
| `api.registerTool()` | `tool.list` / `tool.execute` | ✅ |
| `api.registerChannel()` | `channel.send` / `message.inbound` | ✅ |
| `api.registerProvider()` | — | ⬜ Not bridged |
| `api.registerSpeechProvider()` | — | ⬜ Not bridged |

## Usage

### 1. Build the proxy

```bash
cd tools/openclaw-proxy
pnpm install && pnpm build
```

### 2. Create a FastClaw plugin wrapper

Given an OpenClaw plugin installed at `./node_modules/@openclaw/some-plugin`:

```
~/.fastclaw/plugins/some-plugin/
├── plugin.json
└── node_modules/@openclaw/some-plugin/   (npm install here)
```

**plugin.json:**
```json
{
  "id": "some-plugin",
  "type": "tool",
  "command": "node /path/to/openclaw-proxy/proxy.js ./node_modules/@openclaw/some-plugin"
}
```

### 3. That's it

FastClaw will start the proxy as a subprocess, which loads the OpenClaw plugin and bridges all registered tools/channels.

## Example

See `examples/plugins/openclaw-demo/` for a working example.

```bash
# Test manually
echo '{"jsonrpc":"2.0","method":"initialize","params":{"config":{}},"id":1}
{"jsonrpc":"2.0","method":"tool.list","id":2}
{"jsonrpc":"2.0","method":"tool.execute","params":{"name":"get_weather","args":{"city":"Tokyo"}},"id":3}' \
| node proxy.js ../../examples/plugins/openclaw-demo/openclaw-plugin.js
```
