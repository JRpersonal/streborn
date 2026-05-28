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

# LD_PRELOAD shim for chipset-whitelist hijack of Bose's SoftwareUpdate
# daemon. The shim is a tiny .so that hooks accept() on port 17008 and
# proxies incoming connections to STR webui on 127.0.0.1:8888. Without
# this hijack STR's :8888 listener is unreachable from outside on every
# SoundTouch variant we have tested — the BCO wifi chipset firmware
# whitelists only listeners bound by binaries linked against Bose's
# libProtobufMessagingIPC / libIPC / libSoundTouchInternal libraries.
# See usb-stick/shim/README.md for the full story and
# project_taigan_chipset_whitelist memory.
STICK_SHIM="$STICK/str-shim.so"
NAND_SHIM="$PERSIST/lib/str-shim.so"

mkdir -p "$PERSIST/bin" "$PERSIST/lib" "$PERSIST/logs" "$PERSIST/state" 2>/dev/null

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
        STR_API=0; STR_MARGE=0; STR_BMX=0; STR_TLS=0
        DEADLINE_UP=$(( $(uptime_s) + 240 ))
        # tcp_probe tries /dev/tcp first (cheap, no fork) and falls back
        # to nc -z. Returns 0 on connect, non-zero on refusal/timeout.
        tcp_probe() {
            p=$1
            (echo > /dev/tcp/127.0.0.1/"$p") >/dev/null 2>&1 \
                || nc -z 127.0.0.1 "$p" >/dev/null 2>&1
        }
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
            if [ "$BOSE_WS" -eq 0 ] && tcp_probe 8080; then
                BOSE_WS=$UP
                setup_log "phase: gabbo WS :8080 listening at uptime=${UP}s"
            fi
            if [ "$AVT" -eq 0 ] && tcp_probe 8091; then
                AVT=$UP
                setup_log "phase: AVTransport :8091 listening at uptime=${UP}s"
            fi
            # STR agent listeners. Probing these explicitly is the
            # whole point of the rewrite: when :8888 silently never
            # binds (observed on Brecht's ST20 v0.5.10..v0.5.12), the
            # phase summary line is the first place a remote diagnostic
            # bundle reveals it.
            if [ "$STR_API" -eq 0 ] && tcp_probe 8888; then
                STR_API=$UP
                setup_log "phase: STR webui :8888 listening at uptime=${UP}s"
            fi
            if [ "$STR_MARGE" -eq 0 ] && tcp_probe 9080; then
                STR_MARGE=$UP
                setup_log "phase: STR marge :9080 listening at uptime=${UP}s"
            fi
            if [ "$STR_BMX" -eq 0 ] && tcp_probe 8081; then
                STR_BMX=$UP
                setup_log "phase: STR bmx :8081 listening at uptime=${UP}s"
            fi
            if [ "$STR_TLS" -eq 0 ] && tcp_probe 443; then
                STR_TLS=$UP
                setup_log "phase: STR marge-tls :443 listening at uptime=${UP}s"
            fi
            if [ "$WLAN_UP" -eq 0 ] && [ -r /sys/class/net/wlan0/operstate ]; then
                STATE=$(cat /sys/class/net/wlan0/operstate 2>/dev/null)
                if [ "$STATE" = "up" ]; then
                    WLAN_UP=$UP
                    IPADDR=$(ip -4 addr show wlan0 2>/dev/null | sed -n 's/.*inet \([0-9.]*\).*/\1/p' | head -1)
                    setup_log "phase: wlan0 link up at uptime=${UP}s ip=${IPADDR:-none}"
                fi
            fi
            # Done early once everything is up — including the four
            # STR listener phases so we always log them even on a
            # WLAN-free ethernet-only setup.
            if [ "$WPA" -gt 0 ] && [ "$BOSE_HTTP" -gt 0 ] \
                && [ "$BOSE_WS" -gt 0 ] && [ "$AVT" -gt 0 ] \
                && [ "$WLAN_UP" -gt 0 ] \
                && [ "$STR_API" -gt 0 ] && [ "$STR_MARGE" -gt 0 ] \
                && [ "$STR_BMX" -gt 0 ] && [ "$STR_TLS" -gt 0 ]; then
                break
            fi
            sleep 3
        done
        setup_log "phase summary: wpa=${WPA}s boseHTTP=${BOSE_HTTP}s gabbo=${BOSE_WS}s avt=${AVT}s wlan0Up=${WLAN_UP}s strAPI=${STR_API}s strMarge=${STR_MARGE}s strBmx=${STR_BMX}s strTLS=${STR_TLS}s"
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
# Wireless-interface detection. Two real cases on SoundTouch hardware
# (every model has Wi-Fi — the Portable has ONLY Wi-Fi, no RJ45):
#
#   1. Classic-stack variants where the Wi-Fi chip is enumerated as
#      /sys/class/net/wlan0 (sometimes wlan1) with full wpa_supplicant
#      + wpa_cli userland: rhino (ST10), some sm2/scm ST20 builds, etc.
#
#   2. BCO-stack variants where the Wi-Fi chip is enumerated as
#      /sys/class/net/eth0 (no wlan* iface at all) and wpa_supplicant /
#      wpa_cli binaries are missing. NetManager talks to the chip via
#      TAP CLI on :17000 under the `network wifi` namespace; the HTTP
#      /addWirelessProfile + /performWirelessSiteSurvey endpoints are
#      unwired in this build and reliably return 500/400.
#      Confirmed BCO codenames so far: `taigan` (Portable), `spotty`
#      (ST20 rev observed in #60). The list is extended as new
#      diagnostic bundles surface more codenames.
#
# Structural fallback: if hostname matches none of the known BCO
# codenames, but /sys/class/net has eth0 and no wlan*, we still run
# the BCO/TAP-CLI provisioning path. Reasoning: every SoundTouch model
# has Wi-Fi; "no wlan*" + "eth0 only" implies the chip is exposed as
# eth0 (the BCO pattern). A real ethernet-cable-only scenario WOULD
# still be reachable on the LAN, so over-provisioning Wi-Fi on top is
# at worst a wasted 60s — better than the v0.5.13 mis-classification
# that silently skipped provisioning on spotty boxes that needed it.
WLAN_IFACE=""
BCO_MODE=""
IS_TAIGAN=""
if [ -d /sys/class/net/wlan0 ]; then
    WLAN_IFACE="wlan0"
    echo "wlan0" > "$PERSIST/wlan-mode" 2>/dev/null
elif [ -d /sys/class/net/wlan1 ]; then
    WLAN_IFACE="wlan1"
    echo "wlan1" > "$PERSIST/wlan-mode" 2>/dev/null
elif [ -d /sys/class/net/eth0 ]; then
    # Known BCO codenames + structural fallback. `uname -n` reliably
    # mirrors the Bose codename ("Linux taigan ...", "Linux spotty ...")
    # in every captured setup.log. /proc/variant is unreadable on some
    # FW revisions and has-bco lives in /sbin OR /usr/sbin depending on
    # build, so we check all three plus the structural pattern.
    VARIANT=$(cat /proc/variant 2>/dev/null | tr -d '\n\r ' | head -c 32)
    HOSTID=$(uname -n 2>/dev/null | tr -d '\n\r ' | head -c 32)
    case "$VARIANT" in taigan|spotty) BCO_BY_VARIANT=1 ;; *) BCO_BY_VARIANT="" ;; esac
    case "$HOSTID"  in taigan|spotty) BCO_BY_HOST=1    ;; *) BCO_BY_HOST=""    ;; esac
    # taigan-specific flag: on this firmware build the documented WLAN
    # provisioning channels are all dead (HTTP /addWirelessProfile 500,
    # TAP CLI accepts add but never persists, wpa_* binaries missing).
    # The ONLY working channel is the Bose iOS app via BLE — see
    # [[taigan-quirks]] memory. Skipping A/B saves ~7 min of futile
    # retries per boot and stops the 5-min Bose-Setup-AP reboot loop.
    case "$VARIANT" in taigan) IS_TAIGAN=1 ;; esac
    case "$HOSTID"  in taigan) IS_TAIGAN=1 ;; esac
    if [ -n "$IS_TAIGAN" ]; then
        BCO_MODE=1
        WLAN_IFACE="eth0"
        echo "taigan-bco" > "$PERSIST/wlan-mode" 2>/dev/null
        setup_log "WLAN: taigan/BCO chassis (variant=${VARIANT:-?} host=${HOSTID:-?}), Wi-Fi-via-eth0, documented APIs dead — see Bose iOS app channel"
    elif [ -n "$BCO_BY_VARIANT" ] || [ -n "$BCO_BY_HOST" ] \
       || [ -x /sbin/has-bco ] || [ -x /usr/sbin/has-bco ]; then
        BCO_MODE=1
        WLAN_IFACE="eth0"
        echo "bco" > "$PERSIST/wlan-mode" 2>/dev/null
        setup_log "WLAN: BCO chassis detected (variant=${VARIANT:-?} host=${HOSTID:-?}), Wi-Fi-via-eth0"
    else
        # Structural fallback: SoundTouch hardware always has Wi-Fi,
        # so eth0-only + no wlan* almost certainly means the chip is
        # exposed as eth0 under a codename we haven't catalogued yet.
        BCO_MODE=1
        WLAN_IFACE="eth0"
        echo "bco" > "$PERSIST/wlan-mode" 2>/dev/null
        setup_log "WLAN: eth0-only with no wlan*, assuming BCO pattern (variant=${VARIANT:-?} host=${HOSTID:-?}) — codename not in known list, treating as Wi-Fi-via-eth0"
    fi
fi
if [ -z "$WLAN_IFACE" ]; then
    setup_log "WLAN: no wireless interface present (no wlan*, no eth0), ethernet-only mode (skip provisioning)"
    echo "ethernet-only" > "$PERSIST/wlan-mode" 2>/dev/null
    SSID=""
    PASS=""
fi

# Whole block runs in a backgrounded subshell so any slow upstream
# (4 minute /addWirelessProfile loops observed on #60, async TAP CLI
# responses landing 15s after the request, hostapd teardown waits)
# cannot delay start_agent below. Agent binds :8888 within seconds
# and the install wizard's 180s poll window succeeds even when this
# block is still trying its later fallbacks.
#
# Architecture: every WLAN provisioning method we have learned across
# the SoundTouch product line is tried IN SEQUENCE on every box, in
# order of "fastest path that has the highest historical success rate
# for the most boxes" and skipped only when its hard preconditions are
# missing (e.g. wpa_supplicant binary absent ⇒ skip Approach A, nc
# binary absent ⇒ skip TAP CLI). After each method we check whether
# the box has a real STA lease yet; the first one to succeed wins
# and the rest is skipped. The same pipeline is used on rhino, sm2,
# scm, spotty, taigan and any future variant — we no longer split
# the code path by model because individual ST20 boxes vary by HW
# revision and stock firmware in ways that make a single switch
# unreliable. The model-detection above is informational only (logs
# what we know about the box, does not control what we try).
( WLAN_T0=$(awk '{print int($1)}' /proc/uptime 2>/dev/null)

# Snapshot box capabilities up front, ALWAYS. Without probes in the
# ethernet-only path we cannot tell from a remote diagnostic bundle
# why a given box was classified the way it was, or which methods
# would have been skipped. Each command is silent on absence.
setup_log "probe: /sys/class/net = $(ls /sys/class/net 2>/dev/null | tr '\n' ' ')"
setup_log "probe: uname -n = $(uname -n 2>/dev/null)"
setup_log "probe: /proc/variant = $(cat /proc/variant 2>/dev/null | head -c 64 || echo missing)"
HAS_WPA_CLI=""; HAS_WPA_SUP=""; HAS_NC=""; HAS_TIMEOUT=""
TAP_CMD=""
if command -v wpa_cli >/dev/null 2>&1;        then HAS_WPA_CLI=1; setup_log "probe: wpa_cli present";         else setup_log "probe: wpa_cli MISSING"; fi
if command -v wpa_supplicant >/dev/null 2>&1; then HAS_WPA_SUP=1; setup_log "probe: wpa_supplicant present";  else setup_log "probe: wpa_supplicant MISSING"; fi
if command -v nc >/dev/null 2>&1;             then HAS_NC=1;      setup_log "probe: nc present";              else setup_log "probe: nc MISSING"; fi
if command -v timeout >/dev/null 2>&1;        then HAS_TIMEOUT=1;                                                                                                fi
# Build the TAP CLI invocation once. BusyBox `nc -w SECS` is a connect/
# final-read idle timeout, NOT a session cap — the TAP server on
# :17000 keeps the socket open after the last command, so nc would
# block forever. `timeout` is a hard wall-clock cap, but its argument
# syntax differs: BusyBox uses `timeout -t SECS CMD`, GNU coreutils
# uses `timeout SECS CMD`. Probe which.
if [ "$HAS_NC" = "1" ]; then
    if [ "$HAS_TIMEOUT" = "1" ]; then
        if timeout --help 2>&1 | grep -q '\-t '; then
            TAP_CMD="timeout -t 80 nc 127.0.0.1 17000"
        else
            TAP_CMD="timeout 80 nc 127.0.0.1 17000"
        fi
    else
        TAP_CMD="nc -w 70 127.0.0.1 17000"
    fi
    TAP_VER=$(printf 'sys ver\n' | nc -w 2 127.0.0.1 17000 2>/dev/null | tr '\n' ' ' | head -c 200)
    setup_log "probe: TAP :17000 sys ver = ${TAP_VER:-no-response}"
    TAP_NET=$(printf 'network status\n' | nc -w 2 127.0.0.1 17000 2>/dev/null | tr '\n' ' ' | head -c 300)
    setup_log "probe: TAP :17000 network status = ${TAP_NET:-no-response}"
    TAP_WIFI=$(printf 'network wifi profiles info\n' | nc -w 3 127.0.0.1 17000 2>/dev/null | tr '\n' ' ' | head -c 300)
    setup_log "probe: TAP :17000 wifi profiles info = ${TAP_WIFI:-no-response}"
else
    setup_log "probe: TAP CLI probes skipped (nc missing)"
fi

# Helpers used by every method below.
is_real_sta_addr() {
    # Setup-AP gateway IPs the speaker hosts itself on. Anything else
    # is a real DHCP lease (even 192.168.1.x from a home router whose
    # subnet happens to match — only the literal .1 / .0.2.1 gateway
    # addresses are setup-AP). A correctness fix vs the earlier
    # 192.168.1.0/24 wildcard, which broke any user whose home LAN
    # used the very common 192.168.1.0/24 default.
    case "$1" in
        ""|192.168.1.1|192.0.2.1) return 1 ;;
        *) return 0 ;;
    esac
}
current_sta_lease() {
    # Walks every wireless-candidate iface (wlan0, wlan1, eth0 on
    # BCO chassis) and prints "iface|ip" for the first real STA lease
    # it finds. Returns 1 if none.
    for _iface in wlan0 wlan1 eth0; do
        [ -d "/sys/class/net/$_iface" ] || continue
        for _ip in $(ip -4 addr show "$_iface" 2>/dev/null | sed -n 's/.*inet \([0-9.]*\)\/.*/\1/p'); do
            if is_real_sta_addr "$_ip"; then
                printf '%s|%s' "$_iface" "$_ip"
                return 0
            fi
        done
    done
    return 1
}
wait_for_sta_lease() {
    # $1 = total seconds to wait, polling every 5s.
    _budget="$1"
    while [ "$_budget" -gt 0 ]; do
        if current_sta_lease >/dev/null; then return 0; fi
        sleep 5
        _budget=$((_budget - 5))
    done
    return 1
}

if [ -n "$SSID" ] && [ -n "$PASS" ]; then
    setup_log "=== WLAN provisioning start (boot at $(uptime | tr -s ' ')) source=$WLAN_SOURCE ==="
    setup_log "wlan.conf parsed: SSID='$SSID' password_length=${#PASS}"

    BOSE_API="http://127.0.0.1:8090"
    WINNER="none"

    # ---- M0a: Pre-flight bypass — already on wifi? ----
    # If the box already has a real STA lease (e.g. user provisioned
    # via Bose iOS app before this STR install, or a previous STR
    # boot's profile is still in NetManager's DB and Bose just
    # associated to it), DO NOT run any of the M1..M6 provisioning
    # methods. M2's `network wifi profiles clear` would wipe whatever
    # is in the DB; M1's HTTP /addWirelessProfile would race the
    # in-flight associate; M3 would overwrite /etc/wpa_supplicant.conf;
    # M5/M6 would tear down the very network we already have. All of
    # those are destructive when the user-visible network already
    # works — observed live on taigan/Portable 2026-05-28 where
    # STR's profiles clear wiped JJ3 right after Bose iOS app
    # provisioned it, and applies equally to ST10/20/30 if the user
    # re-installs STR on top of a working box.
    #
    # Bose's stack is idempotent on STA lease (it does not unjoin and
    # rejoin every boot), so if a lease is present we know the box
    # is fine without us. STR's REST API, mDNS announce, marge stub,
    # autopair etc. all run downstream of this block, unaffected.
    if PRE_LEASE=$(current_sta_lease 2>/dev/null) && [ -n "$PRE_LEASE" ]; then
        setup_log "M0a: pre-flight detected real STA lease ($PRE_LEASE) — skipping all WLAN provisioning, leaving Bose state intact"
        setup_log "Approach SUMMARY: winner=already-on-wifi elapsed=0s iface=${PRE_LEASE%%|*} ip=${PRE_LEASE#*|} bco=${BCO_MODE:-0} taigan=${IS_TAIGAN:-0}"
        setup_log "=== WLAN provisioning end (skipped) ==="
        # Persist the credentials anyway — they may differ from what's
        # currently in NetManager's DB, and a future Bose factory
        # reset that wipes that DB should still let us replay from
        # NAND on the next boot.
        { printf 'SSID=%s\n' "$SSID"
          printf 'PASS=%s\n' "$PASS"
        } > "$WLAN_CREDS_NAND.new" 2>/dev/null
        if [ -s "$WLAN_CREDS_NAND.new" ]; then
            mv "$WLAN_CREDS_NAND.new" "$WLAN_CREDS_NAND" 2>/dev/null
            chmod 600 "$WLAN_CREDS_NAND" 2>/dev/null
        fi
        WINNER="M0a-prelease"
    fi

    # Wait for BoseApp HTTP server up to 30s. M1 needs it; M2..M6
    # do not and run regardless. If BoseApp never comes up we still
    # try TAP CLI / wpa_supplicant / wpa_cli paths.
    setup_log "M0: waiting for BoseApp on $BOSE_API (timeout 30s)"
    i=0
    BOSE_OK=""
    while [ $i -lt 30 ]; do
        if wget -qO- -T 2 "$BOSE_API/info" >/dev/null 2>&1; then
            setup_log "M0: BoseApp reachable after ${i}s"
            BOSE_OK=1
            break
        fi
        sleep 1
        i=$((i + 1))
    done
    [ -z "$BOSE_OK" ] && setup_log "M0: BoseApp did not respond within 30s, will skip BoseApp-dependent methods"

    if [ "$BOSE_OK" = "1" ]; then
        NETINFO_BEFORE=$(wget -qO- -T 3 "$BOSE_API/networkInfo" 2>/dev/null | head -c 400)
        setup_log "pre-state: $NETINFO_BEFORE"
    fi

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
    HTTP_BODY="<AddWirelessProfile timeout=\"30\"><profile ssid=\"$ESSID\" password=\"$EPASS\" securityType=\"wpa_or_wpa2\" /></AddWirelessProfile>"

    # =====================================================================
    # SHOTGUN PIPELINE.
    # Every method known to work on any SoundTouch variant is tried in
    # sequence on every box. After each method we wait briefly for a
    # real STA lease; first method that produces one wins, rest is
    # skipped. Methods whose hard preconditions are missing (binary
    # absent, endpoint not present) are logged as SKIP with reason.
    #
    # Order (cheapest + highest historical success first):
    #   M1  HTTP B            (/addWirelessProfile on :8090)
    #   M2  TAP B             (network wifi profiles add via :17000)
    #   M3  Approach A        (write /etc/wpa_supplicant.conf + restart)
    #   M4  Approach C        (wpa_cli add_network)
    #   M5  TAP nudge         (airplay setupap exit + network mode auto)
    #   M6  Approach D        (setup-AP teardown + preset-key burst,
    #                          backgrounded so SUMMARY can still fire)
    # =====================================================================

    # ---- M1: HTTP B — POST /addWirelessProfile ------------------------
    if [ -n "$IS_TAIGAN" ]; then
        # Live-verified 2026-05-25 + 2026-05-28: this endpoint returns
        # HTTP 500 on every taigan firmware build observed. The
        # operation is simply not wired in WebServer-taigan.xml. Skipping
        # avoids 8 s of wget + 30 s of lease-wait on every boot.
        setup_log "M1: SKIP reason=taigan-firmware (/addWirelessProfile returns 500 — use Bose iOS app via BLE)"
    elif [ "$BOSE_OK" = "1" ]; then
        setup_log "M1: POST $BOSE_API/addWirelessProfile"
        RESP=$(wget -qO- -T 8 --header="Content-Type: application/xml" \
               --post-data="$HTTP_BODY" "$BOSE_API/addWirelessProfile" 2>&1)
        RC=$?
        setup_log "M1: rc=$RC response='$(echo "$RESP" | head -c 400)'"
        if [ $RC -eq 0 ] && echo "$RESP" | grep -qi "AddWirelessProfileResponse"; then
            setup_log "M1: API persisted profile (NetManager DB updated)"
        fi
        if wait_for_sta_lease 30; then
            RES=$(current_sta_lease)
            WINNER="M1-http"
            setup_log "M1: result=YES lease=$RES"
        else
            setup_log "M1: result=NO no real STA lease within 30s"
        fi
    else
        setup_log "M1: SKIP reason=BoseApp-not-reachable"
    fi

    # ---- M2: TAP B — network wifi profiles add via :17000 -------------
    if [ "$WINNER" = "none" ] && [ -n "$IS_TAIGAN" ]; then
        # On taigan the TAP sequence accepts every command with OK but
        # `network wifi profiles info` returns <WiFiProfiles /> (empty)
        # right after the supposedly-successful add. Verified across
        # password variants, scan-wait durations, and security_type
        # spellings — see [[taigan-quirks]] memory. Skip to avoid the
        # 60 s lease-wait that always fails and the misleading "M2:
        # NetManager accepted the sequence" log line.
        setup_log "M2: SKIP reason=taigan-firmware (TAP CLI accepts add but never persists — use Bose iOS app via BLE)"
    fi
    if [ "$WINNER" = "none" ] && [ -z "$IS_TAIGAN" ]; then
        if [ -n "$TAP_CMD" ]; then
            setup_log "M2: TAP CLI sequence (scan, profiles clear/add, mode auto, setupap exit)"
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
                    printf 'airplay setupap exit\n'
                    sleep 5
                    printf 'network mode auto\n'
                    sleep 8
                ) | $TAP_CMD 2>&1
            )
            echo "$TAP_OUT" > "$PERSIST/tap-trace.log" 2>/dev/null
            setup_log "M2: response (first 800c)='$(echo "$TAP_OUT" | tr '\n' '|' | head -c 800)'"
            setup_log "M2: full trace -> $PERSIST/tap-trace.log ($(wc -c < "$PERSIST/tap-trace.log" 2>/dev/null || echo 0) bytes)"
            if echo "$TAP_OUT" | grep -qiF "Add requested" \
               || echo "$TAP_OUT" | grep -qF "$SSID" \
               || echo "$TAP_OUT" | grep -qiF "mode set to auto"; then
                setup_log "M2: NetManager accepted the sequence"
            fi
            if wait_for_sta_lease 60; then
                RES=$(current_sta_lease)
                WINNER="M2-tap"
                setup_log "M2: result=YES lease=$RES"
            else
                setup_log "M2: result=NO no real STA lease within 60s"
            fi
        else
            setup_log "M2: SKIP reason=nc-missing (TAP CLI unavailable)"
        fi
    fi

    # ---- M3: Approach A — write /etc/wpa_supplicant.conf + restart ----
    if [ "$WINNER" = "none" ]; then
        if [ "$HAS_WPA_SUP" = "1" ]; then
            setup_log "M3: write /etc/wpa_supplicant.conf + restart"
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
                setup_log "M3: direct-write OK ($(wc -c < "$WPA_CONF") bytes)"
            elif mount --bind "$TMP" "$WPA_CONF" 2>/dev/null; then
                setup_log "M3: bind-mount active (read-only /etc workaround)"
            else
                setup_log "M3: WARN could not write /etc/wpa_supplicant.conf (direct + bind both failed)"
            fi
            if pidof wpa_supplicant >/dev/null 2>&1; then
                killall wpa_supplicant 2>/dev/null
                sleep 1
                _WI="wlan0"
                [ -d /sys/class/net/wlan1 ] && [ ! -d /sys/class/net/wlan0 ] && _WI="wlan1"
                wpa_supplicant -B -i "$_WI" -s -c "$WPA_CONF" -D nl80211 2>/dev/null &
                setup_log "M3: wpa_supplicant restarted on $_WI"
            else
                setup_log "M3: wpa_supplicant not running (Bose may bring it up later)"
            fi
            if wait_for_sta_lease 30; then
                RES=$(current_sta_lease)
                WINNER="M3-confwrite"
                setup_log "M3: result=YES lease=$RES"
            else
                setup_log "M3: result=NO no real STA lease within 30s"
            fi
        else
            setup_log "M3: SKIP reason=wpa_supplicant-binary-missing"
        fi
    fi

    # ---- M4: Approach C — wpa_cli add_network -------------------------
    if [ "$WINNER" = "none" ]; then
        if [ "$HAS_WPA_CLI" = "1" ]; then
            _WI="wlan0"
            [ -d /sys/class/net/wlan1 ] && [ ! -d /sys/class/net/wlan0 ] && _WI="wlan1"
            setup_log "M4: wpa_cli add_network on $_WI"
            NETID=$(wpa_cli -i "$_WI" add_network 2>/dev/null | tail -1)
            setup_log "M4: add_network -> id=$NETID"
            if [ -n "$NETID" ] && [ "$NETID" -ge 0 ] 2>/dev/null; then
                SSID_ESC=$(printf '%s' "$SSID" | sed 's/"/\\"/g')
                PSK_ESC=$(printf '%s' "$PASS" | sed 's/"/\\"/g')
                wpa_cli -i "$_WI" set_network "$NETID" ssid "\"$SSID_ESC\"" >/dev/null 2>&1; R1=$?
                wpa_cli -i "$_WI" set_network "$NETID" psk "\"$PSK_ESC\""   >/dev/null 2>&1; R2=$?
                wpa_cli -i "$_WI" set_network "$NETID" key_mgmt WPA-PSK    >/dev/null 2>&1; R3=$?
                wpa_cli -i "$_WI" enable_network "$NETID"                  >/dev/null 2>&1; R4=$?
                wpa_cli -i "$_WI" select_network "$NETID"                  >/dev/null 2>&1; R5=$?
                wpa_cli -i "$_WI" save_config                              >/dev/null 2>&1; R6=$?
                setup_log "M4: set ssid=$R1 psk=$R2 key_mgmt=$R3 enable=$R4 select=$R5 save=$R6"
                if [ "$R1" = "0" ] && [ "$R2" = "0" ] && [ "$R4" = "0" ]; then
                    { printf 'SSID=%s\n' "$SSID"
                      printf 'PASS=%s\n' "$PASS"
                    } > "$WLAN_CREDS_NAND.new" 2>/dev/null
                    if [ -s "$WLAN_CREDS_NAND.new" ]; then
                        mv "$WLAN_CREDS_NAND.new" "$WLAN_CREDS_NAND" 2>/dev/null
                        chmod 600 "$WLAN_CREDS_NAND" 2>/dev/null
                        setup_log "M4: persisted creds to NAND for next-boot replay"
                    fi
                fi
            fi
            if wait_for_sta_lease 30; then
                RES=$(current_sta_lease)
                WINNER="M4-wpacli"
                setup_log "M4: result=YES lease=$RES"
            else
                setup_log "M4: result=NO no real STA lease within 30s"
            fi
        else
            setup_log "M4: SKIP reason=wpa_cli-binary-missing"
        fi
    fi

    # ---- M5: TAP nudge — airplay setupap exit + network mode auto -----
    if [ "$WINNER" = "none" ] && [ -n "$IS_TAIGAN" ]; then
        # M5 is structurally fine on taigan (the TAP namespaces it uses
        # exist), but with no profile to fall back to it just bounces
        # NetManager between setup-AP and station-with-no-profile,
        # contributing to the 5-min Bose-reset reboot loop. Skip and let
        # the box sit in setup-AP waiting for the Bose iOS app.
        setup_log "M5: SKIP reason=taigan-firmware (no profile in DB, nudge would just churn state — wait for Bose iOS app via BLE)"
    fi
    if [ "$WINNER" = "none" ] && [ -z "$IS_TAIGAN" ]; then
        if [ -n "$TAP_CMD" ]; then
            setup_log "M5: TAP nudge (setupap exit, mode auto)"
            NUDGE=$(
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
            setup_log "M5: response (first 400c)='$(echo "$NUDGE" | tr '\n' '|' | head -c 400)'"
            if wait_for_sta_lease 30; then
                RES=$(current_sta_lease)
                WINNER="M5-tapnudge"
                setup_log "M5: result=YES lease=$RES"
            else
                setup_log "M5: result=NO no real STA lease within 30s"
            fi
        else
            setup_log "M5: SKIP reason=nc-missing"
        fi
    fi

    # ---- M6: Approach D — setup-AP teardown + preset-key burst --------
    # Always backgrounded so the SUMMARY line below fires whether D
    # is still trying or already done. D's own result lines land
    # later in the same log when it completes.
    if [ "$WINNER" = "none" ] && [ -n "$IS_TAIGAN" ]; then
        # M6 kills hostapd/udhcpd/dnsmasq + bursts preset keys to force
        # the Bose stack to reassociate. On taigan there is no hostapd
        # to kill, no wpa_cli to reassociate with, and the preset-key
        # burst cannot help because no profile is in the DB to associate
        # to. Skip and leave the box in setup-AP for the Bose iOS app.
        setup_log "M6: SKIP reason=taigan-firmware (no profile in DB and no AP daemons to tear down — wait for Bose iOS app via BLE)"
    fi
    if [ "$WINNER" = "none" ] && [ -z "$IS_TAIGAN" ]; then
        setup_log "M6: spawning backgrounded setup-AP teardown + preset burst"
        (
            sleep 8
            setup_log "M6: setup-AP teardown attempt"
            AP_PROCS=$(ps 2>/dev/null | grep -E 'hostapd|udhcpd|dnsmasq|nodogsplash' | grep -v grep | tr '\n' '|' | head -c 400)
            setup_log "M6: AP-related procs: $AP_PROCS"
            killall hostapd  2>/dev/null && setup_log "M6: hostapd killed"
            killall udhcpd   2>/dev/null && setup_log "M6: udhcpd killed"
            killall dnsmasq  2>/dev/null && setup_log "M6: dnsmasq killed"
            sleep 2
            if [ "$HAS_WPA_CLI" = "1" ]; then
                _WI="wlan0"
                [ -d /sys/class/net/wlan1 ] && [ ! -d /sys/class/net/wlan0 ] && _WI="wlan1"
                wpa_cli -i "$_WI" reassociate >/dev/null 2>&1 && setup_log "M6: $_WI reassociate sent"
                wpa_cli -i "$_WI" reconfigure >/dev/null 2>&1 && setup_log "M6: $_WI reconfigure sent"
            fi
            for wait in 6 10 15 20; do
                sleep "$wait"
                if current_sta_lease >/dev/null; then
                    setup_log "M6: result=YES lease=$(current_sta_lease) after teardown"
                    return 0
                fi
                setup_log "M6: still no lease at t+${wait}s"
            done
            if [ "$HAS_NC" = "1" ]; then
                setup_log "M6-burst: 8x slot-2, 1s apart"
                for _ in 1 2 3 4 5 6 7 8; do
                    printf 'sys presetkey 2 p\n' | nc -w 1 127.0.0.1 17000 >/dev/null 2>&1
                    sleep 1
                done
                sleep 6
                if current_sta_lease >/dev/null; then
                    setup_log "M6-burst: result=YES lease=$(current_sta_lease)"
                    return 0
                fi
                setup_log "M6-burst: spaced phase (slots 2..1, 12s apart)"
                for slot in 2 3 4 5 6 1; do
                    printf 'sys presetkey %d p\n' "$slot" | nc -w 2 127.0.0.1 17000 >/dev/null 2>&1
                    setup_log "M6-burst: slot $slot sent"
                    sleep 12
                    if current_sta_lease >/dev/null; then
                        setup_log "M6-burst: result=YES lease=$(current_sta_lease) after slot $slot"
                        return 0
                    fi
                done
            fi
            setup_log "M6: all approaches exhausted — manual preset press may be required"
        ) &
    fi

    if [ "$BOSE_OK" = "1" ]; then
        sleep 2
        NETINFO_AFTER=$(wget -qO- -T 3 "$BOSE_API/networkInfo" 2>/dev/null | head -c 400)
        setup_log "post-state: $NETINFO_AFTER"
    fi

    if [ -f "$WLAN_CONF" ]; then
        rm -f "$WLAN_CONF" 2>/dev/null
        setup_log "wlan.conf removed from stick"
    fi

    WLAN_T1=$(awk '{print int($1)}' /proc/uptime 2>/dev/null)
    WLAN_ELAPSED=$(( ${WLAN_T1:-0} - ${WLAN_T0:-0} ))
    FINAL_LEASE=$(current_sta_lease)
    FINAL_IFACE=${FINAL_LEASE%%|*}
    FINAL_IP=${FINAL_LEASE##*|}
    [ "$FINAL_IFACE" = "$FINAL_LEASE" ] && FINAL_IFACE="" && FINAL_IP=""
    setup_log "Approach SUMMARY: winner=$WINNER elapsed=${WLAN_ELAPSED}s iface=${FINAL_IFACE:-?} ip=${FINAL_IP:-none} bco=${BCO_MODE:-0} taigan=${IS_TAIGAN:-0} probes='wpa_cli=$HAS_WPA_CLI wpa_sup=$HAS_WPA_SUP nc=$HAS_NC tap=${TAP_CMD:+yes}'"
    setup_log "=== WLAN provisioning end ==="
else
    WLAN_T1=$(awk '{print int($1)}' /proc/uptime 2>/dev/null)
    WLAN_ELAPSED=$(( ${WLAN_T1:-0} - ${WLAN_T0:-0} ))
    ETH_STATE=$(cat /sys/class/net/eth0/operstate 2>/dev/null)
    ETH_IP=$(ip -4 addr show eth0 2>/dev/null | sed -n 's/.*inet \([0-9.]*\)\/.*/\1/p' | head -1)
    setup_log "no WLAN creds (SSID/PASS empty), skipping provisioning pipeline"
    setup_log "Approach SUMMARY: winner=ethernet-only elapsed=${WLAN_ELAPSED}s iface=${WLAN_IFACE:-none} eth0_state=${ETH_STATE:-?} eth0_ip=${ETH_IP:-none} bco=${BCO_MODE:-0}"
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

# === iptables INPUT ACCEPT for our listener ports ===
#
# Series-I SoundTouch (SMSC/SCM components, no wlan0 interface) ships
# a Bose stock firewall that REJECTs every TCP port outside a small
# whitelist (8080/8090/8091/80/443). STR's :8888 / :9080 / :8081 /
# :8443 listeners bind fine and `netstat -ltn` shows LISTEN, but every
# inbound SYN from a desktop client gets RST'd at the INPUT chain.
# Reported live by @deqw on #60 with `nc -vz <lan-ip> 8888` returning
# RST while `nc -vz <lan-ip> 8091` succeeds, and matches Brecht's
# ST20 email thread where `reachable8888=false` despite a healthy
# agent bootstrap.
#
# Insert ACCEPT rules at position 1 of the INPUT chain so they win
# over the Bose firmware's DROP/REJECT entries. The streborn-fw
# marker lets us identify the rules later for clean removal.
#
# Two complications observed live on ST10 .66 v0.5.13 setup.log:
#
#   1. The filter table is NOT loaded yet at uptime ~22 s when our
#      run.sh block first runs. Bose's `/etc/init.d/Firewalls/
#      update_iptables` script (PID seen alive in the post-start
#      snapshot) does the modprobe + chain build later in boot. Our
#      first iptables -I INPUT calls returned non-zero and logged
#      "FAILED (filter table missing?)" — Series-II boxes still work
#      because Bose's eventual rule #3 accepts all LAN traffic, but
#      Series-I boxes silently kept rejecting :8888 because we never
#      retried.
#
#   2. Bose's Firewall script may re-build the chain later (init or
#      periodic), flushing our rules. A one-shot install is therefore
#      not enough.
#
# Solution: do the install in a background subshell that waits up to
# 60 s for the filter table to appear, installs, then re-asserts the
# rules every 30 s for the lifetime of run.sh. iptables -C is the
# idempotency guard: it returns 0 if the rule already exists, so a
# re-assert is cheap and writes nothing when nothing changed.
INPUT_ACCEPT_PORTS="8888 9080 8081 8443"
iptables_install_streborn_fw() {
    rc_total=0
    for port in $INPUT_ACCEPT_PORTS; do
        if iptables -C INPUT -p tcp --dport "$port" \
            -m comment --comment "streborn-fw" -j ACCEPT 2>/dev/null; then
            continue  # rule already present, no-op
        fi
        if iptables -I INPUT 1 -p tcp --dport "$port" \
            -m comment --comment "streborn-fw" -j ACCEPT 2>/dev/null; then
            setup_log "iptables INPUT ACCEPT tcp/$port installed at uptime=$(uptime_s)s"
        else
            rc_total=$((rc_total + 1))
        fi
    done
    return $rc_total
}
(
    # Wait up to 60 s for Bose's Firewall init to load the filter
    # table. Probe via `iptables -nL INPUT` which is the cheapest
    # query that fails when the kernel module is absent or the table
    # is not yet attached.
    w=0
    while [ $w -lt 60 ]; do
        if iptables -nL INPUT >/dev/null 2>&1; then
            setup_log "iptables filter table ready at uptime=$(uptime_s)s (wait=${w}s)"
            break
        fi
        sleep 1
        w=$((w + 1))
    done
    if [ $w -ge 60 ]; then
        setup_log "iptables filter table never came up after 60 s, skipping INPUT ACCEPT"
        exit 0
    fi
    # First install pass.
    iptables_install_streborn_fw
    # Watchdog: re-assert every 30 s in case Bose's Firewall init
    # script flushes the chain after we set up. iptables -C inside
    # iptables_install_streborn_fw makes this a no-op when our rules
    # are still present. Runs for the lifetime of run.sh.
    while true; do
        sleep 30
        iptables_install_streborn_fw
    done
) &

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
    # log-level info, not warn. Earlier builds passed `warn` and the
    # consequence was that the listener bring-up logs (`Webui Server
    # startet`, `HTTP Server startet`, ...) were suppressed entirely.
    # When :8888 silently failed to bind on a user's box we had no
    # signal in the diagnostic bundle at all. info is loud enough to
    # tell us which step reached its bind call without producing
    # tick-rate spam (autopair/zeroconf are bounded).
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
        --log-level info \
        >> "$LOG" 2>&1 &
    AGENT_PID=$!
    echo "$AGENT_PID" > "$PIDFILE"
}

try_http_date_sync
start_agent
log "agent started with PID $AGENT_PID"

# === Chipset-whitelist hijack via LD_PRELOAD on SoftwareUpdate ===
#
# On Series-I boxes (moduleType=scm, codenames taigan/spotty) the
# BCO wifi chipset firmware drops inbound external TCP to STR's
# :8888 / :9080 / :8081 at the chipset level, regardless of iptables
# state. The only externally-reachable ports are those bound by
# specific Bose binaries (libProtobufMessagingIPC magic). Verified
# live 2026-05-28 on the Portable.
#
# Fix on Series-I: keep Bose's SoftwareUpdate binary as the listener
# on :17008 (chipset stays happy) but launch it under LD_PRELOAD of
# our str-shim.so, which hooks accept() and forwards every inbound
# connection to 127.0.0.1:8888 (STR webui). SoftwareUpdate has no
# remaining purpose post-cloud-shutdown so hijacking it costs
# nothing functional.
#
# On Series-II boxes (moduleType=sm2, codenames rhino/maple/etc.)
# the chipset is permissive — STR's own :8888 is directly reachable
# from outside. The shim would hijack :17008 which is closed there
# anyway. Hijack runs only when IS_SERIES_ONE is detected.
#
# Stick-to-NAND sync of the .so happens here so a stickless boot can
# still re-hijack via the NAND copy. Watchdog re-asserts every 30 s
# in case shepherdd respawns SoftwareUpdate without our env.
sync_shim_to_nand() {
    if [ -r "$STICK_SHIM" ]; then
        if cp "$STICK_SHIM" "$NAND_SHIM.new" 2>/dev/null && \
           mv "$NAND_SHIM.new" "$NAND_SHIM" 2>/dev/null; then
            chmod 644 "$NAND_SHIM" 2>/dev/null
            log "shim deployed: $NAND_SHIM ($(wc -c < "$NAND_SHIM") bytes)"
        else
            log "shim deploy: cp/mv failed, keeping previous NAND shim"
            rm -f "$NAND_SHIM.new" 2>/dev/null
        fi
    elif [ ! -r "$NAND_SHIM" ]; then
        log "shim deploy: stick has no $STICK_SHIM and NAND has no $NAND_SHIM, hijack disabled this boot"
    fi
}
sync_shim_to_nand

# Series-I detection. Live-verified pattern: moduleType=scm in
# Bose's /info OR variant codename in {taigan, spotty} → Series-I,
# chipset whitelist blocks STR's :8888 / :9080 / :8081 externally,
# shim hijack on SoftwareUpdate :17008 is required. Everything else
# (sm2, rhino, maple, ...) is Series-II with a permissive chipset
# that lets external clients hit STR's own listeners directly; the
# shim would only block :17008 on those without offering any new
# reachability, so we skip it.
#
# The matching table is fed by user diagnostic bundles. Known
# entries 2026-05-28:
#   scm + taigan     → Portable          → Series-I → shim ON
#   scm + spotty     → ST20 (rev A, #60) → Series-I → shim ON
#   sm2 + (none/rhino/maple) → ST10/20/30 → Series-II → shim OFF
detect_series_one() {
    case "$VARIANT" in taigan|spotty) echo 1; return; esac
    case "$HOSTID"  in taigan|spotty) echo 1; return; esac
    MT=$(wget -qO- -T 3 http://127.0.0.1:8090/info 2>/dev/null \
         | sed -n 's/.*<moduleType>\([^<]*\)<\/moduleType>.*/\1/p' \
         | head -c 16)
    case "$MT" in scm) echo 1; return; esac
    echo ""
}
IS_SERIES_ONE=$(detect_series_one)
setup_log "shim gate: variant='${VARIANT:-?}' host='${HOSTID:-?}' moduleType='$(wget -qO- -T 3 http://127.0.0.1:8090/info 2>/dev/null | sed -n 's/.*<moduleType>\([^<]*\)<\/moduleType>.*/\1/p' | head -c 16)' is_series_one='${IS_SERIES_ONE:-0}'"

# SoftwareUpdate-Hijack-Logik: returnt 0 wenn der lokale Listener-PID
# auf :17008 wirklich von einem SoftwareUpdate-Prozess kommt UND
# dieser Prozess unsere LD_PRELOAD-Env-Var bereits gesetzt hat.
shim_already_active() {
    SU_PID=$(pidof SoftwareUpdate 2>/dev/null | head -c 16 | awk '{print $1}')
    [ -n "$SU_PID" ] || return 1
    if grep -qa "LD_PRELOAD=.*str-shim.so" "/proc/$SU_PID/environ" 2>/dev/null; then
        return 0
    fi
    return 1
}

# hijack_softwareupdate: kill the running SoftwareUpdate (Bose-init's
# instance, no LD_PRELOAD) and relaunch /opt/Bose/SoftwareUpdate with
# our shim preloaded. The chipset whitelist tracks the binary content,
# not the process identity — restarting with LD_PRELOAD keeps the
# whitelist slot valid.
hijack_softwareupdate() {
    if [ ! -r "$NAND_SHIM" ]; then
        return 1
    fi
    if [ ! -x /opt/Bose/SoftwareUpdate ]; then
        setup_log "shim: /opt/Bose/SoftwareUpdate not present, cannot hijack"
        return 1
    fi
    # Bose's instance has to be killed first; we hold the port via
    # SO_REUSEADDR-less default so a race-free swap requires the old
    # listener gone before we bind. start_agent has SO_REUSEADDR but
    # SoftwareUpdate does not, hence the explicit wait.
    killall SoftwareUpdate 2>/dev/null
    # Wait up to 5 s for the port to free.
    j=0
    while [ $j -lt 5 ]; do
        if ! (echo > /dev/tcp/127.0.0.1/17008) 2>/dev/null; then
            break
        fi
        sleep 1
        j=$((j + 1))
    done
    LD_PRELOAD="$NAND_SHIM" nohup /opt/Bose/SoftwareUpdate >/dev/null 2>&1 &
    setup_log "shim: SoftwareUpdate relaunched under LD_PRELOAD=$NAND_SHIM (took ${j}s for port to free)"
    return 0
}

# Background runner: wait until SoftwareUpdate is up, hijack it, then
# loop watchdog every 30 s to re-hijack if shepherdd respawned. On
# Series-II boxes the chipset whitelist is permissive and STR's :8888
# is directly reachable externally, so the hijack is unnecessary and
# is skipped to avoid breaking any localhost callers of SoftwareUpdate.
if [ -n "$IS_SERIES_ONE" ]; then
    (
        # Wait up to 90 s for the initial SoftwareUpdate to come alive.
        # On a cold boot Bose's stack typically brings it up at uptime
        # ~40 s after init finishes.
        w=0
        while [ $w -lt 90 ]; do
            if pidof SoftwareUpdate >/dev/null 2>&1; then
                setup_log "shim: SoftwareUpdate first seen at uptime=$(uptime_s)s (wait=${w}s)"
                break
            fi
            sleep 1
            w=$((w + 1))
        done
        if [ $w -ge 90 ]; then
            setup_log "shim: SoftwareUpdate never appeared in 90 s, hijack skipped (variant without SU?)"
            exit 0
        fi
        hijack_softwareupdate

        # Watchdog
        while true; do
            sleep 30
            if shim_already_active; then
                continue
            fi
            # SoftwareUpdate is either dead or running without our env.
            # Re-hijack either way.
            setup_log "shim watchdog: SU running without LD_PRELOAD at uptime=$(uptime_s)s, re-hijacking"
            hijack_softwareupdate
        done
    ) &
else
    setup_log "shim: Series-II box (chipset permissive), STR :8888 reachable directly, hijack skipped"
fi

# One-shot diagnostic snapshot 90 s after start_agent. By then any
# fast respawn churn has settled and either :8888 is up or it never
# will be. The snapshot dumps listening sockets and process tree into
# setup.log (NAND-persisted, captured in full by the diagnostic),
# replacing the SSH session we cannot run on a user's box.
(
    sleep 90
    setup_log "=== one-shot post-start snapshot (uptime=$(uptime_s)s) ==="
    if command -v ss >/dev/null 2>&1; then
        setup_log "listening sockets (ss -ltnp):"
        ss -ltnp 2>&1 | while IFS= read -r line; do setup_log "  $line"; done
    elif command -v netstat >/dev/null 2>&1; then
        setup_log "listening sockets (netstat -ltnp):"
        netstat -ltnp 2>&1 | while IFS= read -r line; do setup_log "  $line"; done
    else
        setup_log "listening sockets: ss and netstat both unavailable"
    fi
    setup_log "process tree (ps -ef or busybox ps):"
    if ps -ef >/dev/null 2>&1; then
        ps -ef 2>&1 | while IFS= read -r line; do setup_log "  $line"; done
    else
        ps 2>&1 | while IFS= read -r line; do setup_log "  $line"; done
    fi
    if [ -f "$PIDFILE" ]; then
        CUR_PID=$(cat "$PIDFILE" 2>/dev/null)
        if [ -n "$CUR_PID" ] && [ -r "/proc/$CUR_PID/status" ]; then
            setup_log "agent /proc/$CUR_PID/status (head):"
            head -20 "/proc/$CUR_PID/status" 2>&1 | while IFS= read -r line; do setup_log "  $line"; done
        else
            setup_log "agent PID $CUR_PID not alive at snapshot time"
        fi
    fi
    # iptables state. Critical for Series-I boxes (SMSC/SCM, no wlan0)
    # where Bose firmware installs a restrictive INPUT chain that
    # silently RSTs our :8888 listener even though it is correctly
    # bound. Without this dump in the bundle the case looks identical
    # to a broken bind (see issue #60 deqw 2026-05-28).
    setup_log "iptables filter INPUT:"
    iptables -L INPUT -n -v --line-numbers 2>&1 | while IFS= read -r line; do setup_log "  $line"; done
    setup_log "iptables nat PREROUTING:"
    iptables -t nat -L PREROUTING -n -v --line-numbers 2>&1 | while IFS= read -r line; do setup_log "  $line"; done
    # Try a localhost loopback connect to :8888 vs the LAN-IP connect.
    # When the two disagree (local ok, lan refused) the firewall is
    # the reason — exactly the deqw / Brecht pattern.
    LAN_IP=$(ip -4 addr show eth0 2>/dev/null | sed -n 's/.*inet \([0-9.]*\).*/\1/p' | head -1)
    if [ -z "$LAN_IP" ]; then
        LAN_IP=$(ip -4 addr show wlan0 2>/dev/null | sed -n 's/.*inet \([0-9.]*\).*/\1/p' | head -1)
    fi
    setup_log "self-connect probe: lan_ip=${LAN_IP:-unknown}"
    if (echo > /dev/tcp/127.0.0.1/8888) >/dev/null 2>&1; then
        setup_log "  127.0.0.1:8888 -> OK"
    else
        setup_log "  127.0.0.1:8888 -> refused"
    fi
    if [ -n "$LAN_IP" ]; then
        if (echo > /dev/tcp/"$LAN_IP"/8888) >/dev/null 2>&1; then
            setup_log "  $LAN_IP:8888 -> OK"
        else
            setup_log "  $LAN_IP:8888 -> refused (firewall? Series-I box?)"
        fi
    fi
    setup_log "=== /one-shot post-start snapshot ==="
) &

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
    # ss is the cheapest probe but BusyBox often ships without it.
    if command -v ss >/dev/null 2>&1; then
        if ss -ltn 2>/dev/null | grep -q ':8888 '; then
            return 0
        fi
        return 1
    fi
    if command -v netstat >/dev/null 2>&1; then
        if netstat -ltn 2>/dev/null | grep -q ':8888 '; then
            return 0
        fi
        return 1
    fi
    # /dev/tcp self-probe — works on many busybox sh builds but not all.
    if (echo > /dev/tcp/127.0.0.1/8888) >/dev/null 2>&1; then
        return 0
    fi
    # nc as the third option. Some images ship nc, others don't.
    if command -v nc >/dev/null 2>&1; then
        if nc -z 127.0.0.1 8888 >/dev/null 2>&1; then
            return 0
        fi
        return 1
    fi
    # Truly no probe available. Log once per minute (gated by the
    # AGENT_PORT_PROBE_UNKNOWN_T marker so the watchdog loop does not
    # spam the log) and treat as bound. Returning 1 here would cause
    # the watchdog to restart-loop a working agent because it cannot
    # confirm bind — strictly worse than a silent assumption. The new
    # `phase: STR webui :8888 listening` line from background_phase_probe
    # is the authoritative signal in the diagnostic bundle.
    NOW=$(uptime_s)
    LAST=${AGENT_PORT_PROBE_UNKNOWN_T:-0}
    if [ $((NOW - LAST)) -gt 60 ]; then
        setup_log "agent_port_bound: ss/netstat/dev-tcp/nc all unavailable, cannot confirm :8888 bind, assuming up"
        AGENT_PORT_PROBE_UNKNOWN_T=$NOW
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
