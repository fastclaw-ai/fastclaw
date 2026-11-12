# fastclaw-sandbox — runtime image for the agent's exec sandbox.
#
# Why this exists: the default `python:3.12-slim` image only has Python.
# Bundled skills (like camoufox-cli) drive a real browser, so we need
# Python + Node + a browser binary pre-installed so the agent can run
# `camoufox-cli open <url>` on the first turn with zero round-trips.
#
# Base: `node:22-bookworm-slim` — small Debian image with Node already
# wired up. We add Python on top and let camoufox-cli pull its own
# anti-detect Firefox (Camoufox) via `camoufox-cli install --with-deps`.

FROM node:22-bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

# System deps: Python toolchain + the usual fetch/cert essentials +
# CJK/emoji fonts so Firefox can render real glyphs instead of tofu
# (.notdef) on Chinese/Japanese/Korean pages and emoji-laden sites.
# `sudo` is needed because camoufox-cli's `install --with-deps` shells
# out to `sudo apt-get …` to grab Firefox runtime libs even when the
# caller is already root.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        python3 \
        python3-pip \
        python3-venv \
        ca-certificates \
        curl \
        git \
        sudo \
        fonts-noto-cjk \
        fonts-noto-color-emoji \
 && ln -sf /usr/bin/python3 /usr/local/bin/python

# camoufox-cli + uv (provides `uv` and `uvx`) + common Python utilities.
# Debian Bookworm enforces PEP 668 on the system Python;
# `--break-system-packages` is the documented escape hatch and safe
# inside a container.
RUN pip install --break-system-packages --no-cache-dir \
        camoufox-cli \
        uv \
        requests \
        pillow \
        beautifulsoup4 \
        lxml

# Pull Camoufox (anti-detect Firefox) + the system libs it needs to run
# headless. `--with-deps` shells out to apt for the Firefox runtime
# dependencies, so this has to happen before we wipe apt lists below.
# We deliberately let this fail the build if it errors — a silently
# broken browser ships an image that looks fine until the first
# `camoufox-cli open` blows up at runtime, which is worse than a
# build-time failure with an actionable error message.
RUN camoufox-cli install --with-deps

RUN rm -rf /var/lib/apt/lists/*

# Screenshots came out cropped: Camoufox's fingerprint sizes the window
# (e.g. 1728 or 1920 wide) and the page lays out at that width, but
# camoufox-cli opens the page with Playwright's default 1280x720 viewport,
# so `screenshot` captured only the top-left part of the layout. With
# no_viewport the viewport follows the fingerprinted window and the
# screenshot matches what the page rendered. The assert fails the build
# if an upgrade changes that line, rather than silently shipping crops.
RUN python3 - <<'EOF'
import camoufox_cli, os
p = os.path.join(os.path.dirname(camoufox_cli.__file__), "browser.py")
s = open(p).read()
old = "self._page = result.new_page()"
assert s.count(old) == 1, "camoufox-cli browser.py changed; revisit the no_viewport patch"
open(p, "w").write(s.replace(old, "self._page = result.new_page(no_viewport=True)"))
EOF

# Auto-inject --proxy from $HTTPS_PROXY (or its lowercase variants) into
# every camoufox-cli call when the caller didn't pass --proxy explicitly.
# Playwright/Camoufox don't pick up HTTPS_PROXY for the browser process —
# only the --proxy flag works — so without this shim a sandbox in a
# proxy-only network (e.g. behind the GFW) silently fails with
# `NS_ERROR_NET_INTERRUPT` on the first Page.goto, even though curl /
# pip / npm in the same container work fine via the env vars.
#
# Move the pip-installed entry point to camoufox-cli-bin and replace
# /usr/local/bin/camoufox-cli with the shim so PATH-based lookups (the
# only way the agent invokes it) flow through us transparently.
RUN mv /usr/local/bin/camoufox-cli /usr/local/bin/camoufox-cli-bin \
 && printf '%s\n' \
        '#!/bin/sh' \
        'proxy="${HTTPS_PROXY:-${https_proxy:-${HTTP_PROXY:-${http_proxy:-}}}}"' \
        'if [ -n "$proxy" ]; then' \
        '    for a in "$@"; do' \
        '        case "$a" in' \
        '            --proxy|--proxy=*) exec /usr/local/bin/camoufox-cli-bin "$@" ;;' \
        '        esac' \
        '    done' \
        '    exec /usr/local/bin/camoufox-cli-bin --proxy "$proxy" "$@"' \
        'fi' \
        'exec /usr/local/bin/camoufox-cli-bin "$@"' \
    > /usr/local/bin/camoufox-cli \
 && chmod +x /usr/local/bin/camoufox-cli

WORKDIR /workspace
