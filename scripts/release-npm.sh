#!/usr/bin/env bash
# Build the OmniHarness binary into the npm wrapper and publish it to npmjs.
#
# Usage:
#   scripts/release-npm.sh            # publish current package version
#   scripts/release-npm.sh 0.2.0      # bump to 0.2.0, stamp the binary, publish
#
# Prerequisites: `npm adduser` once, and the portable Go toolchain
# (scripts/env.sh handles PATH).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

export PATH="$HOME/go-sdk/go/bin:$PATH"

VERSION="${1:-}"
LDFLAGS=""
if [[ -n "$VERSION" ]]; then
  node -e "
    const fs = require('fs');
    const p = 'npm/package.json';
    const j = JSON.parse(fs.readFileSync(p, 'utf8'));
    j.version = '$VERSION';
    fs.writeFileSync(p, JSON.stringify(j, null, 2) + '\n');
  "
  LDFLAGS="-X omniharness/internal/version.Version=$VERSION"
else
  VERSION="$(node -e "console.log(require('./npm/package.json').version)")"
fi

TARGET="npm/vendor/win32-x64/omniharness.exe"
mkdir -p "$(dirname "$TARGET")"
if [[ -n "$LDFLAGS" ]]; then
  go build -ldflags "$LDFLAGS" -o "$TARGET" ./cmd/omniharness
else
  go build -o "$TARGET" ./cmd/omniharness
fi

echo "Built omniharness-cli v$VERSION (win32-x64) -> $TARGET"

cd npm
npm publish
echo "Published omniharness-cli v$VERSION"
