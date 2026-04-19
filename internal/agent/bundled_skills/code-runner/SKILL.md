---
name: code-runner
description: Execute code in multiple programming languages. Use when the user asks to run, test, or debug code in Python, JavaScript, shell, or other languages.
metadata:
  fastclaw:
    always: true
---

# Code Runner Skill

Execute code using the `exec` tool in the sandbox environment.

## Available Languages

### Python 3
```bash
python3 -c "print('hello')"
# Or write to file and run:
python3 script.py
```
Install packages with: `pip install package_name`

### Shell/Bash
```bash
echo "hello" && ls -la
```

### Node.js (install first if needed)
```bash
# Install node if not available
apt-get update && apt-get install -y nodejs npm 2>/dev/null || apk add nodejs npm 2>/dev/null
node -e "console.log('hello')"
```

## Guidelines

- **Always execute code immediately** — don't just show it
- For multi-line scripts, use `write_file` to save then `exec` to run
- Install missing packages automatically without asking
- Show the complete output to the user
- If code fails, analyze the error and fix it automatically
- For Python one-liners, use `python3 -c "..."`
- For complex scripts, write to a `.py` file first

## Error Handling
- If a command fails, check the error and try to fix it
- Common issues: missing packages (install them), syntax errors (fix them), permission denied (use appropriate paths)
- Always retry at least once after fixing the issue
