#!/usr/bin/env bash
# Install pi-agent globally as `pi`. Overrides any pre-existing `pi` binary.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"

bold() { printf "\033[1m%s\033[0m\n" "$*"; }
dim()  { printf "\033[2m%s\033[0m\n" "$*"; }

bold "▶ pi installer"

unlink_existing() {
  local cmd="$1"
  local existing
  existing="$(command -v "$cmd" 2>/dev/null || true)"
  if [ -z "$existing" ]; then return 0; fi
  dim "Found existing $cmd at: $existing"
  if [ -L "$existing" ]; then
    dim "Removing symlink"
    rm -f "$existing"
  elif [[ "$existing" == */node_modules/* ]] || [[ "$existing" == */npm/* ]] || [[ "$existing" == */bin/"$cmd" ]]; then
    dim "Backing up to: ${existing}.bak"
    mv "$existing" "${existing}.bak" 2>/dev/null || sudo mv "$existing" "${existing}.bak"
  else
    dim "(leaving $existing in place — PATH order will determine which wins)"
  fi
}

# 1. Detect and unlink any pre-existing `pi` / `agent` binaries
unlink_existing pi
unlink_existing agent

# 2. Try npm-managed unlink for prior installs
npm unlink -g @earendil-works/pi-coding-agent 2>/dev/null || true
npm unlink -g pi 2>/dev/null || true
npm unlink -g @pi/cli 2>/dev/null || true

# 3. Install + build
bold "▶ Installing dependencies"
npm install --silent

bold "▶ Building"
npm run gen:catalog
npm -w @pi/core run build
npm -w @pi/cli run build

# 4. Link as global `pi`
bold "▶ Linking pi globally"
cd packages/cli
npm link --silent

# 5. Verify
new_pi="$(command -v pi 2>/dev/null || true)"
new_agent="$(command -v agent 2>/dev/null || true)"
if [ -z "$new_pi" ] && [ -z "$new_agent" ]; then
  echo "❌ Neither pi nor agent found on PATH after linking. Check your npm global bin path." >&2
  exit 1
fi

bold "✓ Installed"
[ -n "$new_pi" ]    && echo "  pi:    $new_pi"
[ -n "$new_agent" ] && echo "  agent: $new_agent"
echo "  source: $HERE"
echo
dim "Run \`pi\` or \`agent\` to start. Run \`pi login\` to add a provider."
