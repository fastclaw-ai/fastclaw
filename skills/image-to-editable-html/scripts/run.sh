#!/bin/bash
# Entry point: bootstraps a private Python env on first use, then runs
# pipeline.py with the given arguments. Works on Linux (e.g. an E2B
# sandbox) and macOS. The heavy models run on fal.ai, so the env is small.
set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
HOME_DIR="${IMG2HTML_HOME:-$HOME/.cache/fastclaw/image-to-editable-html}"
VENV="$HOME_DIR/venv"
# Keep Playwright's browser private so installing it never prunes browsers
# other projects rely on.
export PLAYWRIGHT_BROWSERS_PATH="$HOME_DIR/browsers"
if [ ! -f "$VENV/.ready" ] || [ "$DIR/requirements.txt" -nt "$VENV/.ready" ]; then
  echo "[setup] preparing Python env in $VENV (first run takes a minute or two)" >&2
  if command -v uv >/dev/null; then
    [ -x "$VENV/bin/python" ] || uv venv -q --python 3.12 "$VENV"
    PIP=(uv pip install -q --python "$VENV/bin/python")
  else
    [ -x "$VENV/bin/python" ] || python3 -m venv "$VENV"
    PIP=("$VENV/bin/python" -m pip install -q)
  fi
  "${PIP[@]}" -r "$DIR/requirements.txt" >&2
  # --with-deps pulls the system libraries Chromium needs on Linux.
  if [ "$(uname)" = "Linux" ]; then "$VENV/bin/python" -m playwright install --with-deps chromium >&2 || "$VENV/bin/python" -m playwright install chromium >&2
  else "$VENV/bin/python" -m playwright install chromium >&2; fi
  touch "$VENV/.ready"
fi
if [ -z "${FAL_KEY:-}" ] && [ -f "$HOME/.fal_key" ]; then
  FAL_KEY="$(cat "$HOME/.fal_key")"; export FAL_KEY
fi
# `run.sh scene <cmd> <out dir> …` edits an existing design (scene.py);
# anything else goes to the pipeline (run / rebuild).
if [ "${1:-}" = "scene" ]; then shift; exec "$VENV/bin/python" -W ignore "$DIR/scene.py" "$@"; fi
exec "$VENV/bin/python" -W ignore "$DIR/pipeline.py" "$@"
