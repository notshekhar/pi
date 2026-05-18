#!/usr/bin/env bash
# pi installer
#   curl -fsSL https://raw.githubusercontent.com/notshekhar/pi/main/install.sh | bash
#
# Downloads the prebuilt release tarball, runs `npm ci --omit=dev` for runtime
# deps, atomically swaps into $PI_HOME, and links `pi` + `agent` globally.
# Source build is the explicit fallback (set PI_FROM_SOURCE=1).
#
# Requires: node ≥ 20, npm, curl, tar. Errors loudly with install hints when
# any of these are missing.
#
# Env knobs:
#   PI_REPO_SLUG    notshekhar/pi             override repo
#   PI_VERSION      vX.Y.Z                    pin a specific tag
#   PI_HOME         $HOME/.pi-src             install dir
#   PI_FORCE        1                         skip "already up to date" gate
#   PI_FROM_SOURCE  1                         git clone + build instead

set -euo pipefail

REPO_SLUG="${PI_REPO_SLUG:-notshekhar/pi}"
REPO="${PI_REPO:-https://github.com/${REPO_SLUG}.git}"
REF="${PI_REF:-main}"
DEST="${PI_HOME:-$HOME/.pi-src}"
FORCE="${PI_FORCE:-0}"
FROM_SOURCE="${PI_FROM_SOURCE:-0}"
PIN_VERSION="${PI_VERSION:-}"

bold() { printf "\033[1m%s\033[0m\n" "$*"; }
dim()  { printf "\033[2m%s\033[0m\n" "$*"; }
err()  { printf "\033[31m%s\033[0m\n" "$*" >&2; }

# ── Prerequisite checks (clear messages, not raw "command not found") ──────
need_tool() {
  local cmd="$1" hint="$2"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    err "Missing required tool: $cmd"
    err "  → $hint"
    exit 1
  fi
}
ensure_node() {
  if ! command -v node >/dev/null 2>&1; then
    err "Missing required tool: node"
    err
    err "Install Node.js ≥ 20 first, then re-run this installer:"
    err "  macOS:        brew install node       (or use nvm)"
    err "  Linux:        sudo apt install nodejs npm   (or nvm.sh)"
    err "  Cross-plat:   https://nodejs.org/  /  https://github.com/nvm-sh/nvm"
    err
    err "Verify with:  node -v   (must be 20.0.0 or newer)"
    exit 1
  fi
  local major
  major="$(node -p 'process.versions.node.split(".")[0]')"
  if [ "$major" -lt 20 ] 2>/dev/null; then
    err "node version too old: $(node -v) — pi requires ≥ 20.0.0"
    exit 1
  fi
}

bold "▶ pi installer"
ensure_node
need_tool npm "Comes with Node.js; reinstall Node from nodejs.org if missing."
need_tool curl "macOS: preinstalled. Linux: sudo apt install curl"
need_tool tar  "Standard on macOS/Linux."

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

# ── Resolve target version ─────────────────────────────────────────────────
LATEST_VERSION="${PIN_VERSION}"
if [ -z "$LATEST_VERSION" ]; then
  LATEST_VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO_SLUG}/releases/latest" 2>/dev/null \
    | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\(v\{0,1\}[0-9][^"]*\)".*/\1/p' \
    | head -n1 || true)"
fi
if [ -z "$LATEST_VERSION" ] && [ "$FROM_SOURCE" != "1" ]; then
  err "Could not resolve latest release tag from $REPO_SLUG."
  err "Set PI_VERSION=vX.Y.Z to pin, or PI_FROM_SOURCE=1 to build from main."
  exit 1
fi
# Normalize: always have leading `v` (matches asset names `pi-vX.Y.Z.tar.gz`).
case "${LATEST_VERSION:-}" in v*|"") ;; *) LATEST_VERSION="v$LATEST_VERSION" ;; esac

# ── Detect already-installed version (look in both legacy layouts) ─────────
INSTALLED_VERSION=""
for candidate in \
  "$DEST/packages/cli/package.json" \
  "$HOME/.pi/pi-bin/package.json"
do
  if [ -f "$candidate" ]; then
    INSTALLED_VERSION="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$candidate" | head -n1 || true)"
    [ -n "$INSTALLED_VERSION" ] && break
  fi
done

if [ "$FORCE" != "1" ] && [ -n "$LATEST_VERSION" ] && [ -n "$INSTALLED_VERSION" ]; then
  if ! ver_gt "${LATEST_VERSION#v}" "${INSTALLED_VERSION#v}"; then
    bold "✓ Up to date (installed $INSTALLED_VERSION, latest $LATEST_VERSION)"
    dim "Set PI_FORCE=1 to reinstall."
    exit 0
  fi
  dim "  update: $INSTALLED_VERSION → $LATEST_VERSION"
else
  dim "  installing $LATEST_VERSION"
fi

# ── Stage scratch ──────────────────────────────────────────────────────────
SCRATCH="${DEST}.new.$$"
trap 'rm -rf "$SCRATCH" 2>/dev/null || true' EXIT

# ── Path A: download release tarball ───────────────────────────────────────
install_from_release() {
  [ "$FROM_SOURCE" = "1" ] && return 1
  [ -n "$LATEST_VERSION" ] || return 1

  mkdir -p "$SCRATCH"
  local base tar sum url
  base="https://github.com/${REPO_SLUG}/releases/download/${LATEST_VERSION}"
  # Asset naming convention: pi-vX.Y.Z.tar.gz (single tarball, all platforms).
  url="${base}/pi-${LATEST_VERSION}.tar.gz"
  tar="$SCRATCH/pi.tar.gz"
  sum="$SCRATCH/pi.tar.gz.sha256"

  bold "▶ Downloading release ${LATEST_VERSION}"
  if ! curl -fL --progress-bar "$url" -o "$tar"; then
    err "download failed: $url"
    return 1
  fi
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

  if [ ! -f "$SCRATCH/packages/cli/dist/cli.js" ]; then
    err "release tarball missing packages/cli/dist/cli.js"
    return 1
  fi

  bold "▶ Installing runtime deps (npm ci --omit=dev)"
  (cd "$SCRATCH" && npm ci --omit=dev --silent --no-audit --no-fund)
  return 0
}

# ── Path B: git clone + build from source ──────────────────────────────────
install_from_source() {
  need_tool git "Install Git first: https://git-scm.com/downloads"
  mkdir -p "$(dirname "$SCRATCH")"
  bold "▶ Cloning $REPO ($REF)"
  git clone --depth=1 --branch "$REF" "$REPO" "$SCRATCH" 2>/dev/null \
    || git clone --depth=1 "$REPO" "$SCRATCH"

  (
    cd "$SCRATCH"
    bold "▶ Installing dependencies"
    npm install --silent --no-audit --no-fund
    bold "▶ Building"
    npm run build --silent
  )

  if [ ! -f "$SCRATCH/packages/cli/dist/cli.js" ]; then
    err "build did not produce packages/cli/dist/cli.js"
    return 1
  fi
}

if install_from_release; then
  dim "  installed from release tarball"
else
  if [ "$FROM_SOURCE" != "1" ]; then
    dim "  release path failed — falling back to source build"
  fi
  rm -rf "$SCRATCH"
  install_from_source
fi

# ── Atomic swap ────────────────────────────────────────────────────────────
bold "▶ Swapping into place: $DEST"
mkdir -p "$(dirname "$DEST")"
BACKUP=""
if [ -e "$DEST" ]; then
  BACKUP="${DEST}.old.$$"
  mv "$DEST" "$BACKUP"
fi
mv "$SCRATCH" "$DEST"
trap - EXIT
[ -n "$BACKUP" ] && rm -rf "$BACKUP" 2>/dev/null || true

# ── Kill shadowing shims from previous installer styles ────────────────────
# Older bun-compiled installs dropped binaries here.
for stale in "$HOME/.local/bin/pi" "$HOME/.local/bin/agent" "$HOME/.pi/pi-bin"; do
  [ -e "$stale" ] && rm -rf "$stale" 2>/dev/null && dim "  removed legacy shim: $stale" || true
done

# ── Global link via npm ────────────────────────────────────────────────────
bold "▶ Linking pi + agent globally"
# Clear any prior pi/agent binaries: previous installs (npm i -g @notshekhar/pi,
# pi-mono, or older curl runs) leave files in npm's global bin that block
# `npm link` with EEXIST. Unlink known package names + force-remove bin files.
for pkg in @notshekhar/pi pi agent pi-coding-agent @earendil-works/pi-coding-agent; do
  npm uninstall -g "$pkg" --silent --no-audit --no-fund 2>/dev/null || true
done
NPM_PREFIX="$(npm prefix -g 2>/dev/null || true)"
if [ -n "$NPM_PREFIX" ]; then
  for bin in "$NPM_PREFIX/bin/pi" "$NPM_PREFIX/bin/agent"; do
    if [ -e "$bin" ] || [ -L "$bin" ]; then
      rm -f "$bin" && dim "  removed stale bin: $bin" || true
    fi
  done
fi
(cd "$DEST/packages/cli" && npm link --silent --no-audit --no-fund)

# Hash-cache flush so the current shell picks up new binaries immediately.
hash -r 2>/dev/null || true

new_pi="$(command -v pi 2>/dev/null || true)"
new_agent="$(command -v agent 2>/dev/null || true)"
if [ -z "$new_pi" ] && [ -z "$new_agent" ]; then
  err "Neither pi nor agent found on PATH after linking."
  err "Check your npm global bin path:  npm bin -g"
  err "Add it to your shell rc (PATH=\"\$(npm bin -g):\$PATH\")."
  exit 1
fi

# Verify the resolved binary actually reports the new version.
if [ -n "$new_pi" ]; then
  reported="$($new_pi --version 2>/dev/null || true)"
  if [ -n "$reported" ] && [ -n "${LATEST_VERSION:-}" ] && [ "v${reported#v}" != "$LATEST_VERSION" ]; then
    err "warning: \`pi --version\` reports $reported but installer expected $LATEST_VERSION."
    err "  Something else on PATH may be shadowing this install. Try a fresh shell, or:"
    err "    rm \"$(command -v pi)\" \"\$(command -v agent 2>/dev/null)\" && curl ... | bash"
  fi
fi

bold "✓ Installed ${LATEST_VERSION:-from source}"
[ -n "$new_pi" ]    && echo "  pi:    $new_pi"
[ -n "$new_agent" ] && echo "  agent: $new_agent"
echo "  source: $DEST"
echo
dim "Run \`pi\` or \`agent\` to start. Run \`pi login\` to add a provider."
