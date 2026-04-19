#!/usr/bin/env python3
"""
Mem0 Long-term Memory Plugin for FastClaw.

JSON-RPC server over stdin/stdout that provides hook-based memory integration.
- before_model_call: searches mem0 for relevant memories, injects them into messages
- after_model_call: stores the conversation turn in mem0 asynchronously
"""

import json
import sys
import threading
import requests

# Plugin state
config = {
    "url": "http://127.0.0.1:8100",
    "apiKey": "",
    "topK": 5,
}


def log(msg):
    """Log to stderr (stdout is reserved for JSON-RPC)."""
    print(f"[mem0] {msg}", file=sys.stderr, flush=True)


def send_response(resp):
    """Write a JSON-RPC response to stdout."""
    line = json.dumps(resp, separators=(",", ":"))
    sys.stdout.write(line + "\n")
    sys.stdout.flush()


def handle_initialize(params, req_id):
    """Store config from host."""
    global config
    cfg = params.get("config", {})
    if cfg.get("url"):
        config["url"] = cfg["url"]
    if cfg.get("apiKey"):
        config["apiKey"] = cfg["apiKey"]
    if cfg.get("topK"):
        config["topK"] = int(cfg["topK"])
    log(f"initialized with url={config['url']}, topK={config['topK']}")
    send_response({"jsonrpc": "2.0", "result": {"status": "ok"}, "id": req_id})


def handle_hook_register(params, req_id):
    """Tell host which hook points we want."""
    send_response({
        "jsonrpc": "2.0",
        "result": {"points": ["before_model_call", "after_model_call"]},
        "id": req_id,
    })


def handle_hook_fire(params, req_id):
    """Handle a hook event from the host."""
    point = params.get("point", "")

    if point == "before_model_call":
        handle_before_model_call(params, req_id)
    elif point == "after_model_call":
        handle_after_model_call(params)
        # after_model_call is sent as a notification (no id), but handle both
        if req_id is not None:
            send_response({"jsonrpc": "2.0", "result": {}, "id": req_id})
    else:
        if req_id is not None:
            send_response({"jsonrpc": "2.0", "result": {}, "id": req_id})


def handle_before_model_call(params, req_id):
    """Search mem0 for memories and inject them before the model call."""
    messages = params.get("messages", [])
    chat_id = params.get("chatId", "")
    owner_user_id = params.get("userId", "")

    if not messages or len(messages) < 2:
        send_response({"jsonrpc": "2.0", "result": {}, "id": req_id})
        return

    # Only inject on first model call (no assistant messages yet)
    has_assistant = any(m.get("role") == "assistant" for m in messages)
    if has_assistant:
        send_response({"jsonrpc": "2.0", "result": {}, "id": req_id})
        return

    # Get last user message
    last_msg = messages[-1]
    if last_msg.get("role") != "user":
        send_response({"jsonrpc": "2.0", "result": {}, "id": req_id})
        return

    user_text = last_msg.get("content", "")
    # Namespace memories by owning user ID (multi-user isolation), then
    # by chat_id within that user for finer granularity.
    user_id = f"{owner_user_id}:{chat_id}" if owner_user_id else chat_id
    if not user_id or user_id == "web-ui":
        send_response({"jsonrpc": "2.0", "result": {}, "id": req_id})
        return

    # Search mem0
    memories = search_memories(user_id, user_text)
    if not memories:
        send_response({"jsonrpc": "2.0", "result": {}, "id": req_id})
        return

    # Build memory injection
    lines = ["# User Memories (from long-term memory store)"]
    lines.append("The following facts were previously learned about this user:")
    for mem in memories:
        lines.append(f"- {mem}")
    lines.append("")
    lines.append("Use these memories to personalize your response when relevant.")
    injection = "\n".join(lines)

    # Inject as system message before the last user message
    injected = list(messages[:-1])
    injected.append({"role": "system", "content": injection})
    injected.append(last_msg)

    log(f"injected {len(memories)} memories for user_id={user_id}")
    send_response({
        "jsonrpc": "2.0",
        "result": {"messages": injected},
        "id": req_id,
    })


def handle_after_model_call(params):
    """Store conversation turn in mem0 asynchronously."""
    messages = params.get("messages", [])
    response = params.get("response", {})
    chat_id = params.get("chatId", "")
    owner_user_id = params.get("userId", "")

    if not response or response.get("hasTools", False):
        return  # Only store on final text response

    user_id = f"{owner_user_id}:{chat_id}" if owner_user_id else chat_id
    if not user_id or user_id == "web-ui":
        return

    # Find last user message
    user_text = ""
    for msg in reversed(messages):
        if msg.get("role") == "user":
            user_text = msg.get("content", "")
            break

    if not user_text:
        return

    assistant_text = response.get("content", "")
    if not assistant_text:
        return

    # Store async in a thread
    threading.Thread(
        target=store_memory,
        args=(user_id, user_text, assistant_text),
        daemon=True,
    ).start()


def search_memories(user_id, query):
    """Call POST /search on the mem0 server."""
    try:
        headers = {"Content-Type": "application/json"}
        if config["apiKey"]:
            headers["X-API-Key"] = config["apiKey"]

        resp = requests.post(
            f"{config['url']}/search",
            json={"query": query, "user_id": user_id, "limit": config["topK"]},
            headers=headers,
            timeout=3,
        )
        if resp.status_code != 200:
            log(f"search error: HTTP {resp.status_code}: {resp.text[:200]}")
            return []

        data = resp.json()
        results = data.get("results", [])
        return [r.get("memory", "") for r in results if r.get("memory")]
    except Exception as e:
        log(f"search error: {e}")
        return []


def store_memory(user_id, user_text, assistant_text):
    """Call POST /memories on the mem0 server."""
    try:
        headers = {"Content-Type": "application/json"}
        if config["apiKey"]:
            headers["X-API-Key"] = config["apiKey"]

        resp = requests.post(
            f"{config['url']}/memories",
            json={
                "messages": [
                    {"role": "user", "content": user_text},
                    {"role": "assistant", "content": assistant_text},
                ],
                "user_id": user_id,
            },
            headers=headers,
            timeout=10,
        )
        if resp.status_code == 200:
            log(f"stored memory for user_id={user_id}")
        else:
            log(f"store error: HTTP {resp.status_code}: {resp.text[:200]}")
    except Exception as e:
        log(f"store error: {e}")


def handle_shutdown(params, req_id):
    """Handle shutdown request."""
    send_response({"jsonrpc": "2.0", "result": {"status": "ok"}, "id": req_id})
    sys.exit(0)


def main():
    """Main JSON-RPC loop: read line-delimited JSON from stdin."""
    log("starting mem0 plugin")

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue

        try:
            msg = json.loads(line)
        except json.JSONDecodeError as e:
            log(f"invalid JSON: {e}")
            continue

        method = msg.get("method", "")
        params = msg.get("params", {})
        req_id = msg.get("id")  # None for notifications

        if method == "initialize":
            handle_initialize(params, req_id)
        elif method == "hook.register":
            handle_hook_register(params, req_id)
        elif method == "hook.fire":
            handle_hook_fire(params, req_id)
        elif method == "shutdown":
            handle_shutdown(params, req_id)
        else:
            if req_id is not None:
                send_response({
                    "jsonrpc": "2.0",
                    "error": {"code": -32601, "message": f"unknown method: {method}"},
                    "id": req_id,
                })


if __name__ == "__main__":
    main()
