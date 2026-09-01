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
#   - npm authenticated. In CI this is npm trusted publishing (OIDC): the
#     workflow's id-token permission is the credential, so no token is stored.
#     Running it by hand needs `npm login`, or a granular token with "Bypass
#     2FA for publish" set as NODE_AUTH_TOKEN or in .npmrc.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

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

# latest_published queries npm for the current published version of the package.
# Returns empty string when the package has never been published.
latest_published() {
  npm view omniharness-cli version 2>/dev/null || true
}

# bump_version writes the next version into npm/package.json and prints it.
# It bumps from the *latest published* version on npm (not the local
# package.json) so that CI runs never try to re-publish an existing version.
bump_version() {
  VERSION_ARG="$VERSION_ARG" BUMP="$BUMP" LATEST_PUBLISHED="$(latest_published)" node -e '
    const fs = require("fs");
    const p = "npm/package.json";
    const j = JSON.parse(fs.readFileSync(p, "utf8"));
    // Use the latest published version as the baseline when available,
    // falling back to the local package.json version.
    const base = process.env.LATEST_PUBLISHED || j.version;
    const [maj, min, pat] = base.split(".").map(Number);
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

# The package ships the new Ink/Mastra TUI. Build it before packaging so the
# published launcher executes the TypeScript interface rather than the legacy
# Go Bubble Tea cockpit.
cd npm
npm install --ignore-scripts

# Verify the TypeScript side before building it. This is the same suite CI
# runs on pull requests; running it here too means a release can never ship a
# tarball whose tests were never executed.
echo "Verifying: npm test"
npm test
npm run build
cd "$REPO_ROOT"

echo "Built omniharness-cli v$VERSION TypeScript CLI"

if [[ "$DRY_RUN" == "1" ]]; then
  echo "Dry run: publish skipped. Next version would be $VERSION"
  exit 0
fi

cd npm
npm publish
echo "Published omniharness-cli v$VERSION"
