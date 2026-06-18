#!/usr/bin/env bash
# loop installer — binary first, npm/source fallbacks.
#   curl -fsSL https://raw.githubusercontent.com/notshekhar/loop/main/install.sh | bash
#
# Default path: downloads prebuilt binary tarball from GitHub Releases
# (bun --compile output, ~67 MB; zero runtime required, no node, no bun).
#
# Layout after install:
#   $LOOP_HOME/                          (default: ~/.loop-bin)
#     ├── loop                          (executable; reads package.json
#     └── package.json                  alongside via dirname(execPath))
#   $BIN_DIR/loop  → $LOOP_HOME/loop     (symlink)
#   $BIN_DIR/lp    → $LOOP_HOME/loop     (symlink, short alias)
#   $BIN_DIR/agent → $LOOP_HOME/loop     (symlink)
#
# Env knobs:
#   LOOP_REPO_SLUG    notshekhar/loop     override repo
#   LOOP_VERSION      vX.Y.Z            pin a specific tag
#   LOOP_HOME         $HOME/.loop-bin     install dir for binary + package.json
#   LOOP_BIN_DIR                        symlink dir (auto: /usr/local/bin or
#                                       $HOME/.local/bin)
#   LOOP_FORCE        1                 skip "already up to date" gate
#   LOOP_FROM_SOURCE  1                 clone + bun build from source
#                                       (requires bun ≥1.2)
#   LOOP_UNINSTALL    1                 remove the install + symlinks and exit

set -euo pipefail

REPO_SLUG="${LOOP_REPO_SLUG:-notshekhar/loop}"
REPO="${LOOP_REPO:-https://github.com/${REPO_SLUG}.git}"
REF="${LOOP_REF:-main}"
LOOP_HOME="${LOOP_HOME:-$HOME/.loop-bin}"
FORCE="${LOOP_FORCE:-0}"
FROM_SOURCE="${LOOP_FROM_SOURCE:-0}"
UNINSTALL="${LOOP_UNINSTALL:-0}"
PIN_VERSION="${LOOP_VERSION:-}"

# Marker for the `lp` shell-alias block we add as a fallback when the system
# `lp` (CUPS printer) shadows our symlink in PATH. Used for idempotent
# add/update and clean removal on uninstall.
LP_ALIAS_MARKER="# loop: lp alias (overrides system /usr/bin/lp)"
# Set by link_globally(): which command names actually resolve to loop, so the
# install summary only advertises commands that really work on this machine.
LOOP_OK=0
AGENT_OK=0
LP_STATUS="none"   # path | alias | none
LP_ALIAS_RC=""

# Older installs (below this version) kept their config in ~/.pi; migrate once.
MIGRATE_FROM_BELOW="0.5.0"

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

# ── Migrate legacy config dir → ~/.loop (one-time, version-gated) ───────────
# MOVE ~/.pi into ~/.loop (copy, then delete the old dir only once the copy
# succeeds) so config is never lost or duplicated. Runs only for installs below
# MIGRATE_FROM_BELOW (version read from ~/.pi-bin/package.json; unknown counts
# as below the cutoff).
migrate_legacy_config() {
  local legacy="$HOME/.pi" current="$HOME/.loop"
  [ -d "$legacy" ] || return 0          # nothing to migrate
  [ -e "$current" ] && return 0         # already migrated / fresh config present

  local legacy_ver=""
  if [ -f "$HOME/.pi-bin/package.json" ]; then
    legacy_ver="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$HOME/.pi-bin/package.json" | head -n1 || true)"
  fi
  # Skip if a known legacy version is at/above the cutoff (not a pre-rename install).
  if [ -n "$legacy_ver" ] && ! ver_gt "$MIGRATE_FROM_BELOW" "$legacy_ver"; then
    return 0
  fi

  bold "▶ Migrating config $legacy → $current (from ${legacy_ver:-unknown}, below $MIGRATE_FROM_BELOW)"
  if cp -R "$legacy" "$current" 2>/dev/null; then
    rm -rf "$legacy" 2>/dev/null || true
    dim "  moved auth, sessions, settings → $current (removed $legacy)"
  else
    err "  migration failed — your config stays in $legacy"
  fi
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
      err "  curl -fsSLo %TEMP%\\loop-install.cmd https://raw.githubusercontent.com/${REPO_SLUG}/main/install.cmd && %TEMP%\\loop-install.cmd"
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

# Release binaries are glibc builds; Alpine and other musl distros need a
# source build (bun's musl build) or a glibc compat layer.
check_libc() {
  [ "$(uname -s)" = "Linux" ] || return 0
  if [ -f /etc/alpine-release ] || (ldd --version 2>&1 | grep -qi musl); then
    err "musl libc detected (Alpine?). Release binaries are glibc builds."
    err "  options:"
    err "    • apk add gcompat              (glibc compatibility layer)"
    err "    • LOOP_FROM_SOURCE=1 <installer> (build with bun on this machine)"
    exit 1
  fi
}

# ── Resolve latest release tag ────────────────────────────────────────────
# Prefer the releases/latest redirect — it isn't subject to the anonymous
# GitHub API rate limit (60 req/h/IP) that bites CI and shared networks.
# Fall back to the API if redirect parsing fails.
resolve_latest_tag() {
  local final tag
  final="$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
    "https://github.com/${REPO_SLUG}/releases/latest" 2>/dev/null || true)"
  tag="${final##*/}"
  case "$tag" in
    v[0-9]*) printf "%s" "$tag"; return 0 ;;
  esac
  curl -fsSL "https://api.github.com/repos/${REPO_SLUG}/releases/latest" 2>/dev/null \
    | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\(v\{0,1\}[0-9][^"]*\)".*/\1/p' \
    | head -n1 || true
}

# ── Resolve bin dir for symlinks ──────────────────────────────────────────
resolve_bin_dir() {
  if [ -n "${LOOP_BIN_DIR:-}" ]; then
    mkdir -p "$LOOP_BIN_DIR"
    printf "%s" "$LOOP_BIN_DIR"
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

# ── Uninstall ─────────────────────────────────────────────────────────────
uninstall() {
  bold "▶ Uninstalling loop"
  # Remove current (loop, lp, agent) and legacy (pi) symlinks from every known dir.
  for link in "$HOME/.local/bin/loop" "$HOME/.local/bin/lp" "$HOME/.local/bin/agent" "$HOME/.local/bin/pi" \
              "/usr/local/bin/loop" "/usr/local/bin/lp" "/usr/local/bin/agent" "/usr/local/bin/pi" \
              "/opt/homebrew/bin/loop" "/opt/homebrew/bin/lp" "/opt/homebrew/bin/agent" "/opt/homebrew/bin/pi" \
              "${LOOP_BIN_DIR:+$LOOP_BIN_DIR/loop}" "${LOOP_BIN_DIR:+$LOOP_BIN_DIR/lp}" "${LOOP_BIN_DIR:+$LOOP_BIN_DIR/agent}" "${LOOP_BIN_DIR:+$LOOP_BIN_DIR/pi}"; do
    [ -n "$link" ] || continue
    if [ -L "$link" ] || [ -f "$link" ]; then
      rm -f "$link" 2>/dev/null && dim "  removed $link" || true
    fi
  done
  # Strip the lp alias we may have added to a shell rc.
  for rc in "${ZDOTDIR:-$HOME}/.zshrc" "$HOME/.zshrc" "$HOME/.bashrc" "$HOME/.config/fish/config.fish"; do
    [ -f "$rc" ] || continue
    if grep -qF "$LP_ALIAS_MARKER" "$rc" 2>/dev/null; then
      tmp="$(mktemp)" && grep -vF "$LP_ALIAS_MARKER" "$rc" > "$tmp" 2>/dev/null \
        && cat "$tmp" > "$rc" && dim "  removed lp alias from $rc"
      rm -f "$tmp" 2>/dev/null || true
    fi
  done
  rm -rf "$LOOP_HOME" 2>/dev/null && dim "  removed $LOOP_HOME" || true
  bold "✓ Uninstalled. Config in ~/.loop (auth, sessions, settings) was kept;"
  dim  "  remove it with: rm -rf ~/.loop"
}

# ── Source build path ─────────────────────────────────────────────────────
install_from_source() {
  bold "▶ loop installer (source build)"
  need_tool git "Install Git first: https://git-scm.com/downloads"
  need_tool bun "Install: curl -fsSL https://bun.sh/install | bash"

  rm -rf "${LOOP_HOME}".old.* "${LOOP_HOME}".new.* "${LOOP_HOME}".src.* 2>/dev/null || true
  local scratch="${LOOP_HOME}.src.$$"
  trap 'rm -rf "$scratch" 2>/dev/null || true' EXIT
  bold "▶ Cloning $REPO ($REF)"
  git clone --depth=1 --branch "$REF" "$REPO" "$scratch" 2>/dev/null \
    || git clone --depth=1 "$REPO" "$scratch"
  ( cd "$scratch" && bun install && bun run build && bun packages/cli/build-bin.ts )

  local target
  target="$(detect_target)"
  local stage="$scratch/packages/cli/dist/bin/$target"
  if [ ! -x "$stage/loop" ]; then
    err "source build did not produce $stage/loop"
    exit 1
  fi
  swap_into_place "$stage"
  trap - EXIT
  rm -rf "$scratch" 2>/dev/null || true
  link_globally
  printf "source\n" > "$LOOP_HOME/.install-method" 2>/dev/null || true
  smoke_test
  finish_message "from source"
}

# ── Binary release path ───────────────────────────────────────────────────
install_from_release() {
  bold "▶ loop installer (binary)"
  need_tool curl "macOS: preinstalled. Linux: sudo apt install curl"
  need_tool tar  "Standard on macOS/Linux."
  check_libc

  local target latest installed
  target="$(detect_target)"
  dim "  target: $target"

  latest="${PIN_VERSION}"
  if [ -z "$latest" ]; then
    latest="$(resolve_latest_tag)"
  fi
  if [ -z "$latest" ]; then
    err "could not resolve latest release tag from $REPO_SLUG"
    err "set LOOP_VERSION=vX.Y.Z to pin, or LOOP_FROM_SOURCE=1 to build from source"
    exit 1
  fi
  case "$latest" in v*) ;; *) latest="v$latest" ;; esac

  installed=""
  if [ -f "$LOOP_HOME/package.json" ]; then
    installed="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$LOOP_HOME/package.json" | head -n1 || true)"
  fi
  if [ "$FORCE" != "1" ] && [ -n "$installed" ]; then
    if ! ver_gt "${latest#v}" "${installed#v}"; then
      bold "✓ Up to date (installed $installed, latest $latest)"
      dim "  LOOP_FORCE=1 to reinstall"
      exit 0
    fi
    dim "  update: $installed → $latest"
  else
    dim "  installing $latest"
  fi

  local scratch tar sum url base
  # Sweep leftovers from interrupted runs / prior self-updates — before the
  # fresh scratch exists, so the glob can't eat it.
  rm -rf "${LOOP_HOME}".old.* "${LOOP_HOME}".new.* "${LOOP_HOME}".src.* 2>/dev/null || true
  scratch="${LOOP_HOME}.new.$$"
  trap 'rm -rf "$scratch" 2>/dev/null || true' EXIT
  mkdir -p "$scratch"

  base="https://github.com/${REPO_SLUG}/releases/download/${latest}"
  url="${base}/loop-${target}.tar.gz"
  tar="$scratch/loop.tar.gz"
  sum="$scratch/loop.tar.gz.sha256"

  bold "▶ Downloading ${url##*/}"
  if ! curl -fL --progress-bar "$url" -o "$tar"; then
    err "download failed: $url"
    err "release may not have $target asset; try LOOP_FROM_SOURCE=1 to build from source"
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
  if [ ! -x "$scratch/$target/loop" ]; then
    err "tarball missing $target/loop"
    exit 1
  fi

  # Defensive: clear quarantine if anything in the chain set it (Gatekeeper
  # blocks unsigned quarantined binaries with a scary dialog).
  if [ "$(uname -s)" = "Darwin" ] && command -v xattr >/dev/null 2>&1; then
    xattr -dr com.apple.quarantine "$scratch/$target" 2>/dev/null || true
  fi

  swap_into_place "$scratch/$target"
  trap - EXIT
  rm -rf "$scratch" 2>/dev/null || true

  link_globally
  printf "binary\n" > "$LOOP_HOME/.install-method" 2>/dev/null || true
  smoke_test
  finish_message "$latest"
}

# ── Atomic swap install dir ───────────────────────────────────────────────
swap_into_place() {
  local src="$1"
  bold "▶ Installing to $LOOP_HOME"
  mkdir -p "$(dirname "$LOOP_HOME")"
  local backup=""
  if [ -e "$LOOP_HOME" ]; then
    backup="${LOOP_HOME}.old.$$"
    mv "$LOOP_HOME" "$backup"
  fi
  mv "$src" "$LOOP_HOME"
  [ -n "$backup" ] && rm -rf "$backup" 2>/dev/null || true
}

# ── Kill stale binaries + symlink fresh ones ──────────────────────────────
link_globally() {
  bold "▶ Linking loop + lp + agent globally"

  # Wipe shims from prior installer styles (bun link, npm i -g, older curl run).
  # Includes the legacy "pi" package/bin names and the upstream pi-coding-agent.
  if command -v npm >/dev/null 2>&1; then
    for p in @notshekhar/loop @notshekhar/pi loop pi agent pi-coding-agent @earendil-works/pi-coding-agent; do
      npm uninstall -g "$p" --silent --no-audit --no-fund 2>/dev/null || true
    done
    local npm_prefix
    npm_prefix="$(npm prefix -g 2>/dev/null || true)"
    if [ -n "$npm_prefix" ]; then
      for b in "$npm_prefix/bin/loop" "$npm_prefix/bin/lp" "$npm_prefix/bin/pi" "$npm_prefix/bin/agent"; do
        [ -e "$b" ] || [ -L "$b" ] && rm -f "$b" 2>/dev/null && dim "  removed stale: $b" || true
      done
    fi
  fi
  if command -v bun >/dev/null 2>&1; then
    local bun_bin
    bun_bin="$(bun pm -g bin 2>/dev/null || true)"
    if [ -n "$bun_bin" ]; then
      for b in "$bun_bin/loop" "$bun_bin/lp" "$bun_bin/pi" "$bun_bin/agent"; do
        [ -e "$b" ] || [ -L "$b" ] && rm -f "$b" 2>/dev/null && dim "  removed stale: $b" || true
      done
    fi
  fi
  for stale in "$HOME/.local/bin/loop" "$HOME/.local/bin/lp" "$HOME/.local/bin/pi" "$HOME/.local/bin/agent" \
               "/usr/local/bin/loop" "/usr/local/bin/lp" "/usr/local/bin/pi" "/usr/local/bin/agent" \
               "/opt/homebrew/bin/loop" "/opt/homebrew/bin/lp" "/opt/homebrew/bin/pi" "/opt/homebrew/bin/agent"; do
    if [ -L "$stale" ] || [ -f "$stale" ]; then
      # only remove if it points at us or is not our target, to allow re-symlink
      rm -f "$stale" 2>/dev/null || true
    fi
  done

  local bin_dir
  bin_dir="$(resolve_bin_dir)"
  ln -sf "$LOOP_HOME/loop" "$bin_dir/loop"
  ln -sf "$LOOP_HOME/loop" "$bin_dir/lp"
  ln -sf "$LOOP_HOME/loop" "$bin_dir/agent"
  hash -r 2>/dev/null || true

  case ":$PATH:" in
    *":$bin_dir:"*) ;;
    *) path_hint "$bin_dir" ;;
  esac

  LOOP_LINK_DIR="$bin_dir"

  # Work out which command names actually launch loop on THIS machine, so the
  # summary never advertises a command that won't run.
  is_ours()    { [ "$(readlink "$1" 2>/dev/null)" = "$LOOP_HOME/loop" ]; }
  resolves_to_loop() { is_ours "$(command -v "$1" 2>/dev/null)"; }

  resolves_to_loop loop  && LOOP_OK=1
  resolves_to_loop agent && AGENT_OK=1

  # `lp` collides with the system CUPS printer (/usr/bin/lp). Our symlink only
  # wins when bin_dir sits ahead of /usr/bin in PATH. When it doesn't, fall back
  # to a shell alias (resolved before PATH in interactive shells) so `lp` still
  # starts loop. If neither sticks, `lp` is unavailable and won't be shown.
  if resolves_to_loop lp; then
    LP_STATUS="path"
  elif register_lp_alias; then
    LP_STATUS="alias"
  else
    LP_STATUS="none"
  fi
}

# Exact copy-pasteable PATH line for the user's shell.
path_hint() {
  local bin_dir="$1" shell_name rc
  shell_name="$(basename "${SHELL:-bash}")"
  err "warning: $bin_dir is not on PATH"
  case "$shell_name" in
    zsh)  rc="~/.zshrc";  err "  echo 'export PATH=\"$bin_dir:\$PATH\"' >> $rc && source $rc" ;;
    bash) rc="~/.bashrc"; err "  echo 'export PATH=\"$bin_dir:\$PATH\"' >> $rc && source $rc" ;;
    fish) err "  fish_add_path $bin_dir" ;;
    *)    err "  add to your shell rc: export PATH=\"$bin_dir:\$PATH\"" ;;
  esac
}

# rc file for the user's login shell, or non-zero if we don't know how to edit
# it (an unknown shell — we won't guess and clobber something).
rc_file_for_shell() {
  case "$(basename "${SHELL:-}")" in
    zsh)  printf '%s' "${ZDOTDIR:-$HOME}/.zshrc" ;;
    bash) printf '%s' "$HOME/.bashrc" ;;
    fish) printf '%s' "$HOME/.config/fish/config.fish" ;;
    *)    return 1 ;;
  esac
}

# Add (or refresh) the `lp` → loop alias in the user's shell rc, idempotently.
# Returns 0 if the alias is in place afterwards, non-zero if we couldn't write
# it (unknown shell / unwritable rc) — the caller then drops `lp` from the
# summary instead of promising a command that won't work.
register_lp_alias() {
  local rc line
  rc="$(rc_file_for_shell)" || return 1
  if [ "$(basename "${SHELL:-}")" = "fish" ]; then
    line="alias lp '$LOOP_HOME/loop'   $LP_ALIAS_MARKER"
  else
    line="alias lp='$LOOP_HOME/loop'   $LP_ALIAS_MARKER"
  fi
  mkdir -p "$(dirname "$rc")" 2>/dev/null || true
  if [ -f "$rc" ] && grep -qF "$LP_ALIAS_MARKER" "$rc" 2>/dev/null; then
    # Rewrite the existing marked line in case LOOP_HOME moved between installs.
    local tmp; tmp="$(mktemp)" || return 1
    if grep -vF "$LP_ALIAS_MARKER" "$rc" > "$tmp" 2>/dev/null; then
      printf '%s\n' "$line" >> "$tmp" && cat "$tmp" > "$rc"
    fi
    rm -f "$tmp"
  else
    printf '\n%s\n' "$line" >> "$rc" 2>/dev/null || return 1
  fi
  LP_ALIAS_RC="$rc"
  return 0
}

# The binary must actually run on this machine (catches libc/arch surprises
# immediately instead of on first use).
smoke_test() {
  local v
  if ! v="$("$LOOP_HOME/loop" --version 2>&1)"; then
    err "installed binary failed to run: $v"
    err "  try LOOP_FROM_SOURCE=1 to build for this machine"
    exit 1
  fi
  dim "  verified: loop v$v"
}

finish_message() {
  local label="$1"
  bold "✓ Installed $label"
  # Only advertise commands that actually resolve to loop on this machine.
  echo "  loop:    $LOOP_LINK_DIR/loop"
  if [ "$LP_STATUS" = "path" ]; then
    echo "  lp:      $LOOP_LINK_DIR/lp"
  elif [ "$LP_STATUS" = "alias" ]; then
    echo "  lp:      alias → $LOOP_HOME/loop  (added to ${LP_ALIAS_RC/#$HOME/\~}; restart your shell)"
  fi
  [ "$AGENT_OK" = "1" ] && echo "  agent:   $LOOP_LINK_DIR/agent"
  echo "  target:  $LOOP_HOME"
  echo
  if [ "$LP_STATUS" = "none" ]; then
    dim "Run \`loop\` to start. Run \`loop login\` to add a provider."
  else
    dim "Run \`loop\` (or \`lp\`) to start. Run \`loop login\` to add a provider."
  fi
  dim "Update later with \`loop update\` (or /update inside the TUI)."
}

# ── Route ──────────────────────────────────────────────────────────────────
if [ "$UNINSTALL" = "1" ]; then
  uninstall
elif [ "$FROM_SOURCE" = "1" ]; then
  migrate_legacy_config
  install_from_source
else
  migrate_legacy_config
  install_from_release
fi
