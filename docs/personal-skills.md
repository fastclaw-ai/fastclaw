# Personal skills

A person can add skills of their own. They live in that person's personal
layer (`~/.fastclaw/users/<uid>/skills`, object-store owner `_user_<uid>`).
The agent loop loads that layer for every turn the person chats in, with any
agent (`SkillsLoader.WithUserID` with the chatter, `loop.go`
`refreshSkillsFromStore`). So a personal skill is usable only in that
person's own conversations. This includes a product's end users: with
`X-Fastclaw-End-User`, the caller is their app_user, the same id their turns
run as.

## API

The caller is the signed-in user or the end user a product acts for.
Channel accounts are refused.

| Route | |
|---|---|
| `GET /api/me/skills` | `{skills:[{name,description,files,bytes,updatedAt}], max}` |
| `POST /api/me/skills` | multipart `file`: a `.zip` of one skill folder, or a bare `SKILL.md`. Re-uploading a name replaces that skill whole. `{skill, replaced}` |
| `DELETE /api/me/skills/{name}` | the caller's own only |

Errors are `{code, detail?}`, where `code` is one of:

- `invalid_file`, `too_large`, `too_many_files`, `no_skill_md`
- `invalid_frontmatter` (SKILL.md needs frontmatter with a `description`)
- `invalid_name` (frontmatter `name` or the folder: `[a-z0-9][a-z0-9_-]{0,47}`)
- `name_reserved` (the deployment has a global skill by that name)
- `file_not_allowed` (`detail` names the file)
- `too_many`, `not_found`

## Instructions only

An upload may hold SKILL.md (100 KB at most) and UTF-8 text reference files
(`.md .markdown .txt .json .yaml .yml .csv .tsv`). The limits are 50 files,
1 MB in all, and 20 skills per person. Scripts, binaries and dotfiles are
refused, for two reasons:

- a personal skill's scripts don't reach the sandbox (`/skills` holds the
  agent's and the global skills only);
- code from anyone who can sign up is not something to run with the
  deployment's keys nearby.

When `load_skill` loads a personal skill, `{baseDir}` becomes
`skills/<name>`, which `read_file` routes to the person's own folder. It
also lists the skill's other files. A global skill's name can't be taken,
so a personal skill never stands in for one or picks up its configured keys.
