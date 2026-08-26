#!/usr/bin/env bash
# Source this file for the full OmniHarness environment — this is the entire
# "install" step for using the harness from any directory:
#
#   source scripts/env.sh
#
# It sets up:
#   1. The portable Go toolchain ($HOME/go-sdk) on PATH, for building.
#   2. The omniharness binary directory (bin/) on PATH, so `omniharness`
#      resolves from any directory. After pulling new code, rebuild with:
#        go build -o bin/omniharness.exe ./cmd/omniharness
#   3. OMNIROUTE_URL defaulting to http://127.0.0.1:20128 (only when unset).
#
# OMNIROUTE_API_KEY is intentionally NOT set here. Provide it when needed:
#
#   export OMNIROUTE_API_KEY=sk-…
#
# If it is unset, the harness prompts to paste the key on interactive launch
# (held in memory only, never persisted). Non-interactive runs without it
# fail with a clear error naming OMNIROUTE_API_KEY.
#
# To make this permanent for every new terminal, add `source …/scripts/env.sh`
# to your shell profile (~/.bashrc). This script does not touch your profile.

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

export GOROOT="$HOME/go-sdk/go"
export GOBIN="$REPO_ROOT/bin"

# Add a directory to PATH once, without duplicating on repeated sourcing.
_add_path() {
  case ":$PATH:" in
    *":$1:"*) ;;
    *) export PATH="$1:$PATH" ;;
  esac
}
_add_path "$GOROOT/bin"
_add_path "$REPO_ROOT/bin"

# Optional convenience default; never overrides an explicit user value.
if [[ -z "${OMNIROUTE_URL:-}" ]]; then
  export OMNIROUTE_URL="http://127.0.0.1:20128"
fi
