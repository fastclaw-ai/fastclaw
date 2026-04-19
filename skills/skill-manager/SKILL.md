---
name: skill-manager
description: Install, search, and manage skills. Use when the user asks to install a skill, find skills, add capabilities, or manage the skill registry.
metadata:
  fastclaw:
    always: false
---

# Skill Manager

Manage skills for all agents via the FastClaw API.

## Search Skills (from ClawHub)

```bash
curl -s "http://localhost:18953/api/skills/search?q=QUERY&source=clawhub" | python3 -m json.tool
```

## Install a Skill

### From ClawHub (clawhub.ai)
```bash
curl -s -X POST http://localhost:18953/api/skills/install \
  -H "Content-Type: application/json" \
  -d '{"source":"clawhub","skill":"SKILL_NAME"}'
```

### From GitHub (skills.sh ecosystem)
```bash
curl -s -X POST http://localhost:18953/api/skills/install \
  -H "Content-Type: application/json" \
  -d '{"source":"github","repo":"owner/repo","skill":"skill-name"}'
```

### From GitHub (entire repo as one skill)
```bash
curl -s -X POST http://localhost:18953/api/skills/install \
  -H "Content-Type: application/json" \
  -d '{"source":"github","repo":"owner/repo"}'
```

## List Installed Skills
```bash
curl -s http://localhost:18953/api/skills
```

## Delete a Skill
```bash
curl -s -X DELETE http://localhost:18953/api/skills/SKILL_NAME
```

## Create a Custom Skill

Write a SKILL.md file to the global skills directory:

```bash
mkdir -p ~/.fastclaw/skills/my-skill
cat > ~/.fastclaw/skills/my-skill/SKILL.md << 'EOF'
---
name: my-skill
description: What this skill does and when to use it
---

# My Skill

Instructions for the agent...
EOF
```

This makes the skill available to **all agents**.

## Create an Agent-Specific Skill

To create a skill only for the current agent, write to the agent's own skills directory instead:
```bash
mkdir -p ./skills/my-private-skill
# Write SKILL.md to ./skills/my-private-skill/SKILL.md
```

## Guidelines
- When the user says "install X skill", search ClawHub first, then try GitHub
- Show the user what was found before installing
- After installing, confirm success and explain what the skill does
- For "create a skill", ask what it should do, then write the SKILL.md
