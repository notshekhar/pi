#!/usr/bin/env bash
# pi installer — prebuilt binary from GitHub Releases, no runtime required.
#   curl -fsSL https://raw.githubusercontent.com/notshekhar/pi/main/install.sh | bash
#
# Layout after install:
#   $PI_HOME/                    (default: ~/.pi-bin)
#     ├── pi                     the executable
#     └── .version               the tag it came from
#   $BIN_DIR/pi → $PI_HOME/pi    (symlink)
#
# Flags (curl | bash -s -- <flags>) — each maps to the env knob beside it:
#   -v, --version <vX.Y.Z>   pin a specific tag        (PI_VERSION)
#       --force              skip the up-to-date gate  (PI_FORCE=1)
#       --from-source        clone + go build          (PI_FROM_SOURCE=1)
#       --uninstall          remove install + links    (PI_UNINSTALL=1)
#       --no-modify-path     don't touch the shell rc  (PI_NO_MODIFY_PATH=1)
#   -h, --help
#
# Extra env knobs:
#   PI_REPO_SLUG   notshekhar/pi   override the repo
#   PI_HOME        $HOME/.pi-bin   install dir
#   PI_BIN_DIR                     symlink dir (auto: /usr/local/bin, else
#                                  /opt/homebrew/bin, else ~/.local/bin)

set -euo pipefail

REPO_SLUG="${PI_REPO_SLUG:-notshekhar/pi}"
REPO="${PI_REPO:-https://github.com/${REPO_SLUG}.git}"
REF="${PI_REF:-main}"
PI_HOME="${PI_HOME:-$HOME/.pi-bin}"
FORCE="${PI_FORCE:-0}"
FROM_SOURCE="${PI_FROM_SOURCE:-0}"
UNINSTALL="${PI_UNINSTALL:-0}"
PIN_VERSION="${PI_VERSION:-}"
NO_MODIFY_PATH="${PI_NO_MODIFY_PATH:-0}"

usage() {
  cat <<EOF
pi installer

Usage: install.sh [options]

Options:
  -v, --version <vX.Y.Z>  Install a specific release
      --force             Reinstall even when up to date
      --from-source       Clone the repo and build with go
      --uninstall         Remove the install and its symlinks
      --no-modify-path    Don't write the PATH line to your shell rc
  -h, --help              Show this help

Examples:
  curl -fsSL https://raw.githubusercontent.com/${REPO_SLUG}/main/install.sh | bash
  curl -fsSL https://raw.githubusercontent.com/${REPO_SLUG}/main/install.sh | bash -s -- --version v0.1.1
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    -v|--version)
      if [ -n "${2:-}" ]; then PIN_VERSION="$2"; shift 2; else
        printf "\033[31m--version requires an argument\033[0m\n" >&2; exit 1; fi ;;
    --force) FORCE=1; shift ;;
    --from-source) FROM_SOURCE=1; shift ;;
    --uninstall) UNINSTALL=1; shift ;;
    --no-modify-path) NO_MODIFY_PATH=1; shift ;;
    *) printf "\033[2mignoring unknown option: %s\033[0m\n" "$1" >&2; shift ;;
  esac
done

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

# ver_gt a b → true when a is strictly newer than b.
ver_gt() {
  local a="${1#v}" b="${2#v}"
  [ "$a" = "$b" ] && return 1
  local top
  top="$(printf '%s\n%s\n' "$a" "$b" | sort -V | head -n1)"
  [ "$top" = "$b" ] && return 0
  return 1
}

# ── Download progress bar ──────────────────────────────────────────────────
# curl writes a --trace-ascii stream into a FIFO; the content-length and each
# `<= recv data` record are parsed live to draw a ■■■･･･ 42% bar on stderr.
# Only on a TTY — anything else, or any failure, falls back to plain curl in
# the caller, because a progress bar is never worth failing an install over.
unbuffered_sed() {
  # GNU takes -u, BSD/macOS takes -l; without either, pad each line past the
  # libc buffer so records flush through the pipe as they happen.
  if echo | sed -u -e "" >/dev/null 2>&1; then
    sed -nu "$@"
  elif echo | sed -l -e "" >/dev/null 2>&1; then
    sed -nl "$@"
  else
    local pad
    pad="$(printf "\n%512s" "")"
    sed -ne "s/$/\\${pad}/" "$@"
  fi
}

PROGRESS_COLOR='\033[38;5;215m'
PROGRESS_NC='\033[0m'

print_progress() {
  local bytes="$1" length="$2"
  [ "$length" -gt 0 ] || return 0

  local width=50
  local percent=$(( bytes * 100 / length ))
  [ "$percent" -gt 100 ] && percent=100
  local on=$(( percent * width / 100 ))
  local off=$(( width - on ))

  local filled empty
  filled="$(printf "%*s" "$on" "")"; filled="${filled// /■}"
  empty="$(printf "%*s" "$off" "")"; empty="${empty// /･}"

  printf "\r${PROGRESS_COLOR}%s%s %3d%%${PROGRESS_NC}" "$filled" "$empty" "$percent" >&4
}

download_with_progress() {
  local url="$1" output="$2"

  if [ -t 2 ]; then exec 4>&2; else exec 4>/dev/null; fi

  local tmp_dir="${TMPDIR:-/tmp}"
  local tracefile="${tmp_dir}/pi_install_$$.trace"

  rm -f "$tracefile"
  mkfifo "$tracefile" 2>/dev/null || return 1

  # Hide the cursor while the bar redraws; always restore it on the way out.
  printf "\033[?25l" >&4
  trap "trap - RETURN; rm -f \"$tracefile\"; printf '\033[?25h' >&4; exec 4>&-" RETURN

  # -f so an HTTP error fails the download (and the caller's fallback runs)
  # instead of tracing a 404 page into the output file.
  ( curl -f --trace-ascii "$tracefile" -s -L -o "$output" "$url" ) &
  local curl_pid=$!

  unbuffered_sed \
    -e 'y/ACDEGHLNORTV/acdeghlnortv/' \
    -e '/^0000: content-length:/p' \
    -e '/^<= recv data/p' \
    "$tracefile" | \
  {
    local length=0 bytes=0
    while IFS=" " read -r -a line; do
      [ "${#line[@]}" -lt 2 ] && continue
      local tag="${line[0]} ${line[1]}"
      if [ "$tag" = "0000: content-length:" ]; then
        # Each response in a redirect chain restarts the count; the final
        # (asset) response's length is the one the bar ends up tracking.
        length="$(echo "${line[2]}" | tr -d '\r')"
        bytes=0
      elif [ "$tag" = "<= recv" ]; then
        bytes=$(( bytes + ${line[3]} ))
        [ "$length" -gt 0 ] && print_progress "$bytes" "$length"
      fi
    done
  }

  wait $curl_pid
  local ret=$?
  echo "" >&4
  return $ret
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
      err "Windows is not supported yet."
      err "  The TUI needs a console raw-mode implementation, and the bash tool,"
      err "  hooks and key helpers all shell out to \`sh\`. Rather than ship a"
      err "  binary that exits on launch, there is no Windows build."
      err "  WSL works today: install pi inside it."
      exit 1
      ;;
    *)      err "unsupported OS: $uname_s"; exit 1 ;;
  esac
  case "$uname_m" in
    x86_64|amd64)   arch="x64" ;;
    arm64|aarch64)  arch="arm64" ;;
    *)              err "unsupported arch: $uname_m"; exit 1 ;;
  esac
  # A shell under Rosetta reports x86_64 on Apple Silicon — install the
  # native arm64 build rather than the emulated one.
  if [ "$os" = "darwin" ] && [ "$arch" = "x64" ]; then
    if [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = "1" ]; then
      arch="arm64"
    fi
  fi
  printf "%s-%s" "$os" "$arch"
}

# The release binaries link glibc, because the SQLite driver is cgo. musl
# distros need a source build or a compatibility layer, and saying so beats a
# "not found" from the dynamic loader on first run.
check_libc() {
  [ "$(uname -s)" = "Linux" ] || return 0
  if [ -f /etc/alpine-release ] || (ldd --version 2>&1 | grep -qi musl); then
    err "musl libc detected (Alpine?). The release binaries are glibc builds."
    err "  options:"
    err "    • apk add gcompat            (glibc compatibility layer)"
    err "    • PI_FROM_SOURCE=1 <installer>  (build on this machine)"
    exit 1
  fi
}

# ── Resolve the latest release tag ────────────────────────────────────────
# The releases/latest redirect first: it is not subject to the anonymous
# GitHub API rate limit (60/h/IP) that bites CI and shared networks.
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

resolve_bin_dir() {
  if [ -n "${PI_BIN_DIR:-}" ]; then
    mkdir -p "$PI_BIN_DIR"
    printf "%s" "$PI_BIN_DIR"
    return
  fi
  for d in /usr/local/bin /opt/homebrew/bin; do
    if [ -w "$d" ] 2>/dev/null; then printf "%s" "$d"; return; fi
  done
  local fallback="$HOME/.local/bin"
  mkdir -p "$fallback"
  printf "%s" "$fallback"
}

uninstall() {
  bold "▶ Uninstalling pi"
  for link in "$HOME/.local/bin/pi" "/usr/local/bin/pi" "/opt/homebrew/bin/pi" \
              "${PI_BIN_DIR:+$PI_BIN_DIR/pi}"; do
    [ -n "$link" ] || continue
    if [ -L "$link" ] || [ -f "$link" ]; then
      rm -f "$link" 2>/dev/null && dim "  removed $link" || true
    fi
  done
  rm -rf "$PI_HOME" 2>/dev/null && dim "  removed $PI_HOME" || true
  bold "✓ Uninstalled."
  dim  "  Your sessions and settings in ~/.pi-agent were kept."
  dim  "  Remove them with: rm -rf ~/.pi-agent"
}

install_from_source() {
  bold "▶ pi installer (source build)"
  need_tool git "Install Git first: https://git-scm.com/downloads"
  need_tool go  "Install Go 1.24+: https://go.dev/dl/"

  rm -rf "${PI_HOME}".old.* "${PI_HOME}".new.* "${PI_HOME}".src.* 2>/dev/null || true
  local scratch="${PI_HOME}.src.$$"
  trap 'rm -rf "$scratch" 2>/dev/null || true' EXIT
  bold "▶ Cloning $REPO ($REF)"
  git clone --depth=1 --branch "$REF" "$REPO" "$scratch" 2>/dev/null \
    || git clone --depth=1 "$REPO" "$scratch"

  bold "▶ Building"
  # CGO_ENABLED=1 explicitly: the SQLite driver needs it, and a cross-compile
  # environment may have turned it off.
  ( cd "$scratch" && CGO_ENABLED=1 go build -trimpath -ldflags "-s -w" -o "$scratch/stage/pi" ./cmd/pi )
  if [ ! -x "$scratch/stage/pi" ]; then
    err "the build did not produce a binary"
    exit 1
  fi
  swap_into_place "$scratch/stage"
  trap - EXIT
  rm -rf "$scratch" 2>/dev/null || true
  link_globally
  smoke_test
  finish_message "from source"
}

install_from_release() {
  bold "▶ pi installer"
  need_tool curl "macOS: preinstalled. Linux: sudo apt install curl"
  need_tool tar  "Standard on macOS/Linux."
  check_libc

  local target latest installed
  target="$(detect_target)"
  dim "  target: $target"

  latest="${PIN_VERSION}"
  [ -z "$latest" ] && latest="$(resolve_latest_tag)"
  if [ -z "$latest" ]; then
    err "could not resolve the latest release tag from $REPO_SLUG"
    err "set PI_VERSION=vX.Y.Z to pin one, or PI_FROM_SOURCE=1 to build from source"
    exit 1
  fi
  case "$latest" in v*) ;; *) latest="v$latest" ;; esac

  installed=""
  [ -f "$PI_HOME/.version" ] && installed="$(cat "$PI_HOME/.version" 2>/dev/null || true)"
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

  local scratch tarball sum url base
  # Sweep leftovers from interrupted runs BEFORE the fresh scratch exists, so
  # the glob cannot eat it.
  rm -rf "${PI_HOME}".old.* "${PI_HOME}".new.* "${PI_HOME}".src.* 2>/dev/null || true
  scratch="${PI_HOME}.new.$$"
  trap 'rm -rf "$scratch" 2>/dev/null || true' EXIT
  mkdir -p "$scratch"

  base="https://github.com/${REPO_SLUG}/releases/download/${latest}"
  url="${base}/pi-${target}.tar.gz"
  tarball="$scratch/pi.tar.gz"
  sum="$scratch/pi.tar.gz.sha256"

  bold "▶ Downloading ${url##*/}"
  if ! { [ -t 2 ] && download_with_progress "$url" "$tarball"; }; then
    if ! curl -fL --progress-bar "$url" -o "$tarball"; then
      err "download failed: $url"
      err "the release may not have a $target asset; PI_FROM_SOURCE=1 builds it here"
      exit 1
    fi
  fi

  if curl -fsSL "${url}.sha256" -o "$sum" 2>/dev/null && [ -s "$sum" ]; then
    local expected got
    expected="$(awk '{print $1}' "$sum")"
    got="$(sha256_of "$tarball")"
    if [ "$expected" != "$got" ]; then
      err "sha256 mismatch (expected $expected, got $got)"
      exit 1
    fi
    dim "  sha256 ok"
  else
    dim "  sha256 file missing — skipping verify"
  fi

  bold "▶ Extracting"
  tar -xzf "$tarball" -C "$scratch"
  if [ ! -x "$scratch/$target/pi" ]; then
    err "the tarball is missing $target/pi"
    exit 1
  fi
  printf "%s\n" "$latest" > "$scratch/$target/.version"

  # Gatekeeper blocks an unsigned quarantined binary with a scary dialog.
  if [ "$(uname -s)" = "Darwin" ] && command -v xattr >/dev/null 2>&1; then
    xattr -dr com.apple.quarantine "$scratch/$target" 2>/dev/null || true
  fi

  swap_into_place "$scratch/$target"
  trap - EXIT
  rm -rf "$scratch" 2>/dev/null || true

  link_globally
  smoke_test
  finish_message "$latest"
}

# swap_into_place moves the staged directory over the old install.
#
# A move rather than a copy-over: the old install is set aside first and
# deleted only once the new one is in place, so an interrupted install leaves
# a working pi rather than half of two.
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

link_globally() {
  bold "▶ Linking pi"
  # Clear shims from a previous install style before symlinking, or an old
  # copy earlier on PATH keeps winning and the update looks like it did
  # nothing.
  for stale in "$HOME/.local/bin/pi" "/usr/local/bin/pi" "/opt/homebrew/bin/pi"; do
    if [ -L "$stale" ] || [ -f "$stale" ]; then rm -f "$stale" 2>/dev/null || true; fi
  done

  local bin_dir
  bin_dir="$(resolve_bin_dir)"
  ln -sf "$PI_HOME/pi" "$bin_dir/pi"
  hash -r 2>/dev/null || true

  case ":$PATH:" in
    *":$bin_dir:"*) ;;
    *) modify_path "$bin_dir" ;;
  esac

  # GitHub Actions: expose the bin dir to later workflow steps.
  if [ "${GITHUB_ACTIONS:-}" = "true" ] && [ -n "${GITHUB_PATH:-}" ]; then
    echo "$bin_dir" >> "$GITHUB_PATH"
    dim "  added $bin_dir to \$GITHUB_PATH"
  fi

  PI_LINK_DIR="$bin_dir"
}

modify_path() {
  local bin_dir="$1" shell_name line config_file=""
  shell_name="$(basename "${SHELL:-bash}")"
  local xdg="${XDG_CONFIG_HOME:-$HOME/.config}"
  local candidates
  case "$shell_name" in
    fish) candidates="$HOME/.config/fish/config.fish"
          line="fish_add_path $bin_dir" ;;
    zsh)  candidates="${ZDOTDIR:-$HOME}/.zshrc ${ZDOTDIR:-$HOME}/.zshenv $xdg/zsh/.zshrc"
          line="export PATH=\"$bin_dir:\$PATH\"" ;;
    bash) candidates="$HOME/.bashrc $HOME/.bash_profile $HOME/.profile $xdg/bash/.bashrc"
          line="export PATH=\"$bin_dir:\$PATH\"" ;;
    *)    candidates="$HOME/.profile"
          line="export PATH=\"$bin_dir:\$PATH\"" ;;
  esac

  if [ "$NO_MODIFY_PATH" = "1" ]; then
    err "warning: $bin_dir is not on PATH (--no-modify-path given)"
    err "  $line"
    return 0
  fi

  for f in $candidates; do
    if [ -f "$f" ]; then config_file="$f"; break; fi
  done
  if [ -z "$config_file" ] || [ ! -w "$config_file" ]; then
    err "warning: $bin_dir is not on PATH — add it to your shell rc:"
    err "  $line"
    return 0
  fi
  if grep -Fxq "$line" "$config_file" 2>/dev/null; then
    dim "  PATH line already in $config_file"
    return 0
  fi
  printf "\n# pi\n%s\n" "$line" >> "$config_file"
  dim "  added $bin_dir to PATH in $config_file (restart your shell to pick it up)"
}

# The binary must actually run here — this catches a libc or architecture
# surprise now rather than on first use.
smoke_test() {
  if ! "$PI_HOME/pi" --help >/dev/null 2>&1; then
    err "the installed binary did not run"
    err "  try PI_FROM_SOURCE=1 to build for this machine"
    exit 1
  fi
  dim "  verified: it runs"
}

finish_message() {
  local label="$1"
  bold "✓ Installed $label"
  echo "  pi:      $PI_LINK_DIR/pi"
  echo "  target:  $PI_HOME"
  echo
  dim  "█▀▀█ ░▀░"
  dim  "█░░█ ▀█▀"
  dim  "█▀▀▀ ▀▀▀"
  echo
  echo "To start:"
  echo
  printf "  cd <project>  "; dim "# open a directory"
  printf "  pi            "; dim "# run the agent"
  printf "  /login        "; dim "# add a provider, inside the TUI"
  echo
  dim "Docs: https://github.com/${REPO_SLUG}#readme"
}

# ── Route ──────────────────────────────────────────────────────────────────
if [ "$UNINSTALL" = "1" ]; then
  uninstall
elif [ "$FROM_SOURCE" = "1" ]; then
  install_from_source
else
  install_from_release
fi
