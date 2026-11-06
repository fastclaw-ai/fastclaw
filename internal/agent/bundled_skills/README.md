Bundled skills ship with the binary and get installed to `~/.fastclaw/skills/`
on every boot via InstallBundledSkills. Products bring their own skills via
FASTCLAW_HOME/skills/ or per-agent skills/ — anything dropped in here is the
runtime's own baseline.

## Upgrade behavior

Each bundled skill carries a `.bundled-hash` sidecar (written by
InstallBundledSkills) that records the SHA-256 of the bundle tree as
shipped. On every boot, the installer:

- Installs fresh if the target is missing.
- Re-hashes on-disk content (ignoring dotfiles) and compares to the
  sidecar. If they match, the user hasn't touched the skill — so when a
  newer binary ships an updated bundle, the installer wipes the old
  tree and lays down the new one (files removed in the new bundle are
  cleaned up too).
- If the on-disk hash diverges from the sidecar, the user customized
  the skill: the installer leaves it alone.
- For installs predating this mechanism (no sidecar), the installer
  silently adopts a sidecar **only** when the on-disk content already
  matches the current bundle. Otherwise it skips conservatively (can't
  tell user-edited apart from older-bundle-untouched).

Net effect: shipping a fix in a bundled skill reaches users on next
binary boot, unless they've intentionally customized the local copy.

Currently bundled:

- `skill-creator/` — meta-skill for authoring new skills via chat. Required
  for the system prompt's "if no skill matches a multi-step task, scaffold
  one with skill-creator" rule to actually have a fallback. Source of truth
  is `skills/skill-creator/` at the repo root; `make bundle-skills` rsyncs
  it here, and `make build` runs that target before linking. Don't edit
  this copy directly — it'll be overwritten on next build.
- `find-skills/` — wraps the `npx skills` CLI so the agent can search
  skills.sh and install community skills before falling back to ad-hoc
  code. Gated on `npx` (declared via `requires.bins`), so on hosts
  without Node it's filtered out by SkillsLoader rather than surfaced
  and broken. Same source-of-truth + rsync flow as skill-creator.

To add another bundled skill: drop the folder in here (or, better, add it
to `make bundle-skills`) and rebuild.
