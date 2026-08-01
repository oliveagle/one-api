#!/usr/bin/env bash
#
# Build the web assets and the Go binary from a single version string.
#
# The frontend bakes its version in at build time (REACT_APP_VERSION) while the
# backend gets its own via -ldflags. App.js compares the two and shows
# "新版本可用：... Shift + F5" whenever they differ, so rebuilding only one half
# produces a banner that no amount of refreshing can clear. Building both here
# keeps them in lockstep, and the script refuses to finish if they disagree.
#
# Usage:
#   ./build-local.sh                  # version = dev-<short sha>[-dirty]
#   ./build-local.sh v1.2.3           # explicit version
#   THEMES="default berry" ./build-local.sh
#
set -euo pipefail

cd "$(dirname "$0")"

VERSION="${1:-}"
if [ -z "$VERSION" ]; then
  sha="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
  VERSION="dev-${sha}"
  if ! git diff --quiet 2>/dev/null || ! git diff --cached --quiet 2>/dev/null; then
    VERSION="${VERSION}-dirty"
  fi
fi

# Only the themes actually rebuilt here will match the binary. "default" is what
# is served unless THEME is overridden at runtime.
THEMES="${THEMES:-default}"
OUT="${OUT:-one-api}"

echo "==> version: ${VERSION}"
echo "==> themes:  ${THEMES}"

for theme in $THEMES; do
  if [ ! -d "web/${theme}" ]; then
    echo "!! no such theme: web/${theme}" >&2
    exit 1
  fi
  echo "==> building web/${theme}"
  (
    cd "web/${theme}"
    [ -d node_modules ] || npm install
    DISABLE_ESLINT_PLUGIN='true' REACT_APP_VERSION="${VERSION}" npm run build
  )
done

echo "==> building ${OUT}"
CGO_ENABLED="${CGO_ENABLED:-1}" go build \
  -ldflags "-s -w -X 'github.com/songquanpeng/one-api/common.Version=${VERSION}'" \
  -o "${OUT}" .

# Self-check: the version must be present in both halves, otherwise the banner
# is back. The binary embeds web/build, so inspect what was just produced.
echo "==> verifying frontend and backend agree"
fail=0
for theme in $THEMES; do
  bundle_dir="web/build/${theme}/static/js"
  if [ ! -d "$bundle_dir" ]; then
    echo "!! ${bundle_dir} missing; did the web build move its output?" >&2
    fail=1
    continue
  fi
  if grep -qr --include='main.*.js' -F "${VERSION}" "$bundle_dir"; then
    echo "   ok: ${theme} bundle carries ${VERSION}"
  else
    echo "!! ${theme} bundle does not carry ${VERSION}" >&2
    fail=1
  fi
done

# The version comes from -ldflags, and -s -w strips the symbol table, so do not
# grep the binary: ask it. --version is wired up in common/init.go.
# OUT may be an absolute path, so only prefix ./ for bare names.
binary_cmd="${OUT}"
case "$binary_cmd" in
  /* | ./*) ;;
  *) binary_cmd="./${binary_cmd}" ;;
esac
binary_version="$("$binary_cmd" --version 2>/dev/null | head -1 | tr -d '[:space:]')"
if [ "$binary_version" = "$VERSION" ]; then
  echo "   ok: binary reports ${VERSION}"
else
  echo "!! binary reports '${binary_version}', want '${VERSION}'" >&2
  fail=1
fi

if [ "$fail" -ne 0 ]; then
  echo "==> FAILED: version mismatch; deploying this would show the update banner" >&2
  exit 1
fi

echo "==> done: ${OUT} @ ${VERSION}"
