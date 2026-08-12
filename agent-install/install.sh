#!/bin/bash
# one-api agent installer (curl | bash entrypoint)
#
# Purpose: from any LAN/internet host, install the Codex + one-api profile
# against a chosen one-api deployment served by /agent-install/.
#
# Usage:
#   curl -fsSL http://<host>:<port>/agent-install/install.sh | \
#       ONEAPI_BASE_URL=http://<host>:<port> bash
#
# Env vars (override):
#   ONEAPI_BASE_URL          base URL of the serving one-api (REQUIRED when piped)
#   ONEAPI_BEARER_TOKEN      API token (sk-cp-...) to bake into /etc/cx1/
#   ONEAPI_TARBALL_PATH      path under one-api where the tarball lives
#                            (default: /agent-install/cx1-latest.tar.gz)
#   ONEAPI_TARBALL_NAME      file name inside the tarball to exec after extract
#                            (default: opt/cx1/bin/install.sh)
#   ONEAPI_NONINTERACTIVE=1  skip interactive prompts
#   ONEAPI_UNINSTALL=1       forward --uninstall to inner installer
#
# Layout expectations (set up by the operator in AGENT_INSTALL_DIR):
#   - install.sh            (this script, served at /agent-install/install.sh)
#   - cx1-latest.tar.gz     (served at /agent-install/cx1-latest.tar.gz)
#     - contains: opt/cx1/bin/install.sh (the real installer)
#
# If the tarball is missing, we fall back to "script-only" mode where we
# just download a sibling install-inner.sh and exec that. This lets a thin
# repo ship only scripts without bundling the heavy codex binary.

set -euo pipefail

PROG="${0##*/}"
err()  { echo "[one-api-agent] $*" >&2; }
die()  { err "$*"; exit 1; }

# === resolve one-api base URL ===
ONEAPI_BASE_URL="${ONEAPI_BASE_URL:-${ONEAPI_URL:-}}"
if [[ -z "$ONEAPI_BASE_URL" ]]; then
    die "ONEAPI_BASE_URL is not set. Re-run with: ONEAPI_BASE_URL=http://<host>:<port> bash"
fi
ONEAPI_BASE_URL="${ONEAPI_BASE_URL%/}"
TARBALL_PATH="${ONEAPI_TARBALL_PATH:-/agent-install/cx1-latest.tar.gz}"
TARBALL_URL="$ONEAPI_BASE_URL$TARBALL_PATH"
INNER_PATH="${ONEAPI_INNER_INSTALL_PATH:-/agent-install/install-inner.sh}"

err "one-api server: $ONEAPI_BASE_URL"

TMPDIR="$(mktemp -d -t oneapi-agent-install.XXXXXX)"
trap 'rm -rf "$TMPDIR"' EXIT

# === try tarball mode first ===
_mode="tarball"
err "probing tarball: $TARBALL_URL"
if ! curl -fsLI --max-time 10 "$TARBALL_URL" >/dev/null 2>&1; then
    _mode="script"
    err "  tarball not reachable, falling back to script-only mode"
fi

if [[ "$_mode" == "tarball" ]]; then
    _expected_size=$(curl -fsLI "$TARBALL_URL" 2>/dev/null | awk '/^[Cc]ontent-[Ll]ength:/ {print $2}' | tr -d '\r' | tail -1)
    [[ -n "$_expected_size" ]] && err "  expected size: $(numfmt --to=iec --suffix=B "$_expected_size" 2>/dev/null || echo "${_expected_size} bytes")"

    err "downloading tarball"
    _t0=$(date +%s%3N 2>/dev/null || date +%s)
    if ! curl -fL --progress-bar -o "$TMPDIR/cx1.tar.gz" "$TARBALL_URL" 2>&1 | sed "s/^/[curl] /"; then
        die "download failed from $TARBALL_URL"
    fi
    _t1=$(date +%s%3N 2>/dev/null || date +%s)
    _actual_size=$(stat -c %s "$TMPDIR/cx1.tar.gz" 2>/dev/null || stat -f %z "$TMPDIR/cx1.tar.gz")
    _dur_ms=$((_t1 - _t0))
    (( _dur_ms > 0 )) || _dur_ms=1
    _speed=$((_actual_size * 1000 / _dur_ms))
    err "  downloaded: $(numfmt --to=iec --suffix=B "$_actual_size" 2>/dev/null || echo "${_actual_size} bytes") in ${_dur_ms}ms"

    err "extracting"
    tar -xzf "$TMPDIR/cx1.tar.gz" -C "$TMPDIR"
    INNER="${CX1_INNER:-${ONEAPI_TARBALL_NAME:-opt/cx1/bin/install.sh}}"
    INNER_FULL="$TMPDIR/$INNER"
    [[ -f "$INNER_FULL" ]] \
        || die "tarball malformed: missing $INNER (looked at $INNER_FULL)"
else
    err "downloading inner installer: $INNER_PATH"
    if ! curl -fsSL --max-time 30 -o "$TMPDIR/install-inner.sh" "$ONEAPI_BASE_URL$INNER_PATH"; then
        die "download failed from $ONEAPI_BASE_URL$INNER_PATH"
    fi
    chmod +x "$TMPDIR/install-inner.sh"
    INNER_FULL="$TMPDIR/install-inner.sh"
fi

err "forwarding to inner installer ($INNER_FULL)"
exec env \
    PKG_SRC_DIR="$TMPDIR" \
    CX1_HOME="${CX1_HOME:-/opt/cx1}" \
    CX1_ETC="${CX1_ETC:-/etc/cx1}" \
    CX1_BIN_LINK="${CX1_BIN_LINK:-/usr/local/bin/cx1}" \
    CX1_NONINTERACTIVE="${CX1_NONINTERACTIVE:-${ONEAPI_NONINTERACTIVE:-}}" \
    CX1_BEARER_TOKEN="${CX1_BEARER_TOKEN:-${ONEAPI_BEARER_TOKEN:-}}" \
    ONEAPI_BASE_URL="$ONEAPI_BASE_URL" \
    "$INNER_FULL" "$@"
