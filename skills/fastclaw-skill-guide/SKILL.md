---
name: fastclaw-skill-guide
description: Create new skills for FastClaw agents. Use when the user asks to create a skill, turn a workflow into a skill, or build reusable automation. Also use when discussing skill format, structure, or best practices.
---

# FastClaw Skill Guide

Guide for creating new skills that FastClaw agents can discover and use.

## Skill Structure

```
skill-name/
├── SKILL.md          # Required — skill instructions
├── scripts/          # Optional — executable scripts for deterministic tasks
├── references/       # Optional — docs loaded into context as needed
└── assets/           # Optional — templates, icons, fonts
```

## SKILL.md Format

Use YAML frontmatter followed by markdown instructions:

```markdown
---
name: My Skill Name
description: One-line description of what this skill does and when to use it
homepage: https://example.com
metadata:
  fastclaw:
    emoji: "🔧"
    always: false
    os: ["darwin", "linux"]
    requires:
      bins: ["git"]
      anyBins: ["python3", "python"]
      env: ["API_KEY"]
    primaryEnv: "API_KEY"
---

# Skill Title

Step-by-step instructions in markdown...
```

### Frontmatter Fields

| Field | Required | Description |
|-------|----------|-------------|
| `name` | Yes | Human-readable skill name |
| `description` | Yes | Brief description — this is the primary trigger mechanism |
| `homepage` | No | URL for more info |
| `metadata.fastclaw.emoji` | No | Display icon |
| `metadata.fastclaw.always` | No | If true, full content is always in system prompt |
| `metadata.fastclaw.os` | No | OS requirements (darwin, linux, windows) |
| `metadata.fastclaw.requires.bins` | No | All listed binaries must exist on PATH |
| `metadata.fastclaw.requires.anyBins` | No | At least one must exist |
| `metadata.fastclaw.requires.env` | No | Required environment variables |
| `metadata.fastclaw.primaryEnv` | No | Maps config apiKey to this env var |

Note: `metadata.openclaw` is also supported for backward compatibility with OpenClaw skills.

### Writing the Description

The description determines whether the agent loads this skill. Make it specific and slightly "pushy" — include both what the skill does AND contexts where it should trigger.

Bad: `"Format data"`
Good: `"Format CSV and Excel data into clean tables. Use whenever the user mentions spreadsheets, data formatting, column alignment, CSV cleanup, or tabular output."`

## Where Skills Are Stored

Skills are discovered from multiple directories in precedence order (higher overrides lower):

1. **Agent workspace** — `{agentDir}/skills/` — skills specific to this agent
2. **Team** — `{teamDir}/skills/` — shared within a team
3. **User installed** — `~/.fastclaw/skills/` — user-level skills
4. **OpenClaw compatible** — `~/.openclaw/skills/` — installed via OpenClaw
5. **System bundled** — npm global locations
6. **Extra dirs** — configured in `fastclaw.json`

## Three-Level Loading

Skills use progressive disclosure:

1. **Metadata** (name + description) — Always in system prompt context (~100 words)
2. **SKILL.md body** — Loaded when agent calls `load_skill` (<500 lines ideal)
3. **Bundled resources** — Loaded on demand via file tools (unlimited size)

Keep SKILL.md under 500 lines. If approaching the limit, move detailed content to `references/` with clear pointers.

## Writing Guidelines

- Use imperative form in instructions ("Run the command", not "You should run the command")
- Explain **why** things are important, not just what to do
- Include examples for output formats
- Use `{baseDir}` token to reference files within the skill directory — it gets replaced with the absolute path at load time
- Avoid heavy MUST/NEVER language — explain reasoning so the agent can handle edge cases

## Creation Steps

1. **Capture intent** — What should this skill enable? When should it trigger?
2. **Write SKILL.md** — Frontmatter + step-by-step instructions
3. **Add resources** — Scripts in `scripts/`, docs in `references/`, templates in `assets/`
4. **Save** — Write to `{agentDir}/skills/{skill-name}/SKILL.md` for agent-level, or `~/.fastclaw/skills/{skill-name}/SKILL.md` for user-level
5. **Verify** — Use `load_skill` tool to confirm it loads correctly

## Example

```markdown
---
name: git-pr-review
description: Review pull requests with structured feedback. Use when the user asks to review a PR, check code changes, or provide feedback on a merge request.
---

# PR Review

Review a pull request and provide structured feedback.

## Steps

1. Get the PR diff:
   ```bash
   git diff main...HEAD
   ```

2. For each changed file, check:
   - Code correctness and edge cases
   - Naming and readability
   - Test coverage

3. Output feedback in this format:
   ## Summary
   One-paragraph overview.

   ## Issues
   - **file:line** — description of issue

   ## Suggestions
   - Optional improvements
```
