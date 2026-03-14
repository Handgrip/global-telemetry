#!/usr/bin/env bash
set -euo pipefail

# ─── Global Telemetry v2 — Bootstrap ───
# This script is the stable entry point for `curl | bash` installs.
# It detects the latest release tag from GitHub, then downloads and
# executes the real install.sh via jsDelivr @{tag} — ensuring the
# installer is never served from a stale CDN cache.
#
# Usage:
#   curl -sSL https://cdn.jsdelivr.net/gh/Handgrip/global-telemetry@main/v2/scripts/bootstrap.sh | bash

REPO="Handgrip/global-telemetry"

TAG=$(curl -sSL "https://api.github.com/repos/${REPO}/tags?per_page=1" 2>/dev/null \
    | sed -n 's/.*"name" *: *"\([^"]*\)".*/\1/p' \
    | head -1) || true

if [[ -z "${TAG:-}" ]]; then
    echo "[WARN] Could not detect latest tag; falling back to 'main'"
    TAG="main"
else
    echo "[INFO] Latest release tag: ${TAG}"
fi

INSTALLER_URL="https://cdn.jsdelivr.net/gh/${REPO}@${TAG}/v2/scripts/install.sh"

echo "[INFO] Fetching installer from ${INSTALLER_URL} ..."
export REPO_TAG="$TAG"
exec bash <(curl -sSL "$INSTALLER_URL") "$@"
