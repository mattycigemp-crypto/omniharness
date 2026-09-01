#!/usr/bin/env bash
# Render the release notes for a version, from the commits since the previous
# tag, and print them on stdout.
#
# Usage:
#   scripts/release-notes.sh <version> [previous-tag]
#
# The commit subjects in this repository are written to be read, so the notes
# are built from them rather than from a generic changelog dump. Conventional
# prefixes (feat/fix/docs/...) decide the section; the scope is kept because it
# says which part of the project moved.
set -euo pipefail

VERSION="${1:-}"
if [[ -z "$VERSION" ]]; then
  echo "usage: scripts/release-notes.sh <version> [previous-tag]" >&2
  exit 2
fi
TAG="v$VERSION"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

REPO_URL="https://github.com/${GITHUB_REPOSITORY:-shipking-ai/omniharness}"

# The previous tag bounds the range. Fall back to the whole history for the
# first release, so it is never empty.
PREV="${2:-}"
if [[ -z "$PREV" ]]; then
  PREV="$(git tag --list 'v*' --sort=-v:refname | grep -v "^${TAG}$" | head -1 || true)"
fi
if [[ -n "$PREV" ]] && git rev-parse -q --verify "$PREV" >/dev/null 2>&1; then
  RANGE="$PREV..HEAD"
else
  PREV=""
  RANGE="HEAD"
fi

# A conventional subject: type(scope)!: text. Held in a variable because bash
# only accepts an unquoted reference on the right of =~.
conventional='^([a-z]+)(\(([^)]+)\))?!?: +(.+)$'

# Collect subjects into per-section buffers.
added=""; fixed=""; changed=""; docs=""; internal=""; other=""

while IFS=$'\t' read -r sha subject; do
  [[ -z "$subject" ]] && continue
  # Skip merge commits; the individual commits carry the detail.
  case "$subject" in "Merge "*) continue ;; esac

  # type(scope): text  ->  text (scope)
  if [[ "$subject" =~ $conventional ]]; then
    type="${BASH_REMATCH[1]}"
    scope="${BASH_REMATCH[3]}"
    text="${BASH_REMATCH[4]}"
  else
    type="other"; scope=""; text="$subject"
  fi

  line="- $text"
  [[ -n "$scope" ]] && line="$line (\`$scope\`)"
  line="$line — [\`$sha\`]($REPO_URL/commit/$sha)"

  case "$type" in
    feat)                  added+="$line"$'\n' ;;
    fix)                   fixed+="$line"$'\n' ;;
    perf|refactor)         changed+="$line"$'\n' ;;
    docs)                  docs+="$line"$'\n' ;;
    test|build|ci|chore)   internal+="$line"$'\n' ;;
    *)                     other+="$line"$'\n' ;;
  esac
done < <(git log --no-merges --format=$'%h\t%s' "$RANGE")

# An empty section is the normal case — most releases touch only a category or
# two — so this must not report failure under set -e.
section() {
  if [[ -n "$2" ]]; then
    printf '### %s\n\n%s\n' "$1" "$2"
  fi
}

section "Added" "$added"
section "Fixed" "$fixed"
section "Changed" "$changed"
section "Documentation" "$docs"
section "Internal" "$internal"
section "Other" "$other"

cat <<EOF
### Install

Prebuilt \`omniharness\` binaries for linux, macOS and Windows (amd64 and arm64) are attached below. The npm package \`omniharness-cli@$VERSION\` is the TypeScript terminal UI — a separate program on the same version line.

\`\`\`
npm install -g omniharness-cli@$VERSION
\`\`\`

Verify a downloaded archive against \`SHA256SUMS\`:

\`\`\`
sha256sum -c SHA256SUMS --ignore-missing
\`\`\`
EOF

if [[ -n "$PREV" ]]; then
  printf '\n**Full changelog**: %s/compare/%s...%s\n' "$REPO_URL" "$PREV" "$TAG"
fi
