#!/usr/bin/env bash
# pi installer — curl pipe friendly
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/notshekhar/pi/main/install.sh | bash
#   curl -fsSL https://raw.githubusercontent.com/notshekhar/pi/main/install.sh | PI_REF=v0.1.0 bash
set -euo pipefail

REPO="${PI_REPO:-https://github.com/notshekhar/pi.git}"
REF="${PI_REF:-main}"
DEST="${PI_HOME:-$HOME/.pi-src}"

bold() { printf "\033[1m%s\033[0m\n" "$*"; }
dim()  { printf "\033[2m%s\033[0m\n" "$*"; }
err()  { printf "\033[31m%s\033[0m\n" "$*" >&2; }

need() {
  command -v "$1" >/dev/null 2>&1 || { err "missing required tool: $1"; exit 1; }
}

bold "▶ pi installer"
need git
need node
need npm

# 1. Clone or update
if [ -d "$DEST/.git" ]; then
  bold "▶ Updating $DEST"
  git -C "$DEST" fetch --depth=1 origin "$REF"
  git -C "$DEST" checkout -q "$REF"
  git -C "$DEST" reset --hard "origin/$REF" 2>/dev/null || true
else
  bold "▶ Cloning to $DEST"
  rm -rf "$DEST"
  git clone --depth=1 --branch "$REF" "$REPO" "$DEST" 2>/dev/null \
    || git clone --depth=1 "$REPO" "$DEST"
fi

# 2. Build + link via in-repo script
cd "$DEST"
bash ./scripts/build-and-link.sh

# 3. Path hint
node_bin="$(npm prefix -g 2>/dev/null)/bin"
if ! echo ":$PATH:" | grep -q ":$node_bin:"; then
  echo
  dim "npm global bin: $node_bin"
  dim "If \`pi\` is not found, add it to PATH:"
  dim "  export PATH=\"$node_bin:\$PATH\""
fi
