#!/bin/sh
# FastClaw Installer
# Usage: curl -fsSL https://raw.githubusercontent.com/fastclaw-ai/fastclaw/main/install.sh | sh
# Or:    FASTCLAW_INSTALL_DIR=~/bin curl -fsSL ... | sh
set -e

REPO="fastclaw-ai/fastclaw"
BINARY="fastclaw"

# Colors (only if terminal supports it)
if [ -t 1 ]; then
  RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BOLD='\033[1m'; NC='\033[0m'
else
  RED=''; GREEN=''; YELLOW=''; BOLD=''; NC=''
fi

info()    { printf "${GREEN}[INFO]${NC} %s\n" "$*"; }
warn()    { printf "${YELLOW}[WARN]${NC} %s\n" "$*"; }
error()   { printf "${RED}[ERROR]${NC} %s\n" "$*" >&2; exit 1; }
success() { printf "${GREEN}[✓]${NC} %s\n" "$*"; }

# ── Detect OS & arch ────────────────────────────────────────────────────────
detect_platform() {
  _os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  _arch="$(uname -m)"

  case "$_os" in
    linux)  OS="linux" ;;
    darwin) OS="darwin" ;;
    msys*|mingw*|cygwin*) OS="windows" ;;
    *) error "Unsupported OS: $_os" ;;
  esac

  case "$_arch" in
    x86_64|amd64)  ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) error "Unsupported architecture: $_arch" ;;
  esac

  PLATFORM="${OS}_${ARCH}"
  EXT="tar.gz"
  [ "$OS" = "windows" ] && EXT="zip"
}

# ── Decide install dir (no sudo, no password) ───────────────────────────────
choose_install_dir() {
  if [ -n "${FASTCLAW_INSTALL_DIR:-}" ]; then
    INSTALL_DIR="$FASTCLAW_INSTALL_DIR"
  else
    INSTALL_DIR="$HOME/.local/bin"
  fi
  mkdir -p "$INSTALL_DIR"
}

# ── Ensure install dir is in PATH and shell config ──────────────────────────
ensure_path() {
  # Check if already in PATH
  case ":$PATH:" in
    *":$INSTALL_DIR:"*) return 0 ;;
  esac

  # Detect shell config file
  _shell="$(basename "${SHELL:-sh}")"
  case "$_shell" in
    zsh)  RC="$HOME/.zshrc" ;;
    bash) RC="${HOME}/.bashrc" ;;
    fish) RC="$HOME/.config/fish/config.fish" ;;
    *)    RC="$HOME/.profile" ;;
  esac

  # Check if already written
  if [ -f "$RC" ] && grep -q "$INSTALL_DIR" "$RC" 2>/dev/null; then
    return 0
  fi

  # Write PATH export
  if [ "$_shell" = "fish" ]; then
    mkdir -p "$(dirname "$RC")"
    printf '\n# FastClaw\nfish_add_path "%s"\n' "$INSTALL_DIR" >> "$RC"
  else
    printf '\n# FastClaw\nexport PATH="%s:$PATH"\n' "$INSTALL_DIR" >> "$RC"
  fi

  NEEDS_SOURCE=1
  SHELL_RC="$RC"
}

# ── Fetch latest version ─────────────────────────────────────────────────────
get_latest_version() {
  if command -v curl >/dev/null 2>&1; then
    _resp=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest")
  elif command -v wget >/dev/null 2>&1; then
    _resp=$(wget -qO- "https://api.github.com/repos/${REPO}/releases/latest")
  else
    error "curl or wget is required."
  fi
  VERSION=$(printf '%s' "$_resp" | grep '"tag_name"' | head -1 | sed -E 's/.*"([^"]+)".*/\1/')
  [ -n "$VERSION" ] || error "Could not fetch latest version. Check https://github.com/${REPO}/releases"
}

# ── Download & install binary ────────────────────────────────────────────────
install_binary() {
  TARBALL="${BINARY}_${PLATFORM}.${EXT}"
  URL="https://github.com/${REPO}/releases/download/${VERSION}/${TARBALL}"
  TMP_DIR="$(mktemp -d)"

  info "Downloading ${BINARY} ${VERSION} (${PLATFORM})..."

  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$URL" -o "${TMP_DIR}/${TARBALL}" || error "Download failed: $URL"
  else
    wget -q "$URL" -O "${TMP_DIR}/${TARBALL}" || error "Download failed: $URL"
  fi

  info "Extracting..."
  if [ "$EXT" = "zip" ]; then
    unzip -q "${TMP_DIR}/${TARBALL}" -d "${TMP_DIR}"
  else
    tar -xzf "${TMP_DIR}/${TARBALL}" -C "${TMP_DIR}"
  fi

  # Atomic replace: backup old → move new → remove backup
  DEST="${INSTALL_DIR}/${BINARY}"
  if [ -f "$DEST" ]; then
    mv "$DEST" "${DEST}.bak"
  fi
  mv "${TMP_DIR}/${BINARY}" "$DEST"
  chmod +x "$DEST"
  rm -f "${DEST}.bak"

  rm -rf "$TMP_DIR"
}

# ── Main ─────────────────────────────────────────────────────────────────────
main() {
  printf "\n${BOLD}  ⚡ FastClaw Installer${NC}\n"
  printf "  ─────────────────────\n\n"

  detect_platform
  info "Platform: ${PLATFORM}"

  choose_install_dir
  info "Install dir: ${INSTALL_DIR}"

  get_latest_version
  info "Version: ${VERSION}"

  install_binary
  ensure_path

  printf "\n"
  success "FastClaw ${VERSION} installed → ${INSTALL_DIR}/${BINARY}"
  printf "\n"

  if [ "${NEEDS_SOURCE:-0}" = "1" ]; then
    printf "  ${YELLOW}Run this to activate:${NC}\n"
    printf "    source %s\n\n" "$SHELL_RC"
    printf "  Or open a new terminal, then run: ${BOLD}fastclaw${NC}\n"
  else
    printf "  Run: ${BOLD}fastclaw${NC}\n"
    printf "  Opens the setup wizard in your browser.\n"
  fi
  printf "\n"
}

main "$@"
