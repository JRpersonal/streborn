#!/bin/sh
# update.sh: prueft GitHub Releases und tauscht das Binary aus.
#
# Achtung: Bose BusyBox auf der ST10 hat kein curl, nur wget. Daher hier
# wget Pfade. wget 1.19.4 unterstuetzt --tries nicht, mit -T fuer Timeout
# arbeiten. Releases werden via api.github.com geholt, aber api.github.com
# liefert JSON. Wir greppen daraus den Tag Namen mit sed.

set -u

STICK_DIR="/media/sda1"
REPO="JRpersonal/streborn"
ARCH_ASSET="streborn-armv7l"
VERSION_FILE="${STICK_DIR}/version.txt"
BIN="${STICK_DIR}/${ARCH_ASSET}"

CURRENT="$(cat "${VERSION_FILE}" 2>/dev/null || echo none)"

# api.github.com mit wget, T 10 fuer 10 Sekunden Timeout
TMP="/tmp/streborn-release.json"
if ! wget -qO "$TMP" -T 10 "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null; then
    echo "$(date) update: GitHub API nicht erreichbar" >&2
    exit 0
fi

LATEST="$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$TMP" | head -n1)"
rm -f "$TMP"

if [ -z "${LATEST}" ]; then
    echo "$(date) update: konnte neueste Version nicht ermitteln" >&2
    exit 0
fi

if [ "${CURRENT}" = "${LATEST}" ]; then
    echo "$(date) update: bereits aktuell (${CURRENT})"
    exit 0
fi

echo "$(date) update: ${CURRENT} -> ${LATEST}"
URL="https://github.com/${REPO}/releases/download/${LATEST}/${ARCH_ASSET}"
if wget -qO "${BIN}.new" -T 60 "${URL}" 2>/dev/null; then
    chmod +x "${BIN}.new"
    mv "${BIN}.new" "${BIN}"
    echo "${LATEST}" > "${VERSION_FILE}"
    echo "$(date) update: auf ${LATEST} abgeschlossen"
else
    echo "$(date) update: Download fehlgeschlagen, kein Tausch" >&2
    rm -f "${BIN}.new"
    exit 1
fi
