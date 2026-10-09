---
name: local-coding-agents
description: Hand a task to the Codex CLI or Claude Code CLI installed on this machine and relay the result. Use when the user says to have / ask / tell "codex", "claude code", "cc" or a coding agent do something — draw or generate an image, write or fix code, explain a repo, answer a question. These are command-line programs on the host, not FastClaw agents, so never use message_agent for them.
metadata:
  fastclaw:
    os: [darwin, linux]
    requires:
      anyBins: [codex, claude]
---

# Local coding agents (Codex / Claude Code)

`codex` and `claude` are agent CLIs on the host. Treat a request to them like
passing a message to a colleague: send it, wait, relay the answer. Handing off
one task is a single step — don't announce a plan, just run it.

Host execution only works when the agent owner is talking to you. If `exec`
says host execution is restricted, or the CLI is missing / not logged in, say
so plainly instead of retrying variations.

Use the CLI the user named; if they didn't name one, use `codex`. Always call
it as `command codex` / `command claude` (bypasses shell aliases that add
unsafe flags).

## Default: one call, then reply

For anything that isn't a change to an existing repository — images,
questions, explanations, small standalone scripts — run the CLI once in your
Working Directory (shown in Runtime info) with a foreground `exec` and
`timeout: 600`. Pass the user's request through faithfully; add only context
the CLI can't know.

**Codex**

```bash
cd "<Working Directory>" && command codex exec -s workspace-write --skip-git-repo-check \
  --json -o .codex-last.md "<request>" > .codex-events.jsonl 2> .codex-stderr.log
echo "exit=$?"; cat .codex-last.md
grep -m1 '"thread.started"' .codex-events.jsonl
```

- Codex can generate images itself. They are saved under
  `${CODEX_HOME:-$HOME/.codex}/generated_images/<thread_id>/`, where
  `thread_id` comes from the `thread.started` event above. Copy them into your
  Working Directory with a meaningful name and show them in your reply:

  ```bash
  cp "${CODEX_HOME:-$HOME/.codex}/generated_images/<thread_id>/"*.png ./cat.png
  ```

  then reply with `![cat](cat.png)`. Any other file Codex wrote in the Working
  Directory (an SVG, a script) is delivered the same way, by relative path.
- If the exit code is non-zero, show the user the last lines of
  `.codex-stderr.log`.

**Claude Code**

```bash
cd "<Working Directory>" && command claude -p "<request>" \
  --permission-mode acceptEdits --output-format json > .claude-result.json 2> .claude-stderr.log
echo "exit=$?"; cat .claude-result.json
```

- The reply is in `result`. If `is_error` is true, `result` holds the reason
  (e.g. "Credit balance is too low", not logged in) — tell the user as-is.
- Claude Code cannot generate raster images; for a picture it can write an
  SVG/HTML file, which you deliver by relative path.

Then answer the user in your own words, say which CLI did the work, and
include any images/files. Keep the reply short.

**Follow-ups** in the same thread ("make the cat orange"): resume instead of
starting over — `command codex exec resume <thread_id> "<follow-up>"` (same
flags), or `command claude -p "<follow-up>" --resume <session_id>` using
`session_id` from `.claude-result.json`.

## Changing code in a repository

Only when the user wants an existing repo modified. Ask which repo if unclear.

1. Isolate the work so the user's checkout is untouched:

   ```bash
   REPO=/path/to/repo; TASK=fc-$(date +%Y%m%d-%H%M%S)
   WT="$REPO/../.fc-worktrees/$TASK"
   git -C "$REPO" worktree add -b "$TASK" "$WT" HEAD
   ```

2. Run the CLI inside `$WT` with a self-contained brief (goal, constraints,
   "run the tests; do not commit"). Repo changes can take many minutes, so use
   `run_in_background: true` and check with `bash_output` until it exits.
   - Codex: same command as above with `cd "$WT"`. Network is off in
     `workspace-write`; add `-c sandbox_workspace_write.network_access=true`
     only if it must install dependencies.
   - Claude Code: add `--allowedTools "Bash"` so it can run builds and tests.
3. Verify yourself: `git -C "$WT" status --short && git -C "$WT" diff --stat`,
   and run the tests if it claims they pass. If it's wrong, resume the same
   session with concrete feedback (at most two rounds).
4. Deliver: commit, push the branch and open a PR (`gh pr create --fill`) if
   the user wants one; otherwise summarise the diff and name the branch. Send
   the PR link with a 2–4 line summary.

Never use `--dangerously-bypass-approvals-and-sandbox` (Codex) or
`bypassPermissions` (Claude Code). For read-only questions about a repo, run
in the repo with `-s read-only` (Codex) or `--permission-mode plan` (Claude
Code) — no worktree needed.
