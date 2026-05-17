#!/usr/bin/env bash
# Build pi and link `pi` + `agent` globally. Safe to run from a fresh checkout.
# This script does NOT pre-unlink existing binaries — `npm link` replaces them
# atomically, so the previous install stays in place until the new one is ready.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$HERE"

bold() { printf "\033[1m%s\033[0m\n" "$*"; }
dim()  { printf "\033[2m%s\033[0m\n" "$*"; }

bold "▶ Installing dependencies"
npm install --silent

bold "▶ Building"
npm run gen:catalog --silent
npm -w @pi/core run build --silent
npm -w @pi/cli run build --silent

if [ ! -f "packages/cli/dist/cli.js" ]; then
  echo "build did not produce packages/cli/dist/cli.js — aborting" >&2
  exit 1
fi

bold "▶ Linking pi + agent globally"
cd packages/cli
# npm link atomically replaces existing symlinks; no need to unlink first.
npm link --silent

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
