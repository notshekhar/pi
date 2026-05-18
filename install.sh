#!/usr/bin/env bash
# pi installer — downloads a prebuilt single binary (bun-compiled). No node,
# npm, or git required.
#   curl -fsSL https://raw.githubusercontent.com/notshekhar/agent/main/install.sh | bash
#
# Layout after install:
#   $PI_BIN_DIR/pi-bin/           bundled binary + assets (theme, export-html, ...)
#   $PI_LINK_DIR/pi               symlink to pi-bin/pi
#
# Env knobs:
#   PI_REPO_SLUG    "notshekhar/agent"           override GitHub repo
#   PI_VERSION      "v0.2.0"                      pin specific tag (else latest release)
#   PI_BIN_DIR      "$HOME/.pi"                   where to extract the bundle
#   PI_LINK_DIR     "$HOME/.local/bin"            where to symlink the binary
#   PI_FORCE        "1"                           skip "already up to date" gate

set -euo pipefail

REPO_SLUG="${PI_REPO_SLUG:-notshekhar/agent}"
PIN_VERSION="${PI_VERSION:-}"
BIN_DIR="${PI_BIN_DIR:-$HOME/.pi}"
LINK_DIR="${PI_LINK_DIR:-$HOME/.local/bin}"
FORCE="${PI_FORCE:-0}"

bold() { printf "\033[1m%s\033[0m\n" "$*"; }
dim()  { printf "\033[2m%s\033[0m\n" "$*"; }
err()  { printf "\033[31m%s\033[0m\n" "$*" >&2; }
need() { command -v "$1" >/dev/null 2>&1 || { err "missing required tool: $1"; exit 1; }; }

bold "▶ pi installer"
need curl
need tar

# ── Detect target ──────────────────────────────────────────────────────────
uname_s="$(uname -s)"
uname_m="$(uname -m)"

case "$uname_s" in
  Darwin)
    os="darwin"
    case "$uname_m" in
      arm64|aarch64) arch="arm64" ;;
      x86_64)        arch="x64" ;;
      *) err "unsupported macOS arch: $uname_m"; exit 1 ;;
    esac
    ;;
  Linux)
    os="linux"
    case "$uname_m" in
      x86_64|amd64)  arch="x64" ;;
      aarch64|arm64) arch="arm64" ;;
      *) err "unsupported Linux arch: $uname_m"; exit 1 ;;
    esac
    ;;
  MINGW*|MSYS*|CYGWIN*)
    os="windows"; arch="x64"
    ;;
  *)
    err "unsupported OS: $uname_s. Use the source build, or open an issue."
    exit 1
    ;;
esac

target="${os}-${arch}"
dim "  target: $target"

# ── Helpers ────────────────────────────────────────────────────────────────
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
  local a="${1#v}" b="${2#v}"
  [ "$a" = "$b" ] && return 1
  local top
  top="$(printf '%s\n%s\n' "$a" "$b" | sort -V | head -n1)"
  [ "$top" = "$b" ] && return 0
  return 1
}

# ── Resolve target version ─────────────────────────────────────────────────
LATEST_VERSION="$PIN_VERSION"
if [ -z "$LATEST_VERSION" ]; then
  LATEST_VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO_SLUG}/releases/latest" 2>/dev/null \
    | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\(v\{0,1\}[0-9][^"]*\)".*/\1/p' \
    | head -n1 || true)"
fi
if [ -z "$LATEST_VERSION" ]; then
  err "could not resolve a release version. Set PI_VERSION=vX.Y.Z to pin one."
  exit 1
fi

INSTALLED_VERSION=""
if [ -x "$BIN_DIR/pi-bin/pi" ]; then
  INSTALLED_VERSION="$("$BIN_DIR/pi-bin/pi" --version 2>/dev/null || true)"
  [ -n "$INSTALLED_VERSION" ] && INSTALLED_VERSION="v${INSTALLED_VERSION#v}"
fi

if [ "$FORCE" != "1" ] && [ -n "$INSTALLED_VERSION" ]; then
  if ! ver_gt "$LATEST_VERSION" "$INSTALLED_VERSION"; then
    bold "✓ Up to date (installed $INSTALLED_VERSION, latest $LATEST_VERSION)"
    dim "Set PI_FORCE=1 to reinstall."
    exit 0
  fi
  dim "  update: $INSTALLED_VERSION → $LATEST_VERSION"
else
  dim "  installing $LATEST_VERSION"
fi

# ── Download tarball ───────────────────────────────────────────────────────
BASE_URL="https://github.com/${REPO_SLUG}/releases/download/${LATEST_VERSION}"
TAR_NAME="pi-${LATEST_VERSION}-${target}.tar.gz"
SUM_NAME="${TAR_NAME}.sha256"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

bold "▶ Downloading $TAR_NAME"
if ! curl -fL --progress-bar "${BASE_URL}/${TAR_NAME}" -o "$TMP_DIR/$TAR_NAME"; then
  err "download failed: ${BASE_URL}/${TAR_NAME}"
  err "no prebuilt binary for ${target} in release ${LATEST_VERSION}?"
  exit 1
fi
curl -fsSL "${BASE_URL}/${SUM_NAME}" -o "$TMP_DIR/$SUM_NAME" 2>/dev/null || true

if [ -s "$TMP_DIR/$SUM_NAME" ]; then
  expected="$(awk '{print $1}' "$TMP_DIR/$SUM_NAME")"
  got="$(sha256_of "$TMP_DIR/$TAR_NAME")"
  if [ "$expected" != "$got" ]; then
    err "sha256 mismatch (expected $expected, got $got)"
    exit 1
  fi
  dim "  sha256 ok"
else
  dim "  sha256 file missing — skipping verify"
fi

# ── Atomic extract + swap ──────────────────────────────────────────────────
STAGE_DIR="$TMP_DIR/stage"
mkdir -p "$STAGE_DIR"
tar -xzf "$TMP_DIR/$TAR_NAME" -C "$STAGE_DIR"

if [ ! -x "$STAGE_DIR/pi" ] && [ ! -f "$STAGE_DIR/pi.exe" ]; then
  err "extracted tarball missing pi binary"
  exit 1
fi
chmod +x "$STAGE_DIR/pi" 2>/dev/null || true

mkdir -p "$BIN_DIR"
BACKUP=""
if [ -e "$BIN_DIR/pi-bin" ]; then
  BACKUP="$BIN_DIR/pi-bin.old.$$"
  mv "$BIN_DIR/pi-bin" "$BACKUP"
fi
mv "$STAGE_DIR" "$BIN_DIR/pi-bin"
[ -n "$BACKUP" ] && rm -rf "$BACKUP" 2>/dev/null || true

# ── Symlink into $LINK_DIR ─────────────────────────────────────────────────
mkdir -p "$LINK_DIR"
exe_name="pi"
[ "$os" = "windows" ] && exe_name="pi.exe"
ln -sfn "$BIN_DIR/pi-bin/$exe_name" "$LINK_DIR/pi"

bold "✓ Installed pi $LATEST_VERSION"
echo "  binary:  $BIN_DIR/pi-bin/$exe_name"
echo "  link:    $LINK_DIR/pi"

case ":$PATH:" in
  *":$LINK_DIR:"*) ;;
  *)
    echo
    dim "$LINK_DIR is not on \$PATH. Add this to your shell rc:"
    dim "  export PATH=\"$LINK_DIR:\$PATH\""
    ;;
esac

echo
dim "Run \`pi\` to start. Run \`pi login\` to add a provider."
