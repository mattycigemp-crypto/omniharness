#!/usr/bin/env bash
# Build, verify and publish the omniharness-cli npm wrapper.
#
# Usage:
#   scripts/release-npm.sh               # auto-bump patch (0.1.1 -> 0.1.2), verify, publish
#   scripts/release-npm.sh --minor       # auto-bump minor (0.1.1 -> 0.2.0)
#   scripts/release-npm.sh --major       # auto-bump major (0.1.1 -> 1.0.0)
#   scripts/release-npm.sh 0.2.0         # explicit version
#   scripts/release-npm.sh --dry-run     # bump + verify only, skip publish
#
# Prerequisites:
#   - npm authenticated. Interactive: `npm publish` prompts for the 2FA OTP.
#     Unattended: a granular token with "Bypass 2FA for publish" enabled, set
#     as NODE_AUTH_TOKEN or in .npmrc — without a bypass token every publish
#     needs your one-time code.
#   - The portable Go toolchain (scripts/env.sh handles PATH).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# Prefer the portable Go toolchain when present (local dev); CI installs Go
# itself via actions/setup-go, so a missing ~/go-sdk must not break PATH.
if [[ -x "$HOME/go-sdk/go/bin/go" ]]; then
  export PATH="$HOME/go-sdk/go/bin:$PATH"
fi

DRY_RUN=0
BUMP="patch"
VERSION_ARG=""
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --minor)   BUMP="minor" ;;
    --major)   BUMP="major" ;;
    -*)        echo "unknown option: $arg" >&2; exit 2 ;;
    *)         VERSION_ARG="$arg" ;;
  esac
done

# bump_version writes the next version into npm/package.json and prints it.
bump_version() {
  VERSION_ARG="$VERSION_ARG" BUMP="$BUMP" node -e '
    const fs = require("fs");
    const p = "npm/package.json";
    const j = JSON.parse(fs.readFileSync(p, "utf8"));
    const [maj, min, pat] = j.version.split(".").map(Number);
    let v;
    if (process.env.VERSION_ARG) v = process.env.VERSION_ARG;
    else if (process.env.BUMP === "major") v = `${maj + 1}.0.0`;
    else if (process.env.BUMP === "minor") v = `${maj}.${min + 1}.0`;
    else v = `${maj}.${min}.${pat + 1}`;
    j.version = v;
    fs.writeFileSync(p, JSON.stringify(j, null, 2) + "\n");
    console.log(v);
  '
}

VERSION="$(bump_version)"
LDFLAGS="-X omniharness/internal/version.Version=$VERSION -X omniharness/internal/version.Commit=release"

TARGET="npm/vendor/win32-x64/omniharness.exe"
mkdir -p "$(dirname "$TARGET")"
go build -ldflags "$LDFLAGS" -o "$TARGET" ./cmd/omniharness
echo "Built omniharness-cli v$VERSION (win32-x64) -> $TARGET"

echo "Verifying: go vet ./... && go test -count=1 ./..."
go vet ./...
go test -count=1 ./...
echo "Verification passed"

if [[ "$DRY_RUN" == "1" ]]; then
  echo "Dry run: publish skipped. Next version would be $VERSION"
  exit 0
fi

cd npm
npm publish
echo "Published omniharness-cli v$VERSION"
