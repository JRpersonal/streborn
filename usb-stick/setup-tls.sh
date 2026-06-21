#!/bin/sh
# setup-tls.sh: installs the STR Root CA into the system
# trust stores via a bind mount.
#
# Background: BoseApp/IoT link dynamically against libssl/libcrypto/libcurl.
# The bundles at /etc/pki/tls/certs/ca-bundle.crt and
# /etc/ssl/certs/ca-certificates.crt are what they read. The rootfs is read only,
# so we copy the originals to /tmp, append our Root CA to them
# and mount over them via a bind mount.
#
# Usage:
#   sh setup-tls.sh install   # set up the bind mount
#   sh setup-tls.sh remove    # remove the bind mount
#   sh setup-tls.sh status    # show status

set -u

ROOT_CA="/mnt/nv/streborn/ca/root.crt"
BUNDLE1="/etc/pki/tls/certs/ca-bundle.crt"
BUNDLE2="/etc/ssl/certs/ca-certificates.crt"
OVERLAY1="/tmp/streborn-bundle1.crt"
OVERLAY2="/tmp/streborn-bundle2.crt"

is_mounted() {
    mount | grep -q " on $1 "
}

install_one() {
    target="$1"
    overlay="$2"
    if [ ! -e "$target" ]; then
        echo "WARN: $target does not exist, skipping"
        return
    fi
    if is_mounted "$target"; then
        echo "$target already mounted, removing it first"
        umount "$target" 2>/dev/null
    fi
    cp "$target" "$overlay"
    {
        echo ""
        echo "# >>> STR Root CA >>>"
        cat "$ROOT_CA"
        echo "# <<< STR Root CA <<<"
    } >> "$overlay"
    if mount --bind "$overlay" "$target"; then
        echo "bind mount active: $overlay -> $target"
    else
        echo "ERROR: bind mount on $target failed" >&2
    fi
}

remove_one() {
    target="$1"
    if is_mounted "$target"; then
        if umount "$target"; then
            echo "bind mount removed: $target"
        else
            echo "ERROR: umount $target failed" >&2
        fi
    else
        echo "no bind mount on $target"
    fi
}

case "${1:-install}" in
    install)
        if [ ! -r "$ROOT_CA" ]; then
            echo "ERROR: Root CA not readable at $ROOT_CA" >&2
            echo "Has the agent ever run? It generates the CA on first start." >&2
            exit 1
        fi
        install_one "$BUNDLE1" "$OVERLAY1"
        install_one "$BUNDLE2" "$OVERLAY2"
        ;;
    remove|uninstall)
        remove_one "$BUNDLE1"
        remove_one "$BUNDLE2"
        ;;
    status)
        echo "Root CA:"
        if [ -r "$ROOT_CA" ]; then
            echo "  present: $ROOT_CA"
            openssl x509 -in "$ROOT_CA" -noout -subject 2>/dev/null
        else
            echo "  missing: $ROOT_CA"
        fi
        echo "Bundle 1: $BUNDLE1"
        if is_mounted "$BUNDLE1"; then
            echo "  bind mount active"
        else
            echo "  bind mount NOT active"
        fi
        echo "Bundle 2: $BUNDLE2"
        if is_mounted "$BUNDLE2"; then
            echo "  bind mount active"
        else
            echo "  bind mount NOT active"
        fi
        ;;
    *)
        echo "Usage: $0 {install|remove|status}" >&2
        exit 1
        ;;
esac
