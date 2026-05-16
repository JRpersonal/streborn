#!/bin/sh
# setup-tls.sh: installiert die STR Root CA in die System
# Trust Stores via bind mount.
#
# Hintergrund: BoseApp/IoT linken dynamisch gegen libssl/libcrypto/libcurl.
# Die Bundles unter /etc/pki/tls/certs/ca-bundle.crt und
# /etc/ssl/certs/ca-certificates.crt werden geleen. rootfs ist read only,
# also kopieren wir die Originals nach /tmp, haengen unsere Root CA dran
# und mounten via bind drueber.
#
# Aufruf:
#   sh setup-tls.sh install   # bind mount setzen
#   sh setup-tls.sh remove    # bind mount entfernen
#   sh setup-tls.sh status    # Status anzeigen

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
        echo "WARN: $target existiert nicht, ueberspringe"
        return
    fi
    if is_mounted "$target"; then
        echo "$target bereits gemountet, entferne erst"
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
        echo "bind mount aktiv: $overlay -> $target"
    else
        echo "FEHLER: bind mount auf $target schlug fehl" >&2
    fi
}

remove_one() {
    target="$1"
    if is_mounted "$target"; then
        if umount "$target"; then
            echo "bind mount entfernt: $target"
        else
            echo "FEHLER: umount $target schlug fehl" >&2
        fi
    else
        echo "kein bind mount auf $target"
    fi
}

case "${1:-install}" in
    install)
        if [ ! -r "$ROOT_CA" ]; then
            echo "FEHLER: Root CA nicht lesbar unter $ROOT_CA" >&2
            echo "Hat der Agent schon mal gelaufen? Er erzeugt die CA beim ersten Start." >&2
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
            echo "  vorhanden: $ROOT_CA"
            openssl x509 -in "$ROOT_CA" -noout -subject 2>/dev/null
        else
            echo "  fehlt: $ROOT_CA"
        fi
        echo "Bundle 1: $BUNDLE1"
        if is_mounted "$BUNDLE1"; then
            echo "  bind mount aktiv"
        else
            echo "  bind mount NICHT aktiv"
        fi
        echo "Bundle 2: $BUNDLE2"
        if is_mounted "$BUNDLE2"; then
            echo "  bind mount aktiv"
        else
            echo "  bind mount NICHT aktiv"
        fi
        ;;
    *)
        echo "Verwendung: $0 {install|remove|status}" >&2
        exit 1
        ;;
esac
