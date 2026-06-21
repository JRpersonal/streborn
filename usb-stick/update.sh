#!/bin/sh
# update.sh: checks GitHub releases and swaps out the binary.
#
# Note: the Bose BusyBox on the ST10 has no curl, only wget. Hence the
# wget paths here. wget 1.19.4 does not support --tries, so work with -T
# for the timeout. Releases are fetched via api.github.com, but
# api.github.com returns JSON. We grep the tag name out of it with sed.

set -u

STICK_DIR="/media/sda1"
REPO="JRpersonal/streborn"
ARCH_ASSET="streborn-armv7l"
VERSION_FILE="${STICK_DIR}/version.txt"
BIN="${STICK_DIR}/${ARCH_ASSET}"

CURRENT="$(cat "${VERSION_FILE}" 2>/dev/null || echo none)"

# api.github.com via wget, T 10 for a 10 second timeout
TMP="/tmp/streborn-release.json"
if ! wget -qO "$TMP" -T 10 "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null; then
    echo "$(date) update: GitHub API unreachable" >&2
    exit 0
fi

LATEST="$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$TMP" | head -n1)"
rm -f "$TMP"

if [ -z "${LATEST}" ]; then
    echo "$(date) update: could not determine latest version" >&2
    exit 0
fi

if [ "${CURRENT}" = "${LATEST}" ]; then
    echo "$(date) update: already up to date (${CURRENT})"
    exit 0
fi

echo "$(date) update: ${CURRENT} -> ${LATEST}"
URL="https://github.com/${REPO}/releases/download/${LATEST}/${ARCH_ASSET}"
if wget -qO "${BIN}.new" -T 60 "${URL}" 2>/dev/null; then
    chmod +x "${BIN}.new"
    mv "${BIN}.new" "${BIN}"
    echo "${LATEST}" > "${VERSION_FILE}"
    echo "$(date) update: completed to ${LATEST}"
else
    echo "$(date) update: download failed, kept existing binary" >&2
    rm -f "${BIN}.new"
    exit 1
fi
