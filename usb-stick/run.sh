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

# STICK path discovery: Bose's udev rule mounts the USB stick at
# /media/sda1 on every model we have observed (ST10 micro-USB,
# ST20/30 USB-A — same /etc/udev/scripts/mount.sh). Keep sda1 as
# the primary path so existing boxes do not change behaviour, and
# probe /media/sd[a-d]1 as a fallback if a firmware variant ever
# numbers differently (defensive, no live evidence of this yet).
STICK="/media/sda1"
if [ ! -e "$STICK/run.sh" ] && [ ! -e "$STICK/streborn-armv7l" ]; then
    for cand in /media/sdb1 /media/sdc1 /media/sdd1; do
        if [ -e "$cand/run.sh" ] || [ -e "$cand/streborn-armv7l" ]; then
            STICK="$cand"
            break
        fi
    done
fi
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

# setup_log mirrors the message to a stick-local setup.log so the
# user can pull the diagnostic without SSH. The path lives on the
# FAT32 stick and survives a Bose factory reset (Bose's reset wipes
# NAND, not the stick). Append mode so multiple boot cycles
# accumulate. Best-effort: if /media/sda1 is read-only or full, we
# silently fall through; the in-tmpfs log() above still has it.
#
# Each line carries kernel-uptime so we can correlate phases without
# the Bose clock (which is wrong until WLAN+NTP succeed — early lines
# read "Mon Jul  6 20:15:06 GMT 2015" and only become real after
# association). Uptime is monotonic from kernel boot and unaffected.
SETUP_LOG="$STICK/setup.log"
# NAND mirror so a stick-less normal-boot (no /media/sda1 mount or
# stick yanked) still records the WLAN provisioning trace. Without
# this every reboot without the stick was a black box — we could
# not see whether Approach C/D even ran. NAND survives reboot,
# survives a Bose factory reset (we have observed this), and rotates
# only via the previous.log copy at the top of run.sh.
SETUP_LOG_NAND="$PERSIST/setup.log"
uptime_s() {
    awk '{print int($1)}' /proc/uptime 2>/dev/null || echo "?"
}
setup_log() {
    log "$*"
    line="[up=$(uptime_s)s] $(date): $*"
    echo "$line" >> "$SETUP_LOG" 2>/dev/null
    echo "$line" >> "$SETUP_LOG_NAND" 2>/dev/null
}

# initial_snapshot writes a one-shot record of "what does the box
# look like the moment run.sh starts" so we can compare variants and
# see what was already initialised by Bose vs what we had to wait
# for. Cheap: a handful of file reads, no network.
initial_snapshot() {
    # Fat boot-marker so that the same stick visiting multiple
    # speakers (or the same speaker over many boots) produces a log
    # a human can scroll through and instantly see boundaries. The
    # hostname / variant / MAC fields below identify which box a
    # block belongs to even if Jens swaps the stick between rooms.
    {
        echo ""
        echo "########################################################################"
        echo "### BOOT MARKER  $(date)  uptime=$(uptime_s)s"
        echo "###   host=$(hostname 2>/dev/null)  mac0=$(cat /sys/class/net/wlan0/address 2>/dev/null)  mac1=$(cat /sys/class/net/wlan1/address 2>/dev/null)"
        echo "###   variant=$(head -c 40 /etc/Variant 2>/dev/null | tr -d '\n')  version=$(head -c 80 /etc/version 2>/dev/null | tr -d '\n')"
        echo "########################################################################"
    } >> "$SETUP_LOG" 2>/dev/null
    setup_log "=== initial snapshot ==="
    setup_log "kernel: $(uname -a 2>/dev/null | head -c 200)"
    if [ -r /etc/version ]; then
        setup_log "bose /etc/version: $(head -c 200 /etc/version 2>/dev/null | tr '\n' ' ')"
    fi
    if [ -r /etc/Variant ]; then
        setup_log "bose /etc/Variant: $(head -c 80 /etc/Variant 2>/dev/null | tr '\n' ' ')"
    fi
    setup_log "loadavg: $(cat /proc/loadavg 2>/dev/null)"
    setup_log "meminfo: $(grep -E 'MemTotal|MemFree|MemAvailable' /proc/meminfo 2>/dev/null | tr '\n' ' ')"
    # Mount state: which filesystems are up and how (ro/rw matters
    # most — rootfs read-only is the reason Approach A had to fall
    # back to bind mount on the first run).
    setup_log "mounts: $(mount 2>/dev/null | awk '{print $1\":\"$3\":\"$6}' | tr '\n' '|' | head -c 600)"
    # Probe writability of the four paths we care about.
    for p in /etc /mnt/nv /tmp /media/sda1; do
        if [ -d "$p" ] && touch "$p/.streborn-write-probe" 2>/dev/null; then
            rm -f "$p/.streborn-write-probe"
            setup_log "writable: $p YES"
        else
            setup_log "writable: $p NO"
        fi
    done
    setup_log "interfaces: $(ls /sys/class/net 2>/dev/null | tr '\n' ' ')"
    setup_log "wpa_supplicant pid: $(pidof wpa_supplicant 2>/dev/null || echo none)"
    setup_log "processes (head): $(ps 2>/dev/null | head -20 | tr '\n' '|' | head -c 800)"
    setup_log "=== /initial snapshot ==="
}

# wait_for_ready blocks until the prerequisites for talking to Bose
# are actually in place. Without this we used to hit /etc/wpa_supplicant.conf
# at uptime 0 — the rootfs was still ro and wpa_supplicant had not
# even started, so Approach A always degraded to bind mount and B
# had to wait alone. The gate has hard timeouts so a broken box
# does not freeze the boot.
wait_for_ready() {
    setup_log "wait-for-ready: begin"
    # /etc writable: Bose rootfs is ALWAYS ro, the 60s wait we used
    # to do here was pure latency burn — bind-mount fallback works
    # immediately. 3s spin is enough to catch a rare case where Bose
    # remounts late on a future firmware variant.
    i=0
    while [ $i -lt 3 ]; do
        if touch /etc/.streborn-write-probe 2>/dev/null; then
            rm -f /etc/.streborn-write-probe
            setup_log "wait-for-ready: /etc writable after ${i}s wait"
            break
        fi
        sleep 1
        i=$((i + 1))
    done
    if [ $i -ge 3 ]; then
        setup_log "wait-for-ready: /etc ro (expected on Bose) — A uses bind-mount fallback"
    fi
    # wpa_supplicant: needed by Approach A for killall+restart and by
    # Approach C for wpa_cli ctrl interface. Phase probe on rhino ST10
    # showed it consistently up by uptime=34s; 25s cap covers boot
    # variance without burning idle time on slower variants.
    i=0
    while [ $i -lt 25 ]; do
        if pidof wpa_supplicant >/dev/null 2>&1; then
            setup_log "wait-for-ready: wpa_supplicant pid present after ${i}s wait"
            break
        fi
        sleep 1
        i=$((i + 1))
    done
    if [ $i -ge 25 ]; then
        setup_log "wait-for-ready: wpa_supplicant not running after 25s, A skips restart"
    fi
    setup_log "wait-for-ready: end at $(uptime | tr -s ' ')"
}

# background_phase_probe records the first uptime at which each known
# Bose service becomes reachable, then writes a one-line summary.
# Pure observation — never blocks, never retries provisioning. The
# summary is what we use to tune timeouts on new firmware variants
# without needing SSH.
background_phase_probe() {
    (
        BOSE_HTTP=0; BOSE_WS=0; AVT=0; WPA=0; WLAN_UP=0
        DEADLINE_UP=$(( $(uptime_s) + 240 ))
        while [ "$(uptime_s)" -lt "$DEADLINE_UP" ]; do
            UP=$(uptime_s)
            if [ "$WPA" -eq 0 ] && pidof wpa_supplicant >/dev/null 2>&1; then
                WPA=$UP
                setup_log "phase: wpa_supplicant up at uptime=${UP}s"
            fi
            if [ "$BOSE_HTTP" -eq 0 ] && wget -qO- -T 2 http://127.0.0.1:8090/info >/dev/null 2>&1; then
                BOSE_HTTP=$UP
                setup_log "phase: BoseApp HTTP :8090 up at uptime=${UP}s"
            fi
            if [ "$BOSE_WS" -eq 0 ]; then
                # gabbo speaks HTTP-Upgrade; a plain GET returns 400
                # but that proves the socket is bound. wget returns
                # non-zero on 400 so we look at connect via /dev/tcp
                # if shell supports it, else TCP-only probe via nc.
                if (echo > /dev/tcp/127.0.0.1/8080) >/dev/null 2>&1 \
                    || nc -z 127.0.0.1 8080 >/dev/null 2>&1; then
                    BOSE_WS=$UP
                    setup_log "phase: gabbo WS :8080 listening at uptime=${UP}s"
                fi
            fi
            if [ "$AVT" -eq 0 ]; then
                if (echo > /dev/tcp/127.0.0.1/8091) >/dev/null 2>&1 \
                    || nc -z 127.0.0.1 8091 >/dev/null 2>&1; then
                    AVT=$UP
                    setup_log "phase: AVTransport :8091 listening at uptime=${UP}s"
                fi
            fi
            if [ "$WLAN_UP" -eq 0 ] && [ -r /sys/class/net/wlan0/operstate ]; then
                STATE=$(cat /sys/class/net/wlan0/operstate 2>/dev/null)
                if [ "$STATE" = "up" ]; then
                    WLAN_UP=$UP
                    IPADDR=$(ip -4 addr show wlan0 2>/dev/null | sed -n 's/.*inet \([0-9.]*\).*/\1/p' | head -1)
                    setup_log "phase: wlan0 link up at uptime=${UP}s ip=${IPADDR:-none}"
                fi
            fi
            # Done early once everything is up.
            if [ "$WPA" -gt 0 ] && [ "$BOSE_HTTP" -gt 0 ] \
                && [ "$BOSE_WS" -gt 0 ] && [ "$AVT" -gt 0 ] \
                && [ "$WLAN_UP" -gt 0 ]; then
                break
            fi
            sleep 3
        done
        setup_log "phase summary: wpa=${WPA}s boseHTTP=${BOSE_HTTP}s gabbo=${BOSE_WS}s avt=${AVT}s wlan0Up=${WLAN_UP}s"
    ) &
}

# Snapshot the boot state right away and start the background phase
# probe so we capture First-Seen timestamps for BoseApp, gabbo, AVT,
# wlan0 — even if nothing further in this script touches them.
# Probe is best-effort: failures are silent, only successes log.
initial_snapshot
background_phase_probe

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
        log "sync requested but stick binary unreadable, ignoring"
        rm -f "$SYNC_FLAG"
        return 1
    fi
    if cp "$STICK_BIN" "$CACHED_BIN.new" 2>/dev/null; then
        chmod +x "$CACHED_BIN.new"
        mv "$CACHED_BIN.new" "$CACHED_BIN"
        log "stick binary synced to NAND cache"
        # Record the version that now lives in the NAND cache so
        # the next boot's version check has something to compare
        # against.
        if [ -r "$STICK_VER_FILE" ]; then
            cp "$STICK_VER_FILE" "$NAND_VER_FILE" 2>/dev/null
            log "NAND version.txt updated: $(cat "$NAND_VER_FILE" 2>/dev/null)"
        fi
        rm -f "$SYNC_FLAG"
        return 0
    fi
    log "sync failed (stick I/O error?), keeping NAND cache"
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
if [ -f "$STICK/rc.local" ] && [ "$STICK/rc.local" -nt /mnt/nv/rc.local ]; then
    cp "$STICK/rc.local" /mnt/nv/rc.local 2>/dev/null
    chmod +x /mnt/nv/rc.local 2>/dev/null
    log "redeployed /mnt/nv/rc.local from stick (effective next boot)"
fi
if [ -f "$STICK/run.sh" ] && [ "$STICK/run.sh" -nt /mnt/nv/streborn/run-override.sh ]; then
    cp "$STICK/run.sh" /mnt/nv/streborn/run-override.sh 2>/dev/null
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
    log "ERROR: neither NAND cache nor stick binary available"
    exit 1
fi

# === Schon laufender Agent? Dann Stop. ===
if [ -f "$PIDFILE" ]; then
    OLDPID=$(cat "$PIDFILE" 2>/dev/null || echo 0)
    if [ -n "$OLDPID" ] && kill -0 "$OLDPID" 2>/dev/null; then
        log "previous agent still running (PID $OLDPID), stopping it"
        kill -TERM "$OLDPID" 2>/dev/null
        sleep 2
        kill -KILL "$OLDPID" 2>/dev/null
    fi
    rm -f "$PIDFILE"
fi

# === Optional Update anwenden ===
if [ -x "$STICK/update.sh" ]; then
    log "checking for update via $STICK/update.sh"
    "$STICK/update.sh" 2>&1 | tee -a "$LOG" || true
fi

# === WLAN Provisioning aus wlan.conf vom Stick (multi-approach) ===
#
# Eine factory-reset Bose schreibt /etc/wpa_supplicant.conf beim
# Boot aus ihrer eigenen NetManager DB. Wenn dort kein Profil hinter
# legt ist, schmeisst sie unsere Direct-Write Variante beim naechsten
# Boot wieder raus. Deshalb fahren wir BEIDE Wege parallel:
#
#   A) Direct write nach /etc/wpa_supplicant.conf + wpa_supplicant
#      Restart. Greift sofort, Box ist binnen Sekunden im WLAN.
#   B) addWirelessProfile API call gegen 127.0.0.1:8090. Persistiert
#      das Profil in NetManagers eigener DB, ueberlebt damit den
#      naechsten Reboot ohne dass wir wlan.conf wieder lesen muessen.
#
# Was zuerst erfolgreich ist gewinnt. Jeder Schritt wird in
# /media/sda1/setup.log mit Timestamp geschrieben damit ein User
# das Stick einfach abziehen und Diagnose-Log via App hochladen kann
# (Bose's Factory Reset wischt NAND, der Stick bleibt unberuehrt).
# NAND-persisted credentials cache: once one of the WLAN provisioning
# approaches actually succeeded on a previous boot, we wrote the
# SSID+pass into $PERSIST/wlan-creds so subsequent boots can replay
# wpa_cli even though Bose's NetManager forgot the profile. Without
# this every reboot drops the box back to yellow because NetManager
# does not persist a wpa_cli-added network into its own DB.
WLAN_CREDS_NAND="$PERSIST/wlan-creds"
WLAN_CONF="$STICK/wlan.conf"
SSID=""
PASS=""
WLAN_SOURCE=""
if [ -f "$WLAN_CONF" ]; then
    SSID=$(sed -n 's/.*"ssid":"\([^"]*\)".*/\1/p' "$WLAN_CONF" | head -1)
    PASS=$(sed -n 's/.*"password":"\([^"]*\)".*/\1/p' "$WLAN_CONF" | head -1)
    WLAN_SOURCE="stick wlan.conf"
elif [ -r "$WLAN_CREDS_NAND" ]; then
    SSID=$(sed -n 's/^SSID=\(.*\)$/\1/p' "$WLAN_CREDS_NAND" | head -1)
    PASS=$(sed -n 's/^PASS=\(.*\)$/\1/p' "$WLAN_CREDS_NAND" | head -1)
    WLAN_SOURCE="NAND wlan-creds (replay)"
fi
# Ethernet-only short-circuit. If neither wlan0 nor wlan1 exists in
# /sys/class/net, the box has no wireless radio (driver did not load,
# or the chip is dead). Observed live on a scm-variant ST20 in #60
# where /addWirelessProfile sat on slow 500 responses for ~4 minutes
# per attempt, starving the agent start that lives after this block.
# We instead skip the whole block: the speaker is already reachable
# on its Ethernet cable per Bose's /networkInfo, and the desktop app
# install path works fine against eth0.
if [ ! -d /sys/class/net/wlan0 ] && [ ! -d /sys/class/net/wlan1 ]; then
    setup_log "WLAN: no wlan0/wlan1 interface present, ethernet-only mode (skip provisioning)"
    echo "ethernet-only" > "$PERSIST/wlan-mode" 2>/dev/null
    SSID=""
    PASS=""
fi

# Whole block runs in a backgrounded subshell so a slow Bose API
# (4 minute /addWirelessProfile loops, observed on #60) cannot delay
# start_agent below. Agent binds :8888 within seconds and the install
# wizard's 180s poll window succeeds even when WLAN provisioning is
# still trying its A/B/C/D fallbacks.
( if [ -n "$SSID" ] && [ -n "$PASS" ]; then
    setup_log "=== WLAN provisioning start (boot at $(uptime | tr -s ' ')) source=$WLAN_SOURCE ==="
    setup_log "wlan.conf parsed: SSID='$SSID' password_length=${#PASS}"
    if [ -n "$SSID" ] && [ -n "$PASS" ]; then
        # CRITICAL TIMING WINDOW. The very first time this code ran
        # successfully on a freshly factory-reset rhino ST10, B's
        # POST /addWirelessProfile returned 200 + AddWirelessProfileResponse
        # and wifiProfileCount went 0 → 1 — persistent for every later
        # boot, no flapping, no preset-press required. That run hit
        # the API at uptime ~16s, when /networkInfo before showed
        # wlan1 state="NETWORK_WIFI_DISCONNECTED" (NetManager had NOT
        # yet flipped into setup-AP mode). Every later run that
        # arrived AFTER NetManager had already moved wlan1 to
        # mode="ACCESS_POINT" got HTTP 500 instead and we lost
        # persistence forever. The 60s wait_for_ready that used to be
        # here guaranteed we missed the window on every retry — it is
        # gone now. Approach B fires first, as fast as possible.
        BOSE_API="http://127.0.0.1:8090"
        setup_log "B: waiting for BoseApp on $BOSE_API"
        i=0
        while [ $i -lt 30 ]; do
            if wget -qO- -T 2 "$BOSE_API/info" >/dev/null 2>&1; then
                setup_log "B: BoseApp reachable after ${i}s"
                break
            fi
            sleep 1
            i=$((i + 1))
        done
        B_OK=""
        if [ $i -ge 30 ]; then
            setup_log "B: BoseApp did not respond within 30s, skipping API call"
        else
            NETINFO=$(wget -qO- -T 3 "$BOSE_API/networkInfo" 2>/dev/null | head -c 400)
            setup_log "B: /networkInfo before: $NETINFO"
            xml_escape() {
                printf '%s' "$1" | sed \
                    -e 's/\&/\&amp;/g' \
                    -e 's/</\&lt;/g' \
                    -e 's/>/\&gt;/g' \
                    -e 's/"/\&quot;/g' \
                    -e "s/'/\&apos;/g"
            }
            ESSID=$(xml_escape "$SSID")
            EPASS=$(xml_escape "$PASS")
            BODY="<AddWirelessProfile timeout=\"30\"><profile ssid=\"$ESSID\" password=\"$EPASS\" securityType=\"wpa_or_wpa2\" /></AddWirelessProfile>"
            RESP=$(wget -qO- -T 8 --header="Content-Type: application/xml" \
                   --post-data="$BODY" "$BOSE_API/addWirelessProfile" 2>&1)
            RC=$?
            setup_log "B: POST addWirelessProfile rc=$RC response='$(echo "$RESP" | head -c 400)'"
            if [ $RC -eq 0 ] && echo "$RESP" | grep -qi "AddWirelessProfileResponse"; then
                setup_log "B: API persisted profile successfully — NetManager-DB updated"
                B_OK=1
            fi
        fi

        # If B persisted: NetManager has the profile in its DB,
        # everything else is unnecessary. Skip A/C/D.
        if [ "$B_OK" = "1" ]; then
            setup_log "B succeeded — skipping A/C/D fallbacks"
        else
        # --- Approach A: direct /etc/wpa_supplicant.conf write (fallback) ---
        WPA_CONF="/etc/wpa_supplicant.conf"
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
        cp "$WPA_CONF" "$PERSIST/wpa_supplicant.conf.bak" 2>/dev/null
        if cat "$TMP" > "$WPA_CONF" 2>/dev/null; then
            setup_log "A: wpa_supplicant.conf direct-write OK ($(wc -c < "$WPA_CONF") bytes)"
        else
            setup_log "A: WARN /etc/wpa_supplicant.conf not writable, trying bind mount"
            if mount --bind "$TMP" "$WPA_CONF" 2>/dev/null; then
                setup_log "A: bind mount active"
            else
                setup_log "A: FAIL both direct-write and bind mount"
            fi
        fi
        if pidof wpa_supplicant >/dev/null 2>&1; then
            killall wpa_supplicant 2>/dev/null
            sleep 1
            wpa_supplicant -B -i wlan0 -s -c "$WPA_CONF" -D nl80211 2>/dev/null &
            setup_log "A: wpa_supplicant restarted"
        else
            setup_log "A: no wpa_supplicant process to restart (Bose may bring it up later)"
        fi
        # Re-attempt the API once after Approach A has done its
        # bind-mount and (maybe) restart — sometimes that nudges
        # NetManager hard enough to accept the call even when the
        # initial probe got 500. Cheap, harmless on success.
        if [ -n "$BODY" ]; then
            RESP=$(wget -qO- -T 8 --header="Content-Type: application/xml" \
                   --post-data="$BODY" "$BOSE_API/addWirelessProfile" 2>&1)
            RC=$?
            setup_log "B: post-A retry addWirelessProfile rc=$RC response='$(echo "$RESP" | head -c 400)'"
            if [ $RC -eq 0 ] && echo "$RESP" | grep -qi "AddWirelessProfileResponse"; then
                setup_log "B: API persisted profile successfully"
            else
                setup_log "B: API call did not confirm success (rc=$RC), retrying after 4s"
                sleep 4
                RESP2=$(wget -qO- -T 8 --header="Content-Type: application/xml" \
                       --post-data="$BODY" "$BOSE_API/addWirelessProfile" 2>&1)
                RC2=$?
                setup_log "B: retry POST addWirelessProfile rc=$RC2 response='$(echo "$RESP2" | head -c 400)'"
            fi

            # --- Approach C: wpa_cli direct ---
            # Talks to whichever wpa_supplicant instance is currently
            # alive on the box (Bose's or ours). add_network /
            # set_network / select_network / save_config is the
            # canonical wpa_supplicant.conf-independent path. If the
            # ctrl interface is reachable, this works even when
            # /addWirelessProfile is refusing service.
            if command -v wpa_cli >/dev/null 2>&1; then
                NETID=$(wpa_cli -i wlan0 add_network 2>/dev/null | tail -1)
                setup_log "C: wpa_cli add_network on wlan0 -> id=$NETID"
                if [ -n "$NETID" ] && [ "$NETID" -ge 0 ] 2>/dev/null; then
                    SSID_ESC=$(printf '%s' "$SSID" | sed 's/"/\\"/g')
                    PSK_ESC=$(printf '%s' "$PASS" | sed 's/"/\\"/g')
                    wpa_cli -i wlan0 set_network "$NETID" ssid "\"$SSID_ESC\"" >/dev/null 2>&1
                    R1=$?
                    wpa_cli -i wlan0 set_network "$NETID" psk "\"$PSK_ESC\"" >/dev/null 2>&1
                    R2=$?
                    wpa_cli -i wlan0 set_network "$NETID" key_mgmt WPA-PSK >/dev/null 2>&1
                    R3=$?
                    wpa_cli -i wlan0 enable_network "$NETID" >/dev/null 2>&1
                    R4=$?
                    wpa_cli -i wlan0 select_network "$NETID" >/dev/null 2>&1
                    R5=$?
                    wpa_cli -i wlan0 save_config >/dev/null 2>&1
                    R6=$?
                    setup_log "C: wpa_cli set ssid=$R1 psk=$R2 key_mgmt=$R3 enable=$R4 select=$R5 save=$R6"
                    # Persist creds into NAND so subsequent boots
                    # (when stick wlan.conf is gone) can replay this
                    # exact wpa_cli dance — Bose's NetManager does
                    # NOT remember wpa_cli-added networks across
                    # reboot. Without this every reboot drops back
                    # to yellow LED. Stored next to other NAND
                    # state, fat32-safe newlines.
                    if [ "$R1" = "0" ] && [ "$R2" = "0" ] && [ "$R4" = "0" ]; then
                        { printf 'SSID=%s\n' "$SSID"
                          printf 'PASS=%s\n' "$PASS"
                        } > "$WLAN_CREDS_NAND.new" 2>/dev/null
                        if [ -s "$WLAN_CREDS_NAND.new" ]; then
                            mv "$WLAN_CREDS_NAND.new" "$WLAN_CREDS_NAND" 2>/dev/null
                            chmod 600 "$WLAN_CREDS_NAND" 2>/dev/null
                            setup_log "C: persisted wlan creds to NAND for replay on next boot"
                        fi
                    fi
                fi
            else
                setup_log "C: wpa_cli not in PATH, skipping"
            fi
            # --- Approach D: kill setup-AP processes so NetManager
            # falls back to station mode ---
            #
            # Empirical: on factory-reset rhino ST10, Approach C writes
            # a valid wpa_supplicant profile but wlan0 stays DISCONNECTED
            # because Bose's NetManager keeps wlan1 in setup-AP mode and
            # never re-evaluates wlan0. A physical preset button press
            # WAS observed to break this (LED goes white within seconds);
            # the CLI equivalent `sys presetkey N p` is accepted with
            # ->OK but does NOT propagate the same NetworkManager side
            # effect (six slots tried, all wlan0 state=up ip=none).
            #
            # So we go one level lower: kill the userland processes that
            # actually run the setup-AP (hostapd serves the AP, udhcpd
            # hands out 192.0.2.x leases). With those gone, NetManager
            # has nothing to keep alive and the station-mode profile we
            # already loaded becomes the next thing to try.
            #
            # Backgrounded so run.sh does not block. Best-effort: each
            # kill is silent on absence.
            (
                sleep 8
                setup_log "D: setup-AP teardown attempt"
                # Snapshot what's actually running so we can refine the
                # kill list on other firmware variants without guessing.
                AP_PROCS=$(ps 2>/dev/null | grep -E 'hostapd|udhcpd|dnsmasq|nodogsplash' | grep -v grep | tr '\n' '|' | head -c 400)
                setup_log "D: AP-related procs: $AP_PROCS"
                killall hostapd 2>/dev/null && setup_log "D: hostapd killed"
                killall udhcpd 2>/dev/null && setup_log "D: udhcpd killed"
                # Some Bose builds use dnsmasq instead.
                killall dnsmasq 2>/dev/null && setup_log "D: dnsmasq killed"
                sleep 2
                # Force wpa_supplicant to retry association with the
                # config we just wrote. reassociate is the cheap path;
                # reconfigure re-reads the file in case Bose's NetManager
                # rewrote it from underneath us between our write and now.
                wpa_cli -i wlan0 reassociate >/dev/null 2>&1 \
                    && setup_log "D: wlan0 reassociate sent"
                wpa_cli -i wlan0 reconfigure >/dev/null 2>&1 \
                    && setup_log "D: wlan0 reconfigure sent"
                # Give the radio time to scan + 4-way handshake + DHCP.
                for wait in 6 10 15 20; do
                    sleep "$wait"
                    STATE=$(cat /sys/class/net/wlan0/operstate 2>/dev/null)
                    IP=$(ip -4 addr show wlan0 2>/dev/null | sed -n 's/.*inet \([0-9.]*\).*/\1/p' | head -1)
                    setup_log "D: t+${wait}s wlan0 state=$STATE ip=${IP:-none}"
                    if [ "$STATE" = "up" ] && [ -n "$IP" ]; then
                        setup_log "D: associated — teardown approach won"
                        return 0
                    fi
                done
                # Last resort: simulated hardware presses. Jens-observed
                # pattern on rhino ST10: a single CLI press has no
                # NetManager effect, but a RAPID BURST of presses does
                # — as if Bose has a user-interaction counter that
                # must cross a threshold before the state machine flips
                # out of setup-AP. So we send a burst first, then
                # spaced retries to nudge the NetManager state without
                # spamming.
                if command -v nc >/dev/null 2>&1; then
                    setup_log "D-fallback: burst phase (8x slot 2, 1s apart)"
                    for _ in 1 2 3 4 5 6 7 8; do
                        printf 'sys presetkey 2 p\n' | nc -w 1 127.0.0.1 17000 >/dev/null 2>&1
                        sleep 1
                    done
                    sleep 6
                    IP=$(ip -4 addr show wlan0 2>/dev/null | sed -n 's/.*inet \([0-9.]*\).*/\1/p' | head -1)
                    setup_log "D-fallback: after burst wlan0 ip=${IP:-none}"
                    if [ -n "$IP" ]; then
                        setup_log "D-fallback: burst won — associated ip=$IP"
                        return 0
                    fi
                    # Spaced retries across all slots in case slot 2 is
                    # specifically suppressed by Bose's preset handler.
                    setup_log "D-fallback: spaced phase (slots 2..1, 12s apart)"
                    for slot in 2 3 4 5 6 1; do
                        printf 'sys presetkey %d p\n' "$slot" | nc -w 2 127.0.0.1 17000 >/dev/null 2>&1
                        setup_log "D-fallback: spaced presetkey $slot sent"
                        sleep 12
                        IP=$(ip -4 addr show wlan0 2>/dev/null | sed -n 's/.*inet \([0-9.]*\).*/\1/p' | head -1)
                        setup_log "D-fallback: after slot $slot wlan0 ip=${IP:-none}"
                        if [ -n "$IP" ]; then
                            setup_log "D-fallback: spaced won — associated after slot $slot ip=$IP"
                            return 0
                        fi
                    done
                fi
                setup_log "D: all approaches exhausted — manual preset press needed"
            ) &

            # Post-state, with grace period for the box to attempt
            # association from the just-stored profile.
            sleep 5
            NETINFO2=$(wget -qO- -T 3 "$BOSE_API/networkInfo" 2>/dev/null | head -c 400)
            setup_log "B: /networkInfo after: $NETINFO2"
        fi
        fi

        if [ -f "$WLAN_CONF" ]; then
            rm -f "$WLAN_CONF" 2>/dev/null
            setup_log "wlan.conf removed from stick"
        fi
        setup_log "=== WLAN provisioning end ==="
    else
        setup_log "wlan creds invalid (SSID or PASS empty), aborting WLAN provisioning"
    fi
fi
) &

# === Region Provisioning aus region.conf vom Stick ===
# User hat im Setup Wizard ein Land gewaehlt. Persistieren nach NAND,
# danach vom Stick loeschen.
REGION_CONF="$STICK/region.conf"
REGION_NAND="$PERSIST/region.txt"
if [ -f "$REGION_CONF" ]; then
    CC=$(sed -n 's/.*"country":"\([^"]*\)".*/\1/p' "$REGION_CONF" | head -1)
    if [ -n "$CC" ]; then
        echo "$CC" > "$REGION_NAND"
        log "region '$CC' from region.conf persisted to NAND"
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
        log "box name '$NAME' from name.conf persisted to NAND"
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
    log "iptables NAT available"
else
    log "iptables NAT unavailable, marge will listen directly on :443"
fi

log "bind mount on /etc/hosts active"
log "starting agent version $(${BIN} --version 2>/dev/null || echo v0.0.0)"

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

# ports_busy returns 0 if ANY of the agent's listener ports is still
# bound, 1 if all are free. Used by wait_ports_clear before respawn so
# the new agent does not race into the previous instance's sockets.
# The agent now sets SO_REUSEADDR (see internal/netutil/listener.go),
# which makes TIME_WAIT irrelevant for binding — but this remains a
# belt-and-suspenders guard against the case where the old process is
# still alive AND holding the listener fd (kill -KILL not yet
# delivered, or shell waiting on TERM grace).
ports_busy() {
    for p in 8081 8888 9080 8091 8080; do
        if command -v ss >/dev/null 2>&1; then
            ss -ltn 2>/dev/null | grep -q ":$p "
            if [ $? = 0 ]; then return 0; fi
        elif command -v netstat >/dev/null 2>&1; then
            netstat -ltn 2>/dev/null | grep -q ":$p "
            if [ $? = 0 ]; then return 0; fi
        else
            if (echo > /dev/tcp/127.0.0.1/$p) >/dev/null 2>&1; then
                return 0
            fi
        fi
    done
    return 1
}

# wait_ports_clear loops until ports_busy reports clear or up to
# max_seconds (default 30) elapse. Returns 0 either way; the caller
# proceeds and start_agent's own bind will surface a real failure.
wait_ports_clear() {
    max=${1:-30}
    i=0
    while [ $i -lt $max ]; do
        if ! ports_busy; then
            return 0
        fi
        sleep 1
        i=$((i + 1))
    done
    setup_log "wait_ports_clear: gave up after ${max}s, proceeding with start_agent"
    return 0
}

# try_http_date_sync best-effort sets the box clock from a public
# HTTP Date header. Bose's RTC reads 2015 right after power-on which
# breaks any TLS handshake against the stick before NTP catches up.
# Runs once, with a tight timeout, before start_agent so the agent's
# loopback TLS endpoint is reachable for the auto-pair flow. Tries
# wget first (busybox), then curl, then fails silently — the agent's
# autopair clock-gate (internal/autopair/autopair.go) is the
# fallback.
try_http_date_sync() {
    # Already past 2024? Nothing to do.
    yr=$(date -u +%Y 2>/dev/null)
    case "$yr" in
        2024|2025|2026|2027|2028|2029|203[0-9])
            return 0
            ;;
    esac
    for host in www.google.com www.cloudflare.com www.bose.com; do
        d=""
        if command -v wget >/dev/null 2>&1; then
            d=$(wget -qSO /dev/null --max-redirect=0 --tries=1 --timeout=4 "http://$host/" 2>&1 | sed -n 's/^[[:space:]]*Date:[[:space:]]*\(.*\)$/\1/p' | head -1)
        fi
        if [ -z "$d" ] && command -v curl >/dev/null 2>&1; then
            d=$(curl -sI --max-time 4 "http://$host/" 2>/dev/null | sed -n 's/^Date:[[:space:]]*\(.*\)$/\1/p' | head -1 | tr -d '\r')
        fi
        if [ -n "$d" ]; then
            # busybox `date -s` parses "Day, DD Mon YYYY HH:MM:SS GMT".
            if date -u -s "$d" >/dev/null 2>&1; then
                setup_log "clock-sync: set from HTTP Date via $host -> $(date -u)"
                return 0
            fi
        fi
    done
    setup_log "clock-sync: no HTTP Date source reachable, leaving RTC as-is"
    return 1
}

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

try_http_date_sync
start_agent
log "agent started with PID $AGENT_PID"

# === Aggressive Boot-Race Watchdog (Phase A: t=0..120s) ===
#
# Hintergrund: Auf langsameren ST20-Varianten haben Reporter
# beobachtet, dass die Box nach dem Stick-Boot zwar rebootet, aber
# Port 8888 nie öffnet. Ursache ist nicht reproduzierbar — Verdacht
# fällt auf Bose's Service-Manager (shepherdd / SCM), der waehrend
# der ersten ~90s nach dem Boot eigene Aufraeumarbeiten faehrt und
# unter Last unseren nohup-Prozess mit verreisst, oder auf OOM-
# Kills (RAM-Druck im Boot). Der bestehende 90s-Watchdog unten
# fängt das ein, aber erst NACH dem ersten Zyklus — dadurch ist
# der Agent auf einer langsamen Box fuer bis zu 90s tot, der User
# sieht "Install failed" und gibt auf.
#
# Diese Phase-A-Schleife prueft ALLE 5s die ersten 120s ob (a) der
# Agent-PID noch lebt UND (b) :8888 tatsaechlich gebunden ist.
# Wenn entweder ausfaellt, sofort neustarten. Kosten pro Check:
# 1 kill -0, 1 nc/ss-Lookup. Bei stabilem Agent feuert kein cp,
# kein Flash-Write. Nach 120s übergibt es an den langsamen
# 90s-Watchdog (Phase B).
agent_port_bound() {
    if command -v ss >/dev/null 2>&1; then
        ss -ltn 2>/dev/null | grep -q ':8888 '
        return $?
    fi
    if command -v netstat >/dev/null 2>&1; then
        netstat -ltn 2>/dev/null | grep -q ':8888 '
        return $?
    fi
    # Last resort: try /dev/tcp self-probe. If shell does not
    # support it, assume bound (we cannot tell — better not
    # respawn-loop on a working agent).
    if (echo > /dev/tcp/127.0.0.1/8888) >/dev/null 2>&1; then
        return 0
    fi
    return 0
}

(
    # 24 checks * 5s = 120s aggressive phase. After that, the slow
    # Phase-B loop below takes over. No flash writes while agent
    # stays up.
    BOOT_RESTARTS=0
    i=0
    while [ $i -lt 24 ]; do
        sleep 5
        i=$((i+1))
        if [ ! -f "$PIDFILE" ]; then
            break  # PID File weg, slow watchdog uebernimmt
        fi
        CUR_PID=$(cat "$PIDFILE" 2>/dev/null || echo 0)
        ALIVE=0
        if [ -n "$CUR_PID" ] && [ "$CUR_PID" -gt 0 ] && kill -0 "$CUR_PID" 2>/dev/null; then
            ALIVE=1
        fi
        BOUND=0
        if agent_port_bound; then
            BOUND=1
        fi
        if [ "$ALIVE" = "1" ] && [ "$BOUND" = "1" ]; then
            continue
        fi
        # Hard cap: 6 restarts in 120s. If we still cannot keep the
        # agent up, something fundamental is wrong (binary corrupt,
        # NAND full, ...) and respawning faster will not help.
        if [ "$BOOT_RESTARTS" -ge 6 ]; then
            setup_log "boot-watchdog: 6 restarts in 120s exhausted, falling back to slow loop"
            break
        fi
        BOOT_RESTARTS=$((BOOT_RESTARTS+1))
        setup_log "boot-watchdog: agent dead/unbound at t=$(uptime_s)s pid=$CUR_PID alive=$ALIVE bound=$BOUND, fast respawn #$BOOT_RESTARTS"
        # Drain listener ports before respawn. The agent uses
        # SO_REUSEADDR so TIME_WAIT alone would not block bind, but
        # the previous instance may still be alive briefly after
        # kill -KILL has been queued. Cap at 10 s in Phase A so the
        # respawn cadence stays tight.
        wait_ports_clear 10
        start_agent
        setup_log "boot-watchdog: agent respawned PID $AGENT_PID"
    done
) &

# === Slow Watchdog (Phase B: t=120s.. forever) ===
# Cost per check: 1 sleep + 1 read from page cache + 1 kill -0
# syscall — negligible (~50us/min). No flash writes while agent
# stays up; only an actual restart rewrites agent.pid once.
#
# Sleep 90 s instead of 60: TIME_WAIT on the listener ports is
# tcp_fin_timeout (60 s) long. If the agent dies while Bose's
# firmware still holds open connections to :8081 (bmx) or :9080
# (marge), a 60 s watchdog respawn would fire exactly at the end
# of the TIME_WAIT window and fail to bind — self-perpetuating.
# 90 s lets TIME_WAIT expire safely.
(
    # Give the aggressive phase its 120s head start, plus a 20s
    # buffer so the two loops do not both reach start_agent at
    # exactly the same instant (which would double-launch and fail
    # the second bind).
    sleep 140
    while true; do
        sleep 90
        if [ ! -f "$PIDFILE" ]; then
            break  # PID File weg, run.sh wird neu durchlaufen
        fi
        CUR_PID=$(cat "$PIDFILE" 2>/dev/null || echo 0)
        if [ -n "$CUR_PID" ] && [ "$CUR_PID" -gt 0 ] && kill -0 "$CUR_PID" 2>/dev/null; then
            continue  # Agent laeuft noch
        fi
        log "watchdog: agent (PID $CUR_PID) died, restarting"
        # Belt-and-suspenders: even though the agent has SO_REUSEADDR,
        # poll the listener ports to make sure no leftover process
        # holds the fd before we respawn. Caps at 30 s.
        wait_ports_clear 30
        start_agent
        log "watchdog: agent restarted with PID $AGENT_PID"
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
    log "root CA available after ${WAIT}s, applying bind mount"

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

log "bootstrap complete"

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
# Debug opt-out: if /media/sda1/keep-open exists, skip the entire
# cleanup so SSH stays reachable and the stick stays mounted. Used
# during interactive debugging when we want to read /mnt/nv state
# live over SSH without rebooting the box. Removed by deleting the
# file (e.g. del E:\keep-open from Jens' laptop) — next boot
# returns to normal cleanup behavior.
if [ -e "$STICK/keep-open" ]; then
    setup_log "keep-open marker on stick — skipping umount + sshd kill for live debug"
else
#
# Detached Hintergrund Block damit run.sh sofort returnen kann
# (shelby_local will dass rc.local schnell durchlaeuft). Lange
# Wartezeit + aktive Pruefung dass kein Prozess mehr den Stick
# offen hat, bevor wir tatsaechlich umount machen — sonst riskieren
# wir den Agent zu verwirren weil seine Goroutines (syncRunOverride,
# initialBoxPresetSync) noch lesen.
(
    # Pre-cleanup wait. Pre-1.0 we run it long, on purpose: the box
    # is on the user's LAN and the SSH cost is real, but right now
    # losing the debug channel costs us more than leaving it open
    # does. 300 s covers (a) syncRunOverrideFromStick + initial preset
    # sync + autopair timing (the old 60 s lower bound), (b) the
    # backgrounded WLAN provisioning chain which on a failing scm-
    # variant ST20 spans ~4 minutes of slow /addWirelessProfile 500s,
    # (c) slow agent boot races on weak hardware, and (d) a healthy
    # buffer before we commit to closing SSH. The :8888 gate below
    # also still applies — if the agent never came up we never close
    # SSH regardless of the timer.
    #
    # Trade-off accepted: stick stays mounted writable for 5 minutes,
    # so a user who yanks it mid-bootstrap may see a dirty FAT on
    # Windows. We accept that during dev. Tighten before broad
    # release (see SECURITY.md hardening section).
    sleep 300

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

    # Agent reachability gate: never close the SSH escape hatch while
    # the STR agent is missing. Observed live on #60: install timed
    # out, agent never bound :8888, cleanup killed sshd anyway, and
    # the diagnostic bundle came back empty because the on-box pull
    # had no way in. Probe :8888 from inside the box before deciding.
    AGENT_OK=0
    if (echo > /dev/tcp/127.0.0.1/8888) >/dev/null 2>&1; then
        AGENT_OK=1
    elif command -v nc >/dev/null 2>&1 && nc -z 127.0.0.1 8888 >/dev/null 2>&1; then
        AGENT_OK=1
    fi

    sync
    if [ "$AGENT_OK" = "0" ]; then
        log "post-bootstrap: agent NOT bound on :8888 — leaving stick mounted + sshd alive for diagnostics"
        setup_log "post-bootstrap: agent NOT bound on :8888 — leaving stick mounted + sshd alive"
        # Read-only remount so a yanked stick does not corrupt FAT,
        # but DO NOT umount and DO NOT stop sshd. Lets the desktop
        # app's diagnostic bundle pull box-side logs after a failed
        # install without the user typing anything.
        mount -o remount,ro "$STICK" 2>/dev/null \
            && log "USB Stick read-only remounted (sshd kept alive)"
        exit 0
    fi
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
fi
