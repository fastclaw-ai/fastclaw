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
| `POST /api/me/skills` | multipart `file`: a `.zip` holding a skill, or a bare `SKILL.md`. Re-uploading a name replaces that skill whole. `{skill, replaced}` |
| `DELETE /api/me/skills/{name}` | the caller's own only |

Errors are `{code, detail?}`, where `code` is one of:

- `invalid_file`, `too_large`, `too_many_files`, `no_skill_md`
- `multiple_skills` (two SKILL.md files at the same, shallowest depth)
- `invalid_frontmatter` (SKILL.md needs frontmatter with a `description`)
- `invalid_name` (frontmatter `name` or the folder: `[a-z0-9][a-z0-9_-]{0,47}`)
- `name_reserved` (the deployment has a global skill by that name)
- `file_not_allowed` (a path leaving the skill folder, or a symlink; `detail` names it)
- `too_many`, `not_found`

## What an upload holds

The skill is the folder of the shallowest `SKILL.md` in the zip: the zip's
root, a wrapping folder (a GitHub "Download ZIP"), or any subfolder. That
folder is kept whole (references, templates, images, scripts, dotfiles),
and everything outside it is left out. `.git/`, `__MACOSX/`, `.DS_Store`
and `._*` are skipped. Paths that leave the folder and symlinks are
refused. SKILL.md must be UTF-8 and 256 KB at most. The limits are 200
files, 10 MB in all (uncompressed), and 20 skills per person.

A personal skill's files don't reach the sandbox yet (`/skills` holds the
agent's and the global skills only), so its scripts and templates can be
read but not run there. A personal skill is never given configured keys:
a global skill's name can't be taken, and keys go only to scripts under
`/skills/<name>`.

When `load_skill` loads a personal skill, `{baseDir}` becomes
`skills/<name>`, which `read_file` routes to the person's own folder. It
also lists the skill's other files.
