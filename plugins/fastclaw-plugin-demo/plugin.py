#!/usr/bin/env python3
"""
Echo plugin for FastClaw.

A minimal example demonstrating the JSON-RPC plugin protocol.
This plugin provides a single "echo" tool that returns whatever text is sent to it.
"""

import json
import sys


def send(obj):
    """Write a JSON object to stdout followed by a newline."""
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()


def handle_request(req):
    """Handle a JSON-RPC request and return a response."""
    method = req.get("method", "")
    req_id = req.get("id")
    params = req.get("params", {})

    if method == "initialize":
        return {"jsonrpc": "2.0", "result": {"status": "ok"}, "id": req_id}

    elif method == "shutdown":
        send({"jsonrpc": "2.0", "result": {"status": "ok"}, "id": req_id})
        sys.exit(0)

    elif method == "tool.list":
        tools = {
            "tools": [
                {
                    "name": "echo",
                    "description": "Returns whatever text is sent to it. Useful for testing.",
                    "parameters": {
                        "type": "object",
                        "properties": {
                            "text": {
                                "type": "string",
                                "description": "The text to echo back",
                            }
                        },
                        "required": ["text"],
                    },
                }
            ]
        }
        return {"jsonrpc": "2.0", "result": tools, "id": req_id}

    elif method == "tool.execute":
        name = params.get("name", "")
        args = params.get("args", {})

        if name == "echo":
            text = args.get("text", "")
            return {
                "jsonrpc": "2.0",
                "result": {"result": f"Echo: {text}"},
                "id": req_id,
            }
        else:
            return {
                "jsonrpc": "2.0",
                "error": {"code": -32601, "message": f"Unknown tool: {name}"},
                "id": req_id,
            }

    else:
        return {
            "jsonrpc": "2.0",
            "error": {"code": -32601, "message": f"Unknown method: {method}"},
            "id": req_id,
        }


def main():
    """Main loop: read JSON-RPC requests from stdin, write responses to stdout."""
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue

        try:
            req = json.loads(line)
        except json.JSONDecodeError:
            send(
                {
                    "jsonrpc": "2.0",
                    "error": {"code": -32700, "message": "Parse error"},
                    "id": None,
                }
            )
            continue

        resp = handle_request(req)
        if resp is not None:
            send(resp)


if __name__ == "__main__":
    main()
