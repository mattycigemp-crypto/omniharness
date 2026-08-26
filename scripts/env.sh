#!/usr/bin/env bash
# Source this file to put the portable Go toolchain on PATH:
#   source scripts/env.sh
export GOROOT="$HOME/go-sdk/go"
export PATH="$GOROOT/bin:$PATH"
export GOBIN="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/bin"
