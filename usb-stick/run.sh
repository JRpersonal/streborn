#!/bin/sh
# run.sh v2: NAND Cache ist Source of Truth.
#
# Geaendert gegenueber v1 (15.05.2026):
#   - Stick Binary auf SD card wird NICHT automatisch auf NAND kopiert.
#     Damit verschwinden manuell deployte NAND Updates nicht beim Reboot.
#   - Stick Binary wird nur noch als FALLBACK genutzt, wenn NAND leer ist.
#   - Manuelles Stick->NAND Sync: touch /mnt/nv/streborn/sync-from-stick
#     Dann wird beim naechsten Boot vom Stick gesynct.
#
# Auf der Box installieren: scp setup/run.sh stbox:/media/sda1/run.sh

set -u

STICK="/media/sda1"
PERSIST="/mnt/nv/streborn"
# Aktiver Log liegt in tmpfs (/tmp) damit der NAND Flash im Dauerbetrieb
# nicht abgenutzt wird. Bei jedem Start retten wir den vorherigen Log
# nach NAND als previous.log (ueberlebt einen Box Reboot).
LOG="/tmp/streborn-agent.log"
PREV_LOG="$PERSIST/previous.log"
PIDFILE="$PERSIST/agent.pid"
SYNC_FLAG="$PERSIST/sync-from-stick"

# Vorherige Session in NAND retten bevor wir den tmpfs Log
# ueberschreiben — damit haben wir nach jedem Crash / Reboot noch den
# letzten Log zur Hand.
if [ -f "$LOG" ] && [ -s "$LOG" ]; then
    cp "$LOG" "$PREV_LOG" 2>/dev/null
fi
: > "$LOG"

STICK_BIN="$STICK/streborn-armv7l"
[ -e "$STICK_BIN" ] || STICK_BIN="$STICK/streborn"
CACHED_BIN="$PERSIST/bin/streborn-armv7l"
STICK_VER_FILE="$STICK/version.txt"
NAND_VER_FILE="$PERSIST/version.txt"

mkdir -p "$PERSIST/bin" "$PERSIST/logs" "$PERSIST/state" 2>/dev/null

log() {
    echo "$(date): $*" >> "$LOG"
}

# Auto-sync trigger: a freshly prepared stick (Setup-Wizard) ships a
# version.txt. If that string differs from the version we recorded
# for the NAND cache the stick is authoritative — the user just
# prepared it explicitly, so apply it. Without this, sync only
# happens when something else touches $SYNC_FLAG, but the flag
# path is on NAND and the Setup Wizard cannot write there.
maybe_force_sync_on_version_mismatch() {
    if [ ! -r "$STICK_VER_FILE" ]; then
        return 0
    fi
    STICK_VER=$(cat "$STICK_VER_FILE" 2>/dev/null | tr -d '\r\n')
    NAND_VER=""
    if [ -r "$NAND_VER_FILE" ]; then
        NAND_VER=$(cat "$NAND_VER_FILE" 2>/dev/null | tr -d '\r\n')
    fi
    if [ -z "$STICK_VER" ]; then
        return 0
    fi
    if [ "$STICK_VER" = "$NAND_VER" ]; then
        return 0
    fi
    log "version mismatch: stick='$STICK_VER' nand='$NAND_VER' — forcing sync"
    touch "$SYNC_FLAG"
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
        # Record the version that now lives in the NAND cache so
        # the next boot's version check has something to compare
        # against.
        if [ -r "$STICK_VER_FILE" ]; then
            cp "$STICK_VER_FILE" "$NAND_VER_FILE" 2>/dev/null
            log "NAND version.txt aktualisiert: $(cat "$NAND_VER_FILE" 2>/dev/null)"
        fi
        rm -f "$SYNC_FLAG"
        return 0
    fi
    log "Sync gescheitert (Stick I/O Error?), behalte NAND Cache"
    rm -f "$CACHED_BIN.new"
    rm -f "$SYNC_FLAG"
    return 1
}

# Defense in depth: if the NAND cache is empty and the stick mount
# is still racing in (rc.local should have waited, but a direct
# invocation of run.sh may skip that), give the stick up to 20s to
# appear. Otherwise the version-mismatch sync below has nothing to
# work with and we abort immediately.
if [ ! -x "$CACHED_BIN" ]; then
    j=0
    while [ $j -lt 20 ]; do
        if [ -e "$STICK_BIN" ] || [ -e "$STICK_VER_FILE" ]; then
            log "stick became visible after ${j}s wait"
            break
        fi
        sleep 1
        j=$((j+1))
    done
fi

# Defense in depth: redeploy rc.local + run-override.sh from stick
# if newer. rc.local itself does the same — but if a buggy
# rc.local on NAND skipped that step (e.g. older release without
# the self-update block), this run.sh invocation can still fix it
# so the NEXT boot uses the fresh files.
if [ -f /media/sda1/rc.local ] && [ /media/sda1/rc.local -nt /mnt/nv/rc.local ]; then
    cp /media/sda1/rc.local /mnt/nv/rc.local 2>/dev/null
    chmod +x /mnt/nv/rc.local 2>/dev/null
    log "redeployed /mnt/nv/rc.local from stick (effective next boot)"
fi
if [ -f /media/sda1/run.sh ] && [ /media/sda1/run.sh -nt /mnt/nv/streborn/run-override.sh ]; then
    cp /media/sda1/run.sh /mnt/nv/streborn/run-override.sh 2>/dev/null
    chmod +x /mnt/nv/streborn/run-override.sh 2>/dev/null
    log "redeployed /mnt/nv/streborn/run-override.sh from stick (effective next boot)"
fi

maybe_force_sync_on_version_mismatch
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

# === WLAN Provisioning aus wlan.conf vom Stick ===
# Wenn der User beim Stick Setup in der Desktop App WLAN Daten eingegeben
# hat, liegt eine wlan.conf JSON Datei auf der SD Karte. Wir verarbeiten
# die einmal und loeschen sie danach damit Credentials nicht offen liegen.
WLAN_CONF="$STICK/wlan.conf"
if [ -f "$WLAN_CONF" ]; then
    SSID=$(sed -n 's/.*"ssid":"\([^"]*\)".*/\1/p' "$WLAN_CONF" | head -1)
    PASS=$(sed -n 's/.*"password":"\([^"]*\)".*/\1/p' "$WLAN_CONF" | head -1)
    if [ -n "$SSID" ]; then
        log "wlan.conf gefunden, provisioniere wpa_supplicant fuer SSID '$SSID'"
        # Bose wpa_supplicant laeuft mit -c /etc/wpa_supplicant.conf.
        # WICHTIG: wir schreiben die Datei KOMPLETT neu mit nur einem
        # network={} Block. Frueheres Append fuehrt zu Chaos wenn alte
        # Profile drin sind — Box probiert dann nicht erreichbare Netze
        # zuerst und kann ewig hangen. Sauberer Reset = ein WLAN gilt.
        WPA_CONF="/etc/wpa_supplicant.conf"
        # Header in tmp Datei aufbauen, dann atomar nach Ziel
        TMP="/tmp/wpa_supplicant.conf.new"
        cat > "$TMP" <<WPAEOF
ctrl_interface=DIR=/var/run/wpa_supplicant GROUP=root
update_config=1
eapol_version=1
ap_scan=1
fast_reauth=1
config_methods=virtual_display virtual_push_button keypad

network={
    ssid="$SSID"
    psk="$PASS"
    key_mgmt=WPA-PSK
}
WPAEOF
        # Backup der alten Config (falls /etc Schreiben fehl schlaegt
        # ueberleben wir mit dem Backup im Persist Bereich).
        cp "$WPA_CONF" "$PERSIST/wpa_supplicant.conf.bak" 2>/dev/null
        # Direktes Schreiben — falls /etc read only ist, faellt das hier
        # auf, dann muessen wir den Inhalt streamen statt mv.
        if cat "$TMP" > "$WPA_CONF" 2>/dev/null; then
            log "wpa_supplicant.conf komplett ueberschrieben mit nur '$SSID'"
        else
            log "WARN: /etc/wpa_supplicant.conf nicht schreibbar — versuche bind mount"
            mount --bind "$TMP" "$WPA_CONF" 2>/dev/null && log "bind mount aktiv"
        fi
        # Restart wpa_supplicant damit neue Config sofort greift.
        # Bei Reload haengt sich die Box manchmal — daher Restart statt SIGHUP.
        if pidof wpa_supplicant >/dev/null 2>&1; then
            killall wpa_supplicant 2>/dev/null
            sleep 1
            wpa_supplicant -B -i wlan0 -s -c "$WPA_CONF" -D nl80211 2>/dev/null &
        fi
        # WLAN config nur einmal anwenden, dann loeschen damit das Passwort
        # nicht auf der SD card stehen bleibt
        rm -f "$WLAN_CONF" 2>/dev/null
        log "WLAN provisioniert, wlan.conf vom Stick geloescht"
    fi
fi

# === Region Provisioning aus region.conf vom Stick ===
# User hat im Setup Wizard ein Land gewaehlt. Persistieren nach NAND,
# danach vom Stick loeschen.
REGION_CONF="$STICK/region.conf"
REGION_NAND="$PERSIST/region.txt"
if [ -f "$REGION_CONF" ]; then
    CC=$(sed -n 's/.*"country":"\([^"]*\)".*/\1/p' "$REGION_CONF" | head -1)
    if [ -n "$CC" ]; then
        echo "$CC" > "$REGION_NAND"
        log "Region '$CC' aus region.conf nach NAND persistiert"
        rm -f "$REGION_CONF" 2>/dev/null
    fi
fi

# === Box Name aus name.conf vom Stick ===
# User hat im Setup einen Namen vergeben (z.B. "Wohnzimmer"). Persistieren
# nach NAND, der Agent wendet ihn beim ersten Boot auf die Box an + UID.
NAME_CONF="$STICK/name.conf"
NAME_NAND="$PERSIST/name.txt"
if [ -f "$NAME_CONF" ]; then
    NAME=$(sed -n 's/.*"name":"\([^"]*\)".*/\1/p' "$NAME_CONF" | head -1)
    if [ -n "$NAME" ]; then
        echo "$NAME" > "$NAME_NAND"
        log "Box Name '$NAME' aus name.conf nach NAND persistiert"
        rm -f "$NAME_CONF" 2>/dev/null
    fi
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

# === Stick Binary → NAND Auto Update ===
# Wenn der User die App aktualisiert hat und einen frischen Stick
# beschrieben, liegt das neue Binary auf dem Stick. Wir wollen dass die
# Box es automatisch uebernimmt — sonst bleibt sie ewig auf dem Build
# von der Erst Provisionierung haengen. Vergleich via version.txt:
# Stick schreibt da seinen Build Stamp rein, NAND bekommt eine Kopie
# nach erfolgreichem Update.
STICK_BIN="$STICK/streborn-armv7l"
NAND_BIN="/mnt/nv/streborn/bin/streborn-armv7l"
STICK_VER=$(cat "$STICK/version.txt" 2>/dev/null | head -1)
NAND_VER=$(cat "$PERSIST/version.txt" 2>/dev/null | head -1)
if [ -n "$STICK_VER" ] && [ "$STICK_VER" != "$NAND_VER" ] && [ -f "$STICK_BIN" ]; then
    log "Auto Update: Stick Version '$STICK_VER' != NAND Version '$NAND_VER' — Binary uebernehmen"
    if cp "$STICK_BIN" "${NAND_BIN}.new" 2>/dev/null; then
        chmod +x "${NAND_BIN}.new"
        if mv "${NAND_BIN}.new" "$NAND_BIN" 2>/dev/null; then
            echo "$STICK_VER" > "$PERSIST/version.txt"
            log "Auto Update OK: NAND Binary jetzt $STICK_VER"
            # BIN ist die Variable die start_agent unten verwendet —
            # zeigt automatisch auf NAND_BIN, kein extra Reload noetig.
        else
            log "Auto Update FAIL: mv NAND_BIN gescheitert"
            rm -f "${NAND_BIN}.new"
        fi
    else
        log "Auto Update FAIL: cp Stick -> NAND.new gescheitert"
    fi
fi

# === Agent starten ===
# Presets liegen auf NAND (read/write). SD card ist FAT32 und wirft oft
# I/O Error bei Schreibversuchen, deshalb wird die Liste auf NAND gehalten.
# Erste Migration vom Stick falls NAND noch leer.
PRESETS_NAND="$PERSIST/presets.json"
if [ ! -f "$PRESETS_NAND" ] && [ -r "$STICK/presets.json" ]; then
    cp "$STICK/presets.json" "$PRESETS_NAND" 2>/dev/null
    log "presets.json von Stick nach NAND uebertragen"
fi

start_agent() {
    nohup "$BIN" \
        --presets "$PRESETS_NAND" \
        --region-file "$PERSIST/region.txt" \
        --pending-name-file "$PERSIST/name.txt" \
        --listen-webui :8888 \
        --listen-marge :9080 \
        --listen-marge-tls :443 \
        --listen-bmx :8081 \
        --box-host 127.0.0.1 \
        --hosts /etc/hosts \
        --apply-hosts=true \
        --tls=true \
        --log-level warn \
        >> "$LOG" 2>&1 &
    AGENT_PID=$!
    echo "$AGENT_PID" > "$PIDFILE"
}

start_agent
log "Agent gestartet mit PID $AGENT_PID"

# Watchdog: Agent neu starten wenn er crashed. Kosten pro Check:
# 1 sleep + 1 read aus Page Cache + 1 kill -0 Syscall — vernachlaessigbar
# (~50us pro Minute). Kein Flash Write im Normalbetrieb, nur bei
# tatsaechlichem Restart wird agent.pid einmal neu geschrieben.
(
    while true; do
        sleep 60
        if [ ! -f "$PIDFILE" ]; then
            break  # PID File weg, run.sh wird neu durchlaufen
        fi
        CUR_PID=$(cat "$PIDFILE" 2>/dev/null || echo 0)
        if [ -n "$CUR_PID" ] && [ "$CUR_PID" -gt 0 ] && kill -0 "$CUR_PID" 2>/dev/null; then
            continue  # Agent laeuft noch
        fi
        log "Watchdog: Agent (PID $CUR_PID) gestorben, restart"
        start_agent
        log "Watchdog: Agent neu gestartet mit PID $AGENT_PID"
    done
) &

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

# === USB Stick komplett aushaengen + SSH schliessen ===
# Bootstrap ist durch — alle Configs (wlan/region/name/presets/binary)
# sind nach NAND uebernommen. Den Stick brauchen wir zur Laufzeit
# nicht mehr. Wir haengen ihn aktiv aus damit:
#   1. User den Stick im Betrieb ziehen kann ohne dirty FS (Windows
#      muss dann keine FAT Reparatur mehr machen).
#   2. SSH wird sauber gestoppt — Bose's sshd Init hat ihn nur
#      gestartet weil remote_services auf dem Stick lag. Nach umount
#      ist diese Datei nicht mehr lesbar; wir stoppen sshd zusaetzlich.
#
# Detached Hintergrund Block damit run.sh sofort returnen kann
# (shelby_local will dass rc.local schnell durchlaeuft). Lange
# Wartezeit + aktive Pruefung dass kein Prozess mehr den Stick
# offen hat, bevor wir tatsaechlich umount machen — sonst riskieren
# wir den Agent zu verwirren weil seine Goroutines (syncRunOverride,
# initialBoxPresetSync) noch lesen.
(
    # 60 s grundsaetzliche Wartezeit: deckt syncRunOverrideFromStick
    # (5 s) + initialBoxPresetSync (12 s) + autopair Timing + Sicherheits
    # Puffer. Sollte fuer alle einmal-pro-Boot Stick Lesevorgaenge reichen.
    sleep 60

    if ! mountpoint -q "$STICK" 2>/dev/null && ! mount | grep -q " $STICK "; then
        exit 0  # schon ausgehaengt, nichts zu tun
    fi

    # Aktive Pruefung: wer haelt noch ein File auf dem Stick offen?
    # Wir scannen /proc/*/fd/* nach Links die auf $STICK zeigen. Wenn
    # noch jemand drauf zugreift, warten wir nochmal — bis zu 90 s
    # zusaetzlich. Danach erzwingen wir den umount nicht, sondern
    # fallen auf Read Only Remount zurueck (Flash Wear Schutz).
    STICK_DEV=$(mount | grep " $STICK " | awk '{print \$1}')
    WAIT_BUSY=0
    while [ "$WAIT_BUSY" -lt 90 ]; do
        BUSY=$(ls -l /proc/*/fd/* 2>/dev/null | grep " $STICK/" | head -1)
        if [ -z "$BUSY" ]; then
            break
        fi
        sleep 5
        WAIT_BUSY=$((WAIT_BUSY+5))
    done

    sync
    if umount "$STICK" 2>/dev/null; then
        log "USB Stick ausgehaengt — kann sicher gezogen werden"
        if [ -x /etc/init.d/sshd ]; then
            /etc/init.d/sshd stop 2>/dev/null && log "sshd gestoppt"
        else
            killall sshd 2>/dev/null && log "sshd via killall beendet"
        fi
    else
        log "umount fehlgeschlagen (Prozess haelt Stick), versuche read only remount"
        if mount -o remount,ro "$STICK" 2>/dev/null; then
            log "USB Stick read only remounted"
        fi
    fi
) &
