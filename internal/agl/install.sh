#!/bin/sh
# AgentLink installer: downloads agl and verifies its checksum.
#
# Served by a hub (with its URL and a one-time pairing code filled in), it
# also installs autostart and links this machine to the hub. Downloaded from
# GitHub Releases as is, it only installs agl:
#   curl -fsSL https://github.com/thinkany-ai/agl/releases/latest/download/install.sh | sh
set -eu
umask 077

HUB_URL="${AGL_HUB_URL:-__HUB_URL__}"
CONNECT_ARGS="${AGL_CONNECT_ARGS:-__CONNECT_ARGS__}"
INSTALL_DIR="${AGL_INSTALL_DIR:-$HOME/.agl/bin}"
RELEASES="${AGL_RELEASES_URL:-https://github.com/thinkany-ai/agl/releases/latest/download}"
# Spelled in pieces so a hub filling in the placeholders leaves these alone.
[ "$HUB_URL" = "__HUB""_URL__" ] && HUB_URL=""
[ "$CONNECT_ARGS" = "__CONNECT""_ARGS__" ] && CONNECT_ARGS=""

say() { printf '\033[1m%s\033[0m\n' "$*"; }
fail() { printf '\033[31magl:\033[0m %s\n' "$*" >&2; exit 1; }
# Whether dotted version $1 is newer than $2 ("" is older than anything).
newer() {
  awk -v a="$1" -v b="$2" 'BEGIN { n = split(a, x, "."); m = split(b, y, "."); if (m > n) n = m
    for (i = 1; i <= n; i++) { if (x[i] + 0 > y[i] + 0) exit 0; if (x[i] + 0 < y[i] + 0) exit 1 } exit 1 }'
}

if [ "$(id -u)" = "0" ] && [ "${AGL_ALLOW_ROOT:-}" != "1" ]; then
  fail "do not run this installer with sudo; agents run as your user (AGL_ALLOW_ROOT=1 overrides)"
fi
HUB_URL="${HUB_URL%/}"
case "$HUB_URL" in
  ""|https://*) ;;
  http://localhost|http://localhost[:/]*|http://127.0.0.1|http://127.0.0.1[:/]*|http://\[::1\]|http://\[::1\][:/]*) ;;
  *) [ "${AGL_ALLOW_HTTP:-}" = "1" ] || fail "the hub address must use HTTPS: $HUB_URL (AGL_ALLOW_HTTP=1 overrides)" ;;
esac
command -v curl >/dev/null 2>&1 || fail "curl is required"

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "unsupported OS: $(uname -s)" ;;
esac
case "$(uname -m)" in
  arm64|aarch64) arch=arm64 ;;
  x86_64|amd64) arch=x64 ;;
  *) fail "unsupported CPU: $(uname -m)" ;;
esac
target="$os-$arch"

say "Installing agl ($target) into $INSTALL_DIR"
mkdir -p "$INSTALL_DIR"
tmp="$(mktemp "$INSTALL_DIR/.agl.XXXXXX")"
trap 'rm -f "$tmp"' EXIT INT TERM
if [ -n "${AGL_DOWNLOAD_URL:-}" ]; then url="$AGL_DOWNLOAD_URL"
elif [ -n "$HUB_URL" ]; then url="$HUB_URL/download/$target"
else url="$RELEASES/agl-$target"; fi
curl -fL --retry 3 --connect-timeout 20 --progress-bar "$url" -o "$tmp" || fail "download failed: $url"

# Verify the checksum the hub publishes next to the binary.
want="$(curl -fsSL --retry 3 --connect-timeout 20 "$url.sha256" 2>/dev/null | awk '{print $1}')" || want=""
if [ -n "$want" ]; then
  if command -v shasum >/dev/null 2>&1; then got="$(shasum -a 256 "$tmp" | awk '{print $1}')"
  else got="$(sha256sum "$tmp" | awk '{print $1}')"; fi
  [ "$got" = "$want" ] || fail "checksum mismatch for $url"
elif [ -z "${AGL_DOWNLOAD_URL:-}" ]; then
  fail "no checksum published for $url"
fi
chmod 755 "$tmp"
got_version="$("$tmp" --version 2>/dev/null | awk '{print $2}')"
[ -n "$got_version" ] || fail "the downloaded binary does not run on this machine"
# Several web apps can install agl on one machine, each from its own hub; one
# serving an older release must not take the others back (AGL_FORCE=1 does).
have_version="$("$INSTALL_DIR/agl" --version 2>/dev/null | awk '{print $2}')" || have_version=""
if [ -z "${AGL_FORCE:-}" ] && newer "$have_version" "$got_version"; then
  say "Keeping agl $have_version (newer than $got_version from $url)"
  rm -f "$tmp"
else
  mv -f "$tmp" "$INSTALL_DIR/agl"
fi
trap - EXIT INT TERM
if [ "$os" = "darwin" ]; then
  xattr -d com.apple.quarantine "$INSTALL_DIR/agl" 2>/dev/null || true
fi

# Make `agl` available in new shells (AGL_NO_MODIFY_PATH=1 to skip).
[ -n "${AGL_NO_MODIFY_PATH:-}" ] || case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    line="export PATH=\"$INSTALL_DIR:\$PATH\""
    for rc in "$HOME/.zshrc" "$HOME/.bashrc" "$HOME/.profile"; do
      if [ -f "$rc" ] && ! grep -qs "$INSTALL_DIR" "$rc"; then
        printf '\n# agl\n%s\n' "$line" >> "$rc"
      fi
    done
    ;;
esac

"$INSTALL_DIR/agl" agents || true

# One daemon serves every web app linked from this machine: restart it only to
# run a new binary, and not while it runs a turn, which a restart would end.
status_json="$("$INSTALL_DIR/agl" status --json 2>/dev/null)" || status_json=""
field() { printf '%s\n' "$status_json" | sed -n "s/^  \"$1\": \"\{0,1\}\([^\",]*\)\"\{0,1\},\{0,1\}\$/\1/p" | head -n 1; }
start_daemon() {
  # Autostart, so a linked daemon survives logout/reboot; fall back to a
  # background daemon where launchd/systemd --user is unavailable.
  if [ -n "${AGL_NO_SERVICE:-}" ] || ! "$INSTALL_DIR/agl" service install; then
    "$INSTALL_DIR/agl" start
  fi
}
running="$(field running)"
if [ "$running" = "true" ] && [ "$(field version)" != "$(field daemon_version)" ]; then
  busy="$(field busy)"
  case "$busy" in ""|null) busy=0 ;; esac
  if [ "$busy" -gt 0 ]; then
    say "The running agl $(field daemon_version) has $busy turn(s) in progress; it moves to $(field version) on its next restart"
    echo "  agl service install   # restart now (ends running turns)"
  else
    "$INSTALL_DIR/agl" stop >/dev/null 2>&1 || true
    start_daemon
    running="restarted"
  fi
fi

if [ -z "$HUB_URL" ]; then
  say "agl installed: $INSTALL_DIR/agl"
  echo "  agl run claude \"hello\"                     # the daemon starts on first use"
  echo "  agl connect <hub url> --code <pairing code>  # link this machine to a web app"
  exit 0
fi

say "Linking this machine to $HUB_URL"
# A daemon already running keeps running (other web apps may be using it).
[ "$running" = "true" ] || [ "$running" = "restarted" ] || start_daemon
# shellcheck disable=SC2086
"$INSTALL_DIR/agl" connect "$HUB_URL" $CONNECT_ARGS

say "Done. This machine is now available at $HUB_URL"
echo "  agl status        # daemon + hub link"
echo "  agl run claude \"hello\""
if [ -z "${AGL_NO_OPEN:-}" ]; then
  if command -v open >/dev/null 2>&1; then
    open "$HUB_URL/" >/dev/null 2>&1 || true
  elif command -v xdg-open >/dev/null 2>&1 && [ -n "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ]; then
    xdg-open "$HUB_URL/" >/dev/null 2>&1 || true
  fi
fi
