#!/usr/bin/env bash
# Cross-compile the Go CLI for every published platform and stage the release
# artifacts in dist/.
#
# Usage:
#   scripts/build-binaries.sh <version> [commit]
#
# Everything is pure Go — modernc.org/sqlite means no CGO — so one host builds
# every target and no per-platform runner matrix is needed.
#
# Produces, for each target:
#   dist/omniharness_<version>_<os>_<arch>.tar.gz   (.zip on Windows)
# plus dist/SHA256SUMS covering all of them.
set -euo pipefail

VERSION="${1:-}"
COMMIT="${2:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
if [[ -z "$VERSION" ]]; then
  echo "usage: scripts/build-binaries.sh <version> [commit]" >&2
  exit 2
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# Prefer the portable toolchain when present (local dev); CI installs its own.
if [[ -x "$HOME/go-sdk/go/bin/go" ]]; then
  export PATH="$HOME/go-sdk/go/bin:$PATH"
fi

OUT="$REPO_ROOT/dist"
rm -rf "$OUT"
mkdir -p "$OUT"

# Trim the DWARF tables and symbol table: these are shipped binaries, not ones
# anyone will attach a debugger to, and it takes a meaningful bite out of the
# download.
LDFLAGS="-s -w"
LDFLAGS+=" -X omniharness/internal/version.Version=$VERSION"
LDFLAGS+=" -X omniharness/internal/version.Commit=$COMMIT"

TARGETS=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
  "windows/arm64"
)

echo "Building omniharness $VERSION ($COMMIT) for ${#TARGETS[@]} targets"

for target in "${TARGETS[@]}"; do
  os="${target%/*}"
  arch="${target#*/}"
  name="omniharness"
  [[ "$os" == "windows" ]] && name="omniharness.exe"

  stage="$OUT/stage/${os}_${arch}"
  mkdir -p "$stage"

  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$stage/$name" ./cmd/omniharness

  # Ship the licence and a pointer to the docs alongside the binary.
  cp LICENSE "$stage/"
  cp README.md "$stage/"

  base="omniharness_${VERSION}_${os}_${arch}"
  if [[ "$os" == "windows" ]]; then
    # zip is standard on the CI runners but absent from a stock Git Bash, so
    # fall back to Python rather than making the script CI-only.
    if command -v zip >/dev/null 2>&1; then
      (cd "$stage" && zip -q -r "$OUT/${base}.zip" .)
    elif command -v python3 >/dev/null 2>&1 || command -v python >/dev/null 2>&1; then
      PY=$(command -v python3 || command -v python)
      "$PY" -c "import shutil,sys; shutil.make_archive(sys.argv[1], 'zip', sys.argv[2])" "$OUT/${base}" "$stage"
    else
      echo "need zip or python to package $base" >&2
      exit 1
    fi
    echo "  $base.zip"
  else
    tar -czf "$OUT/${base}.tar.gz" -C "$stage" .
    echo "  $base.tar.gz"
  fi
done

rm -rf "$OUT/stage"

# Checksums let anyone verify a download against the release page.
(cd "$OUT" && sha256sum omniharness_* > SHA256SUMS)
echo "Wrote $OUT/SHA256SUMS"
ls -la "$OUT"
