---
name: fastclaw-skill-learner
description: Analyze conversations to extract reusable skill patterns. Used internally by FastClaw to auto-generate skills from complex multi-step tasks.
metadata:
  fastclaw:
    always: true
---

# Skill Learner

Analyze a conversation and determine if it demonstrates a reusable multi-step workflow that should be saved as a skill.

## When to Extract

Extract a skill when ALL of the following are true:

- The task involved 3+ tool calls in a clear, repeatable sequence
- The steps form a general procedure useful beyond this specific conversation
- The workflow is not trivially simple (not just "read a file and summarize it")

Do NOT extract when:

- The task is one-off or highly specific to current context
- The steps are standard and don't need specialized instructions
- A similar skill already exists

## How to Analyze

Given a conversation transcript, identify:

1. **The core workflow** — What sequence of actions was performed?
2. **The pattern** — Is this generalizable to other inputs/contexts?
3. **The value** — Would having this as a skill save significant effort next time?

## Output Format

If the conversation demonstrates a reusable skill, output JSON:

```json
{
  "extract": true,
  "skill": {
    "name": "Human Readable Name",
    "slug": "kebab-case-slug",
    "description": "One-line description of what this skill does and when to trigger it",
    "content": "Full SKILL.md content with YAML frontmatter and markdown instructions"
  }
}
```

If not reusable, output:

```json
{
  "extract": false
}
```

## Skill Content Guidelines

When generating the SKILL.md content:

- Include proper YAML frontmatter with `name` and `description`
- Write clear step-by-step instructions in markdown
- Generalize from the specific conversation — replace specific values with placeholders
- Explain the reasoning behind each step, not just the commands
- Include example inputs/outputs where helpful
- Keep under 500 lines
- Use `{baseDir}` for any bundled resource references

## Example Extraction

A conversation where the user asks to set up a new Go project with CI, and the agent creates go.mod, writes a Makefile, sets up GitHub Actions, and adds a Dockerfile — this is a good extraction candidate because:

- Multiple coordinated steps (4+ tool calls)
- Generalizable to any new Go project
- Saves significant setup time

The extracted skill would capture the project structure, file templates, and the sequence of steps, parameterized for project name and Go version.
