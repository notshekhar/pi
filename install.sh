#!/usr/bin/env bash
# pi installer — atomic
#   curl -fsSL https://raw.githubusercontent.com/notshekhar/agent/main/install.sh | bash
#
# Default path: download prebuilt release tarball, verify sha256, npm ci for
# runtime deps, atomic swap, npm link. Fallback: git clone + build from source
# when no release exists or PI_FROM_SOURCE=1.
#
# Strategy: all work lands in a SCRATCH dir adjacent to DEST so the final mv
# stays on the same filesystem (atomic on POSIX). ^C before swap leaves the
# existing install untouched.

set -euo pipefail

REPO="${PI_REPO:-https://github.com/notshekhar/agent.git}"
REPO_SLUG="${PI_REPO_SLUG:-notshekhar/agent}"
REF="${PI_REF:-main}"
DEST="${PI_HOME:-$HOME/.pi-src}"
FORCE="${PI_FORCE:-0}"
FROM_SOURCE="${PI_FROM_SOURCE:-0}"
PIN_VERSION="${PI_VERSION:-}"  # explicit tag override, e.g. v0.2.1

bold() { printf "\033[1m%s\033[0m\n" "$*"; }
dim()  { printf "\033[2m%s\033[0m\n" "$*"; }
err()  { printf "\033[31m%s\033[0m\n" "$*" >&2; }

need() {
  command -v "$1" >/dev/null 2>&1 || { err "missing required tool: $1"; exit 1; }
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    err "missing sha256sum/shasum"; return 1
  fi
}

ver_gt() {
  # ver_gt A B → returns 0 when A > B (semver, no pre-releases)
  local a="${1#v}" b="${2#v}"
  [ "$a" = "$b" ] && return 1
  local top
  top="$(printf '%s\n%s\n' "$a" "$b" | sort -V | head -n1)"
  [ "$top" = "$b" ] && return 0
  return 1
}

bold "▶ pi installer (atomic)"
need node
need npm
need curl

# ── Resolve target version ─────────────────────────────────────────────────
LATEST_VERSION="${PIN_VERSION}"
if [ -z "$LATEST_VERSION" ]; then
  LATEST_VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO_SLUG}/releases/latest" 2>/dev/null \
    | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\(v\{0,1\}[0-9][^"]*\)".*/\1/p' \
    | head -n1 || true)"
fi

INSTALLED_VERSION=""
if [ -f "$DEST/packages/cli/package.json" ]; then
  INSTALLED_VERSION="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$DEST/packages/cli/package.json" | head -n1 || true)"
fi

# ── Version gate: install only if latest > installed ───────────────────────
if [ "$FORCE" != "1" ] && [ -n "$LATEST_VERSION" ] && [ -n "$INSTALLED_VERSION" ]; then
  if ! ver_gt "$LATEST_VERSION" "$INSTALLED_VERSION"; then
    bold "✓ Up to date (installed $INSTALLED_VERSION, latest ${LATEST_VERSION})"
    dim "Set PI_FORCE=1 to reinstall."
    exit 0
  fi
  dim "Update: $INSTALLED_VERSION → $LATEST_VERSION"
fi

# ── Stage scratch ──────────────────────────────────────────────────────────
SCRATCH="${DEST}.new.$$"
trap 'rm -rf "$SCRATCH" 2>/dev/null || true' EXIT

# ── Path A: download release tarball ───────────────────────────────────────
install_from_release() {
  [ "$FROM_SOURCE" = "1" ] && return 1
  [ -n "$LATEST_VERSION" ] || return 1

  local base url tar sum
  base="https://github.com/${REPO_SLUG}/releases/download/${LATEST_VERSION}"
  url="${base}/pi-${LATEST_VERSION}.tar.gz"
  tar="$SCRATCH/pi.tar.gz"
  sum="$SCRATCH/pi.tar.gz.sha256"

  mkdir -p "$SCRATCH"
  bold "▶ Downloading release ${LATEST_VERSION}"
  curl -fL --progress-bar "$url" -o "$tar" || return 1
  curl -fsSL "${url}.sha256" -o "$sum" 2>/dev/null || true

  if [ -s "$sum" ]; then
    local expected got
    expected="$(awk '{print $1}' "$sum")"
    got="$(sha256_of "$tar")"
    if [ "$expected" != "$got" ]; then
      err "sha256 mismatch (expected $expected, got $got)"
      return 1
    fi
    dim "  sha256 ok"
  else
    dim "  sha256 file missing — skipping verify"
  fi

  bold "▶ Extracting"
  tar -xzf "$tar" -C "$SCRATCH"
  rm -f "$tar" "$sum"

  bold "▶ Installing runtime deps"
  (cd "$SCRATCH" && npm ci --omit=dev --silent)

  if [ ! -f "$SCRATCH/packages/cli/dist/cli.js" ]; then
    err "release tarball missing packages/cli/dist/cli.js"
    return 1
  fi
  return 0
}

# ── Path B: git clone + build from source ──────────────────────────────────
install_from_source() {
  need git
  REFERENCE_ARGS=()
  if [ -d "$DEST/.git" ]; then
    REFERENCE_ARGS=(--reference-if-able "$DEST" --dissociate)
  fi

  bold "▶ Cloning $REPO ($REF)"
  git clone --depth=1 --branch "$REF" ${REFERENCE_ARGS[@]+"${REFERENCE_ARGS[@]}"} "$REPO" "$SCRATCH" 2>/dev/null \
    || git clone --depth=1 "$REPO" "$SCRATCH"

  cd "$SCRATCH"
  bold "▶ Installing dependencies"
  npm install --silent
  bold "▶ Building"
  npm run gen:catalog --silent
  npm -w @pi/core run build --silent
  npm -w @pi/cli run build --silent
  cd - >/dev/null

  if [ ! -f "$SCRATCH/packages/cli/dist/cli.js" ]; then
    err "build did not produce packages/cli/dist/cli.js"
    return 1
  fi
}

if install_from_release; then
  dim "  installed from release tarball"
else
  if [ "$FROM_SOURCE" != "1" ]; then
    dim "  release path unavailable — falling back to source build"
  fi
  rm -rf "$SCRATCH"
  install_from_source
fi

# ── Atomic swap ────────────────────────────────────────────────────────────
bold "▶ Swapping into place: $DEST"
BACKUP=""
if [ -e "$DEST" ]; then
  BACKUP="${DEST}.old.$$"
  mv "$DEST" "$BACKUP"
fi
mv "$SCRATCH" "$DEST"
trap - EXIT
[ -n "$BACKUP" ] && rm -rf "$BACKUP" 2>/dev/null || true

# ── Global link ────────────────────────────────────────────────────────────
bold "▶ Linking pi + agent globally"
cd "$DEST/packages/cli"
npm link --silent

new_pi="$(command -v pi 2>/dev/null || true)"
new_agent="$(command -v agent 2>/dev/null || true)"
if [ -z "$new_pi" ] && [ -z "$new_agent" ]; then
  err "Neither pi nor agent found on PATH after linking. Check your npm global bin path."
  exit 1
fi

bold "✓ Installed${LATEST_VERSION:+ $LATEST_VERSION}"
[ -n "$new_pi" ]    && echo "  pi:    $new_pi"
[ -n "$new_agent" ] && echo "  agent: $new_agent"
echo "  source: $DEST"
echo
dim "Run \`pi\` or \`agent\` to start. Run \`pi login\` to add a provider."
