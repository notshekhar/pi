#!/usr/bin/env bash
# pi installer — binary first, npm/source fallbacks.
#   curl -fsSL https://raw.githubusercontent.com/notshekhar/pi/main/install.sh | bash
#
# Default path: downloads prebuilt binary tarball from GitHub Releases
# (bun --compile output, ~67 MB; zero runtime required, no node, no bun).
#
# Layout after install:
#   $PI_HOME/                          (default: ~/.pi-bin)
#     ├── pi                           (executable; pi-coding-agent reads
#     └── package.json                  alongside via dirname(execPath))
#   $BIN_DIR/pi    → $PI_HOME/pi       (symlink)
#   $BIN_DIR/agent → $PI_HOME/pi       (symlink)
#
# Env knobs:
#   PI_REPO_SLUG    notshekhar/pi     override repo
#   PI_VERSION      vX.Y.Z            pin a specific tag
#   PI_HOME         $HOME/.pi-bin     install dir for binary + package.json
#   PI_BIN_DIR                        symlink dir (auto: /usr/local/bin or
#                                       $HOME/.local/bin)
#   PI_FORCE        1                 skip "already up to date" gate
#   PI_FROM_SOURCE  1                 clone + bun build from source
#                                       (requires bun ≥1.2)

set -euo pipefail

REPO_SLUG="${PI_REPO_SLUG:-notshekhar/pi}"
REPO="${PI_REPO:-https://github.com/${REPO_SLUG}.git}"
REF="${PI_REF:-main}"
PI_HOME="${PI_HOME:-$HOME/.pi-bin}"
FORCE="${PI_FORCE:-0}"
FROM_SOURCE="${PI_FROM_SOURCE:-0}"
PIN_VERSION="${PI_VERSION:-}"

bold() { printf "\033[1m%s\033[0m\n" "$*"; }
dim()  { printf "\033[2m%s\033[0m\n" "$*"; }
err()  { printf "\033[31m%s\033[0m\n" "$*" >&2; }

need_tool() {
  local cmd="$1" hint="$2"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    err "Missing required tool: $cmd"
    err "  → $hint"
    exit 1
  fi
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
  local a="${1#v}" b="${2#v}"
  [ "$a" = "$b" ] && return 1
  local top
  top="$(printf '%s\n%s\n' "$a" "$b" | sort -V | head -n1)"
  [ "$top" = "$b" ] && return 0
  return 1
}

# ── Detect target ─────────────────────────────────────────────────────────
detect_target() {
  local uname_s uname_m os arch
  uname_s="$(uname -s)"
  uname_m="$(uname -m)"
  case "$uname_s" in
    Darwin) os="darwin" ;;
    Linux)  os="linux" ;;
    MINGW*|MSYS*|CYGWIN*)
      err "Detected Git Bash / MSYS on Windows. Use the PowerShell installer instead:"
      err "  irm https://raw.githubusercontent.com/${REPO_SLUG}/main/install.ps1 | iex"
      err "Or from cmd.exe:"
      err "  curl -fsSLo %TEMP%\\pi-install.cmd https://raw.githubusercontent.com/${REPO_SLUG}/main/install.cmd && %TEMP%\\pi-install.cmd"
      exit 1
      ;;
    *)      err "unsupported OS: $uname_s"; exit 1 ;;
  esac
  case "$uname_m" in
    x86_64|amd64)   arch="x64" ;;
    arm64|aarch64)  arch="arm64" ;;
    *)              err "unsupported arch: $uname_m"; exit 1 ;;
  esac
  printf "%s-%s" "$os" "$arch"
}

# ── Resolve bin dir for symlinks ──────────────────────────────────────────
resolve_bin_dir() {
  if [ -n "${PI_BIN_DIR:-}" ]; then
    mkdir -p "$PI_BIN_DIR"
    printf "%s" "$PI_BIN_DIR"
    return
  fi
  for d in /usr/local/bin /opt/homebrew/bin; do
    if [ -w "$d" ] 2>/dev/null; then
      printf "%s" "$d"
      return
    fi
  done
  local fallback="$HOME/.local/bin"
  mkdir -p "$fallback"
  printf "%s" "$fallback"
}

# ── Source build path ─────────────────────────────────────────────────────
install_from_source() {
  bold "▶ pi installer (source build)"
  need_tool git "Install Git first: https://git-scm.com/downloads"
  need_tool bun "Install: curl -fsSL https://bun.sh/install | bash"

  local scratch="${PI_HOME}.src.$$"
  trap 'rm -rf "$scratch" 2>/dev/null || true' EXIT
  bold "▶ Cloning $REPO ($REF)"
  git clone --depth=1 --branch "$REF" "$REPO" "$scratch" 2>/dev/null \
    || git clone --depth=1 "$REPO" "$scratch"
  ( cd "$scratch" && bun install && bun run build && bun packages/cli/build-bin.ts )

  local target
  target="$(detect_target)"
  local stage="$scratch/packages/cli/dist/bin/$target"
  if [ ! -x "$stage/pi" ]; then
    err "source build did not produce $stage/pi"
    exit 1
  fi
  swap_into_place "$stage"
  trap - EXIT
  rm -rf "$scratch" 2>/dev/null || true
  link_globally
  printf "source\n" > "$PI_HOME/.install-method" 2>/dev/null || true
  finish_message "from source"
}

# ── Binary release path ───────────────────────────────────────────────────
install_from_release() {
  bold "▶ pi installer (binary)"
  need_tool curl "macOS: preinstalled. Linux: sudo apt install curl"
  need_tool tar  "Standard on macOS/Linux."

  local target latest installed
  target="$(detect_target)"
  dim "  target: $target"

  latest="${PIN_VERSION}"
  if [ -z "$latest" ]; then
    latest="$(curl -fsSL "https://api.github.com/repos/${REPO_SLUG}/releases/latest" 2>/dev/null \
      | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\(v\{0,1\}[0-9][^"]*\)".*/\1/p' \
      | head -n1 || true)"
  fi
  if [ -z "$latest" ]; then
    err "could not resolve latest release tag from $REPO_SLUG"
    err "set PI_VERSION=vX.Y.Z to pin, or PI_FROM_SOURCE=1 to build from source"
    exit 1
  fi
  case "$latest" in v*) ;; *) latest="v$latest" ;; esac

  installed=""
  if [ -f "$PI_HOME/package.json" ]; then
    installed="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$PI_HOME/package.json" | head -n1 || true)"
  fi
  if [ "$FORCE" != "1" ] && [ -n "$installed" ]; then
    if ! ver_gt "${latest#v}" "${installed#v}"; then
      bold "✓ Up to date (installed $installed, latest $latest)"
      dim "  PI_FORCE=1 to reinstall"
      exit 0
    fi
    dim "  update: $installed → $latest"
  else
    dim "  installing $latest"
  fi

  local scratch tar sum url base
  scratch="${PI_HOME}.new.$$"
  trap 'rm -rf "$scratch" 2>/dev/null || true' EXIT
  mkdir -p "$scratch"

  base="https://github.com/${REPO_SLUG}/releases/download/${latest}"
  url="${base}/pi-${target}.tar.gz"
  tar="$scratch/pi.tar.gz"
  sum="$scratch/pi.tar.gz.sha256"

  bold "▶ Downloading ${url##*/}"
  if ! curl -fL --progress-bar "$url" -o "$tar"; then
    err "download failed: $url"
    err "release may not have $target asset; try PI_FROM_SOURCE=1 to build from source"
    exit 1
  fi
  if curl -fsSL "${url}.sha256" -o "$sum" 2>/dev/null && [ -s "$sum" ]; then
    local expected got
    expected="$(awk '{print $1}' "$sum")"
    got="$(sha256_of "$tar")"
    if [ "$expected" != "$got" ]; then
      err "sha256 mismatch (expected $expected, got $got)"
      exit 1
    fi
    dim "  sha256 ok"
  else
    dim "  sha256 file missing — skipping verify"
  fi

  bold "▶ Extracting"
  tar -xzf "$tar" -C "$scratch"
  if [ ! -x "$scratch/$target/pi" ]; then
    err "tarball missing $target/pi"
    exit 1
  fi

  swap_into_place "$scratch/$target"
  trap - EXIT
  rm -rf "$scratch" 2>/dev/null || true

  link_globally
  printf "binary\n" > "$PI_HOME/.install-method" 2>/dev/null || true
  finish_message "$latest"
}

# ── Atomic swap install dir ───────────────────────────────────────────────
swap_into_place() {
  local src="$1"
  bold "▶ Installing to $PI_HOME"
  mkdir -p "$(dirname "$PI_HOME")"
  local backup=""
  if [ -e "$PI_HOME" ]; then
    backup="${PI_HOME}.old.$$"
    mv "$PI_HOME" "$backup"
  fi
  mv "$src" "$PI_HOME"
  [ -n "$backup" ] && rm -rf "$backup" 2>/dev/null || true
}

# ── Kill stale binaries + symlink fresh ones ──────────────────────────────
link_globally() {
  bold "▶ Linking pi + agent globally"

  # Wipe shims from prior installer styles (bun link, npm i -g, older curl run).
  if command -v npm >/dev/null 2>&1; then
    for p in @notshekhar/pi pi agent pi-coding-agent @earendil-works/pi-coding-agent; do
      npm uninstall -g "$p" --silent --no-audit --no-fund 2>/dev/null || true
    done
    local npm_prefix
    npm_prefix="$(npm prefix -g 2>/dev/null || true)"
    if [ -n "$npm_prefix" ]; then
      for b in "$npm_prefix/bin/pi" "$npm_prefix/bin/agent"; do
        [ -e "$b" ] || [ -L "$b" ] && rm -f "$b" 2>/dev/null && dim "  removed stale: $b" || true
      done
    fi
  fi
  if command -v bun >/dev/null 2>&1; then
    local bun_bin
    bun_bin="$(bun pm -g bin 2>/dev/null || true)"
    if [ -n "$bun_bin" ]; then
      for b in "$bun_bin/pi" "$bun_bin/agent"; do
        [ -e "$b" ] || [ -L "$b" ] && rm -f "$b" 2>/dev/null && dim "  removed stale: $b" || true
      done
    fi
  fi
  for stale in "$HOME/.local/bin/pi" "$HOME/.local/bin/agent" "/usr/local/bin/pi" "/usr/local/bin/agent" "/opt/homebrew/bin/pi" "/opt/homebrew/bin/agent"; do
    if [ -L "$stale" ] || [ -f "$stale" ]; then
      # only remove if it points at us or is not our target, to allow re-symlink
      rm -f "$stale" 2>/dev/null || true
    fi
  done

  local bin_dir
  bin_dir="$(resolve_bin_dir)"
  ln -sf "$PI_HOME/pi" "$bin_dir/pi"
  ln -sf "$PI_HOME/pi" "$bin_dir/agent"
  hash -r 2>/dev/null || true

  case ":$PATH:" in
    *":$bin_dir:"*) ;;
    *)
      err "warning: $bin_dir is not on PATH"
      err "  add to your shell rc: export PATH=\"$bin_dir:\$PATH\""
      ;;
  esac

  PI_LINK_DIR="$bin_dir"
}

finish_message() {
  local label="$1"
  bold "✓ Installed $label"
  echo "  pi:      $PI_LINK_DIR/pi"
  echo "  agent:   $PI_LINK_DIR/agent"
  echo "  target:  $PI_HOME"
  echo
  dim "Run \`pi\` to start. Run \`pi login\` to add a provider."
}

# ── Route ──────────────────────────────────────────────────────────────────
if [ "$FROM_SOURCE" = "1" ]; then
  install_from_source
else
  install_from_release
fi
