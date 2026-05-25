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

# ensure_sshd_running keeps the box reachable by SSH from boot until
# next reboot, on every boot regardless of whether the stick is
# inserted. Pre-1.0 we explicitly prefer debug visibility over
# security: when an install or OTA leaves the agent stuck, SSH is
# the only channel that still lets the desktop app's diagnostic
# bundle pull /tmp/streborn-agent.log, /mnt/nv/streborn/setup.log
# etc. Without it every "no luck yet" report ended in a stick-yank
# cycle.
#
# Box default state: Bose's /etc/init.d/shelby_local starts sshd
# only when /media/sda1/remote_services exists. On a steady-state
# boot (no stick, NAND override only) that file is absent, so sshd
# never comes up. This function plugs that gap by starting sshd
# unconditionally from run.sh. If sshd is already running (e.g. the
# stick is in and Bose already started it), the start call is a
# cheap no-op.
#
# Security note: the speaker's root password is the well-known Bose
# default. As soon as the v1.0 hardening lands ([[project-box-
# security-hardening]]), this function becomes opt-in via a stick
# marker file, not opt-out. Tracked separately.
ensure_sshd_running() {
    if pidof sshd >/dev/null 2>&1; then
        setup_log "sshd already running, leaving it alone"
        return 0
    fi
    if [ -x /etc/init.d/sshd ]; then
        /etc/init.d/sshd start >/dev/null 2>&1 \
            && setup_log "sshd started via /etc/init.d/sshd" \
            && return 0
    fi
    if [ -x /usr/sbin/sshd ]; then
        /usr/sbin/sshd >/dev/null 2>&1 \
            && setup_log "sshd started via /usr/sbin/sshd direct" \
            && return 0
    fi
    setup_log "sshd start: no init script and no /usr/sbin/sshd found"
    return 1
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

# Keep SSH up across stick + stickless boots. Has to happen early so
# the channel is available even if the agent never binds.
ensure_sshd_running

# Unconditional Stick -> NAND sync. Every boot with a stick present
# copies the stick binary AND the stick version.txt to NAND, no
# version check, no mtime guard.
#
# Why brute force: the previous version-string + mtime gated logic
# silently refused to sync in several scenarios:
#   - identical version strings across two different builds (e.g.
#     v0.5.12 + dev iterations both stamped v0.5.12)
#   - box RTC at 2015 on cold boot while FAT32 stick mtimes are at
#     real time, so the `-nt` mtime comparison flipped both ways
#     depending on whether NTP sync had run yet
#   - partial-sync recovery: SYNC_FLAG could be cleared by an earlier
#     half-failed copy, leaving NAND binary mid-state until next mismatch
#
# Result was that the Setup Wizard "prepare stick" felt broken from
# the user's perspective even though the stick had the right files.
# Live-verified 2026-05-24 on ST10 .66 across three back-to-back
# cold boots with a freshly-prepared stick.
#
# The fail-closed "stick wins, every time" model matches the user
# mental model: "what I just put on the stick is what the box runs
# on the next boot, period." The CACHED_BIN.new + mv atomic-replace
# pattern keeps the binary swap safe under power loss mid-copy.
# Removing SYNC_FLAG removes the only state that could go out of
# sync with reality across boots.
sync_stick_to_nand_always() {
    if [ ! -r "$STICK_BIN" ]; then
        return 0
    fi
    if cp "$STICK_BIN" "$CACHED_BIN.new" 2>/dev/null; then
        chmod +x "$CACHED_BIN.new"
        if mv "$CACHED_BIN.new" "$CACHED_BIN" 2>/dev/null; then
            log "stick binary deployed to NAND cache ($(wc -c < "$CACHED_BIN") bytes)"
            if [ -r "$STICK_VER_FILE" ]; then
                cp "$STICK_VER_FILE" "$NAND_VER_FILE" 2>/dev/null
                log "NAND version.txt updated: $(cat "$NAND_VER_FILE" 2>/dev/null)"
            fi
            return 0
        fi
        log "stick -> NAND mv failed, keeping previous NAND binary"
        rm -f "$CACHED_BIN.new"
        return 1
    fi
    log "stick -> NAND cp failed (stick I/O error?), keeping previous NAND binary"
    rm -f "$CACHED_BIN.new"
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
# unconditionally. rc.local itself does the same; this duplicate path
# heals a box whose NAND rc.local is an older release that skipped
# the self-update block entirely.
if [ -f "$STICK/rc.local" ]; then
    cp "$STICK/rc.local" /mnt/nv/rc.local 2>/dev/null
    chmod +x /mnt/nv/rc.local 2>/dev/null
    log "redeployed /mnt/nv/rc.local from stick (effective next boot)"
fi
if [ -f "$STICK/run.sh" ]; then
    cp "$STICK/run.sh" /mnt/nv/streborn/run-override.sh 2>/dev/null
    chmod +x /mnt/nv/streborn/run-override.sh 2>/dev/null
    log "redeployed /mnt/nv/streborn/run-override.sh from stick (effective next boot)"
fi

sync_stick_to_nand_always

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
# Wireless-interface detection. Three cases:
#   1. rhino/spotty/sm2/scm/maple: wireless is /sys/class/net/wlan0
#      (sometimes wlan1). wpa_supplicant + wpa_cli present.
#   2. taigan (SoundTouch Portable, BCO chassis): wireless chip is
#      exposed as /sys/class/net/eth0 with type=1 (Ethernet) — no
#      wlan* iface at all, and wpa_supplicant / wpa_cli BINARIES are
#      missing on this firmware. NetManager talks to the chip directly
#      via HTTP /addWirelessProfile + TAP CLI `network wifi profiles`.
#      Detection: /proc/variant says "taigan", or /sbin/has-bco exists,
#      or — fallback — eth0 is present without any wlan*.
#   3. Genuine ethernet-only / dead radio: no wlan*, no eth0 in the
#      "single non-loopback iface" pattern, or eth0 carries a real
#      cable. Skip provisioning, the desktop install path works on
#      a wired connection anyway. Observed scm-variant ST20 in #60
#      where /addWirelessProfile sat on slow 500 responses for ~4
#      minutes per attempt, starving start_agent below.
WLAN_IFACE=""
TAIGAN_MODE=""
if [ -d /sys/class/net/wlan0 ]; then
    WLAN_IFACE="wlan0"
elif [ -d /sys/class/net/wlan1 ]; then
    WLAN_IFACE="wlan1"
elif [ -d /sys/class/net/eth0 ]; then
    # Three independent taigan indicators. We accept any one because
    # individual ones have been observed missing across FW rev's:
    # /proc/variant is unreadable on some images, has-bco lives in
    # /sbin OR /usr/sbin depending on build, but `uname -n` reliably
    # mirrors the Bose codename ("Linux taigan 3.14...") which we see
    # in EVERY captured setup.log across variants.
    VARIANT=$(cat /proc/variant 2>/dev/null | tr -d '\n\r ' | head -c 32)
    HOSTID=$(uname -n 2>/dev/null | tr -d '\n\r ' | head -c 32)
    if [ "$VARIANT" = "taigan" ] || [ "$HOSTID" = "taigan" ] \
       || [ -x /sbin/has-bco ] || [ -x /usr/sbin/has-bco ]; then
        TAIGAN_MODE=1
        WLAN_IFACE="eth0"
        setup_log "WLAN: taigan/BCO detected (variant=${VARIANT:-?} host=${HOSTID:-?}), using eth0 as wireless interface"
    fi
fi
if [ -z "$WLAN_IFACE" ]; then
    setup_log "WLAN: no wireless interface present, ethernet-only mode (skip provisioning)"
    echo "ethernet-only" > "$PERSIST/wlan-mode" 2>/dev/null
    SSID=""
    PASS=""
fi

# Whole block runs in a backgrounded subshell so a slow Bose API
# (4 minute /addWirelessProfile loops, observed on #60) cannot delay
# start_agent below. Agent binds :8888 within seconds and the install
# wizard's 180s poll window succeeds even when WLAN provisioning is
# still trying its A/B/C/D fallbacks.
( WLAN_T0=$(awk '{print int($1)}' /proc/uptime 2>/dev/null)

# Snapshot Bose state up front, ALWAYS, regardless of whether we have
# creds and regardless of which WLAN code-path we'll take. Without
# probes in the ethernet-only path we cannot tell from a diagnostic
# bundle why a given box was classified the way it was. Each command
# is silent on absence (taigan has no nc, some images have no wpa_cli).
setup_log "probe: /sys/class/net = $(ls /sys/class/net 2>/dev/null | tr '\n' ' ')"
setup_log "probe: uname -n = $(uname -n 2>/dev/null)"
setup_log "probe: /proc/variant = $(cat /proc/variant 2>/dev/null | head -c 64 || echo missing)"
if command -v wpa_cli >/dev/null 2>&1; then
    setup_log "probe: wpa_cli present"
else
    setup_log "probe: wpa_cli MISSING"
fi
if command -v wpa_supplicant >/dev/null 2>&1; then
    setup_log "probe: wpa_supplicant binary present"
else
    setup_log "probe: wpa_supplicant binary MISSING"
fi
if command -v nc >/dev/null 2>&1; then
    TAP_VER=$(printf 'sys ver\n' | nc -w 2 127.0.0.1 17000 2>/dev/null | tr '\n' ' ' | head -c 200)
    setup_log "probe: TAP :17000 sys ver = ${TAP_VER:-no-response}"
    TAP_NET=$(printf 'network status\n' | nc -w 2 127.0.0.1 17000 2>/dev/null | tr '\n' ' ' | head -c 300)
    setup_log "probe: TAP :17000 network status = ${TAP_NET:-no-response}"
else
    setup_log "probe: nc MISSING — TAP CLI probes skipped"
fi

if [ -n "$SSID" ] && [ -n "$PASS" ]; then
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
        elif [ "$TAIGAN_MODE" = "1" ]; then
            # taigan path: Bose's HTTP /performWirelessSiteSurvey
            # returns 400 and /addWirelessProfile returns 500 on this
            # firmware — both endpoints are simply not wired in the
            # taigan/BCO build. Live-verified 2026-05-24 on Portable
            # .79 across two cold boots. The TAP CLI on :17000
            # accepts the same operations under the `network wifi`
            # namespace, so we route the taigan provisioning through
            # that instead. See [[taigan-quirks]] for the validated
            # command set.
            #
            # Single persistent socket because async TAP responses
            # come back on the same connection that issued the
            # command — closing the socket after each command drops
            # the response and may abort the request mid-flight. The
            # nc -w window must cover the longest expected wait
            # (the wifi scan is asynchronous, results land 5-10s
            # after the request).
            NETINFO=$(wget -qO- -T 3 "$BOSE_API/networkInfo" 2>/dev/null | head -c 400)
            setup_log "B-taigan: /networkInfo before: $NETINFO"
            if ! command -v nc >/dev/null 2>&1; then
                setup_log "B-taigan: nc missing — cannot drive TAP CLI, giving up"
            else
                # BusyBox `nc -w SECS` is a connect / final-read
                # idle timeout, NOT a session cap. The TAP server on
                # :17000 keeps the socket open after the last command
                # waiting for more input, so nc never sees "idle for
                # 25s" and run.sh's $() blocks indefinitely. Live-
                # verified 2026-05-25: WLAN block hung for 2+ minutes
                # with a 25s -w value and the SUMMARY line never
                # fired. `timeout SECS CMD` (BusyBox has it) is a
                # hard wall-clock cap on the whole nc invocation.
                # We pick 30s because the inner sleeps total 16s and
                # we need slack for the BusyBox nc startup plus the
                # async wifi-scan response landing late.
                # `timeout` syntax differs between GNU coreutils
                # (`timeout SECS CMD`) and BusyBox (`timeout -t SECS
                # CMD`). The Bose firmware ships BusyBox 1.19 which
                # uses the `-t` form — GNU-style invocation tried to
                # exec the literal string "60" and died immediately
                # (live-verified 2026-05-25, Portable .79). Probe
                # which form works by checking the help text.
                if command -v timeout >/dev/null 2>&1; then
                    if timeout --help 2>&1 | grep -q '\-t '; then
                        TAP_CMD="timeout -t 80 nc 127.0.0.1 17000"
                    else
                        TAP_CMD="timeout 80 nc 127.0.0.1 17000"
                    fi
                else
                    # Fallback for the unlikely BusyBox build that
                    # ships nc but no timeout. -w is at least
                    # something, even though it can leak.
                    TAP_CMD="nc -w 70 127.0.0.1 17000"
                fi
                # Timing matters here. Both `network wifi scan` and
                # `network wifi profiles add` are ASYNC on taigan —
                # the initial OK only confirms "request accepted",
                # the actual completion lands on the same socket
                # 5-15s later (NetManager has to drive the radio).
                # Live-verified 2026-05-25 on Portable: 8s after
                # scan was not enough, profiles info still showed
                # empty, and `network mode auto` fired before any
                # association attempt could happen. New cadence:
                #   - scan + 15s grace for async scan results
                #   - clear (synchronous)
                #   - add + 15s grace for async add completion
                #   - profiles info (verification)
                #   - mode auto (kick NetManager from setup-AP to
                #     station mode now that DB has the profile)
                #   - 5s settle so the verify loop below has a real
                #     pre-state snapshot to compare against
                TAP_OUT=$(
                    (
                        printf 'async_responses on\n'
                        sleep 1
                        printf 'network wifi scan\n'
                        sleep 25
                        printf 'network wifi profiles clear\n'
                        sleep 2
                        printf 'network wifi profiles add %s wpa_or_wpa2 %s\n' "$SSID" "$PASS"
                        sleep 20
                        printf 'network wifi profiles info\n'
                        sleep 2
                        # `network mode auto` alone is not enough on
                        # taigan: NetManager stays in setup-AP mode
                        # because the chassis has no separate radio
                        # for station + AP at the same time. Live-
                        # verified 2026-05-25 on Portable: after
                        # profile add + mode auto, eth0 still
                        # carried the setup-AP IP 192.168.1.1.
                        # `airplay setupap exit` explicitly drops
                        # the setup-AP listener, which frees the
                        # chip to associate as a station using the
                        # profile we just added.
                        printf 'airplay setupap exit\n'
                        sleep 5
                        printf 'network mode auto\n'
                        sleep 8
                    ) | $TAP_CMD 2>&1
                )
                # Persist the full TAP response trace to NAND so the
                # diagnostic bundle has the complete async-response
                # stream (the inline log is truncated to 800 chars and
                # late async responses get cut off there).
                echo "$TAP_OUT" > "$PERSIST/tap-trace.log" 2>/dev/null
                setup_log "B-taigan: TAP sequence response (first 800c)='$(echo "$TAP_OUT" | tr '\n' '|' | head -c 800)'"
                setup_log "B-taigan: full TAP trace persisted to $PERSIST/tap-trace.log ($(wc -c < "$PERSIST/tap-trace.log" 2>/dev/null || echo 0) bytes)"
                # Acceptance check: with async_responses on, the
                # success indicators we can rely on are the OK
                # acknowledgements ("Profiles Deleted", "Add
                # requested", "mode set to auto"). Any of these
                # means NetManager processed the command — final
                # association success is tested by the B-verify
                # loop afterward via /sys/class/net/$WLAN_IFACE.
                # If we cannot even see "Add requested" it means
                # the TAP path itself is broken on this firmware
                # variant and there is no point waiting for an IP.
                #
                # SSID is checked with `grep -F` (fixed-string, not
                # regex) because it can contain regex metacharacters
                # (`.`, `(`, `[`, `*`) which would either silently
                # mis-match or break the pattern entirely. The other
                # two indicators are literal anyway, so we run three
                # separate fixed-string greps and OR the results.
                if echo "$TAP_OUT" | grep -qiF "Add requested" \
                   || echo "$TAP_OUT" | grep -qF "$SSID" \
                   || echo "$TAP_OUT" | grep -qiF "mode set to auto"; then
                    setup_log "B-taigan: TAP profile add accepted by NetManager"
                    B_OK=1
                else
                    setup_log "B-taigan: TAP profile add rejected or no confirmation"
                fi
            fi
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

        # If B persisted: NetManager has the profile in its DB.
        # But HTTP 200 only means "accepted into DB", not "associated".
        # On taigan (Portable) we observed 2026-05-24 live that B
        # returns 200 yet wlan0 never gets an IP — NetManager kept
        # the profile but never reassociated. Verify via DHCP-wait.
        #
        # Setup-AP IP discrimination: the Bose setup-AP serves itself
        # at the LITERAL gateway address 192.168.1.1 (and 192.0.2.1
        # on some FW). Any OTHER IP — including 192.168.1.50, 192.168.1.42
        # etc — is a real STA lease from the user's home router. The
        # earlier `192.168.1.*` /24 wildcard was a correctness bug: it
        # made any home network that uses 192.168.1.0/24 (very common
        # default, e.g. Speedport, TP-Link, many ISPs) get classified
        # as "still setup-AP" and B-verify would falsely fail.
        # `ip addr` enumeration must look at ALL inet entries because
        # NetManager has been seen to briefly carry both the setup-AP
        # alias and the new STA lease at the same time during the
        # handover, in which order is undefined.
        is_real_sta_addr() {
            # $1 = ip address
            case "$1" in
                ""|192.168.1.1|192.0.2.1) return 1 ;;
                *) return 0 ;;
            esac
        }
        # Taigan reassoc + DHCP can be slow (drop setup-AP, reconfigure
        # radio, scan, 4-way handshake, DHCP DISCOVER → 60s headroom).
        # Non-taigan has working A/C/D fallbacks so we keep the original
        # 30s budget there to fall through faster.
        if [ "$TAIGAN_MODE" = "1" ]; then
            VERIFY_WAITS="5 5 10 10 10 10 10"
        else
            VERIFY_WAITS="5 5 5 5 5 5"
        fi
        B_VERIFIED=""
        if [ "$B_OK" = "1" ]; then
            for wait in $VERIFY_WAITS; do
                sleep "$wait"
                STV=$(cat "/sys/class/net/$WLAN_IFACE/operstate" 2>/dev/null)
                # Enumerate ALL inet addrs on the iface, pick the first
                # that is NOT a setup-AP gateway IP. If none are real
                # STA addresses, IPV stays empty and B-verify keeps
                # waiting.
                IPV=""
                IPS_ALL=$(ip -4 addr show "$WLAN_IFACE" 2>/dev/null | sed -n 's/.*inet \([0-9.]*\)\/.*/\1/p')
                for cand in $IPS_ALL; do
                    if is_real_sta_addr "$cand"; then
                        IPV="$cand"
                        break
                    fi
                done
                setup_log "B-verify: $WLAN_IFACE state=${STV:-?} all_ips='$(echo "$IPS_ALL" | tr '\n' ' ')' real_sta_ip=${IPV:-none}"
                if [ "$STV" = "up" ] && [ -n "$IPV" ]; then
                    B_VERIFIED=1
                    setup_log "Approach B: result=YES ip=$IPV reason=NetManager-DB-replay"
                    break
                fi
            done
            if [ -z "$B_VERIFIED" ]; then
                setup_log "Approach B: result=NO reason=API-200-but-no-real-STA-IP-within-verify-window, falling through to A/C/D"
                B_OK=""
            fi
        else
            setup_log "Approach B: result=NO reason=API-call-rejected-or-unreachable"
        fi
        if [ "$B_OK" = "1" ]; then
            setup_log "B verified — skipping A/C/D fallbacks"
        elif [ "$TAIGAN_MODE" = "1" ]; then
            # taigan has no wpa_supplicant binary and no wpa_cli, so
            # Approach A (file write + restart) and Approach C (wpa_cli
            # add_network) are both impossible. D's preset-burst is
            # also not useful: the Portable's preset buttons are routed
            # through BoseApp not NetManager, so a CLI burst has no
            # association side-effect.
            #
            # The earlier HTTP-only B-retry was useless work: the same
            # /addWirelessProfile endpoint that returned 500 in the
            # primary attempt will return 500 again — taigan/BCO does
            # not implement the HTTP endpoint at all. We only re-run
            # the path that actually has a chance: another TAP CLI
            # sequence with `mode auto` + setup-AP-exit, in case the
            # first sequence raced NetManager's scan cache.
            setup_log "Approach A: result=NO reason=skipped-on-taigan (no wpa_supplicant binary)"
            setup_log "Approach C: result=NO reason=skipped-on-taigan (no wpa_cli binary)"
            setup_log "Approach D: result=NO reason=skipped-on-taigan (preset keys do not affect NetManager on BCO)"
            if command -v nc >/dev/null 2>&1; then
                # Reuse the same timeout-wrapped TAP_CMD if it was set
                # in the primary attempt. If not (nc-only fallback),
                # use a short -w on a single command.
                if [ -n "$TAP_CMD" ]; then
                    RETRY_OUT=$(
                        (
                            printf 'async_responses on\n'
                            sleep 1
                            printf 'airplay setupap exit\n'
                            sleep 4
                            printf 'network mode auto\n'
                            sleep 6
                            printf 'network wifi profiles info\n'
                            sleep 2
                        ) | $TAP_CMD 2>&1
                    )
                else
                    RETRY_OUT=$(printf 'network mode auto\n' | nc -w 3 127.0.0.1 17000 2>/dev/null)
                fi
                setup_log "B-taigan-retry: TAP nudge (first 400c)='$(echo "$RETRY_OUT" | tr '\n' '|' | head -c 400)'"
            else
                setup_log "B-taigan-retry: no nc — cannot nudge NetManager"
            fi
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

        # Final summary line: parseable, one-shot, captures the outcome
        # of the entire WLAN block so diagnostic bundles surface the
        # winner without needing to grep multi-line approach traces.
        # Note: Approach D runs backgrounded and may still be deciding
        # when this line fires; that is fine — its own result lines
        # land later in the same log.
        WLAN_T1=$(awk '{print int($1)}' /proc/uptime 2>/dev/null)
        WLAN_ELAPSED=$(( ${WLAN_T1:-0} - ${WLAN_T0:-0} ))
        FINAL_STATE=$(cat "/sys/class/net/$WLAN_IFACE/operstate" 2>/dev/null)
        # Same is_real_sta_addr discrimination as B-verify so the
        # summary line cannot lie about success on taigan AND cannot
        # mis-classify a STA lease that happens to be 192.168.1.x
        # from a home router on 192.168.1.0/24.
        FINAL_IP=""
        FINAL_IPS_ALL=$(ip -4 addr show "$WLAN_IFACE" 2>/dev/null | sed -n 's/.*inet \([0-9.]*\)\/.*/\1/p')
        for cand in $FINAL_IPS_ALL; do
            if is_real_sta_addr "$cand"; then
                FINAL_IP="$cand"
                break
            fi
        done
        WINNER="none"
        if [ "$B_VERIFIED" = "1" ]; then
            WINNER="B"
        elif [ "$FINAL_STATE" = "up" ] && [ -n "$FINAL_IP" ]; then
            # B did not verify but the wireless iface came up anyway
            # with a non-setup-AP IP. On rhino/spotty/maple this is
            # typically A (direct wpa_supplicant.conf write + restart)
            # or C (wpa_cli add_network). On taigan it would be the
            # post-survey B retry or NetManager picking up the profile
            # after `network mode auto`. D runs backgrounded and may
            # still be deciding at this point — its own result line in
            # the log distinguishes if D actually finished the job
            # after this summary fired.
            WINNER="A_or_C_or_taigan-retry"
        fi
        SETUPAP_ACTIVE=""
        case "$FINAL_IPS_ALL" in
            *192.168.1.1*|*192.0.2.1*) SETUPAP_ACTIVE=1 ;;
        esac
        setup_log "Approach SUMMARY: winner=$WINNER elapsed=${WLAN_ELAPSED}s iface=$WLAN_IFACE state=${FINAL_STATE:-?} ip=${FINAL_IP:-none}${SETUPAP_ACTIVE:+ (setup-AP-alias-still-present)} taigan=${TAIGAN_MODE:-0}"
        setup_log "=== WLAN provisioning end ==="
    else
        # Ethernet-only / no-creds path. Still emit a SUMMARY line so the
        # diagnostic bundle has a parseable single-line winner record on
        # every boot regardless of WLAN code-path taken.
        WLAN_T1=$(awk '{print int($1)}' /proc/uptime 2>/dev/null)
        WLAN_ELAPSED=$(( ${WLAN_T1:-0} - ${WLAN_T0:-0} ))
        ETH_STATE=$(cat /sys/class/net/eth0/operstate 2>/dev/null)
        ETH_IP=$(ip -4 addr show eth0 2>/dev/null | sed -n 's/.*inet \([0-9.]*\)\/.*/\1/p' | head -1)
        setup_log "wlan creds invalid or absent (SSID/PASS empty), aborting WLAN provisioning"
        setup_log "Approach SUMMARY: winner=ethernet-only elapsed=${WLAN_ELAPSED}s iface=${WLAN_IFACE:-none} eth0_state=${ETH_STATE:-?} eth0_ip=${ETH_IP:-none} taigan=${TAIGAN_MODE:-0}"
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

# The earlier "Auto Update" version-compare block that lived here is
# gone: sync_stick_to_nand_always already mirrored stick -> NAND
# unconditionally during the early-boot phase of this script. By the
# time we get here, $CACHED_BIN is already the stick's binary (or
# the previous NAND binary if the stick was unreadable, which the
# early sync logs). Re-running the sync here would just double the
# work.

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
    #
    # Boot grace window: for the first 30 s after start_agent the
    # watchdog only checks ALIVE, not BOUND. The Go agent does a
    # sequence of pre-listen init steps (presets.Load, hosts.Apply,
    # tlsgen.EnsureBundle which generates a CA on first run, mDNS
    # announce, a 5 s timeout against the Bose firmware /info
    # endpoint) before startHTTP fires. On weak hardware those steps
    # can take 20-25 s end to end. Previously the t=5 s check saw
    # alive=1 bound=0, killed the process during init, and the box
    # ended up in a respawn loop with no listener ever reaching its
    # bind call. Observed live in deqw #60 v0.5.5 setup_log at t=57s.
    #
    # During grace we still respawn if the process is GONE (ALIVE=0)
    # because that means it crashed — we want to recover. After the
    # grace window the full BOUND check kicks in so a hung agent
    # that never binds also gets restarted.
    BOOT_RESTARTS=0
    GRACE_S=30
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
        NOW_S=$(uptime_s)
        IN_GRACE=0
        if [ "$NOW_S" != "?" ] && [ "$i" -lt $((GRACE_S / 5)) ]; then
            IN_GRACE=1
        fi
        if [ "$ALIVE" = "1" ] && [ "$BOUND" = "1" ]; then
            continue
        fi
        if [ "$ALIVE" = "1" ] && [ "$IN_GRACE" = "1" ]; then
            # Process is alive, bind has not happened yet, still in
            # the 30 s pre-listen init window. Let it cook.
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
        setup_log "boot-watchdog: agent dead/unbound at t=$(uptime_s)s pid=$CUR_PID alive=$ALIVE bound=$BOUND grace=$IN_GRACE, fast respawn #$BOOT_RESTARTS"
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

# === USB Stick aushaengen ===
# Bootstrap ist durch — alle Configs (wlan/region/name/presets/binary)
# sind nach NAND uebernommen. Den Stick brauchen wir zur Laufzeit
# nicht mehr. Wir haengen ihn aktiv aus damit der User den Stick im
# Betrieb ziehen kann ohne dirty FS (Windows muss dann keine FAT
# Reparatur mehr machen).
#
# SSH wird absichtlich NICHT gestoppt — pre-1.0 lassen wir den Kanal
# offen damit die Desktop App ihren Diagnostic Bundle Pull auch bei
# kaputtem Agent durchziehen kann (siehe ensure_sshd_running am
# Anfang von run.sh). Die fruehere Logik hat sshd hier explizit
# beendet sobald der Agent auf :8888 erreichbar war, was bei jedem
# spaeteren Crash den Pfad zu den Logs verschloss.
#
# Debug opt-out: if /media/sda1/keep-open exists, skip the entire
# cleanup so the stick stays mounted. Used during interactive
# debugging when we want to read live /media/sda1 state without
# rebooting the box. Removed by deleting the file (e.g. del
# E:\keep-open from Jens' laptop) — next boot returns to normal
# cleanup behavior.
if [ -e "$STICK/keep-open" ]; then
    setup_log "keep-open marker on stick — skipping umount for live debug"
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

    # Agent reachability gate: if the agent is still down we keep the
    # stick MOUNTED (not just sshd alive) so manual recovery via the
    # stick's run.sh / install.sh stays possible. Probe :8888 from
    # inside the box. sshd itself stays alive in either branch — that
    # decision moved to ensure_sshd_running at boot time.
    AGENT_OK=0
    if (echo > /dev/tcp/127.0.0.1/8888) >/dev/null 2>&1; then
        AGENT_OK=1
    elif command -v nc >/dev/null 2>&1 && nc -z 127.0.0.1 8888 >/dev/null 2>&1; then
        AGENT_OK=1
    fi

    sync
    if [ "$AGENT_OK" = "0" ]; then
        log "post-bootstrap: agent NOT bound on :8888 — leaving stick mounted for diagnostics"
        setup_log "post-bootstrap: agent NOT bound on :8888 — leaving stick mounted"
        # Read-only remount so a yanked stick does not corrupt FAT,
        # but DO NOT umount. Lets the desktop app's diagnostic bundle
        # pull box-side logs after a failed install without the user
        # typing anything.
        mount -o remount,ro "$STICK" 2>/dev/null \
            && log "USB Stick read-only remounted"
        exit 0
    fi
    if umount "$STICK" 2>/dev/null; then
        log "USB Stick ausgehaengt — kann sicher gezogen werden"
    else
        log "umount fehlgeschlagen (Prozess haelt Stick), versuche read only remount"
        if mount -o remount,ro "$STICK" 2>/dev/null; then
            log "USB Stick read only remounted"
        fi
    fi
) &
fi
