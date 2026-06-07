#!/bin/sh
# run.sh v2: the NAND cache is the source of truth for the agent binary.
#
# DEPLOY PROCEDURE -- read this before pushing a new agent build:
#   - ALWAYS copy the new binary to BOTH locations, never just one:
#       stick: /media/sda1/streborn-armv7l
#       NAND : /mnt/nv/streborn/bin/streborn-armv7l
#     NAND is what boots; the stick is the recovery/fallback copy and must
#     not be left stale. Deploying to NAND only silently rots the stick.
#   - After deploying, REBOOT the box. Do NOT kill+respawn the running agent
#     by hand: the live process keeps the old binary mapped, and a manual
#     restart has proven unreliable. A clean reboot loads the new binary.
#   - Verify the running version from the log with `tail` (not head).
#
# Boot-time behaviour (the logic below):
#   - The stick binary is NOT auto-copied to NAND on every boot, so a manual
#     NAND deploy survives a reboot.
#   - The stick binary is used only as a FALLBACK when the NAND cache is empty.
#   - To force a one-off stick->NAND sync: touch /mnt/nv/streborn/sync-from-stick
#     (applied on the next boot).
#
# Install on the box: scp setup/run.sh stbox:/media/sda1/run.sh

set -u

STICK="/media/sda1"
PERSIST="/mnt/nv/streborn"
LOG="$PERSIST/agent.log"
PIDFILE="$PERSIST/agent.pid"
SYNC_FLAG="$PERSIST/sync-from-stick"

STICK_BIN="$STICK/streborn-armv7l"
[ -e "$STICK_BIN" ] || STICK_BIN="$STICK/streborn"
CACHED_BIN="$PERSIST/bin/streborn-armv7l"

mkdir -p "$PERSIST/bin" "$PERSIST/logs" "$PERSIST/state" 2>/dev/null

log() {
    echo "$(date): $*" >> "$LOG"
}

# NUR wenn Sync Flag gesetzt ist: Stick -> NAND kopieren.
# Standard: NAND ist Source of Truth.
sync_from_stick_if_requested() {
    if [ ! -f "$SYNC_FLAG" ]; then
        return 0
    fi
    if [ ! -r "$STICK_BIN" ]; then
        log "Sync angefordert aber Stick Binary nicht lesbar, ignoriere"
        rm -f "$SYNC_FLAG"
        return 1
    fi
    if cp "$STICK_BIN" "$CACHED_BIN.new" 2>/dev/null; then
        chmod +x "$CACHED_BIN.new"
        mv "$CACHED_BIN.new" "$CACHED_BIN"
        log "Stick Binary in NAND Cache gesynct"
        rm -f "$SYNC_FLAG"
        return 0
    fi
    log "Sync gescheitert (Stick I/O Error?), behalte NAND Cache"
    rm -f "$CACHED_BIN.new"
    rm -f "$SYNC_FLAG"
    return 1
}

sync_from_stick_if_requested

# Binary Auswahl: NAND Cache zuerst, Stick als Fallback.
if [ -x "$CACHED_BIN" ]; then
    BIN="$CACHED_BIN"
elif [ -x "$STICK_BIN" ]; then
    BIN="$STICK_BIN"
    log "Kein NAND Cache, nutze Stick Binary direkt"
else
    log "FEHLER: weder NAND Cache noch Stick Binary verfuegbar"
    exit 1
fi

# === Schon laufender Agent? Dann Stop. ===
if [ -f "$PIDFILE" ]; then
    OLDPID=$(cat "$PIDFILE" 2>/dev/null || echo 0)
    if [ -n "$OLDPID" ] && kill -0 "$OLDPID" 2>/dev/null; then
        log "Alter Agent laeuft noch (PID $OLDPID), stoppe ihn"
        kill -TERM "$OLDPID" 2>/dev/null
        sleep 2
        kill -KILL "$OLDPID" 2>/dev/null
    fi
    rm -f "$PIDFILE"
fi

# === Optional Update anwenden ===
if [ -x "$STICK/update.sh" ]; then
    log "Pruefe Update via $STICK/update.sh"
    "$STICK/update.sh" 2>&1 | tee -a "$LOG" || true
fi

# === Hosts Block via bind mount schreibbar machen (rootfs ro) ===
mount | grep -q '/etc/hosts' || {
    cp /etc/hosts /tmp/hosts.original 2>/dev/null
    cp /etc/hosts /tmp/hosts.live 2>/dev/null
    mount --bind /tmp/hosts.live /etc/hosts 2>/dev/null
}

# === iptables NAT optional ===
if command -v iptables >/dev/null 2>&1; then
    log "iptables NAT verfuegbar"
else
    log "iptables NAT nicht verfuegbar, Marge laeuft direkt auf 443"
fi

log "bind mount auf /etc/hosts aktiv"
log "Starte Agent Version $(${BIN} --version 2>/dev/null || echo v0.0.0)"

# === Agent starten ===
nohup "$BIN" \
    --presets "$STICK/presets.json" \
    --listen-webui :8888 \
    --listen-marge :9080 \
    --listen-marge-tls :443 \
    --listen-bmx :8081 \
    --hosts /etc/hosts \
    --apply-hosts=true \
    --tls=true \
    --log-level info \
    >> "$LOG" 2>&1 &

NEW_PID=$!
echo "$NEW_PID" > "$PIDFILE"
log "Agent gestartet mit PID $NEW_PID"

# === Root CA in System Trust Store mounten ===
ROOT_CA="$PERSIST/ca/root.crt"
WAIT=0
while [ ! -r "$ROOT_CA" ] && [ "$WAIT" -lt 20 ]; do
    sleep 1
    WAIT=$((WAIT+1))
done

if [ -r "$ROOT_CA" ]; then
    log "Root CA vorhanden nach ${WAIT}s, setze bind mount"

    for target in /etc/pki/tls/certs/ca-bundle.crt /etc/ssl/certs/ca-certificates.crt; do
        if [ ! -f "$target" ]; then continue; fi
        bundle="/tmp/streborn-bundle$(echo "$target" | md5sum | head -c 1).crt"
        cp "$target" "$bundle" 2>/dev/null
        cat "$ROOT_CA" >> "$bundle"
        echo "# <<< STR Root CA <<<" >> "$bundle"
        if mount | grep -q "$target"; then
            umount "$target" 2>/dev/null
        fi
        mount --bind "$bundle" "$target" 2>/dev/null && echo "bind mount aktiv: $bundle -> $target"
    done
fi

log "Bootstrap abgeschlossen"
