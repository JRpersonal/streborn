#!/bin/sh
# install.sh: Einmal manuell auf der Box ausfuehren, um den Autostart
# zu aktivieren.
#
# Phase 1 (rc.local direkt):
#   sh /media/sda1/install.sh             # Phase 1 aktivieren
#   sh /media/sda1/install.sh remove      # Phase 1 deaktivieren
#   sh /media/sda1/install.sh status      # Status anzeigen
#
# Phase 2 (Integration in shepherdd):
#   sh /media/sda1/install.sh shepherd install   # Phase 2 aktivieren
#   sh /media/sda1/install.sh shepherd remove    # Phase 2 deaktivieren
#   sh /media/sda1/install.sh shepherd status    # Shepherd Status
#
# Phase 1 und Phase 2 sind exklusiv. Aktivieren von Phase 2 deaktiviert
# automatisch Phase 1, und umgekehrt.
#
# Achtung: Erfordert root. Auf der ST10 sind wir das ohnehin.

set -u

STICK="/media/sda1"
RC_SRC="$STICK/rc.local"
RC_DST="/mnt/nv/rc.local"
RUN_SRC="$STICK/run.sh"
RUN_OVERRIDE="/mnt/nv/streborn/run-override.sh"
PRESETS_SRC="$STICK/presets.json"
PRESETS_DST="/mnt/nv/streborn/presets.json"
BIN="$STICK/streborn-armv7l"
[ -x "$BIN" ] || BIN="$STICK/streborn"

# Sicherstellen dass NAND Persist Verzeichnis existiert.
mkdir -p /mnt/nv/streborn/bin 2>/dev/null

phase1_install() {
    if [ ! -f "$RC_SRC" ]; then
        echo "FEHLER: $RC_SRC nicht gefunden" >&2
        exit 1
    fi
    # Phase 2 vorher entfernen wenn aktiv
    if [ -d /mnt/nv/shepherd ]; then
        echo "Phase 2 ist aktiv, entferne sie zuerst"
        phase2_remove
    fi
    echo "Kopiere $RC_SRC nach $RC_DST"
    cp "$RC_SRC" "$RC_DST" || { echo "FEHLER beim Kopieren" >&2; exit 1; }
    chmod +x "$RC_DST"

    # run.sh in NAND als run-override.sh deployen. Damit ist NAND die
    # Source of Truth fuer den Boot - SD card I/O Probleme oder veraltete
    # run.sh auf der SD machen kein Problem mehr.
    if [ -f "$RUN_SRC" ]; then
        echo "Kopiere $RUN_SRC nach $RUN_OVERRIDE"
        cp "$RUN_SRC" "$RUN_OVERRIDE"
        chmod +x "$RUN_OVERRIDE"
    fi

    # presets.json von SD nach NAND migrieren beim ersten Install.
    # SD card wirft oft I/O Error beim Schreiben (FAT32), deshalb haelt
    # der Agent die presets.json auf NAND.
    if [ -f "$PRESETS_SRC" ] && [ ! -f "$PRESETS_DST" ]; then
        echo "Migriere presets.json nach NAND"
        cp "$PRESETS_SRC" "$PRESETS_DST"
    fi

    ls -la "$RC_DST" "$RUN_OVERRIDE" 2>/dev/null
    echo "Phase 1 aktiv. Beim naechsten Boot wird $RC_DST ausgefuehrt."
    echo "NAND Override aktiv: Agent laeuft via $RUN_OVERRIDE"
}

phase1_remove() {
    if [ -e "$RC_DST" ]; then
        rm -f "$RC_DST"
        echo "Phase 1 entfernt: $RC_DST"
    else
        echo "Phase 1 war nicht aktiv"
    fi
    if [ -e "$RUN_OVERRIDE" ]; then
        rm -f "$RUN_OVERRIDE"
        echo "NAND Override entfernt: $RUN_OVERRIDE"
    fi
}

phase1_status() {
    if [ -x "$RC_DST" ]; then
        echo "Phase 1 (rc.local direct): aktiv"
        ls -la "$RC_DST"
    else
        echo "Phase 1 (rc.local direct): inaktiv"
    fi
    if [ -x "$RUN_OVERRIDE" ]; then
        echo "NAND Override: aktiv"
        ls -la "$RUN_OVERRIDE"
    else
        echo "NAND Override: nicht installiert"
    fi
    if [ -f "$PRESETS_DST" ]; then
        cnt=$(grep -c '"slot"' "$PRESETS_DST" 2>/dev/null || echo 0)
        echo "Presets auf NAND: $cnt Eintraege"
    fi
}

phase2_install() {
    if [ ! -x "$BIN" ]; then
        echo "FEHLER: Agent Binary nicht ausfuehrbar: $BIN" >&2
        exit 1
    fi
    # Phase 1 vorher entfernen wenn aktiv
    if [ -x "$RC_DST" ]; then
        echo "Phase 1 ist aktiv, entferne sie zuerst"
        phase1_remove
    fi
    echo "Aktiviere Phase 2: shepherd install ueber $BIN"
    "$BIN" shepherd install || { echo "FEHLER" >&2; exit 1; }
    echo
    echo "Phase 2 aktiv. Beim naechsten Boot startet shepherdd unseren Agent."
    echo "Empfehlung: jetzt 'reboot' oder 'kill -HUP \$(pgrep shepherdd)' damit es sofort wirkt."
}

phase2_remove() {
    if [ -x "$BIN" ]; then
        "$BIN" shepherd remove || true
    else
        rm -rf /mnt/nv/shepherd
        echo "Phase 2 entfernt (per rm, Binary war nicht da)"
    fi
}

phase2_status() {
    if [ -x "$BIN" ]; then
        "$BIN" shepherd status
    else
        if [ -d /mnt/nv/shepherd ]; then
            echo "Phase 2: Verzeichnis vorhanden, Binary aber nicht ausfuehrbar"
            ls -la /mnt/nv/shepherd
        else
            echo "Phase 2 (shepherd integration): inaktiv"
        fi
    fi
}

case "${1:-install}" in
  install)        phase1_install ;;
  remove|uninstall) phase1_remove ;;
  status)         phase1_status; echo; phase2_status ;;
  shepherd)
    case "${2:-status}" in
      install)        phase2_install ;;
      remove|uninstall) phase2_remove ;;
      status)         phase2_status ;;
      *)
        echo "Verwendung: $0 shepherd {install|remove|status}" >&2
        exit 1
        ;;
    esac
    ;;
  *)
    echo "Verwendung: $0 [install|remove|status|shepherd <install|remove|status>]" >&2
    exit 1
    ;;
esac
