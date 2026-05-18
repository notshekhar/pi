#!/usr/bin/env bash
# pi installer — atomic
#   curl -fsSL https://raw.githubusercontent.com/notshekhar/agent/main/install.sh | bash
#
# Strategy: clone + install + build into a SCRATCH dir.  Only after every
# step succeeds do we atomically swap it into place and re-link the global
# `pi` / `agent` binaries.  If you ^C anywhere before the swap, the existing
# install is untouched.
set -euo pipefail

REPO="${PI_REPO:-https://github.com/notshekhar/agent.git}"
REPO_SLUG="${PI_REPO_SLUG:-notshekhar/agent}"
REF="${PI_REF:-main}"
DEST="${PI_HOME:-$HOME/.pi-src}"
FORCE="${PI_FORCE:-0}"

bold() { printf "\033[1m%s\033[0m\n" "$*"; }
dim()  { printf "\033[2m%s\033[0m\n" "$*"; }
err()  { printf "\033[31m%s\033[0m\n" "$*" >&2; }

need() {
  command -v "$1" >/dev/null 2>&1 || { err "missing required tool: $1"; exit 1; }
}

bold "▶ pi installer (atomic)"
need git
need node
need npm

# Stage all work in a scratch dir next to the final destination so the final
# `mv` stays on the same filesystem (atomic on POSIX).
SCRATCH="${DEST}.new.$$"
trap 'rm -rf "$SCRATCH" 2>/dev/null || true' EXIT

# Reuse the existing checkout as a git reference when present to speed up clones.
# Use `${arr[@]+...}` so the expansion is safe under `set -u` when the array is empty.
REFERENCE_ARGS=()
if [ -d "$DEST/.git" ]; then
  REFERENCE_ARGS=(--reference-if-able "$DEST" --dissociate)
fi

# Version gate: skip work when installed version is >= latest tag.
# Compare `vX.Y.Z` from GitHub releases API against the installed CLI package.json.
ver_ge() {
  # ver_ge A B → returns 0 when A >= B (semver, no pre-releases)
  local a="${1#v}" b="${2#v}"
  [ "$a" = "$b" ] && return 0
  local top
  top="$(printf '%s\n%s\n' "$a" "$b" | sort -V | head -n1)"
  [ "$top" = "$b" ] && return 0
  return 1
}

LATEST_VERSION=""
if command -v curl >/dev/null 2>&1; then
  LATEST_VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO_SLUG}/releases/latest" 2>/dev/null \
    | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\(v\{0,1\}[0-9][^"]*\)".*/\1/p' \
    | head -n1 || true)"
fi
INSTALLED_VERSION=""
if [ -f "$DEST/packages/cli/package.json" ]; then
  INSTALLED_VERSION="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$DEST/packages/cli/package.json" | head -n1 || true)"
fi

if [ "$FORCE" != "1" ] && [ -n "$LATEST_VERSION" ] && [ -n "$INSTALLED_VERSION" ]; then
  if ver_ge "$INSTALLED_VERSION" "$LATEST_VERSION"; then
    bold "✓ Up to date (installed $INSTALLED_VERSION ≥ latest $LATEST_VERSION)"
    dim "Set PI_FORCE=1 to reinstall."
    exit 0
  fi
fi

bold "▶ Staging clone → $SCRATCH"
git clone --depth=1 --branch "$REF" ${REFERENCE_ARGS[@]+"${REFERENCE_ARGS[@]}"} "$REPO" "$SCRATCH" 2>/dev/null \
  || git clone --depth=1 "$REPO" "$SCRATCH"

cd "$SCRATCH"

bold "▶ Installing dependencies (scratch)"
npm install --silent

bold "▶ Building (scratch)"
npm run gen:catalog --silent
npm -w @pi/core run build --silent
npm -w @pi/cli run build --silent

# Sanity check: built binary must exist before we swap
if [ ! -f "$SCRATCH/packages/cli/dist/cli.js" ]; then
  err "build did not produce packages/cli/dist/cli.js — aborting"
  exit 1
fi

bold "▶ Swapping into place: $DEST"
# Move the previous install out of the way first (still on disk in case the
# rename fails); only then rename scratch into DEST.  Both renames are
# constant-time and atomic on the same filesystem.
BACKUP=""
if [ -e "$DEST" ]; then
  BACKUP="${DEST}.old.$$"
  mv "$DEST" "$BACKUP"
fi
mv "$SCRATCH" "$DEST"
trap - EXIT
[ -n "$BACKUP" ] && rm -rf "$BACKUP" 2>/dev/null || true

bold "▶ Linking pi + agent globally"
cd "$DEST/packages/cli"
# npm link replaces existing symlinks atomically — no pre-unlink needed.
npm link --silent

# 5. Verify
new_pi="$(command -v pi 2>/dev/null || true)"
new_agent="$(command -v agent 2>/dev/null || true)"
if [ -z "$new_pi" ] && [ -z "$new_agent" ]; then
  err "Neither pi nor agent found on PATH after linking. Check your npm global bin path."
  exit 1
fi

bold "✓ Installed"
[ -n "$new_pi" ]    && echo "  pi:    $new_pi"
[ -n "$new_agent" ] && echo "  agent: $new_agent"
echo "  source: $DEST"
echo
dim "Run \`pi\` or \`agent\` to start. Run \`pi login\` to add a provider."
