#!/bin/sh
# run.sh v2: the NAND cache is the source of truth.
#
# Changed compared to v1 (2026-05-15):
#   - The stick binary on the SD card is NOT automatically copied to NAND.
#     This keeps manually deployed NAND updates from disappearing on reboot.
#   - The stick binary is only used as a FALLBACK when NAND is empty.
#   - Manual stick->NAND sync: touch /mnt/nv/streborn/sync-from-stick
#     Then it syncs from the stick on the next boot.
#
# Install on the box: scp setup/run.sh stbox:/media/sda1/run.sh
#
# ===========================================================================
# TABLE OF CONTENTS (~3460 lines, single file on purpose: Bose runs this from
# the NAND rc.local copy chain, which cannot source sibling files)
# ---------------------------------------------------------------------------
#   Paths + logging .......... STICK/PERSIST/LOG vars, log, setup_log,
#                              uptime_s, start_stick_log_mirror
#   Language/region .......... lang_int_for_cc (parity-tested against
#                              sticksetup.SysLanguageForCountry), resolve_setup_language
#   Boot bring-up ............ ensure_sshd_running, initial_snapshot,
#                              wait_for_ready, background_phase_probe
#   NAND <-> stick sync ...... cleanup_nand, sync_stick_to_nand_always,
#                              sync_shim_to_nand
#   LD_PRELOAD shim .......... shim_stage_wrapper, shim_late_swap,
#                              shim_already_active, hijack_softwareupdate
#   Wi-Fi provisioning ....... persist_wlan_creds, is_real_sta_addr,
#                              current_sta_lease, wait_for_sta_lease
#   Firewall/NAT (BCO) ....... detect_series_one, iptables_nat_probe_and_modprobe,
#                              redirect_lan_ip, iptables_install_redirect_series_one,
#                              iptables_install_streborn_fw
#   Agent start .............. ports_busy, wait_ports_clear, try_http_date_sync,
#                              start_agent, agent_port_bound
#   Main flow ................ at the very bottom
#
# Every on-box script is syntax-gated in CI by `busybox sh -n` (build.yml),
# since shellcheck's parser is not the ash the speaker actually execs.
# ===========================================================================

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
# The active log lives in tmpfs (/tmp) so the NAND flash is not worn out
# in continuous operation. On every start we save the previous log to
# NAND as previous.log (survives a box reboot).
LOG="/tmp/streborn-agent.log"
PREV_LOG="$PERSIST/previous.log"
PIDFILE="$PERSIST/agent.pid"

# Save the previous session to NAND before we overwrite the tmpfs log,
# so after every crash / reboot we still have the last log at hand.
if [ -f "$LOG" ] && [ -s "$LOG" ]; then
    cp "$LOG" "$PREV_LOG" 2>/dev/null
fi
: > "$LOG"

STICK_BIN="$STICK/streborn-armv7l"
[ -e "$STICK_BIN" ] || STICK_BIN="$STICK/streborn"
CACHED_BIN="$PERSIST/bin/streborn-armv7l"
# go-librespot: the Spotify Connect sidecar (#78). Cached stick -> NAND the
# same way as the agent so it survives stick removal + reboot.
STICK_GLR="$STICK/go-librespot"
CACHED_GLR="$PERSIST/bin/go-librespot"
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
# not see whether Approach C/D even ran. NAND survives reboot and
# survives a Bose factory reset (we have observed this).
SETUP_LOG_NAND="$PERSIST/setup.log"
# Per-boot rotation: keep at most the current boot plus the previous
# boot. The NAND setup.log used to be append-only across every reboot
# (cleanup_nand only trimmed it once it passed 256KB), so a box that
# rebooted often grew it to hundreds of KB / thousands of lines (one
# diagnostic spanned 11 days / 2629 lines), wearing NAND and bloating
# the bundle. Rotate it here, at the very top, before this boot writes
# its first line. Best-effort: a missing or read-only NAND just falls
# through and this boot appends as before.
SETUP_LOG_NAND_PREV="$PERSIST/setup.log.prev"
if [ -f "$SETUP_LOG_NAND" ] && [ -s "$SETUP_LOG_NAND" ]; then
    mv "$SETUP_LOG_NAND" "$SETUP_LOG_NAND_PREV" 2>/dev/null
fi
uptime_s() {
    awk '{print int($1)}' /proc/uptime 2>/dev/null || echo "?"
}
setup_log() {
    log "$*"
    line="[up=$(uptime_s)s] $(date): $*"
    # NAND only. Per-line appends to the FAT32 stick during early boot
    # caused enough IO contention to wedge the Bose mesh init on taigan:
    # the one-shot post-start snapshot alone fans ps -ef / netstat / ss
    # out to ~150 individual setup_log calls, and on a slow stick each
    # append is a multi-ms open+seek+FAT-update. Live-observed 2026-05-31
    # the stick boot hanging hard enough that the box brought up NO wifi
    # at all, while the same box boots clean without the stick. The stick
    # copy is now produced by a slow bulk mirror instead
    # (start_stick_log_mirror), so the pull-the-stick-and-read workflow
    # still works without thousands of tiny FAT32 ops in the
    # boot-critical window.
    echo "$line" >> "$SETUP_LOG_NAND" 2>/dev/null
}

# goform_wlan_push SSID PASS
# Pushes a Wi-Fi profile straight to the SMSC/JukeBlox coprocessor's GoAhead
# :80 goform handler — the ONE path that actually PROGRAMS the radio on the
# BCO/scm chassis. NetManager's /addWirelessProfile writes its DB but the
# coprocessor never applies it (live 2026-07-09, scm SoundTouch 30:
# /addWirelessProfile set the active profile yet the box would not associate;
# the goform POST answered "New settings were successfully applied" and it
# joined). This is the standard Wi-Fi provisioning step. Safe on ANY model:
# it only POSTs where a goform server actually answers, so on an sm2 box with
# no :80 coprocessor the probe finds nothing and it no-ops; and it configures
# a profile without tearing down a working ethernet link. Cheap (a couple of
# tiny HTTP POSTs, negligible USB-port power draw), which is why it is also
# used for the early low-power one-shot before the heavy stick copy. Returns
# 0 on a confirmed apply. Defined at top level so both the early one-shot and
# the full provisioning subshell can call it. Best-effort and heavily logged.
goform_wlan_push() {
    _gf_ssid="$1"; _gf_pass="$2"
    [ -n "$_gf_ssid" ] || return 1
    # RFC-3986 url-encode, BusyBox-safe (same technique as M_jukebox _ue).
    _gf_ue() {
        _gf_s="$1"; _gf_o=""
        while [ -n "$_gf_s" ]; do
            _gf_c=${_gf_s%"${_gf_s#?}"}; _gf_s=${_gf_s#?}
            case "$_gf_c" in
                [a-zA-Z0-9._~-]) _gf_o="$_gf_o$_gf_c" ;;
                *) _gf_o="$_gf_o$(printf '%%%02X' "'$_gf_c")" ;;
            esac
        done
        printf '%s' "$_gf_o"
    }
    _gf_sec=""; _gf_cip=""; _gf_pp=""
    if [ -n "$_gf_pass" ]; then
        _gf_sec="WPA2PSK"; _gf_cip="CCMP"; _gf_pp=$(_gf_ue "$_gf_pass")
    fi
    _gf_body="ConfigManual=1&SSID=$(_gf_ue "$_gf_ssid")&Passphrase=$_gf_pp&Key0=&Security=$_gf_sec&Cipher=$_gf_cip&DHCPClient=1&IP=&Mask=&DefGW=&DNSSrv1=&DNSSrv2=&ProxyServer=&ProxyServerPort="
    _gf_gw=$(ip route 2>/dev/null | sed -n 's/^default via \([0-9.]*\).*/\1/p' | head -1)
    for _gf_h in 127.0.0.1 ${_gf_gw:-x} 192.168.1.1; do
        [ "$_gf_h" = "x" ] && continue
        # Only POST where the goform handler actually answers, so we never
        # spray an unrelated :80 on the LAN.
        wget -qO- -T 2 "http://$_gf_h/goform/aformHandlerConfigureProfileSettings" >/dev/null 2>&1 || continue
        _gf_resp=$(wget -qO- -T 20 \
            --header="Content-Type: application/x-www-form-urlencoded" \
            --header="Referer: http://$_gf_h/" \
            --post-data="$_gf_body" \
            "http://$_gf_h/goform/aformHandlerConfigureProfileSettings" 2>&1)
        setup_log "goform_wlan_push @ $_gf_h ssid='$_gf_ssid' rc=$? resp='$(echo "$_gf_resp" | tr -d '\r\n' | head -c 120)'"
        case "$_gf_resp" in
            *"successfully applied"*|*EndRes*) return 0 ;;
        esac
    done
    return 1
}

# Earliest breadcrumb: prove run.sh actually started and stamp the box
# fingerprint (kernel + Bose variant/firmware) up front, BEFORE the ~170 lines
# of provisioning logic that could abort. A genuinely failed ST10 boot left a
# 0-byte setup.log and we were blind to both how far the script got and what
# hardware/firmware it ran on (Markus' second ST10). Now even an immediate abort
# leaves a non-empty setup.log with the fingerprint. NAND-only, best-effort.
mkdir -p "$PERSIST" 2>/dev/null
setup_log "=== run.sh start ==="
setup_log "kernel: $(uname -a 2>/dev/null | head -c 200)"
setup_log "bose /etc/Variant: $(head -c 80 /etc/Variant 2>/dev/null | tr '\n' ' ')"
setup_log "bose /etc/version: $(head -c 120 /etc/version 2>/dev/null | tr '\n' ' ')"

# --- Display-language resolution for the OOB language gate -----------
# The Bose OOB language gate (POST /language) needs any non-zero
# sysLanguage integer to advance systemstate out of SETUP_LANG_NOT_SET,
# and that same integer becomes the box's on-screen display language.
# STR used to hardcode 2 (German) at every gate, which gave every box
# worldwide a German display, contradicting the worldwide audience. We
# now resolve the USER's language instead:
#   1. lang.conf the desktop app wrote from the user's UI locale (it
#      carries the resolved sysLanguage int directly),
#   2. the value persisted to NAND on a previous boot,
#   3. derived from the region country code (region.conf / NAND),
#   4. English (3) as the neutral worldwide default.
# Full enum 1..25 in project_bose_language_enum (0 and 14 are invalid).
# Defined at top level so it is in scope inside the provisioning
# subshell AND the OOB-finalize path (see the sibling-subshell scope
# trap that bit current_sta_lease).
lang_int_for_cc() {
    # Country-code -> sysLanguage fallback for region-only sticks. The desktop
    # app normally supplies the exact value from the user's UI language, so this
    # only runs when lang.conf is absent. This table MUST stay in lockstep with
    # sticksetup.SysLanguageForCountry (Go); sticksetup/langparity_test.go parses
    # both and fails the build on any drift. Unknown codes floor to English (3),
    # which is the Go side's 0-default after the caller's English floor.
    case "$(printf '%s' "$1" | tr 'a-z' 'A-Z')" in
        DK|GL|FO) echo 1 ;;
        DE|AT|CH|LI) echo 2 ;;
        US|GB|IE|AU|NZ|CA|ZA|IN|SG|NG|PH|MT) echo 3 ;;
        ES|MX|AR|CO|CL|PE|VE|EC|GT|CU|BO|DO|HN|PY|SV|NI|CR|PA|UY) echo 4 ;;
        FR|LU|MC|SN|CI) echo 5 ;;
        IT|SM|VA) echo 6 ;;
        NL|BE|SR) echo 7 ;;
        SE) echo 8 ;;
        JP) echo 9 ;;
        CN) echo 10 ;;
        TW|HK|MO) echo 11 ;;
        KR|KP) echo 12 ;;
        TH) echo 13 ;;
        CZ) echo 15 ;;
        FI) echo 16 ;;
        GR|CY) echo 17 ;;
        NO) echo 18 ;;
        PL) echo 19 ;;
        PT|BR|AO|MZ) echo 20 ;;
        RO|MD) echo 21 ;;
        RU|BY|KZ|KG|UA) echo 22 ;;
        SI) echo 23 ;;
        TR) echo 24 ;;
        HU) echo 25 ;;
        *) echo 3 ;;
    esac
}

resolve_setup_language() {
    _rl=""
    [ -f "$STICK/lang.conf" ] && _rl=$(sed -n 's/.*"sysLanguage"[ ]*:[ ]*\([0-9]\{1,2\}\).*/\1/p' "$STICK/lang.conf" | head -1)
    [ -z "$_rl" ] && [ -f "$PERSIST/lang.txt" ] && _rl=$(sed -n 's/[^0-9]*\([0-9]\{1,2\}\).*/\1/p' "$PERSIST/lang.txt" | head -1)
    if [ -z "$_rl" ]; then
        _cc=""
        [ -f "$STICK/region.conf" ] && _cc=$(sed -n 's/.*"country"[ ]*:[ ]*"\([^"]*\)".*/\1/p' "$STICK/region.conf" | head -1)
        [ -z "$_cc" ] && [ -f "$PERSIST/region.txt" ] && _cc=$(head -1 "$PERSIST/region.txt" 2>/dev/null)
        _rl=$(lang_int_for_cc "$_cc")
    fi
    case "$_rl" in
        1|2|3|4|5|6|7|8|9|10|11|12|13|15|16|17|18|19|20|21|22|23|24|25) : ;;
        *) _rl=3 ;;
    esac
    printf '%s' "$_rl"
}

# Mirror the NAND setup.log to the stick on a slow cadence so a yanked
# or stickless box still carries a near-current trace, without the
# per-line FAT32 IO the old dual-write setup_log incurred. The first
# tick is deferred 60s so the boot-critical window (Bose mesh + network
# bring-up) sees ZERO stick writes; thereafter one bulk copy every 25s,
# a handful of FAT32 ops vs the thousands a dual-write did. Best-effort:
# the stick may be unmounted (early boot) or read-only; failures are
# ignored and retried next tick. Copy-then-rename so a reader never sees
# a half-written file.
_stick_log_mirror_started=""
start_stick_log_mirror() {
    [ -n "$_stick_log_mirror_started" ] && return 0
    _stick_log_mirror_started=1
    (
        sleep 60
        while :; do
            if [ -d "$STICK" ] && [ -r "$SETUP_LOG_NAND" ]; then
                # sync after the rename so the FAT directory entry + cluster
                # chain actually reach the stick. Without it the write sits in
                # the OS buffer and a power-cycle (users cut power to reboot the
                # speaker) corrupts the file that is mid-mirror: Windows then
                # reports setup.log as "corrupted and unreadable" and the one
                # diagnostic we need is lost (live-seen on an ST20 install).
                if cp "$SETUP_LOG_NAND" "$SETUP_LOG.mirror" 2>/dev/null \
                    && mv "$SETUP_LOG.mirror" "$SETUP_LOG" 2>/dev/null; then
                    sync 2>/dev/null
                fi
            fi
            sleep 25
        done
    ) &
}
start_stick_log_mirror

# ensure_sshd_running force-starts sshd irrespective of the stick. As of the
# pre-1.0 hardening it is OPT-IN: the caller only invokes it when the
# /mnt/nv/streborn/enable-ssh marker is present. By default SSH instead follows
# Bose's own gate (the stick's /media/sda1/remote_services marker, started by
# /etc/init.d/shelby_local / udev mount.sh): open while the STR stick is in,
# closed on a stickless steady-state boot.
#
# Security: the speaker's root password is the well-known Bose default, so a
# permanently-open :22 leaves every shipped box reachable on the LAN. Defaulting
# this OFF (SSH only while the stick is plugged in for install / recovery /
# diagnostics) is the right end-user posture pre-1.0 ([[project-box-security-
# hardening]]). The cost is that SSH-based diagnostics / SSH-OTA fallback on a
# stickless box now need the stick plugged back in, consistent with the stick
# being STR's recovery medium.
ensure_sshd_running() {
    if pidof sshd >/dev/null 2>&1; then
        setup_log "sshd already running, leaving it alone"
        return 0
    fi
    # Bose's /etc/init.d/sshd only starts sshd when the stick's
    # remote_services marker is present, but it still `exit 0`s when it
    # skips ("Not starting sshd"). So on a no-stick steady-state boot the
    # init script is a silent no-op yet reports success. Never trust its
    # exit code: try it, then VERIFY a real sshd process, and fall through
    # to starting the daemon directly. Host keys ship in /etc/ssh, so the
    # direct start works without the stick. Without this, :22 stays closed
    # on stick-out boots and the SSH-based uninstall / diagnostics cannot
    # reach the box (live taigan 2026-06-10).
    if [ -x /etc/init.d/sshd ]; then
        /etc/init.d/sshd start >/dev/null 2>&1
        if pidof sshd >/dev/null 2>&1; then
            setup_log "sshd started via /etc/init.d/sshd"
            return 0
        fi
    fi
    if [ -x /usr/sbin/sshd ]; then
        /usr/sbin/sshd >/dev/null 2>&1
        if pidof sshd >/dev/null 2>&1; then
            setup_log "sshd started via /usr/sbin/sshd direct"
            return 0
        fi
        setup_log "sshd start: /usr/sbin/sshd ran but no sshd process appeared"
        return 1
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
    #
    # Stick-only marker: gate the write on the stick actually being mounted
    # writable. On a stick-free OTA/SSH install /media/sda1 does not exist, and
    # a bare `>> "$SETUP_LOG"` opens the redirect BEFORE 2>/dev/null takes
    # effect, so the failed open leaks "run-override.sh: line N:
    # /media/sda1/setup.log: No such file or directory" to the console every
    # boot. The NAND setup.log (setup_log) is unaffected and still records the
    # boot. When a stick IS present the guard passes and behaviour is unchanged.
    if [ -d "$STICK" ] && [ -w "$STICK" ]; then
        {
            echo ""
            echo "########################################################################"
            echo "### BOOT MARKER  $(date)  uptime=$(uptime_s)s"
            echo "###   host=$(hostname 2>/dev/null)  mac0=$(cat /sys/class/net/wlan0/address 2>/dev/null)  mac1=$(cat /sys/class/net/wlan1/address 2>/dev/null)"
            echo "###   variant=$(head -c 40 /etc/Variant 2>/dev/null | tr -d '\n')  version=$(head -c 80 /etc/version 2>/dev/null | tr -d '\n')"
            echo "########################################################################"
        } >> "$SETUP_LOG" 2>/dev/null
    fi
    setup_log "=== initial snapshot ==="
    setup_log "kernel: $(uname -a 2>/dev/null | head -c 200)"
    if [ -r /etc/version ]; then
        setup_log "bose /etc/version: $(head -c 200 /etc/version 2>/dev/null | tr '\n' ' ')"
    fi
    # STR's own version. The NAND copy survives reboot; the stick copy
    # may not be mounted this early. Without this line a setup.log /
    # diagnostic bundle never says which STR build produced it, so every
    # triage started by guessing the version from behaviour.
    setup_log "STR version: $(cat "$NAND_VER_FILE" 2>/dev/null || cat "$STICK_VER_FILE" 2>/dev/null || echo unknown)"
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
    nand_inventory
    setup_log "=== /initial snapshot ==="
}

# nand_inventory logs the contents of the writable NAND (/mnt/nv) at
# boot. Two reasons. (1) Tell a FRESH box apart from one that other
# post-cloud-shutdown community tools have already been installed on:
# their leftovers can hold our :8888 / :9080 / :8081, or eat the little
# NAND and RAM the box has, which looks exactly like an agent that gets
# a PID then dies in seconds (the reported ST10 crash-loop). (2) Capture
# NAND free space: a full filesystem is a frequent cause of a binary
# that half-writes then crashes. Read-only (df / du / ls / ps / ss), no
# writes beyond the setup.log lines, run once at boot.
nand_inventory() {
    setup_log "=== nand inventory (/mnt/nv) ==="
    # Wrap-safe parse: `df` wraps the long ubi1:persistent_volume device name onto
    # its own line on these boxes, so an `NR==2` read lands on the wrapped name and
    # logs blank total/used/free. tail -1 + $(NF-n) reads the data row regardless
    # of wrapping (matches the cleanup_nand form), so NAND headroom is finally
    # visible in the bundle on BCO/scm boxes too (#119 no-space visibility).
    setup_log "nand df: $(df -k /mnt/nv 2>/dev/null | tail -1 | awk '{print "total="$(NF-4)"KB used="$(NF-3)"KB free="$(NF-2)"KB ("$(NF-1)")"}')"
    setup_log "mem: $(grep -E 'MemTotal|MemFree|MemAvailable|SwapTotal|SwapFree' /proc/meminfo 2>/dev/null | awk '{printf "%s=%s ",$1,$2}')"
    # Top-level entries with size and mtime so a human can see at a
    # glance what is on the box and how old each piece is.
    if [ -d /mnt/nv ]; then
        for e in /mnt/nv/* /mnt/nv/.[!.]*; do
            [ -e "$e" ] || continue
            _sz=$(du -sk "$e" 2>/dev/null | awk '{print $1}')
            _mt=$(ls -ld "$e" 2>/dev/null | awk '{print $6" "$7" "$8}')
            setup_log "  nand entry: ${_sz:-?}KB  ${_mt:-?}  $e"
        done
    fi
    # Freshness verdict: the only top-level dir STR owns is 'streborn'.
    # Anything else that is not a Bose-created dir is a candidate for a
    # previously-installed third-party tool. We never delete or touch
    # it, we only make it visible in the diagnostic bundle.
    _foreign=""
    for e in /mnt/nv/*; do
        [ -d "$e" ] || continue
        _n=$(basename "$e")
        case "$_n" in
            streborn) ;;
            BoseApp-Persistence|product-persistence|nv|*Bose*|*bose*|lost+found) ;;
            *) _foreign="$_foreign $_n" ;;
        esac
    done
    if [ -n "$_foreign" ]; then
        setup_log "nand freshness: NOT fresh, non-STR/non-Bose top-level dirs present:$_foreign"
    else
        setup_log "nand freshness: STR/Bose-only (no obvious foreign tooling dirs)"
    fi
    # Common third-party audio / SoundTouch tools that owners install
    # after the cloud shutdown and that bind audio ports or respawn
    # daemons. Match the process list only OUTSIDE our own tree (STR runs
    # its own go-librespot for Spotify, which is expected, not foreign).
    _markers=$(ps 2>/dev/null | grep -viE 'streborn|/mnt/nv/streborn' \
        | grep -iE 'librespot|shairport|raop|snapcast|snapclient|owntone|forked-daapd|soundtouchctl|mopidy' \
        | head -5 | tr '\n' '|')
    [ -n "$_markers" ] && setup_log "nand inventory: possible foreign audio tooling running: $_markers"
    # Pre-start listening sockets: a foreign daemon already on one of
    # STR's ports is the direct explanation for 'wait_ports_clear: gave
    # up' and an agent that can never bind.
    _ls=""
    if command -v ss >/dev/null 2>&1; then
        _ls=$(ss -ltn 2>/dev/null | grep -E ':8888 |:9080 |:8081 |:443 ' | tr '\n' '|')
    elif command -v netstat >/dev/null 2>&1; then
        _ls=$(netstat -ltn 2>/dev/null | grep -E ':8888 |:9080 |:8081 |:443 ' | tr '\n' '|')
    fi
    [ -n "$_ls" ] && setup_log "nand inventory: STR ports already bound BEFORE start_agent (foreign holder?): $_ls"
    setup_log "=== /nand inventory ==="
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
            # binds (observed on a scm/spotty ST20 across v0.5.10..
            # v0.5.12), the phase summary line is the first place a
            # remote diagnostic bundle reveals it.
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

# SSH bring-up is OPT-IN as of the pre-1.0 hardening: STR no longer forces
# sshd open on every boot. Leaving root SSH on the well-known Bose default
# password permanently reachable on the LAN is the wrong default for the end
# users this ships to. SSH now follows the stick's remote_services marker via
# Bose's udev mount.sh: open while the STR stick is plugged in (install /
# recovery / diagnostics), closed again after the stick is pulled and the box
# reboots. A maintainer who needs persistent debug SSH on a stickless box can
# opt back in by creating /mnt/nv/streborn/enable-ssh.
if [ -e /mnt/nv/streborn/enable-ssh ]; then
    ensure_sshd_running
else
    setup_log "sshd: not forced open (no enable-ssh marker); SSH follows the stick remote_services gate"
fi

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
# cleanup_nand frees space on the small persistent_volume (~31 MB, shared
# with the Bose firmware). It removes known leftovers from earlier
# experiments/debug and caps the agent log so a long-running box does not
# slowly fill NAND. Runs on every boot BEFORE the stick -> NAND binary cache,
# so the cache always has room. All best-effort: a read-only or absent path is
# simply skipped.
cleanup_nand() {
    # 1) Junk that is never needed at runtime.
    #    sp-oauth.out: multi-MB stdout dump from the abandoned librespot-org
    #    (Rust) Spotify spike. cap*.ogg: Ogg captures from passthrough debug.
    #    bin/*.new: interrupted atomic-replace temp files from a failed OTA.
    rm -f /mnt/nv/sp-oauth.out 2>/dev/null
    rm -f /mnt/nv/streborn/cap*.ogg "$STICK"/cap*.ogg 2>/dev/null
    rm -f /mnt/nv/streborn/bin/*.new /mnt/nv/streborn/lib/*.new 2>/dev/null

    #    streborn-install: the SSH-repair install stages the ~28 MB file set into
    #    <base>/streborn-install, and install.sh copies it into /mnt/nv/streborn,
    #    but an older app left the staging copy behind. It filled a ST30 to 80% so
    #    the next OTA could not write its .new (#ST30). The running agent never
    #    uses it (it runs from streborn/bin), so it is always safe to drop on boot.
    rm -rf /mnt/nv/streborn-install /mnt/nv/streborn/streborn-install 2>/dev/null

    # 2) Log caps. The agent rotates agent.log -> agent.log.1 at 1 MiB, so the
    #    pair can hold ~2 MiB; drop the rotated backup and trim any oversized
    #    log to its tail. setup.log is now per-boot-rotated at the top of run.sh,
    #    but this size cap stays as a backstop for a single pathological boot
    #    (e.g. a tight respawn loop) that bloats the current file, and also caps
    #    the rotated setup.log.prev. Trigger lowered to 128KB / tail 64KB so a
    #    single boot cannot keep a quarter-MB on NAND.
    rm -f /mnt/nv/streborn/agent.log.1 2>/dev/null
    for f in /mnt/nv/streborn/setup.log /mnt/nv/streborn/setup.log.prev /mnt/nv/streborn/agent.log \
             /mnt/nv/streborn/previous.log /mnt/nv/streborn/boot.log /mnt/nv/streborn/run.out; do
        [ -f "$f" ] || continue
        sz=$(wc -c < "$f" 2>/dev/null || echo 0)
        if [ "$sz" -gt 131072 ]; then
            tail -c 65536 "$f" > "$f.trim" 2>/dev/null && mv "$f.trim" "$f" 2>/dev/null
        fi
    done

    # 3) Free-space guard: surface the headroom so a tightening NAND shows up in
    #    diagnostics before it bites. Removing Bose stock files is a manual last
    #    resort, never automatic.
    free_kb=$(df -k /mnt/nv 2>/dev/null | tail -1 | awk '{print $(NF-2)}')
    log "NAND cleanup done, ${free_kb:-?} KB free on /mnt/nv"
}

# NAND_BIN_CORRUPT is set to 1 when we wrote the agent binary to NAND but
# could not flash-verify it (a full NAND lets the write land in the page
# cache while the flash pages stay erased = 0xff, which exec later reads
# back as an illegal instruction -> SIGILL crash-loop, live-seen on a tight
# ST20, #302). The BIN selection then runs the agent from a RAM copy of the
# stick binary instead. Unset/0 on a healthy or stickless boot.
NAND_BIN_CORRUPT=0
sync_stick_to_nand_always() {
    # The AGENT BINARY is essential and goes FIRST, before the optional
    # Spotify engine, so a tight NAND can never let go-librespot crowd the
    # agent out (the old order wrote the 16 MB engine first and left no room
    # for the 12 MB agent to commit -> corrupt agent, #302).
    if [ -r "$STICK_BIN" ]; then
        if cp "$STICK_BIN" "$CACHED_BIN.new" 2>/dev/null && chmod +x "$CACHED_BIN.new" && mv "$CACHED_BIN.new" "$CACHED_BIN" 2>/dev/null; then
            log "stick binary deployed to NAND cache ($(wc -c < "$CACHED_BIN") bytes)"
            # Verify against FLASH, not the page cache. Without the sync +
            # drop_caches the md5 reads back the bytes we just wrote from RAM
            # cache and passes even when the flash writeback silently failed on
            # a full NAND (ENOSPC), so a corrupt agent looked fine here and
            # only surfaced as a SIGILL at run time. Force the pages to flash,
            # drop the cache, then re-read from flash and compare to the stick.
            sync 2>/dev/null
            [ -w /proc/sys/vm/drop_caches ] && echo 3 > /proc/sys/vm/drop_caches 2>/dev/null
            _sm5=$(md5sum "$STICK_BIN" 2>/dev/null | awk '{print $1}')
            _nm5=$(md5sum "$CACHED_BIN" 2>/dev/null | awk '{print $1}')
            if [ -n "$_sm5" ] && [ "$_sm5" = "$_nm5" ]; then
                setup_log "binary deploy: md5 OK (flash-verified) $_nm5 ($(wc -c < "$CACHED_BIN") bytes)"
                if [ -r "$STICK_VER_FILE" ]; then
                    cp "$STICK_VER_FILE" "$NAND_VER_FILE" 2>/dev/null
                    log "NAND version.txt updated: $(cat "$NAND_VER_FILE" 2>/dev/null)"
                fi
            else
                NAND_BIN_CORRUPT=1
                setup_log "binary deploy: md5 MISMATCH after flash sync stick=$_sm5 nand=$_nm5 — the NAND write did not commit (full NAND?); the agent will be run from a RAM copy instead (see RAM-exec)"
            fi
        else
            log "stick -> NAND binary cp/chmod/mv failed, keeping previous NAND binary"
            rm -f "$CACHED_BIN.new"
        fi
    fi

    # go-librespot (Spotify sidecar) is OPTIONAL. Deploy it only when there is
    # real headroom AFTER the essential agent, so it can never crowd the agent
    # out of a small NAND. When it does not fit, defer it (Spotify simply stays
    # unavailable until the next OTA finds room) rather than filling the volume.
    if [ -r "$STICK_GLR" ]; then
        _glr_kb=$(( $(wc -c < "$STICK_GLR" 2>/dev/null || echo 0) / 1024 ))
        _sgm5=$(md5sum "$STICK_GLR" 2>/dev/null | awk '{print $1}')
        _ngm5=""
        [ -s "$CACHED_GLR" ] && _ngm5=$(md5sum "$CACHED_GLR" 2>/dev/null | awk '{print $1}')
        if [ "${_glr_kb:-0}" -le 0 ] || [ -z "$_sgm5" ]; then
            # Empty or unhashable stick engine: never trade a working NAND
            # engine for a source we cannot even read back.
            [ "${_glr_kb:-0}" -gt 0 ] && log "stick go-librespot unreadable (md5 failed), keeping the current NAND engine"
        elif [ -n "$_ngm5" ] && [ "$_sgm5" = "$_ngm5" ]; then
            # Identical engine already cached: no copy, no removal, no flash
            # wear on the steady-state boot.
            :
        else
            if [ -n "$_ngm5" ]; then
                # The stick carries a DIFFERENT engine. Old + new (~16 MB
                # each) never fit side by side on a tight NAND, so remove the
                # old engine (and its .sha256 stamp, see webui.go's
                # goLibrespotStamp) BEFORE staging the copy and re-read df so
                # the freed space counts toward the gate. Failure window: a
                # power cut between this remove and the verified copy below
                # leaves the box engine-less - acceptable, the agent's
                # EnsureSpotifyEngine re-delivers over the air and the next
                # stick boot retries this sync.
                log "stick carries a different go-librespot (stick=$_sgm5 nand=$_ngm5), removing the old NAND engine before staging the new one"
                rm -f "$CACHED_GLR" "$CACHED_GLR.sha256" "$CACHED_GLR.new" 2>/dev/null
                sync 2>/dev/null
            fi
            _free_kb=$(df -k /mnt/nv 2>/dev/null | tail -1 | awk '{print $(NF-2)}')
            # UBIFS compresses transparently (LZO, measured 1.5-1.6x on Go
            # binaries) and its df free figure is deliberately pessimistic
            # (it assumes future writes are incompressible). Gating the raw
            # binary size against that figure refuses engines that actually
            # fit, so gate on a conservative 1.33x compression estimate of
            # the on-flash size instead.
            _glr_flash_kb=$(( _glr_kb * 3 / 4 ))
            if [ "${_free_kb:-0}" -gt $(( _glr_flash_kb + 3072 )) ]; then
                if cp "$STICK_GLR" "$CACHED_GLR.new" 2>/dev/null && chmod +x "$CACHED_GLR.new" && mv "$CACHED_GLR.new" "$CACHED_GLR" 2>/dev/null; then
                    # Same flash-verify as the agent binary above: on a full
                    # NAND the write can land in the page cache while the
                    # flash pages stay erased (#302). A corrupt OPTIONAL
                    # engine is worse than a missing one (the agent's
                    # supervise loop would crash-loop it), so drop it on
                    # mismatch and let EnsureSpotifyEngine re-deliver.
                    sync 2>/dev/null
                    [ -w /proc/sys/vm/drop_caches ] && echo 3 > /proc/sys/vm/drop_caches 2>/dev/null
                    _ngm5=$(md5sum "$CACHED_GLR" 2>/dev/null | awk '{print $1}')
                    if [ "$_ngm5" = "$_sgm5" ]; then
                        log "stick go-librespot deployed to NAND cache ($(wc -c < "$CACHED_GLR") bytes, flash-verified)"
                    else
                        rm -f "$CACHED_GLR" "$CACHED_GLR.sha256" 2>/dev/null
                        log "stick -> NAND go-librespot flash-verify FAILED (stick=$_sgm5 nand=$_ngm5), removed the unverified engine; the agent re-delivers it over the air"
                    fi
                else
                    log "stick -> NAND go-librespot deploy failed; engine absent until the agent re-delivers it over the air"
                    rm -f "$CACHED_GLR.new"
                fi
            else
                log "NAND tight (${_free_kb:-?}KB free, go-librespot needs ~${_glr_flash_kb}KB on flash + margin), deferring the Spotify engine so it cannot crowd out the agent binary"
            fi
        fi
    fi
    return 0
}

# stage_ram_binary copies the STICK agent binary into tmpfs (RAM) and points
# RAM_BIN at it. Used when the NAND write could not be flash-verified: tmpfs
# has no flash writeback, so the bytes exec mmaps are exactly what we copied,
# which sidesteps a full/unwritable NAND entirely (#302). Needs the stick as a
# clean source — a stickless boot with an already-corrupt NAND cannot use this.
RAM_BIN=""
stage_ram_binary() {
    [ -r "$STICK_BIN" ] || { setup_log "RAM-exec: no stick binary to stage (stickless boot), cannot bypass an unwritable NAND"; return 1; }
    _rd=/dev/shm
    { [ -d "$_rd" ] && [ -w "$_rd" ]; } || _rd=/tmp
    RAM_BIN="$_rd/streborn-armv7l"
    if cp "$STICK_BIN" "$RAM_BIN.new" 2>/dev/null && chmod +x "$RAM_BIN.new" && mv "$RAM_BIN.new" "$RAM_BIN" 2>/dev/null; then
        _rm5=$(md5sum "$RAM_BIN" 2>/dev/null | awk '{print $1}')
        _sm5=$(md5sum "$STICK_BIN" 2>/dev/null | awk '{print $1}')
        if [ -n "$_rm5" ] && [ "$_rm5" = "$_sm5" ]; then
            setup_log "RAM-exec: staged agent to $RAM_BIN (tmpfs) md5=$_rm5, running from RAM to bypass the unwritable NAND"
            return 0
        fi
    fi
    setup_log "RAM-exec: staging the agent to tmpfs FAILED, falling back to the NAND/stick binary"
    rm -f "$RAM_BIN.new" 2>/dev/null
    RAM_BIN=""
    return 1
}

# Wait for the USB stick to finish mounting before ANY stick read this
# boot: the binary + version.txt sync below, the rc.local/run-override
# redeploy, the WLAN credentials (M0/M1), and region/name. The stick
# filesystem mounts late on a cold boot; reading /media/sda1 too early
# returns empty and we silently fall through to "no creds ->
# ethernet-only" (box never provisions, LED stuck yellow) or skip the
# binary/version sync (stale NAND version, #94). The old guard only
# waited on a first install ([ ! -x CACHED_BIN ]); every steady-state
# cold boot skipped it and raced the mount.
#
# Gate the wait on a USB BLOCK DEVICE being present. By the time run.sh
# runs (~boot+30s, after the Bose mesh is up) USB has long since
# enumerated, so an absent /sys/block/sda means the box is genuinely
# stickless: we add ZERO delay to the normal NAND-only steady-state
# boot. Only when a stick IS plugged (block device present, filesystem
# possibly still mounting) do we wait, up to 25s, for /media/sda1.
if [ -e /sys/block/sda ] || [ -e /dev/sda1 ]; then
    _stick_wait=0
    while [ $_stick_wait -lt 25 ]; do
        if [ -e "$STICK_BIN" ] || [ -e "$STICK_VER_FILE" ] || [ -e "$STICK/run.sh" ] || [ -e "$STICK/wlan.conf" ]; then
            [ $_stick_wait -gt 0 ] && setup_log "stick: filesystem mounted after ${_stick_wait}s wait"
            break
        fi
        sleep 1
        _stick_wait=$((_stick_wait + 1))
    done
    if [ "$_stick_wait" -ge 25 ]; then
        # The kernel auto-mount fails when a previous box left the stick's FAT
        # "dirty" (it was unplugged or powered off before its filesystem was
        # clean). The next box then never mounts it and reports it as absent
        # ("Stick nicht eingesteckt"), while a fresh stick works on the same box,
        # so it looks like the stick stops being read after the first box (seen
        # reusing one stick across several ST20s, #119). Repair the FAT (fsck
        # clears the dirty flag) and
        # mount it by hand before giving up. We always mount at /media/sda1 so
        # STICK and the *_BIN paths derived above stay valid.
        setup_log "stick: USB present but not auto-mounted after ${_stick_wait}s, trying fsck+manual mount (dirty FAT?)"
        _fsck=""
        command -v fsck.vfat >/dev/null 2>&1 && _fsck=fsck.vfat
        if [ -z "$_fsck" ] && command -v dosfsck >/dev/null 2>&1; then _fsck=dosfsck; fi
        mkdir -p /media/sda1 2>/dev/null
        # Enumerate the USB block devices the kernel actually exposes rather than
        # guessing a fixed name: large sticks put the FAT on a high partition
        # (sda5 seen on ST20/ST30, not always sda1), so a fixed list misses it.
        # The glob lists every partition (name ending in a digit) first, then the
        # whole disks as a last resort for an unpartitioned stick. An unmatched
        # glob stays literal and is filtered out by the [ -b ] test, so this is
        # safe when no stick (or no such device) is present.
        for _dev in /dev/sd*[0-9] /dev/mmcblk*p[0-9] /dev/sda /dev/sdb /dev/sdc /dev/sdd; do
            [ -b "$_dev" ] || continue
            [ -n "$_fsck" ] && "$_fsck" -a "$_dev" >/dev/null 2>&1
            if mount -t vfat -o rw "$_dev" /media/sda1 2>/dev/null; then
                if [ -e /media/sda1/run.sh ] || [ -e /media/sda1/streborn-armv7l ] || [ -e /media/sda1/install.sh ]; then
                    STICK="/media/sda1"
                    setup_log "stick: recovered $_dev via fsck+manual mount (auto-mount had failed, likely a dirty FAT from a previous box)"
                    break
                fi
                umount /media/sda1 2>/dev/null
            fi
        done
        if [ ! -e "$STICK/run.sh" ] && [ ! -e "$STICK/streborn-armv7l" ]; then
            setup_log "stick: still no readable filesystem after fsck recovery, continuing stickless"
        fi
    fi
else
    setup_log "stick: no USB block device, stickless NAND-only boot (no wait)"
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

# === Early low-power Wi-Fi one-shot (BEFORE the heavy stick->NAND copy) ===
# The sync_stick_to_nand_always below reads ~28 MB (agent + go-librespot) off
# the USB stick — the single most power-hungry operation of the boot. On a
# port that cannot sustain it, the read browns out and the install never
# finishes. So fire a MINIMAL Wi-Fi provisioning first: a couple of tiny
# goform POSTs (negligible power) that program the speaker's Wi-Fi
# coprocessor, so that even if the port then collapses mid-copy, the box is
# already on Wi-Fi and can be loaded with STR over the network (network
# install / OTA). The full provisioning below re-applies and verifies it once
# the box is fully up, so this is a best-effort head start, not the only pass.
#
# GUARD: only runs when an SSID+PASS was actually provided (stick wlan.conf,
# or the NAND wlan-creds an app change / OTA persisted). A box with no creds
# is never touched, so a working Wi-Fi profile is never clobbered.
_es_ssid=""; _es_pass=""
# v0.9.7 hands-off boot: the early one-shot fires ONLY for a fresh stick
# wlan.conf (an explicit user setup). It used to also fire from the NAND
# wlan-creds replay on every normal boot, which meant STR re-programmed the
# Wi-Fi coprocessor of boxes that were already perfectly online - the prime
# suspect for unexplained Wi-Fi losses and orange network icons (#270).
if [ -f "$STICK/wlan.conf" ]; then
    _es_ssid=$(sed -n 's/.*"ssid":"\([^"]*\)".*/\1/p' "$STICK/wlan.conf" | head -1)
    _es_pass=$(sed -n 's/.*"password":"\([^"]*\)".*/\1/p' "$STICK/wlan.conf" | head -1)
fi
if [ -n "$_es_ssid" ] && [ -n "$_es_pass" ]; then
    setup_log "early WLAN one-shot: provisioning '$_es_ssid' via goform BEFORE the heavy stick copy (power-weak safety net)"
    goform_wlan_push "$_es_ssid" "$_es_pass" \
        && setup_log "early WLAN one-shot: coprocessor accepted the profile" \
        || setup_log "early WLAN one-shot: no goform apply yet (coprocessor :80 maybe not up); full provisioning will retry"
else
    setup_log "early WLAN one-shot: no SSID/PASS provided, skipping (no profile change)"
fi

cleanup_nand
sync_stick_to_nand_always

# === Shim deploy + LATE swap (after Bose mesh stabilizes) ===
#
# Live-verified 2026-05-28 22:32: bind-mounting the wrapper BEFORE
# Bose's init starts SoftwareUpdate means the wrapped SU is the
# first instance the IPC mesh registers — and that breaks the mesh
# (BoseApp /info returns HTTP 500). The wrapper's intermediate
# /bin/sh + env layers between shepherdd's fork() and the final
# SU-real exec disrupt some handshake that the mesh expects.
#
# Strategy that works (manual test at 22:23 verified):
#   1. Let Bose's init start the ORIGINAL SoftwareUpdate.
#   2. Wait for the mesh to fully stabilize — Bose /info on :8090
#      returning valid <info ...> for at least 30 s steady.
#   3. NOW set up the bind-mount.
#   4. SIGTERM the original SU.
#   5. nohup-relaunch via /opt/Bose/SoftwareUpdate (now bind-mounted).
#      The wrapper exec's SoftwareUpdate-real with LD_PRELOAD. The
#      new SU has the shim active and the mesh — already stable —
#      survives the SU re-registration cleanly.
#
# shepherdd does NOT auto-restart SoftwareUpdate (it is not in
# Shepherd-taigan.xml), so the kill is race-free.

# Early VARIANT / HOSTID detection. The full block in the WLAN-iface
# detection section sets these too but only runs at line ~860, AFTER
# shim_stage_wrapper. A 2026-05-30 scm/spotty bundle proved the gap:
# shim stage logged "variant=? host=?" and the Shepherd-XML probe
# looked for Shepherd-unknown.xml — useless. Hoisting just this read
# pair so the shim stage has real values, with empty fallbacks so a
# firmware that hides /proc/variant does not break the strict-mode
# expansion downstream.
VARIANT=$(cat /proc/variant 2>/dev/null | tr -d '\n\r ' | head -c 32)
HOSTID=$(uname -n 2>/dev/null | tr -d '\n\r ' | head -c 32)

sync_shim_to_nand() {
    if [ -r "$STICK_SHIM" ]; then
        STICK_SHIM_SIZE=$(wc -c < "$STICK_SHIM" 2>/dev/null || echo "?")
        if cp "$STICK_SHIM" "$NAND_SHIM.new" 2>/dev/null && \
           mv "$NAND_SHIM.new" "$NAND_SHIM" 2>/dev/null; then
            chmod 644 "$NAND_SHIM" 2>/dev/null
            setup_log "shim deploy: synced stick -> NAND (stick=${STICK_SHIM_SIZE}B nand=$(wc -c < "$NAND_SHIM" 2>/dev/null)B at $NAND_SHIM)"
        else
            setup_log "shim deploy: cp/mv stick -> NAND FAILED, keeping previous NAND copy (nand_existed=$([ -r "$NAND_SHIM" ] && echo yes || echo no))"
            rm -f "$NAND_SHIM.new" 2>/dev/null
        fi
    elif [ ! -r "$NAND_SHIM" ]; then
        setup_log "shim deploy: stick has no $STICK_SHIM and NAND has no $NAND_SHIM — hijack permanently disabled this boot"
    else
        setup_log "shim deploy: stick has no $STICK_SHIM, reusing NAND copy ($(wc -c < "$NAND_SHIM" 2>/dev/null)B at $NAND_SHIM)"
    fi
}
sync_shim_to_nand

SHIM_DISABLE="$PERSIST/state/shim-disable"
SHIM_BACKUP="$PERSIST/lib/SoftwareUpdate-real"
SHIM_WRAPPER="$PERSIST/lib/SU-wrapper.sh"

# Stage wrapper + binary snapshot unconditionally. The bind-mount
# itself is deferred to the late-swap background runner below.
#
# Observability rationale: every step writes to setup.log (not the
# volatile /tmp log) so a remote diagnostic bundle can answer "did
# the shim stage succeed, and if not, where did it bail?" without
# SSH access. The pre-stage analysis block also captures the box's
# Bose-side state at this moment — Shepherd config, mount options,
# SU binary attributes — so a scm/spotty bundle can tell us in one
# look what is different vs the working taigan path.
shim_stage_wrapper() {
    setup_log "shim stage: enter (variant=${VARIANT:-?} host=${HOSTID:-?} is_series_one=${IS_SERIES_ONE:-0})"
    if [ "${STR_FORCE_SHIM_TAIGAN:-0}" != "1" ]; then
        case "${VARIANT}|${HOSTID}" in
            *taigan*|*spotty*)
                setup_log "shim stage: SKIP — BCO chassis (${VARIANT:-?}/${HOSTID:-?}). The LD_PRELOAD late-swap kills SoftwareUpdate, which on BCO boxes never actually forwards :8888 (spotty :17008 self-probe NOT STR) and races shepherdd's respawn, wedging scm_finalize / the Bose mesh init (live 2026-05-31: taigan boot bar stuck with NO setup-AP; spotty BoseApp never answers within 180s, M0 times out). External :8888 on BCO is served by the iptables PREROUTING REDIRECT path instead, which never touches SoftwareUpdate. STR_FORCE_SHIM_TAIGAN=1 overrides."
                return 0 ;;
            *rhino*|*mojo*)
                setup_log "shim stage: SKIP — sm2 chassis (${VARIANT:-?}/${HOSTID:-?}). sm2 boxes (ST10 rhino, ST30 mojo) are not chipset-whitelisted; STR's :8888 is opened directly by the iptables INPUT ACCEPT path (iptables_install_streborn_fw), so the LD_PRELOAD shim is unnecessary here. Running it only kills/relaunches Bose SoftwareUpdate (racing shepherdd) for no gain, and on mojo the .so cannot even load (live ST30 2026-06-10, #123: box healthy, agent up, shim self-probe NOT STR). STR_FORCE_SHIM_TAIGAN=1 overrides."
                return 0 ;;
        esac
    fi
    if [ -e "$SHIM_DISABLE" ]; then
        setup_log "shim stage: BAIL — disabled via $SHIM_DISABLE marker"
        return 0
    fi
    if [ ! -r "$NAND_SHIM" ]; then
        setup_log "shim stage: BAIL — no shim .so at $NAND_SHIM (sync_shim_to_nand must have failed earlier)"
        return 0
    fi
    if [ ! -e /opt/Bose/SoftwareUpdate ]; then
        setup_log "shim stage: BAIL — /opt/Bose/SoftwareUpdate absent (firmware variant we have not seen?)"
        return 0
    fi

    # Pre-stage analysis — captures the state we are about to swap.
    # Mount options for /opt/Bose tell us whether bind-mount will be
    # rejected (ro? noexec?). File-type of SU confirms it is an ELF,
    # not already a script wrapper. Shepherd XML for this variant
    # tells us whether shepherdd will respawn SU on our kill — the
    # key difference between taigan (SU not in Shepherd) and
    # potentially scm/rhino (SU in Shepherd → race condition).
    SHIM_VARIANT_FOR_XML="${VARIANT:-${HOSTID:-unknown}}"
    SHEPHERD_XML="/opt/Bose/etc/Shepherd-${SHIM_VARIANT_FOR_XML}.xml"
    SHIM_OPT_BOSE_MOUNT=$(mount 2>/dev/null | awk '$3=="/opt/Bose" || $3=="/" {print $3":"$5":"$6; exit}' | head -c 200)
    SHIM_SU_TYPE=$(head -c 4 /opt/Bose/SoftwareUpdate 2>/dev/null | od -An -c | tr -s ' ' | head -c 32)
    SHIM_SU_SIZE=$(wc -c < /opt/Bose/SoftwareUpdate 2>/dev/null || echo "?")
    SHIM_SU_INODE=$(stat -c %i /opt/Bose/SoftwareUpdate 2>/dev/null || echo "?")
    if [ -r "$SHEPHERD_XML" ]; then
        if grep -q "SoftwareUpdate" "$SHEPHERD_XML" 2>/dev/null; then
            SHEPHERD_HAS_SU="YES — shepherdd WILL respawn SU after our kill (race condition)"
        else
            SHEPHERD_HAS_SU="no — kill is race-free (taigan-style)"
        fi
    else
        SHEPHERD_HAS_SU="Shepherd XML missing at $SHEPHERD_XML (could be different filename on this variant)"
    fi
    setup_log "shim stage: pre-state SU=/opt/Bose/SoftwareUpdate size=${SHIM_SU_SIZE}B inode=$SHIM_SU_INODE magic='$SHIM_SU_TYPE' mount=${SHIM_OPT_BOSE_MOUNT:-?}"
    setup_log "shim stage: shepherd check $SHEPHERD_XML -> $SHEPHERD_HAS_SU"

    # Snapshot the real binary if we have not already cached it. We
    # do this BEFORE any bind-mount so the cp reads the original
    # rootfs file, not our wrapper.
    if [ ! -x "$SHIM_BACKUP" ]; then
        if cp /opt/Bose/SoftwareUpdate "$SHIM_BACKUP.new" 2>/dev/null \
           && mv "$SHIM_BACKUP.new" "$SHIM_BACKUP" 2>/dev/null; then
            chmod +x "$SHIM_BACKUP"
            BACKUP_INODE=$(stat -c %i "$SHIM_BACKUP" 2>/dev/null || echo "?")
            setup_log "shim stage: cached real SU at $SHIM_BACKUP ($(wc -c < "$SHIM_BACKUP")B inode=$BACKUP_INODE) — chipset-whitelist proof"
        else
            setup_log "shim stage: FAIL — cp /opt/Bose/SoftwareUpdate -> $SHIM_BACKUP failed (rc=$? errno=$(echo $?))"
            rm -f "$SHIM_BACKUP.new" 2>/dev/null
            return 1
        fi
    else
        setup_log "shim stage: $SHIM_BACKUP already cached ($(wc -c < "$SHIM_BACKUP" 2>/dev/null)B)"
    fi
    # Write wrapper. `export + exec` keeps the exec chain shorter
    # than `exec env LD_PRELOAD=... binary`: only one /bin/sh
    # intermediate before the real binary takes over.
    cat > "$SHIM_WRAPPER.new" <<'WRAP_EOF'
#!/bin/sh
# Auto-generated by STR's run.sh — do not edit directly.
export LD_PRELOAD=/mnt/nv/streborn/lib/str-shim.so
exec /mnt/nv/streborn/lib/SoftwareUpdate-real "$@"
WRAP_EOF
    if mv "$SHIM_WRAPPER.new" "$SHIM_WRAPPER" 2>/dev/null && chmod +x "$SHIM_WRAPPER"; then
        setup_log "shim stage: wrapper written at $SHIM_WRAPPER ($(wc -c < "$SHIM_WRAPPER" 2>/dev/null)B)"
    else
        setup_log "shim stage: FAIL — could not finalise wrapper at $SHIM_WRAPPER"
        return 1
    fi
    setup_log "shim stage: ready — backup+wrapper both in place, late-swap will run after Bose mesh stabilises"
}
shim_stage_wrapper

# Late-swap background runner: wait for Bose's IPC mesh to fully
# stabilize, THEN set up the bind-mount and swap SoftwareUpdate
# under LD_PRELOAD. Doing this early — before /info has been
# answering with valid XML for a sustained period — broke the mesh
# (BoseApp returned HTTP 500 indefinitely until a power-cycle).
#
# Observability rationale: a 2026-05-30 scm/spotty diagnostic bundle
# had ZERO shim log lines anywhere — because the original `log()`
# wrote to /tmp/streborn-agent.log (volatile, sometimes lost,
# definitely interleaved with the Go agent's slog). All shim phase
# markers now go via setup_log so a remote bundle says exactly
# where the swap fell over on a non-taigan box. Each phase boundary
# includes the state we observed AT THAT MOMENT — Bose mesh health,
# SU PID, mount state, listening sockets on :17008 — so we never
# have to guess about ordering.
shim_late_swap() {
    setup_log "shim late-swap: enter (will wait up to 240s for /info to be stable for 30s)"
    if [ "${STR_FORCE_SHIM_TAIGAN:-0}" != "1" ]; then
        case "${VARIANT}|${HOSTID}" in
            *taigan*|*spotty*)
                setup_log "shim late-swap: SKIP — BCO chassis (${VARIANT:-?}/${HOSTID:-?}); SoftwareUpdate left untouched so the Bose mesh init cannot wedge. External :8888 via the REDIRECT path. STR_FORCE_SHIM_TAIGAN=1 overrides."
                return 0 ;;
            *rhino*|*mojo*)
                setup_log "shim late-swap: SKIP — sm2 chassis (${VARIANT:-?}/${HOSTID:-?}); :8888 is opened by the iptables INPUT ACCEPT path, so SoftwareUpdate is left untouched (no shepherdd race, no needless kill/relaunch). STR_FORCE_SHIM_TAIGAN=1 overrides."
                return 0 ;;
        esac
    fi
    if [ -e "$SHIM_DISABLE" ]; then
        setup_log "shim late-swap: BAIL — $SHIM_DISABLE marker present"
        return 0
    fi
    if [ ! -x "$SHIM_BACKUP" ] || [ ! -x "$SHIM_WRAPPER" ]; then
        setup_log "shim late-swap: BAIL — staging did not produce backup ($([ -x "$SHIM_BACKUP" ] && echo present || echo missing)) and/or wrapper ($([ -x "$SHIM_WRAPPER" ] && echo present || echo missing))"
        return 0
    fi
    # Health-wait: /info must return a body containing `<info ` for
    # 30 s continuously before we touch anything. Progress is logged
    # every 60 s so a bundle pulled mid-wait shows how far we got.
    healthy_for=0
    waited=0
    last_logged=0
    while [ $waited -lt 240 ]; do
        if wget -qO- -T 3 http://127.0.0.1:8090/info 2>/dev/null | grep -q "<info "; then
            healthy_for=$((healthy_for + 5))
            if [ $healthy_for -ge 30 ]; then break; fi
        else
            if [ $healthy_for -gt 0 ]; then
                setup_log "shim late-swap: /info health DROPPED at waited=${waited}s (was healthy_for=${healthy_for}s) — restarting streak"
            fi
            healthy_for=0
        fi
        if [ $((waited - last_logged)) -ge 60 ]; then
            setup_log "shim late-swap: health-wait progress waited=${waited}s healthy_for=${healthy_for}s (target 30s)"
            last_logged=$waited
        fi
        sleep 5
        waited=$((waited + 5))
    done
    if [ $healthy_for -lt 30 ]; then
        setup_log "shim late-swap: ABORT — Bose /info never reached 30s steady health in 240s (final healthy_for=${healthy_for}s)"
        return 0
    fi
    setup_log "shim late-swap: Bose mesh stable for ${healthy_for}s at uptime=$(uptime_s)s — beginning swap"

    # Pre-swap snapshot: who owns :17008, what is mounted on the SU
    # path, exact PID of the SU process we are about to evict. If
    # the swap goes wrong on scm/spotty, this snapshot is the
    # baseline we compare the post-state against.
    PRESW_SU_PID=$(pidof SoftwareUpdate 2>/dev/null | awk '{print $1}')
    PRESW_SU_REAL_PID=$(pidof SoftwareUpdate-real 2>/dev/null | awk '{print $1}')
    PRESW_17008=$(netstat -ltnp 2>/dev/null | awk '$4 ~ /:17008$/ {print $7; exit}')
    PRESW_MOUNT_BIND=$(mount 2>/dev/null | awk '$3=="/opt/Bose/SoftwareUpdate" {print; exit}' | head -c 200)
    setup_log "shim late-swap: pre-state pid_SU=${PRESW_SU_PID:-none} pid_SU-real=${PRESW_SU_REAL_PID:-none} owner_17008=${PRESW_17008:-none} existing_bindmount='${PRESW_MOUNT_BIND:-none}'"

    # Bind-mount now (mesh has settled — wrapping a NEW SU instance
    # at this point does not disrupt the registered original).
    if [ -z "$PRESW_MOUNT_BIND" ]; then
        BIND_OUT=$(mount --bind "$SHIM_WRAPPER" /opt/Bose/SoftwareUpdate 2>&1)
        BIND_RC=$?
        if [ "$BIND_RC" = "0" ]; then
            NEW_MOUNT=$(mount 2>/dev/null | awk '$3=="/opt/Bose/SoftwareUpdate" {print; exit}' | head -c 240)
            setup_log "shim late-swap: bind-mount OK — '$NEW_MOUNT'"
        else
            setup_log "shim late-swap: FAIL — mount --bind rc=$BIND_RC output='$(echo "$BIND_OUT" | tr '\n' ' ' | head -c 240)' — most likely /opt/Bose mounted ro or noexec on this variant"
            return 1
        fi
    else
        setup_log "shim late-swap: bind-mount already present from earlier boot — skipping re-mount"
    fi

    # SIGTERM the running SoftwareUpdate. On taigan SU is NOT in
    # Shepherd-taigan.xml (verified live) so the kill is race-free.
    # On rhino/scm shepherd config might list it — the post-kill
    # watchdog below logs whether shepherdd respawned with our
    # bind-mounted path or with some Shepherd-cached invocation
    # that bypasses our wrapper. That signal alone tells us whether
    # to switch to a different approach on the affected variant.
    OLDSU=$(pidof SoftwareUpdate 2>/dev/null | awk '{print $1}')
    if [ -n "$OLDSU" ]; then
        setup_log "shim late-swap: SIGTERM SoftwareUpdate PID=$OLDSU"
        kill -TERM "$OLDSU" 2>/dev/null
        KILL_WAITED=0
        for i in 1 2 3 4 5; do
            sleep 1
            KILL_WAITED=$((KILL_WAITED + 1))
            if ! kill -0 "$OLDSU" 2>/dev/null; then break; fi
        done
        if kill -0 "$OLDSU" 2>/dev/null; then
            setup_log "shim late-swap: WARN — old SU PID=$OLDSU still alive after ${KILL_WAITED}s of SIGTERM, sending SIGKILL"
            kill -KILL "$OLDSU" 2>/dev/null
            sleep 1
        else
            setup_log "shim late-swap: old SU PID=$OLDSU gone after ${KILL_WAITED}s"
        fi
    else
        setup_log "shim late-swap: WARN — no SU process found at swap time (already exited?)"
    fi

    # Wait briefly for any shepherd-driven respawn to fire OR for the
    # port to free. Two distinct possibilities to log:
    #  - Fast respawn (<2 s): shepherdd picked it up. PID will differ
    #    from OLDSU, parent will be shepherdd, our wrapper may or may
    #    not have been used (depends on whether shepherd cached the
    #    exec path or re-evaluates it).
    #  - Slow respawn (no respawn): we do the explicit launch below.
    SHEPHERD_RESPAWN_PID=$(pidof SoftwareUpdate 2>/dev/null | awk '{print $1}')
    if [ -n "$SHEPHERD_RESPAWN_PID" ] && [ "$SHEPHERD_RESPAWN_PID" != "$OLDSU" ]; then
        SR_PPID=$(awk '/^PPid:/ {print $2}' /proc/$SHEPHERD_RESPAWN_PID/status 2>/dev/null)
        SR_LDPRELOAD=$(grep -aoE 'LD_PRELOAD=[^[:cntrl:]]*str-shim.so' /proc/$SHEPHERD_RESPAWN_PID/environ 2>/dev/null | head -c 160)
        SR_EXE=$(readlink /proc/$SHEPHERD_RESPAWN_PID/exe 2>/dev/null | head -c 160)
        if [ -n "$SR_LDPRELOAD" ]; then
            setup_log "shim late-swap: shepherdd-respawn DETECTED with shim active (PID=$SHEPHERD_RESPAWN_PID ppid=$SR_PPID exe=$SR_EXE preload='$SR_LDPRELOAD') — explicit launch skipped"
        else
            setup_log "shim late-swap: shepherdd-respawn DETECTED but WITHOUT shim (PID=$SHEPHERD_RESPAWN_PID ppid=$SR_PPID exe=$SR_EXE) — our bind-mount was bypassed, will retry explicit launch"
            kill -TERM "$SHEPHERD_RESPAWN_PID" 2>/dev/null
            sleep 2
        fi
    fi

    # Isolation probe BEFORE the relaunch: can the shim be loaded at
    # all on this firmware? A 2026-05-30 scm/spotty bundle showed the
    # wrapper-launch produce no SU process whatsoever (new_pid=?),
    # which is the signature of an LD_PRELOAD load failure (the loader
    # bails before main(), the shell wrapper exec'd is gone). Running
    # LD_PRELOAD=$NAND_SHIM against /bin/true lets us catch the
    # loader's stderr in isolation. Output is the smoking gun: if the
    # shim cannot load on this variant, we will see the loader error
    # right here — glibc version mismatch, missing symbol, wrong
    # architecture, all surface here.
    SHIM_ISOLATION_OUT=$(LD_PRELOAD="$NAND_SHIM" /bin/true 2>&1)
    SHIM_ISOLATION_RC=$?
    if [ "$SHIM_ISOLATION_RC" = "0" ] && [ -z "$SHIM_ISOLATION_OUT" ]; then
        setup_log "shim late-swap: shim isolation probe OK — LD_PRELOAD=$NAND_SHIM /bin/true returned cleanly"
    else
        setup_log "shim late-swap: shim isolation probe FAIL rc=$SHIM_ISOLATION_RC output='$(echo "$SHIM_ISOLATION_OUT" | tr '\n' ' ' | head -c 400)' — the .so cannot be loaded on this firmware, wrapper launch will fail the same way"
    fi

    # Relaunch via the bind-mounted path so the wrapper sets
    # LD_PRELOAD and exec's SoftwareUpdate-real. Capture stderr into
    # a tmpfile so the next bundle shows WHY the wrapper died if it
    # does (previous "/dev/null 2>&1" swallowed every loader message
    # and left "new_pid=?" as the only signal).
    NOHUP_ERR="/tmp/streborn-shim-nohup.err"
    rm -f "$NOHUP_ERR" 2>/dev/null
    nohup /opt/Bose/SoftwareUpdate >/dev/null 2>"$NOHUP_ERR" &
    LAUNCH_RC=$?
    sleep 2
    NEWSU=$(pidof SoftwareUpdate-real 2>/dev/null | awk '{print $1}')
    [ -z "$NEWSU" ] && NEWSU=$(pidof SoftwareUpdate 2>/dev/null | awk '{print $1}')
    NEWSU_PPID=$(awk '/^PPid:/ {print $2}' /proc/${NEWSU:-1}/status 2>/dev/null)
    NEWSU_EXE=$(readlink /proc/${NEWSU:-1}/exe 2>/dev/null | head -c 160)
    NEWSU_PRELOAD=$(grep -aoE 'LD_PRELOAD=[^[:cntrl:]]*str-shim.so' /proc/${NEWSU:-1}/environ 2>/dev/null | head -c 160)
    setup_log "shim late-swap: relaunched (launch_rc=$LAUNCH_RC) new_pid=${NEWSU:-?} ppid=$NEWSU_PPID exe='$NEWSU_EXE' preload='${NEWSU_PRELOAD:-not-set}' uptime=$(uptime_s)s"
    if [ -s "$NOHUP_ERR" ]; then
        setup_log "shim late-swap: nohup stderr captured: '$(tr '\n' ' ' < "$NOHUP_ERR" | head -c 400)'"
    fi

    # Fallback launch WITHOUT the wrapper if the first attempt left
    # no SU process. Goes straight to the cached backup binary with
    # no LD_PRELOAD set, so a successful launch here proves the
    # binary itself runs but the LD_PRELOAD path is the culprit. A
    # failure here proves it is the bind-mount or something deeper.
    # Either way the next bundle separates the two hypotheses cleanly.
    if [ -z "$NEWSU" ] && [ -x "$SHIM_BACKUP" ]; then
        FALLBACK_ERR="/tmp/streborn-shim-fallback.err"
        rm -f "$FALLBACK_ERR" 2>/dev/null
        nohup "$SHIM_BACKUP" >/dev/null 2>"$FALLBACK_ERR" &
        FALLBACK_RC=$?
        sleep 2
        FALLBACK_PID=$(pidof SoftwareUpdate-real 2>/dev/null | awk '{print $1}')
        [ -z "$FALLBACK_PID" ] && FALLBACK_PID=$(pidof SoftwareUpdate 2>/dev/null | awk '{print $1}')
        if [ -n "$FALLBACK_PID" ]; then
            setup_log "shim late-swap: fallback launch (no shim, direct $SHIM_BACKUP) OK pid=$FALLBACK_PID rc=$FALLBACK_RC — LD_PRELOAD path is the culprit, binary itself runs fine"
        else
            setup_log "shim late-swap: fallback launch (no shim, direct $SHIM_BACKUP) ALSO FAILED rc=$FALLBACK_RC stderr='$(tr '\n' ' ' < "$FALLBACK_ERR" 2>/dev/null | head -c 400)' — bind-mount or something deeper is the culprit"
        fi
        NEWSU="$FALLBACK_PID"
    fi

    # Verify Bose /info still responds AFTER the swap. If it dropped,
    # log the regression — we will diagnose via the diagnostic bundle
    # rather than try to recover here (the box still has SSH up and
    # the disable marker is one `touch` away).
    sleep 5
    POSTSW_INFO=$(wget -qO- -T 3 http://127.0.0.1:8090/info 2>/dev/null | head -c 200)
    if echo "$POSTSW_INFO" | grep -q "<info "; then
        setup_log "shim late-swap: post-swap Bose /info HEALTHY"
    else
        setup_log "shim late-swap: WARN — post-swap Bose /info NOT healthy, body='$POSTSW_INFO' — mesh may be damaged, will continue monitoring"
    fi
    POSTSW_17008=$(netstat -ltnp 2>/dev/null | awk '$4 ~ /:17008$/ {print $7; exit}')
    setup_log "shim late-swap: post-swap owner of :17008 = ${POSTSW_17008:-none}"

    # Final correctness probe: connect to localhost:17008/api/agent/version.
    # If the shim is forwarding to STR's :8888, we get JSON with a
    # "version" field. If we get Bose's SoftwareUpdate response (HTML
    # or other), the forwarding never happened. This is THE test —
    # everything above is preparation.
    SELF_PROBE_BODY=$(wget -qO- -T 3 http://127.0.0.1:17008/api/agent/version 2>/dev/null | head -c 200)
    if echo "$SELF_PROBE_BODY" | grep -q '"version"'; then
        setup_log "shim late-swap: SUCCESS — :17008 self-probe returned STR JSON ('$(echo "$SELF_PROBE_BODY" | head -c 80)')"
    else
        setup_log "shim late-swap: FAIL — :17008 self-probe NOT STR (body='$SELF_PROBE_BODY' length=$(echo "$SELF_PROBE_BODY" | wc -c)) — the shim is not forwarding to :8888"
    fi

    # Post-swap watchdog: 5 minutes of every-60s liveness checks.
    # On variants where shepherdd respawns SU without our wrapper,
    # the PID will change AND :17008 will start serving Bose's
    # native HTTP again. Detecting that signal in the bundle is the
    # whole point of this watchdog — without it we cannot tell a
    # taigan-style "kill once, shim persists" outcome from an
    # rhino-style "shepherdd keeps overwriting our work".
    (
        WATCH_PID="$NEWSU"
        for n in 1 2 3 4 5; do
            sleep 60
            CUR_PID=$(pidof SoftwareUpdate-real 2>/dev/null | awk '{print $1}')
            [ -z "$CUR_PID" ] && CUR_PID=$(pidof SoftwareUpdate 2>/dev/null | awk '{print $1}')
            CUR_OWNER=$(netstat -ltnp 2>/dev/null | awk '$4 ~ /:17008$/ {print $7; exit}')
            CUR_PROBE=$(wget -qO- -T 2 http://127.0.0.1:17008/api/agent/version 2>/dev/null | head -c 80)
            if echo "$CUR_PROBE" | grep -q '"version"'; then
                CUR_STATE="STR-forwarding"
            else
                CUR_STATE="not-STR"
            fi
            if [ "$CUR_PID" != "$WATCH_PID" ]; then
                setup_log "shim watchdog t+${n}m: PID changed $WATCH_PID -> ${CUR_PID:-none} (probable shepherd respawn), :17008 owner=$CUR_OWNER state=$CUR_STATE"
                WATCH_PID="$CUR_PID"
            else
                setup_log "shim watchdog t+${n}m: PID stable=$CUR_PID owner=$CUR_OWNER state=$CUR_STATE"
            fi
        done
    ) &
}
(shim_late_swap) &

# Binary selection: a flash-verified NAND cache is preferred. If the NAND
# write could not be committed (tight NAND -> erased 0xff pages that crash the
# agent with SIGILL, #302), run from a RAM copy of the stick binary instead.
# A NAND cache that verified fine, or a healthy stickless boot, uses NAND as
# before; the stick binary directly is the last resort.
if [ "$NAND_BIN_CORRUPT" = "1" ] && stage_ram_binary; then
    BIN="$RAM_BIN"
    log "running the agent from RAM ($BIN): the NAND copy did not commit to flash"
elif [ -x "$CACHED_BIN" ]; then
    BIN="$CACHED_BIN"
elif [ -x "$STICK_BIN" ]; then
    BIN="$STICK_BIN"
    log "no NAND cache, using the stick binary directly"
else
    log "ERROR: neither NAND cache nor stick binary available"
    exit 1
fi

# === Agent already running? Then stop it. ===
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

# === Apply optional update ===
if [ -x "$STICK/update.sh" ]; then
    log "checking for update via $STICK/update.sh"
    "$STICK/update.sh" 2>&1 | tee -a "$LOG" || true
fi

# === Wi-Fi provisioning from wlan.conf on the stick (multi-approach) ===
#
# A factory-reset Bose writes /etc/wpa_supplicant.conf on boot from
# its own NetManager DB. If no profile is stored there, it throws out
# our direct-write variant again on the next boot. So we run BOTH
# paths in parallel:
#
#   A) Direct write to /etc/wpa_supplicant.conf + wpa_supplicant
#      restart. Takes effect immediately, the box is on Wi-Fi within seconds.
#   B) addWirelessProfile API call against 127.0.0.1:8090. Persists
#      the profile in NetManager's own DB, so it survives the next
#      reboot without us having to read wlan.conf again.
#
# Whichever succeeds first wins. Every step is written to
# /media/sda1/setup.log with a timestamp so a user can simply pull
# the stick and upload the diagnostic log via the app (Bose's factory
# reset wipes NAND, the stick stays untouched).
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
# HIDDEN=1 marks a hidden network (SSID broadcast disabled). Hidden APs never
# answer broadcast probes, so wpa-based provisioning must set scan_ssid=1 or
# the box will never find the network. Persisted alongside SSID/PASS in the
# NAND wlan-creds (written by the agent's /api/box/wlan and by the persist
# helpers below) so boot replay keeps the flag.
HIDDEN=""
WLAN_SOURCE=""

# persist_wlan_creds writes the live SSID+PASS into NAND so a future
# reboot can replay via the elif branch above when the stick has been
# yanked and NetManager's own profile DB is stale or wiped. Idempotent —
# safe to call from every winner branch and from a single post-summary
# call site. Skips silently when SSID/PASS are empty (ethernet-only or
# WLAN-creds-not-loaded paths).
#
# 2026-05-30 — a rhino ST10 diagnostic bundle proved how badly the
# previous scheme failed: three boots in a row M1-http won with the
# stick's credentials, but M1 had no inline persist, so wlan-creds
# stayed whatever it was before. Then a True Factory Reset wiped
# wlan-creds AND Bose's NetworkProfiles.xml AND stick wlan.conf had
# already been removed by the previous "rm \$WLAN_CONF" line — every
# credential channel was empty on the next boot, and the box dropped
# straight into Setup-AP. Centralising persistence here means every
# M-winner benefits without per-branch boilerplate.
persist_wlan_creds() {
    [ -n "$SSID" ] || return 0
    [ -n "$PASS" ] || return 0
    { printf 'SSID=%s\n' "$SSID"
      printf 'PASS=%s\n' "$PASS"
      if [ "$HIDDEN" = "1" ]; then printf 'HIDDEN=1\n'; fi
    } > "$WLAN_CREDS_NAND.new" 2>/dev/null
    if [ -s "$WLAN_CREDS_NAND.new" ]; then
        mv "$WLAN_CREDS_NAND.new" "$WLAN_CREDS_NAND" 2>/dev/null
        chmod 600 "$WLAN_CREDS_NAND" 2>/dev/null
        setup_log "wlan-creds persisted to NAND for next-boot replay"
    fi
}
if [ -f "$WLAN_CONF" ]; then
    SSID=$(sed -n 's/.*"ssid":"\([^"]*\)".*/\1/p' "$WLAN_CONF" | head -1)
    PASS=$(sed -n 's/.*"password":"\([^"]*\)".*/\1/p' "$WLAN_CONF" | head -1)
    # Forward-compatible: a stick provisioned for a hidden network may carry
    # "hidden":true in wlan.conf (with or without a space after the colon).
    case "$(cat "$WLAN_CONF" 2>/dev/null)" in
        *'"hidden":true'*|*'"hidden": true'*) HIDDEN=1 ;;
    esac
    WLAN_SOURCE="stick wlan.conf"
elif [ -r "$WLAN_CREDS_NAND" ]; then
    SSID=$(sed -n 's/^SSID=\(.*\)$/\1/p' "$WLAN_CREDS_NAND" | head -1)
    PASS=$(sed -n 's/^PASS=\(.*\)$/\1/p' "$WLAN_CREDS_NAND" | head -1)
    HIDDEN=$(sed -n 's/^HIDDEN=\(.*\)$/\1/p' "$WLAN_CREDS_NAND" | head -1)
    WLAN_SOURCE="NAND wlan-creds (replay)"
    # #184: an app-initiated "apply now" Wi-Fi change drops this one-shot marker
    # before rebooting. Its intent is to PROGRAM the new SSID, not to passively
    # replay the current network, so skip the hands-off *replay* early-exit below
    # and run the active provisioning pipeline (goform / M-path) once. Delete the
    # marker on read so a wrong password cannot loop: the next boot is a normal
    # replay again, never worse than before the fix.
    if [ -f "$PERSIST/.wlan-apply-pending" ]; then
        rm -f "$PERSIST/.wlan-apply-pending" 2>/dev/null
        WLAN_SOURCE="NAND wlan-creds (app apply)"
        setup_log "wlan-creds: app apply-pending marker present -> active provision this boot (#184)"
    fi
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
    HIDDEN=""
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
# --- STA-lease helpers: TOP-LEVEL on purpose --------------------------
# These are used by BOTH the WLAN-provisioning subshell below AND the
# separate REDIRECT-install subshell further down. A `( ) &` subshell
# does NOT inherit functions defined in a sibling `( ) &` subshell, so
# when these lived inside the WLAN subshell the REDIRECT subshell calling
# current_sta_lease got "command not found" -> empty -> the REDIRECT
# install bailed "current_sta_lease empty" for the box's lifetime, even
# with eth0 + /networkInfo holding the lease (root-caused 2026-06-01).
# Defining them here, before any subshell, makes every child subshell
# inherit them so that whole class of bug cannot recur.
is_real_sta_addr() {
    # Setup-AP gateway IPs the speaker hosts itself on. Anything else
    # is a real DHCP lease (even 192.168.1.x from a home router whose
    # subnet happens to match — only the literal .1 / .0.2.1 gateway
    # addresses are setup-AP). A correctness fix vs the earlier
    # 192.168.1.0/24 wildcard, which broke any user whose home LAN
    # used the very common 192.168.1.0/24 default.
    case "$1" in
        ""|0.0.0.0|192.168.1.1|192.0.2.1) return 1 ;;
        169.254.*) return 1 ;;
        *) return 0 ;;
    esac
}
current_sta_lease() {
    # Walks every wireless-candidate iface (wlan0, wlan1, eth0 on
    # BCO chassis) and prints "iface|ip" for the first real STA lease
    # it finds. Returns 1 if none.
    #
    # The regex matches "inet 10.0.0.5/24" AND "inet 10.0.0.5 peer ..."
    # (no slash after the IP). spotty 2026-05-30 bundle showed
    # the second form coming out of busybox ip on this firmware, which
    # made the older slash-mandatory regex return empty and the
    # REDIRECT install loop bail with "no wlan0/wlan1/eth0 STA address
    # yet" for the lifetime of the box.
    _csl_cache=/tmp/.streborn-last-lease
    for _iface in wlan0 wlan1 eth0; do
        [ -d "/sys/class/net/$_iface" ] || continue
        for _ip in $(ip -4 addr show "$_iface" 2>/dev/null | sed -n 's/.*inet \([0-9][0-9.]*\).*/\1/p'); do
            if is_real_sta_addr "$_ip"; then
                printf '%s|%s' "$_iface" "$_ip"
                echo "$_iface|$_ip" > "$_csl_cache" 2>/dev/null
                return 0
            fi
        done
        # ifconfig fallback for boxes where ip output format differs
        # enough that the sed above still misses.
        for _ip in $(ifconfig "$_iface" 2>/dev/null | sed -n 's/.*inet addr:\([0-9][0-9.]*\).*/\1/p'); do
            if is_real_sta_addr "$_ip"; then
                printf '%s|%s' "$_iface" "$_ip"
                echo "$_iface|$_ip" > "$_csl_cache" 2>/dev/null
                return 0
            fi
        done
    done
    # Bose-sourced fallback: ask BoseApp what network IP the box holds.
    # CRITICAL (live-verified on a taigan Portable + spotty ST20, both
    # BCO chassis, 2026-05-31): the IP is exposed by /networkInfo as an
    # ATTRIBUTE, `ipAddress="X"` on the active interface. It is NOT a
    # <ipAddress> element and is NOT present in /info at all. The older
    # code parsed /info for a <ipAddress> element, which never matched
    # on these boxes, so the fallback was dead. On BCO chassis the STA
    # IP is managed by the chipset and the Linux iface parse above can
    # come up empty even while the box is fully on the LAN, so this is
    # the path that actually resolves the lease there. Try /networkInfo
    # (attribute) first, then /info (<ipAddress> element) for any
    # firmware that happens to use the element form.
    if command -v wget >/dev/null 2>&1; then
        _bose_ip=$(wget -qO- -T 3 "http://127.0.0.1:8090/networkInfo" 2>/dev/null \
            | sed -n 's/.*ipAddress="\([0-9][0-9.]*\)".*/\1/p' | head -1)
        [ -z "$_bose_ip" ] && _bose_ip=$(wget -qO- -T 3 "http://127.0.0.1:8090/info" 2>/dev/null \
            | sed -n 's/.*<ipAddress>\([0-9][0-9.]*\)<.*/\1/p' | head -1)
        if [ -n "$_bose_ip" ] && is_real_sta_addr "$_bose_ip"; then
            # Prefer the iface that actually has it; otherwise emit the
            # first existing candidate so the LEASE token stays
            # well-formed (eth0 first: it is the WLAN iface on BCO).
            for _iface in eth0 wlan0 wlan1; do
                [ -d "/sys/class/net/$_iface" ] || continue
                if ip -4 addr show "$_iface" 2>/dev/null | grep -q "$_bose_ip"; then
                    printf '%s|%s' "$_iface" "$_bose_ip"
                    echo "$_iface|$_bose_ip" > "$_csl_cache" 2>/dev/null
                    return 0
                fi
            done
            for _iface in eth0 wlan0 wlan1; do
                if [ -d "/sys/class/net/$_iface" ]; then
                    printf '%s|%s' "$_iface" "$_bose_ip"
                    echo "$_iface|$_bose_ip" > "$_csl_cache" 2>/dev/null
                    return 0
                fi
            done
        fi
    fi
    # Final fallback: the last real lease we resolved THIS power cycle
    # (cache lives in /tmp, so it is cleared on every reboot and can
    # never carry a stale cross-boot IP). current_sta_lease is polled
    # from several places (M0a at ~30s, the REDIRECT watchdog every
    # 30s). On BCO boxes BoseApp /networkInfo stops answering under
    # boot-time load (the same wedge that makes M0 time out), which
    # otherwise makes a lease that WAS resolved at 30s vanish at 155s
    # and bail the REDIRECT install for the lifetime of the box
    # (spotty #90, 2026-05-31). A stationary speaker keeps its
    # DHCP lease, so the last-known-good IP is the best answer while
    # every live source is momentarily mute. Only ever cached a
    # real (is_real_sta_addr) IP, so the setup-AP gateway is never here.
    if [ -r "$_csl_cache" ]; then
        _cached=$(cat "$_csl_cache" 2>/dev/null)
        _cached_ip=${_cached##*|}
        if [ -n "$_cached_ip" ] && is_real_sta_addr "$_cached_ip"; then
            printf '%s' "$_cached"
            return 0
        fi
    fi
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

# is_real_sta_addr / current_sta_lease / wait_for_sta_lease are now
# defined at TOP LEVEL (above this subshell) so both this WLAN subshell
# AND the sibling REDIRECT-install subshell inherit them. See the
# comment at their definition for why.

if [ -n "$SSID" ] && [ -n "$PASS" ]; then
    setup_log "=== WLAN provisioning start (boot at $(uptime | tr -s ' ')) source=$WLAN_SOURCE ==="
    setup_log "wlan.conf parsed: SSID='$SSID' password_length=${#PASS}"

    # ---- v0.9.7 hands-off boot (Jens, 2026-07-12) ----------------------
    # On a NORMAL boot (credentials replayed from NAND, no fresh stick
    # wlan.conf) STR no longer provisions Wi-Fi AT ALL: no
    # /addWirelessProfile, no profile-file writes, no goform apply. Every
    # boot-time intervention added since v0.8.48 is the prime suspect for
    # unexplained Wi-Fi losses and for online boxes showing an orange
    # network icon (#270; live-confirmed 2026-07-12 on an scm ST30 whose
    # boot got a needless M1 profile POST while it was already online).
    # The stock firmware owns its Wi-Fi state until a verified-stable path
    # through the boxes' own web interfaces exists. Wi-Fi is provisioned
    # only on explicit user action: a stick wlan.conf (install/setup) or
    # the app's Wi-Fi settings (runtime API).
    #
    # Single exception, pure OFFLINE rescue: if the box holds no lease at
    # all after a 90s grace, the non-destructive goform re-push watchdog
    # (#157) still nudges the coprocessor on a slow cadence. It exits the
    # moment any lease appears and never touches an online box; without it
    # a cold-boot association failure (scm ST20 class) strands the box
    # until a manual power-cycle.
    case "$WLAN_SOURCE" in
        *replay*)
            if wait_for_sta_lease 90; then
                setup_log "hands-off: box came online by itself ($(current_sta_lease 2>/dev/null)) - no boot-time Wi-Fi provisioning (v0.9.7)"
            else
                setup_log "hands-off: no lease after 90s - starting the pure-rescue goform watchdog (non-destructive re-push only, no profile writes)"
                (
                    _rw=0
                    while [ "$_rw" -lt 720 ]; do
                        sleep 60
                        _rw=$(( _rw + 60 ))
                        if current_sta_lease >/dev/null 2>&1; then
                            setup_log "hands-off rescue: lease present at +${_rw}s, done"
                            break
                        fi
                        setup_log "hands-off rescue: still no lease at +${_rw}s, non-destructive goform re-push"
                        goform_wlan_push "$SSID" "$PASS"
                    done
                ) &
            fi
            setup_log "=== WLAN provisioning end (hands-off) ==="
            exit 0
            ;;
    esac

    BOSE_API="http://127.0.0.1:8090"
    WINNER="none"

    # Hoisted above M0a because the M0a-refresh branch
    # (password rotation while staying on the same SSID) needs the
    # escape helper before the main pipeline runs.
    xml_escape() {
        printf '%s' "$1" | sed \
            -e 's/\&/\&amp;/g' \
            -e 's/</\&lt;/g' \
            -e 's/>/\&gt;/g' \
            -e 's/"/\&quot;/g' \
            -e "s/'/\&apos;/g"
    }

    # ---- M_air helpers: AirplayConfiguration.xml WLAN profile ----------
    # On BCO chassis (Portable/taigan, ST20-spotty) the Wi-Fi coprocessor
    # is driven by BoseApp's BCONetworkServicesController, which reads the
    # PersistentWifiProfileArray from
    #   /mnt/nv/BoseApp-Persistence/<N>/AirplayConfiguration.xml
    # and pushes the profile to the coprocessor over USB-CDC-Ethernet
    # (eth0) at boot. The documented HTTP /addWirelessProfile (M1) is
    # cloud-gated (MargeHSM NotAssociated -> HTTP 500) and dead on BCO
    # post-shutdown, so this direct file write is the BCO provisioning
    # path. Live-verified 2026-06-01 on a taigan Portable: a plaintext
    # encrypted="false" PersistentWifiProfile + reboot -> box joined the
    # target SSID (NETWORK_WIFI_CONNECTED, setup state SETUP_INACTIVE).
    # The coprocessor honours encrypted="false"; the AES path is
    # NetManager's separate NetworkProfiles.xml store, not this one.
    AIR_REBOOT_STAMP="$PERSIST/.airplay-reboot-stamp"
    AIR_WROTE=""
    AIR_FILE=""

    airplay_creds_fp() {
        # Stable fingerprint of the current SSID+PASS so the reboot guard
        # fires once per credential set and never loops.
        if command -v md5sum >/dev/null 2>&1; then
            printf '%s\n%s' "$SSID" "$PASS" | md5sum | cut -d' ' -f1
        else
            printf '%s:%s' "$SSID" "${#PASS}"
        fi
    }
    airplay_reboot_guard_ok() {
        # 0 (ok to reboot) only if we have NOT already rebooted for these
        # exact creds. Stamp lives on NAND so it survives the reboot and
        # blocks a second pass on the next boot: if the profile did not
        # take (wrong PSK, or this firmware ignores the file) the next
        # boot falls through to M1..M6 instead of rebooting forever.
        _fp=$(airplay_creds_fp)
        _seen=$(cat "$AIR_REBOOT_STAMP" 2>/dev/null)
        [ "$_fp" = "$_seen" ] && return 1
        printf '%s' "$_fp" > "$AIR_REBOOT_STAMP" 2>/dev/null
        return 0
    }
    airplay_slot0_ssid() {
        # Echo the SSID already stored in the AirplayConfiguration slot-0
        # PersistentWifiProfile, or nothing. Used to detect an
        # already-provisioned BCO box so M_air does not rewrite + reboot
        # on every boot when a provisioned stick is left inserted
        # (#90: spotty white-bar from the needless reboot).
        _asf=""
        for _af in /mnt/nv/BoseApp-Persistence/*/AirplayConfiguration.xml; do
            [ -f "$_af" ] && _asf="$_af" && break
        done
        [ -z "$_asf" ] && return 0
        sed -n 's/.*PersistentWifiProfile[^>]*ssid="\([^"]*\)".*/\1/p' "$_asf" | head -1
    }
    write_airplay_profile() {
        # Non-destructive: only slot 0 is set, other slots are left as-is,
        # existing profiles are never cleared. Sets AIR_WROTE=1 on success.
        # Hidden-SSID note: the PersistentWifiProfile XML schema has no
        # documented hidden/broadcast attribute, so HIDDEN=1 is NOT threaded
        # into this path. Hidden-network support on BCO chassis is unverified
        # chip-side; do not guess undocumented fields here.
        AIR_WROTE=""
        # A double-quote in SSID/PSK would break the XML attribute and
        # could corrupt the file the box boots from. Bail rather than risk
        # it; the other methods still run.
        case "$SSID$PASS" in
            *'"'*) setup_log "M_air: SKIP — SSID/PASS contains a double-quote, cannot build XML attribute safely"; return 1 ;;
        esac
        _air=""
        for _f in /mnt/nv/BoseApp-Persistence/*/AirplayConfiguration.xml; do
            [ -f "$_f" ] && _air="$_f" && break
        done
        if [ -z "$_air" ]; then
            _dir=$(ls -d /mnt/nv/BoseApp-Persistence/*/ 2>/dev/null | head -1)
            [ -z "$_dir" ] && _dir="/mnt/nv/BoseApp-Persistence/1/"
            mkdir -p "$_dir" 2>/dev/null
            _air="${_dir%/}/AirplayConfiguration.xml"
        fi
        AIR_FILE="$_air"
        if [ -f "$_air" ]; then
            _cnt=$(grep -c 'PersistentWifiProfile [^>]*ssid="[^"]\{1,\}"' "$_air" 2>/dev/null)
            setup_log "M_air: prior AirplayConfiguration present at $_air (non-empty-slots=${_cnt:-0})"
        else
            setup_log "M_air: no AirplayConfiguration.xml yet, will create $_air from template"
        fi
        # Pass SSID/PASS to awk via a 0600 temp file read with getline so
        # they never appear on the process command line (ps).
        _vals="/tmp/.str-air-vals"
        { printf 'SSID=%s\n' "$SSID"; printf 'PASS=%s\n' "$PASS"; } > "$_vals" 2>/dev/null
        chmod 600 "$_vals" 2>/dev/null
        if [ -f "$_air" ] && grep -q '<PersistentWifiProfile ' "$_air" 2>/dev/null; then
            # Rebuild slot 0 by printing a fresh line: ssid/pass are
            # injected via string concatenation (NOT sub() replacement),
            # so an ampersand or backslash in the PSK is emitted literally
            # and never mis-interpreted. wepKey is preserved verbatim from
            # the original line (match/substr); every other field is set
            # to the live-verified working values (only the active profile
            # is plaintext; encrypted="false"). Also activate the slot:
            # MaxWifiProfileId + WifiProfileArray[0] -> WIFI_PROFILE_ID_1.
            # Other three slots and the rest of the file pass through
            # untouched.
            awk -v vf="$_vals" '
                BEGIN{
                    while((getline l < vf)>0){
                        if(l ~ /^SSID=/) ssid=substr(l,6)
                        else if(l ~ /^PASS=/) pass=substr(l,6)
                    }
                    pdone=0; inarr=0; idone=0
                }
                /<MaxWifiProfileId>/{ sub(/>[^<]*</, ">WIFI_PROFILE_ID_1<") }
                # Enable the AirPlay optimization by default: this is the
                # iOS app advanced-settings "AirPlay improvement" toggle,
                # stored as BCOResetTimerEnabled on the root (a BCO
                # coprocessor keepalive). Applied on the reboot M_air does.
                /<AirplayConfiguration /{ sub(/BCOResetTimerEnabled="[^"]*"/, "BCOResetTimerEnabled=\"true\"") }
                /<WifiProfileArray>/{ inarr=1 }
                /<\/WifiProfileArray>/{ inarr=0 }
                inarr && /<Item>/ && !idone { sub(/>[^<]*</, ">WIFI_PROFILE_ID_1<"); idone=1 }
                /<PersistentWifiProfile / && !pdone {
                    wk=""
                    if (match($0, /wepKey="[^"]*"/)) wk=substr($0, RSTART, RLENGTH)
                    print "        <PersistentWifiProfile ssid=\"" ssid "\" passphrase=\"" pass "\" wpaCipher=\"AES\" security=\"WPA2PSK\" " wk " encrypted=\"false\" dhcpStatus=\"DHCP_ACTIVE\" ipAddress=\"\" ipMask=\"\" ipGateway=\"\" proxyServerStatus=\"PROXY_SERVER_DISABLED\" proxyServer=\"\" proxyPort=\"\" dnsServer1=\"\" dnsServer2=\"\" />"
                    pdone=1
                    next
                }
                { print }
            ' "$_air" > "$_air.str-new" 2>/dev/null
        else
            # Create from scratch matching the live-verified layout, slot
            # 0 = our profile, slots 1..3 empty.
            awk -v vf="$_vals" '
                BEGIN{
                    while((getline l < vf)>0){
                        if(l ~ /^SSID=/) ssid=substr(l,6)
                        else if(l ~ /^PASS=/) pass=substr(l,6)
                    }
                    e="\" passphrase=\"\" wpaCipher=\"\" security=\"\" wepKey=\"\" encrypted=\"false\" dhcpStatus=\"DHCP_ACTIVE\" ipAddress=\"\" ipMask=\"\" ipGateway=\"\" proxyServerStatus=\"PROXY_SERVER_DISABLED\" proxyServer=\"\" proxyPort=\"\" dnsServer1=\"\" dnsServer2=\"\" />"
                    print "<?xml version=\"1.0\" encoding=\"UTF-8\" ?>"
                    print "<AirplayConfiguration SmscUpdating=\"false\" RestoreAttempts=\"0\" BCOResetTimerEnabled=\"true\">"
                    print "    <MaxWifiProfileId>WIFI_PROFILE_ID_1</MaxWifiProfileId>"
                    print "    <WifiProfileArray>"
                    print "        <Item>WIFI_PROFILE_ID_1</Item>"
                    print "        <Item>INVALID_WIFI_PROFILE_ID</Item>"
                    print "        <Item>INVALID_WIFI_PROFILE_ID</Item>"
                    print "        <Item>INVALID_WIFI_PROFILE_ID</Item>"
                    print "    </WifiProfileArray>"
                    print "    <JBDirectEnabled>false</JBDirectEnabled>"
                    print "    <PersistentWifiProfileArray>"
                    print "        <PersistentWifiProfile ssid=\"" ssid "\" passphrase=\"" pass "\" wpaCipher=\"AES\" security=\"WPA2PSK\" wepKey=\"\" encrypted=\"false\" dhcpStatus=\"DHCP_ACTIVE\" ipAddress=\"\" ipMask=\"\" ipGateway=\"\" proxyServerStatus=\"PROXY_SERVER_DISABLED\" proxyServer=\"\" proxyPort=\"\" dnsServer1=\"\" dnsServer2=\"\" />"
                    print "        <PersistentWifiProfile ssid=\"" e
                    print "        <PersistentWifiProfile ssid=\"" e
                    print "        <PersistentWifiProfile ssid=\"" e
                    print "    </PersistentWifiProfileArray>"
                    print "    <CneSettings cneSettingsPreserved=\"false\" />"
                    print "    <PersistenceSettings persistenceSettingsPreserved=\"false\" />"
                    print "</AirplayConfiguration>"
                }
            ' > "$_air.str-new" 2>/dev/null
        fi
        rm -f "$_vals" 2>/dev/null
        if [ -s "$_air.str-new" ] && grep -q "ssid=\"$SSID\"" "$_air.str-new" 2>/dev/null; then
            [ -f "$_air" ] && cp -p "$_air" "$_air.str-bak" 2>/dev/null
            mv "$_air.str-new" "$_air" 2>/dev/null
            sync 2>/dev/null
            AIR_WROTE=1
            setup_log "M_air: wrote slot-0 PersistentWifiProfile encrypted=false ssid='$SSID' pass_len=${#PASS} -> $_air"
            # acctMode=local so BoseApp does not block on a cloud account.
            _scdb="${_air%/AirplayConfiguration.xml}/SystemConfigurationDB.xml"
            if [ -f "$_scdb" ] && ! grep -q '<acctMode>local</acctMode>' "$_scdb" 2>/dev/null; then
                sed 's#<acctMode>[^<]*</acctMode>#<acctMode>local</acctMode>#' "$_scdb" > "$_scdb.str-new" 2>/dev/null
                [ -s "$_scdb.str-new" ] && mv "$_scdb.str-new" "$_scdb" 2>/dev/null \
                    && setup_log "M_air: set acctMode=local in SystemConfigurationDB.xml"
            fi
            return 0
        fi
        rm -f "$_air.str-new" 2>/dev/null
        setup_log "M_air: write FAILED (new file empty or ssid missing), AirplayConfiguration left untouched"
        return 1
    }

    finalize_oob_setup() {
        # After a BCO box joins Wi-Fi via M_air, its setup state machine
        # can still sit at systemstate=SETUP_LANG_NOT_SET: the on-screen
        # "download the SoundTouch app" hint persists and the box looks
        # unfinished even though it is on the LAN. M_air provisions Wi-Fi
        # by writing AirplayConfiguration.xml and rebooting BEFORE the M1
        # /language + /name gates run, and the post-reboot boot wins via
        # M0a-prelease which skips M1. So fire those same two gates here,
        # once BoseApp is up. Self-gated on systemstate -> no-op once set.
        # Live finding 2026-06-01: a taigan joined JJ3 via M_air but the
        # display kept prompting the iOS app because language stayed unset.
        _fo_api="http://127.0.0.1:8090"
        _fo=0
        while [ "$_fo" -lt 160 ]; do
            if wget -qO- -T 5 "$_fo_api/info" 2>/dev/null | grep -q "<info "; then break; fi
            sleep 5; _fo=$((_fo + 5))
        done
        case "$(wget -qO- -T 5 "$_fo_api/setup" 2>/dev/null)" in
            *SETUP_LANG_NOT_SET*)
                _fo_lang=$(resolve_setup_language)
                setup_log "OOB-finalize: systemstate SETUP_LANG_NOT_SET after WLAN join, POSTing /language=$_fo_lang + /name to leave OOB"
                wget -qO- -T 5 --header="Content-Type: text/xml" --post-data="<sysLanguage>${_fo_lang}</sysLanguage>" "$_fo_api/language" >/dev/null 2>&1
                _nm=""
                [ -f "$STICK/name.conf" ] && _nm=$(sed -n 's/.*"name":"\([^"]*\)".*/\1/p' "$STICK/name.conf" | head -1)
                [ -z "$_nm" ] && _nm=$(wget -qO- -T 5 "$_fo_api/name" 2>/dev/null | sed -n 's:.*<name>\([^<]*\)</name>.*:\1:p' | head -1)
                [ -z "$_nm" ] && _nm="SoundTouch"
                _nme=$(xml_escape "$_nm" 2>/dev/null || printf '%s' "$_nm")
                wget -qO- -T 5 --header="Content-Type: text/xml" --post-data="<name>${_nme}</name>" "$_fo_api/name" >/dev/null 2>&1
                setup_log "OOB-finalize: posted /language=$_fo_lang + /name='$_nm' to clear the app-download OOB hint"
                ;;
            *)
                setup_log "OOB-finalize: systemstate already past SETUP_LANG_NOT_SET, nothing to do"
                ;;
        esac
    }

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
    # provisioned it, and live again on Series-I scm-variant ST20
    # 2026-05-29 (#60) where pre-stick the box was on Wi-Fi,
    # M0a logged "skipping" but the pipeline kept running because
    # M1 lacked a $WINNER guard, then on the next cold boot the
    # replay path tore down the working profile.
    #
    # Bose's stack is idempotent on STA lease (it does not unjoin and
    # rejoin every boot), so if a lease is present we know the box
    # is fine without us. STR's REST API, mDNS announce, marge stub,
    # autopair etc. all run downstream of this block, unaffected.
    #
    # Lease detection window: a one-shot probe at uptime~30s is too
    # early on a cold boot — Bose's stack typically takes 30-60s to
    # reassociate after power-on. If `network wifi profiles info`
    # reports any stored profile (or BoseApp's /networkInfo shows
    # wifiProfileCount > 0), we know the box has somewhere to
    # associate to and it's worth waiting. Without that signal we
    # exit M0a immediately to fall through to provisioning.
    PRE_LEASE=$(current_sta_lease 2>/dev/null || true)
    if [ -z "$PRE_LEASE" ]; then
        HAS_STORED_PROFILE=""
        HAS_STORED_PROFILE_SRC=""
        # Source 1: NetManager's persisted profile DB on disk.
        # NetworkProfiles.xml is written by Bose's NetManager and lives
        # at /mnt/nv/BoseApp-Persistence/1/NetworkProfiles.xml. It is
        # available BEFORE BoseApp's HTTP server comes up (so we do
        # not get bitten by the wget timeout race that v0.5.16 had,
        # where /networkInfo returned empty because BoseApp was not
        # ready at uptime ~31s and M0a defaulted to "no profile" then
        # fell through to destructive M1+M2). Ground truth of what
        # NetManager will associate to on the next iteration.
        _profile_xml="/mnt/nv/BoseApp-Persistence/1/NetworkProfiles.xml"
        if [ -s "$_profile_xml" ] && grep -q "<profile " "$_profile_xml" 2>/dev/null; then
            HAS_STORED_PROFILE=1
            HAS_STORED_PROFILE_SRC="file"
        fi
        # Source 2: TAP CLI runtime view. May be empty at cold boot
        # even when the on-disk XML has an entry, but useful on
        # variants where the file path differs.
        if [ -z "$HAS_STORED_PROFILE" ] && [ -n "$TAP_CMD" ]; then
            _info_xml="${TAP_WIFI}"
            if [ -z "$_info_xml" ]; then
                _info_xml=$(printf 'network wifi profiles info\n' | nc -w 3 127.0.0.1 17000 2>/dev/null | tr '\n' ' ')
            fi
            case "$_info_xml" in
                *"<profile "*|*"<profile>"*) HAS_STORED_PROFILE=1; HAS_STORED_PROFILE_SRC="tap" ;;
            esac
        fi
        # Source 3: BoseApp /networkInfo HTTP. Slowest, racy at boot,
        # only last-resort. Quick timeout so we do not stall when
        # BoseApp is not yet up.
        if [ -z "$HAS_STORED_PROFILE" ]; then
            _ni=$(wget -qO- -T 3 "http://127.0.0.1:8090/networkInfo" 2>/dev/null | head -c 400)
            case "$_ni" in
                *'wifiProfileCount="0"'*) ;;
                *'wifiProfileCount="'*) HAS_STORED_PROFILE=1; HAS_STORED_PROFILE_SRC="http" ;;
            esac
        fi
        if [ -n "$HAS_STORED_PROFILE" ]; then
            setup_log "M0a: no STA lease yet but stored profile present (src=$HAS_STORED_PROFILE_SRC), polling up to 60s for cold-boot reassociation"
            _b=60
            while [ "$_b" -gt 0 ]; do
                sleep 5
                _b=$((_b - 5))
                if PRE_LEASE=$(current_sta_lease 2>/dev/null) && [ -n "$PRE_LEASE" ]; then
                    setup_log "M0a: STA lease appeared after $((60 - _b))s — $PRE_LEASE"
                    break
                fi
            done
        fi
    fi
    if [ -n "$PRE_LEASE" ]; then
        # Network-switch detection: skip provisioning ONLY when the
        # stick's wlan.conf SSID matches the SSID Bose already has
        # stored. If they differ, the user explicitly intended a
        # network change by booting with new credentials — fall
        # through to provisioning so the new SSID actually takes.
        # Sources for "what is Bose already configured for":
        #   1. TAP `network wifi profiles info` ssid="..." attributes
        #   2. BoseApp /networkInfo wireless block (later models)
        # If we can't read either reliably, default to the safe
        # "skip" behaviour to preserve the working network rather
        # than risk wiping it.
        M0A_SSID_MATCH=""
        M0A_SSID_DIFFER=""
        _stored_info="${TAP_WIFI}"
        if [ -z "$_stored_info" ] && [ -n "$TAP_CMD" ]; then
            _stored_info=$(printf 'network wifi profiles info\n' | nc -w 3 127.0.0.1 17000 2>/dev/null | tr '\n' ' ')
        fi
        case "$_stored_info" in
            *"ssid=\"$SSID\""*|*"ssid='$SSID'"*) M0A_SSID_MATCH=1 ;;
            *"<profile "*ssid=*) M0A_SSID_DIFFER=1 ;;
        esac
        if [ -z "$M0A_SSID_MATCH" ] && [ -z "$M0A_SSID_DIFFER" ]; then
            _ni_full=$(wget -qO- -T 3 "http://127.0.0.1:8090/networkInfo" 2>/dev/null | head -c 800)
            case "$_ni_full" in
                *"ssid=\"$SSID\""*) M0A_SSID_MATCH=1 ;;
                *ssid=*) M0A_SSID_DIFFER=1 ;;
            esac
        fi
        if [ -n "$M0A_SSID_DIFFER" ] && [ -z "$M0A_SSID_MATCH" ]; then
            setup_log "M0a: STA lease present BUT stored SSID differs from stick wlan.conf SSID='$SSID' — falling through to provisioning so the network switch can take effect"
            PRE_LEASE=""
        else
            setup_log "M0a: pre-flight detected real STA lease ($PRE_LEASE) — skipping destructive provisioning, leaving Bose state intact (ssid match=${M0A_SSID_MATCH:-unknown})"
            # Persist the credentials so a future Bose factory reset
            # that wipes the NetManager DB can replay from NAND.
            { printf 'SSID=%s\n' "$SSID"
              printf 'PASS=%s\n' "$PASS"
              if [ "$HIDDEN" = "1" ]; then printf 'HIDDEN=1\n'; fi
            } > "$WLAN_CREDS_NAND.new" 2>/dev/null
            if [ -s "$WLAN_CREDS_NAND.new" ]; then
                mv "$WLAN_CREDS_NAND.new" "$WLAN_CREDS_NAND" 2>/dev/null
                chmod 600 "$WLAN_CREDS_NAND" 2>/dev/null
            fi
            # Non-destructive profile seed. TWO cases now use this single
            # /addWirelessProfile POST (no `profiles clear`, no setup-AP
            # teardown, live association unaffected if NetManager rejects):
            #
            #  1. Password-rotation refresh: the box is associated under SSID X
            #     with the OLD password and the user booted with SSID X + a NEW
            #     password; NetManager updates the entry in-place.
            #  2. Seed a Wi-Fi profile for LATER failover on an ETHERNET-only
            #     box (the Wi-Fi-via-eth0 / BCO chassis). The box works over
            #     the cable, has no or a different stored Wi-Fi profile, and the
            #     user asked STR to set a Wi-Fi network (app change -> NAND
            #     replay, or a fresh stick). We do NOT tear down the working
            #     ethernet; we only WRITE the target profile into NetManager so
            #     that when the user later pulls the cable, the box fails over
            #     to it. This is what makes "provision Wi-Fi cleanly + one
            #     reboot, then the user decides when to unplug the cable" work
            #     (SoundTouch 30 scm, 2026-07-09: pulling the cable live never
            #     applied the app-set SSID because the profile was only
            #     persisted to NAND for a stickless boot, never pushed to
            #     NetManager while ethernet was up).
            #
            # Runs for any credential source now (stick or NAND replay), still
            # skipped on taigan where this endpoint silently fails (the taigan
            # Wi-Fi path is the goform :80 handler, see [[taigan-quirks]] /
            # [[project_bco_wlan_profile_file]]).
            #
            # Backgrounded: M0a runs at ~57s but BoseApp :8090 (the POST target)
            # only finishes coming up ~10s later on the ST30 scm, so a single
            # in-line probe misses it and the seed silently never fires (live
            # 2026-07-09). Poll for :8090 in a subshell and push once it is up,
            # so the boot pipeline is not blocked and the seed still lands.
            if [ -n "$SSID" ]; then
                ESSID_R=$(xml_escape "$SSID" 2>/dev/null || printf '%s' "$SSID")
                EPASS_R=$(xml_escape "$PASS" 2>/dev/null || printf '%s' "$PASS")
                (
                    _w=0
                    while [ "$_w" -lt 45 ]; do
                        if wget -qO- -T 2 "$BOSE_API/info" >/dev/null 2>&1; then break; fi
                        sleep 3; _w=$((_w + 3))
                    done
                    # 1) NetManager DB entry (so the profile is on record).
                    # Skipped on taigan, where the endpoint silently fails.
                    if [ -z "$IS_TAIGAN" ]; then
                        REFRESH_BODY="<AddWirelessProfile timeout=\"5\"><profile ssid=\"$ESSID_R\" password=\"$EPASS_R\" securityType=\"wpa_or_wpa2\" /></AddWirelessProfile>"
                        REFRESH_RESP=$(wget -qO- -T 6 --header="Content-Type: text/xml" \
                               --post-data="$REFRESH_BODY" "$BOSE_API/addWirelessProfile" 2>&1)
                        setup_log "M0a-refresh: non-destructive /addWirelessProfile (seed for failover) waited=${_w}s rc=$? src='$WLAN_SOURCE' response='$(echo "$REFRESH_RESP" | head -c 200)'"
                    fi
                    # 2) Program the Wi-Fi coprocessor directly via goform — the
                    # step that actually makes a BCO/scm box associate (the DB
                    # entry alone does not; live 2026-07-09 scm ST30). Runs on
                    # ALL chassis including taigan (this is taigan's working
                    # path); no-op on sm2 boxes without the :80 coprocessor.
                    # This is what lets the box fail over to the app-chosen
                    # network the moment the cable is pulled.
                    goform_wlan_push "$SSID" "$PASS"
                ) &
            fi
            WINNER="M0a-prelease"
        fi
    fi

    # ---- M_jukebox: on-box JukeBlox recon + GoForm + TAP (BCO/taigan) ----
    # BCO speakers (Portable=taigan, some ST20) run a SMSC/Microchip JukeBlox
    # DM870 Wi-Fi co-processor with its OWN GoAhead web server on :80. Its
    # /goform/aformHandlerConfigureProfileSettings handler is the one that
    # actually APPLIES a profile after the Bose cloud shutdown (the :8090
    # /addWirelessProfile path is accepted but never associates here: dead
    # MargeHSM). The desktop app drives that handler over the air from a PC on
    # the setup-AP; this block tries to drive it FROM THE BOX. That can only
    # work while the chipset is still in its OOB/setup-AP window (GoAhead binds
    # all ifaces incl loopback, but the server comes down once the box
    # associates). It is ALSO a recon pass: it dumps the box's exact network
    # state and which :80 candidates answer to the stick's setup.log, so an
    # unreachable channel is visible and we can iterate. Runs BEFORE M_air's
    # reboot, so a win here skips the disruptive reboot path; every existing
    # method stays as a fallback below. Heavy logging is intentional.
    if [ "$WINNER" = "none" ] && { [ -n "$IS_TAIGAN" ] || [ "$BCO_MODE" = "1" ]; }; then
        setup_log "M_jukebox: === BCO on-box recon + GoForm START (ssid='$SSID' src='${WLAN_SOURCE:-none}') ==="
        for _ji in lo eth0 wlan0; do
            [ -d "/sys/class/net/$_ji" ] || continue
            setup_log "M_jukebox recon: $_ji operstate=$(cat "/sys/class/net/$_ji/operstate" 2>/dev/null) carrier=$(cat "/sys/class/net/$_ji/carrier" 2>/dev/null) ip=$(ip -4 addr show "$_ji" 2>/dev/null | sed -n 's/.*inet \([0-9.]*\).*/\1/p' | tr '\n' ',')"
        done
        setup_log "M_jukebox recon: default-route = $(ip route 2>/dev/null | sed -n 's/^default .*/&/p' | tr '\n' ';' | head -c 200)"
        setup_log "M_jukebox recon: arp = $(cat /proc/net/arp 2>/dev/null | tr '\n' ';' | head -c 300)"
        setup_log "M_jukebox recon: listen-80 = $(netstat -ltn 2>/dev/null | grep ':80 ' | tr '\n' ';' | head -c 200)"
        setup_log "M_jukebox recon: setupState = $(wget -qO- -T 5 "$BOSE_API/setup" 2>/dev/null | tr -d '\r\n' | head -c 200)"
        setup_log "M_jukebox recon: networkInfo = $(wget -qO- -T 5 "$BOSE_API/networkInfo" 2>/dev/null | tr -d '\r\n' | head -c 300)"
        if command -v nc >/dev/null 2>&1; then
            setup_log "M_jukebox recon: tap-wifi-status = $(printf 'network wifi status\n' | nc -w 3 127.0.0.1 17000 2>/dev/null | tr '\r\n' '  ' | head -c 250)"
        fi

        # Locate the JukeBlox GoAhead :80 from the host: loopback, the
        # documented setup-AP gateway, and eth0's real default gateway.
        _jb_gw=$(ip route 2>/dev/null | sed -n 's/^default via \([0-9.]*\).*/\1/p' | head -1)
        JB_HIT=""
        for _jb in 127.0.0.1 192.168.1.1 ${_jb_gw:-x} 192.168.10.1 10.0.0.1; do
            [ "$_jb" = "x" ] && continue
            _jb_root=$(wget -qO- -T 5 "http://$_jb/" 2>/dev/null | tr -d '\r\n' | head -c 100)
            _jb_apjs=$(wget -qO- -T 5 "http://$_jb/setup/js/ap.js" 2>/dev/null | tr -d '\r\n' | head -c 60)
            setup_log "M_jukebox probe :80 @ $_jb -> root='${_jb_root:-none}' ap.js='${_jb_apjs:-none}'"
            { [ -n "$_jb_root" ] || [ -n "$_jb_apjs" ]; } && [ -z "$JB_HIT" ] && JB_HIT="$_jb"
        done

        if [ -n "$JB_HIT" ] && [ -n "$SSID" ] && [ -n "$PASS" ]; then
            # RFC-3986 url-encode (BusyBox-safe): keep unreserved, %XX the rest.
            _ue() {
                _ue_s="$1"; _ue_o=""
                while [ -n "$_ue_s" ]; do
                    _ue_c=${_ue_s%"${_ue_s#?}"}; _ue_s=${_ue_s#?}
                    case "$_ue_c" in
                        [a-zA-Z0-9._~-]) _ue_o="$_ue_o$_ue_c" ;;
                        *) _ue_o="$_ue_o$(printf '%%%02X' "'$_ue_c")" ;;
                    esac
                done
                printf '%s' "$_ue_o"
            }
            # Hidden-SSID note: the GoForm ConfigureProfileSettings form has
            # no known hidden-network field, so HIDDEN=1 is NOT threaded into
            # this path. Hidden-network support on BCO chassis is unverified
            # chip-side; do not guess undocumented form fields here.
            JB_BODY="ConfigManual=1&SSID=$(_ue "$SSID")&Passphrase=$(_ue "$PASS")&Key0=&Security=WPA2PSK&Cipher=CCMP&DHCPClient=1&IP=&Mask=&DefGW=&DNSSrv1=&DNSSrv2=&ProxyServer=&ProxyServerPort="
            setup_log "M_jukebox: POST goform -> http://$JB_HIT/goform/aformHandlerConfigureProfileSettings (Security=WPA2PSK Cipher=CCMP DHCP=1, body ${#JB_BODY}B)"
            JB_RESP=$(wget -qO- -T 25 \
                --header="Content-Type: application/x-www-form-urlencoded" \
                --header="Referer: http://$JB_HIT/" \
                --post-data="$JB_BODY" \
                "http://$JB_HIT/goform/aformHandlerConfigureProfileSettings" 2>&1)
            setup_log "M_jukebox: goform rc=$? resp='$(printf '%s' "$JB_RESP" | tr -d '\r\n' | head -c 200)' (a reset/timeout here is the EXPECTED setup-AP teardown)"
            persist_wlan_creds
            if wait_for_sta_lease 90; then
                WINNER="M_jukebox-goform"
                setup_log "M_jukebox: result=YES lease=$(current_sta_lease) via on-box GoForm @ $JB_HIT"
            else
                setup_log "M_jukebox: goform sent but no STA lease in 90s (wrong addr / chipset wants AP context / dead MargeHSM) -- falling through to M_air + M1"
            fi
        else
            setup_log "M_jukebox: no host-reachable GoAhead :80 (hit='${JB_HIT:-none}') or empty creds -- on-box GoForm impossible this boot; recon above shows why. Falling through to M_air + M1"
        fi

        # Flush the recon to the stick NOW: M_air below may reboot, and this
        # boot's trace must survive. One bulk copy is boot-safe (not per-line).
        if [ -d "$STICK" ] && [ -w "$STICK" ]; then
            cp "$SETUP_LOG_NAND" "$SETUP_LOG" 2>/dev/null && sync 2>/dev/null
        fi
        setup_log "M_jukebox: === END (winner=$WINNER) ==="
    fi

    # Hard guard: every block below mutates Bose state (POSTs to
    # /addWirelessProfile, runs `network wifi profiles clear`,
    # overwrites /etc/wpa_supplicant.conf, kills hostapd...). If
    # M0a won, none of them must run. The post-state + cleanup +
    # SUMMARY block at the very end of the WLAN section still runs
    # for both paths so the diagnostic bundle gets a uniform
    # summary line.
    if [ "$WINNER" = "none" ]; then
    # ---- M_air: AirplayConfiguration.xml profile (BCO primary path) ----
    # Write the WLAN profile into the file the box actually boots from,
    # then (on BCO) reboot once so BoseApp applies it. Runs BEFORE the
    # ~180s M0 BoseApp wait below because on BCO the HTTP path (M1) is
    # dead anyway. We ALSO write the file on non-BCO boxes as a logged
    # best-effort fallback, but do NOT reboot there here: rhino's
    # wpa_supplicant path (M3) provisions without a reboot, so a reboot
    # only fires as a last resort at the end if nothing produced a lease.
    # Skip M_air entirely when the box already carries a slot-0 profile
    # for this exact SSID: the WLAN is already provisioned, so rewriting
    # the file and rebooting achieves nothing and just adds a needless
    # reboot (plus the box's slow ~130s BoseApp re-init) every time a
    # provisioned stick is left inserted, #90 saw this as the
    # spotty "full white bar" on every boot. Only (re)write + reboot when
    # the profile is missing or for a different network.
    # Skip M_air entirely when the box is ALREADY provisioned for the
    # current creds: the creds fingerprint matches the reboot stamp (we
    # already wrote + rebooted for this exact SSID+PASS) AND the slot-0
    # profile carries that SSID. Then rewriting + rebooting achieves
    # nothing and just adds a needless reboot every time a provisioned
    # stick is left inserted (#90: spotty "full white bar"). Keyed
    # on the SSID+PASS fingerprint, NOT the SSID alone, so a password
    # change still re-provisions (stick stays a recovery tool).
    AIR_FP_NOW=$(airplay_creds_fp)
    AIR_FP_SEEN=$(cat "$AIR_REBOOT_STAMP" 2>/dev/null)
    if [ -n "$AIR_FP_NOW" ] && [ "$AIR_FP_NOW" = "$AIR_FP_SEEN" ] && [ "$(airplay_slot0_ssid)" = "$SSID" ]; then
        setup_log "M_air: already provisioned + rebooted for the current creds (SSID='$SSID', slot-0 matches), skipping M_air rewrite+reboot"
    else
    write_airplay_profile
    if [ "${AIR_WROTE:-}" = "1" ] && { [ "$BCO_MODE" = "1" ] || [ -n "${IS_TAIGAN:-}" ]; }; then
        if airplay_reboot_guard_ok; then
            setup_log "M_air: BCO chassis — profile written, rebooting once so BoseApp/BCONetworkServicesController applies it (skipping the dead addWirelessProfile path)"
            { printf 'SSID=%s\n' "$SSID"; printf 'PASS=%s\n' "$PASS"; if [ "$HIDDEN" = "1" ]; then printf 'HIDDEN=1\n'; fi; } > "$WLAN_CREDS_NAND.new" 2>/dev/null \
                && mv "$WLAN_CREDS_NAND.new" "$WLAN_CREDS_NAND" 2>/dev/null \
                && chmod 600 "$WLAN_CREDS_NAND" 2>/dev/null
            sync 2>/dev/null; sleep 1
            reboot
            exit 0
        else
            setup_log "M_air: BCO chassis — already rebooted for these creds (stamp match); not rebooting again, falling through to M1/M2"
        fi
    fi
    fi

    # Wait for BoseApp HTTP server up to 30s. M1 needs it; M2..M6
    # do not and run regardless. If BoseApp never comes up we still
    # try TAP CLI / wpa_supplicant / wpa_cli paths.
    # BoseApp cold-start time is highly variable on Series-I/BCO boxes,
    # taigan (Portable) especially. Live bundles (2026-05-31) show it
    # binding :8090 only around 120-130 s on a cold factory-reset boot,
    # and even then answering /info slowly while the rest of the Bose
    # mesh is still coming up under boot-time CPU/IO load. The previous
    # 30 s window with a 2 s per-probe timeout declared BoseApp "not
    # reachable" while it was merely late/slow, so M1 was skipped. On
    # taigan M1 is the ONLY working WLAN path (M2..M6 all SKIP), so the
    # box then sat in OOB with the boot progress bar stuck full and the
    # speaker never joined Wi-Fi. Wait far longer, give each probe room
    # to answer, and require a real <info body (not just a TCP accept)
    # so we never fire M1 against a half-initialised BoseApp.
    #
    # Variant-aware: on taigan/BCO where M1 is the only path, wait up to
    # 180 s. On boxes that have wpa_supplicant / TAP fallbacks (M2..M6),
    # keep the window short so a genuinely dead BoseApp drops through to
    # those paths quickly instead of stalling the whole pipeline.
    BOSE_WAIT_MAX=45
    if [ -n "$IS_TAIGAN" ] || [ "$BCO_MODE" = "1" ]; then
        BOSE_WAIT_MAX=180
    fi
    _bose_start=$(uptime_s)
    case "$_bose_start" in ''|*[!0-9]*) _bose_start=0 ;; esac
    setup_log "M0: waiting for BoseApp on $BOSE_API (timeout ${BOSE_WAIT_MAX}s, per-probe 8s, taigan=${IS_TAIGAN:-0} bco=${BCO_MODE:-0})"
    BOSE_OK=""
    _bose_last_log="$_bose_start"
    while :; do
        _bose_now=$(uptime_s)
        case "$_bose_now" in ''|*[!0-9]*) _bose_now=$((_bose_start + BOSE_WAIT_MAX)) ;; esac
        _bose_elapsed=$((_bose_now - _bose_start))
        [ "$_bose_elapsed" -ge "$BOSE_WAIT_MAX" ] && break
        if wget -qO- -T 8 "$BOSE_API/info" 2>/dev/null | grep -q "<info "; then
            setup_log "M0: BoseApp reachable after ${_bose_elapsed}s"
            BOSE_OK=1
            break
        fi
        if [ $((_bose_now - _bose_last_log)) -ge 30 ]; then
            setup_log "M0: still waiting for BoseApp /info, ${_bose_elapsed}s elapsed (taigan BoseApp can take ~130s to answer)"
            _bose_last_log="$_bose_now"
        fi
        sleep 3
    done
    [ -z "$BOSE_OK" ] && setup_log "M0: BoseApp did not respond within ${BOSE_WAIT_MAX}s, will skip BoseApp-dependent methods"

    if [ "$BOSE_OK" = "1" ]; then
        NETINFO_BEFORE=$(wget -qO- -T 3 "$BOSE_API/networkInfo" 2>/dev/null | head -c 400)
        setup_log "pre-state: $NETINFO_BEFORE"
    fi

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
    #
    # Live-verified 2026-05-29 on a freshly-factory-reset taigan
    # Portable: NetManager rejects /addWirelessProfile in OOB state
    # until TWO preconditions are met, the same ones the Bose iOS app
    # walks through during initial setup:
    #
    #   1. /language   — POST <sysLanguage>N</sysLanguage>
    #      Advances /setup systemstate from SETUP_LANG_NOT_SET to
    #      SETUP_LANG_SET. Without this, /addWirelessProfile returns
    #      HTTP 500 immediately. Verified by the on-box display: after
    #      this POST the "download the SoundTouch app" rolling-language
    #      splash stops, and the box settles on the language picked.
    #
    #   2. /setMargeAccount — POST PairDeviceWithAccount XML
    #      Sets margeAccountUUID (initially empty after factory reset).
    #      Without this, /addWirelessProfile gets accepted by Allegro
    #      but NetManager silently never processes the IPC, so Allegro
    #      eventually emits a 1046 ALLEGROWEBSERVER_TIMEOUT after ~2 min.
    #      The same XML STR's autopair sends on a working box; here we
    #      send a placeholder account because the real autopair runs
    #      downstream once the box is on the LAN.
    #
    # Then /performWirelessSiteSurvey wakes the radio (matches the
    # Bose webpage's ap.js getNetworks() call before submitForm), and
    # finally /addWirelessProfile with the Bose-original body shape
    # (PascalCase root, <profile/> with ssid/password/securityType
    # attributes, content-type text/xml).
    #
    # With both gates open, NetManager actually processes the call,
    # tears down the setup-AP, and associates to the target SSID.
    # The HTTP response is typically a TCP RST (~16 s in) because the
    # setup-AP loopback dies during the WLAN switch — so we treat RST
    # / connection-closed as success and rely on the STA-lease poll
    # rather than the HTTP body for confirmation.
    if [ "$BOSE_OK" = "1" ]; then
        # Gate 1: language. NetManager rejects /addWirelessProfile
        # while /setup systemstate is SETUP_LANG_NOT_SET. Any NON-ZERO
        # sysLanguage clears that state; sysLanguage=0 is the factory
        # default and a NO-OP for the gate (live-verified 2026-05-30 on
        # a factory-reset taigan: /language returns 0 AND systemstate
        # stays NOT_SET, so a digit merely being present never meant
        # the gate was open). We therefore key off systemstate, the
        # authoritative gate signal, not off /language's value.
        #
        # The posted value ALSO becomes the box's on-screen display
        # language, so we resolve it from the USER's locale via
        # resolve_setup_language (lang.conf -> NAND -> region country
        # -> English=3 default) instead of forcing one language on every
        # box worldwide. The full enum 1..25 is resolved now
        # ([[bose-language-enum]]), so the v0.5.16 "radio now speaks
        # Finnish/Swedish" incident (#60, a blind sysLanguage=1 guess)
        # cannot recur. If the gate is already open (prior provisioning,
        # prior factory life, or a non-OOB box) we skip the POST and
        # preserve whatever language the user already had.
        setup_log "M1: gate-1 GET $BOSE_API/setup"
        SETUP_GET=$(wget -qO- -T 5 "$BOSE_API/setup" 2>&1)
        setup_log "M1: setup GET rc=$? response='$(echo "$SETUP_GET" | head -c 160)'"
        LANG_NEED_POST=1
        case "$SETUP_GET" in
            *"systemstate=\"SETUP_LANG_NOT_SET\""*) LANG_NEED_POST=1 ;;
            *"systemstate="*) LANG_NEED_POST="" ;;
        esac
        if [ -n "$LANG_NEED_POST" ]; then
            M1_LANG=$(resolve_setup_language)
            setup_log "M1: gate-1 POST $BOSE_API/language sysLanguage=$M1_LANG (resolved from user locale / region, non-zero gate opener)"
            LANG_RESP=$(wget -qO- -T 5 --header="Content-Type: text/xml" \
                   --post-data="<sysLanguage>${M1_LANG}</sysLanguage>" \
                   "$BOSE_API/language" 2>&1)
            setup_log "M1: language POST rc=$? response='$(echo "$LANG_RESP" | head -c 160)'"
        else
            setup_log "M1: gate-1 skipped, systemstate already past SETUP_LANG_NOT_SET, preserving user locale"
        fi

        # Gate 2: marge account — SKIPPED by default (accountless).
        #
        # Jens (direct testimony 2026-05-31) plus live SSH analysis on a
        # taigan Portable: the current Bose iOS app onboards the speaker
        # with NO account at all (the account prompt was dropped in a
        # later app version). STR's setMargeAccount does not persist
        # anyway — /info echoes the supplied UUID, then clears it within
        # ~2s — and /addWirelessProfile returns HTTP 500 whether or not
        # the account is set. The account is validated against
        # streaming.bose.com, which /etc/hosts redirects to STR's own
        # marge stub, so forcing a bogus account only drives BoseApp's
        # MargeHSM down a validation path it then fails; at best a no-op,
        # at worst what wedges provisioning. We provision accountless
        # like the iOS app. STR's real autopair still runs downstream
        # once the box is on the LAN. The legacy gate (single POST, body
        # captured for the MargeHSM rejection reason) stays available
        # behind STR_FORCE_MARGE_GATE=1 for A/B debugging.
        if [ "${STR_FORCE_MARGE_GATE:-0}" = "1" ]; then
            MARGE_BODY='<?xml version="1.0" encoding="UTF-8" ?><PairDeviceWithAccount><accountId>stick-bootstrap</accountId><userAuthToken>stick-bootstrap</userAuthToken><accountEmail>stick@local</accountEmail></PairDeviceWithAccount>'
            _m1_marge_body_file=/tmp/m1-marge.body
            rm -f "$_m1_marge_body_file" 2>/dev/null
            setup_log "M1: gate-2 (forced via STR_FORCE_MARGE_GATE) POST $BOSE_API/setMargeAccount"
            MARGE_RESP=$(wget -O "$_m1_marge_body_file" -T 10 --header="Content-Type: application/xml" \
                   --post-data="$MARGE_BODY" "$BOSE_API/setMargeAccount" 2>&1)
            _m1_marge_rc=$?
            _m1_marge_body=$(head -c 512 "$_m1_marge_body_file" 2>/dev/null | tr '\r\n' '  ')
            setup_log "M1: gate-2 (forced) rc=$_m1_marge_rc stderr='$(echo "$MARGE_RESP" | head -c 200)' body='$_m1_marge_body'"
        else
            setup_log "M1: gate-2 SKIPPED — accountless provisioning (iOS app sets no account; marge UUID does not persist and addWirelessProfile 500s regardless). Set STR_FORCE_MARGE_GATE=1 to re-enable the legacy gate."
        fi

        # Gate 3: /name. Live-verified 2026-05-30 on a factory-reset
        # taigan Portable that this POST is the difference between
        # LED-stays-yellow and LED-goes-white after the WLAN switch.
        # The Bose iOS app posts a name unconditionally at this step.
        # We use NAME from name.conf if the stick provided one; else
        # we re-post whatever /name currently returns so we still
        # trip the gate without changing the user-visible value.
        NAME_FROM_STICK=""
        if [ -f "$STICK/name.conf" ]; then
            NAME_FROM_STICK=$(sed -n 's/.*"name":"\([^"]*\)".*/\1/p' "$STICK/name.conf" | head -1)
        fi
        if [ -z "$NAME_FROM_STICK" ]; then
            NAME_FROM_BOX=$(wget -qO- -T 5 "$BOSE_API/name" 2>/dev/null \
                | sed -n 's:.*<name>\([^<]*\)</name>.*:\1:p' | head -1)
            NAME_CHOICE="${NAME_FROM_BOX:-SoundTouch}"
            setup_log "M1: gate-3 no name on stick, re-posting existing /name='$NAME_CHOICE' to fire gate"
        else
            NAME_CHOICE="$NAME_FROM_STICK"
            setup_log "M1: gate-3 using name from stick name.conf='$NAME_CHOICE'"
        fi
        NAME_ESC=$(xml_escape "$NAME_CHOICE" 2>/dev/null || printf '%s' "$NAME_CHOICE")
        NAME_RESP=$(wget -qO- -T 5 --header="Content-Type: text/xml" \
               --post-data="<name>${NAME_ESC}</name>" \
               "$BOSE_API/name" 2>&1)
        setup_log "M1: gate-3 POST $BOSE_API/name rc=$? response='$(echo "$NAME_RESP" | head -c 160)'"

        # Survey wakes the radio; matches the Bose webpage's getNetworks().
        SURVEY_BODY='<PerformWirelessSiteSurvey timeout="5"/>'
        setup_log "M1: survey POST $BOSE_API/performWirelessSiteSurvey"
        _m1_survey_body_file=/tmp/m1-survey.body
        rm -f "$_m1_survey_body_file" 2>/dev/null
        SURVEY_RESP=$(wget -O "$_m1_survey_body_file" -T 12 --header="Content-Type: text/xml" \
               --post-data="$SURVEY_BODY" "$BOSE_API/performWirelessSiteSurvey" 2>&1)
        _m1_survey_rc=$?
        _m1_survey_body=$(head -c 512 "$_m1_survey_body_file" 2>/dev/null | tr '\r\n' '  ')
        setup_log "M1: survey rc=$_m1_survey_rc stderr='$(echo "$SURVEY_RESP" | head -c 240)' body='$_m1_survey_body'"

        # The final add. -T 30 because the response normally arrives as
        # an RST around the 16 s mark when NetManager tears down the
        # setup-AP loopback; we do not strictly need a clean response
        # body, the STA-lease poll below is authoritative. Same body
        # capture as gate-2: on spotty/scm this returns HTTP 500 and
        # we want NetManager's actual rejection reason rather than the
        # wget cover line.
        setup_log "M1: POST $BOSE_API/addWirelessProfile body='$(echo "$HTTP_BODY" | head -c 200)'"
        _m1_add_body_file=/tmp/m1-add.body
        rm -f "$_m1_add_body_file" 2>/dev/null
        RESP=$(wget -O "$_m1_add_body_file" -T 30 --header="Content-Type: text/xml" \
               --post-data="$HTTP_BODY" "$BOSE_API/addWirelessProfile" 2>&1)
        RC=$?
        _m1_add_body=$(head -c 512 "$_m1_add_body_file" 2>/dev/null | tr '\r\n' '  ')
        setup_log "M1: rc=$RC stderr='$(echo "$RESP" | head -c 400)' body='$_m1_add_body'"
        # Snapshot Bose state right after the call. BusyBox wget discards
        # 5xx bodies, so the authoritative signal for the accountless
        # experiment is what advanced: this tells a gate-rejection 500
        # (systemstate unchanged, wifiProfileCount still 0) apart from an
        # association failure (profile stored, count 1, but no lease).
        setup_log "M1: post-addProfile setup='$(wget -qO- -T3 "$BOSE_API/setup" 2>/dev/null | tr -d '\r\n' | head -c 120)' netinfo='$(wget -qO- -T3 "$BOSE_API/networkInfo" 2>/dev/null | tr -d '\r\n' | head -c 200)'"
        if [ $RC -eq 0 ] && echo "$RESP" | grep -qi "AddWirelessProfileResponse"; then
            setup_log "M1: API persisted profile (NetManager DB updated)"
        fi
        if wait_for_sta_lease 60; then
            RES=$(current_sta_lease)
            WINNER="M1-http"
            setup_log "M1: result=YES lease=$RES"
        else
            setup_log "M1: result=NO no real STA lease within 60s"
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
        # Belt-and-suspenders: even with M0a's prelease guard upstream,
        # re-probe NetManager's profile DB right here. If it already
        # contains anything (because M0a's lease detection missed,
        # e.g. the Bose stack had not reassociated within M0a's 60s
        # window but the stored profile is still good), refuse to run
        # the destructive `profiles clear`. Verified live on Series-I
        # scm-variant ST20 #60 where M2's clear wiped the working
        # profile and the box went orange-Wi-Fi forever.
        #
        # v0.5.17 source-priority: NetworkProfiles.xml on disk (most
        # reliable, available immediately) > TAP runtime info > nothing.
        # v0.5.16 used only TAP, which returns "<WiFiProfiles />"
        # (self-closing, empty) at cold boot even when the disk file
        # has an entry, so the guard misfired and M2 wiped the profile.
        M2_HAS_PROFILE=""
        M2_HAS_PROFILE_SRC=""
        _profile_xml="/mnt/nv/BoseApp-Persistence/1/NetworkProfiles.xml"
        if [ -s "$_profile_xml" ] && grep -q "<profile " "$_profile_xml" 2>/dev/null; then
            M2_HAS_PROFILE=1
            M2_HAS_PROFILE_SRC="file"
        fi
        if [ -z "$M2_HAS_PROFILE" ] && [ -n "$TAP_CMD" ]; then
            M2_PRE_INFO=$(printf 'network wifi profiles info\n' | nc -w 3 127.0.0.1 17000 2>/dev/null | tr '\n' ' ')
            case "$M2_PRE_INFO" in
                *"<profile "*|*"<profile>"*) M2_HAS_PROFILE=1; M2_HAS_PROFILE_SRC="tap" ;;
            esac
        fi
        if [ -n "$M2_HAS_PROFILE" ]; then
            setup_log "M2: SKIP reason=existing-profile-in-DB (src=$M2_HAS_PROFILE_SRC; 'profiles clear' would wipe the working profile)"
            # Wait a generous additional window for the existing
            # profile to associate. If it does we treat as M0a-late
            # and refuse to provision at all. If not, fall through
            # to M3..M6 which do not wipe the profile DB.
            if wait_for_sta_lease 90; then
                RES=$(current_sta_lease)
                WINNER="M0a-late"
                setup_log "M2: STA lease appeared during deferred wait — $RES (treating as already-on-wifi)"
            fi
        fi
    fi
    if [ "$WINNER" = "none" ] && [ -z "$IS_TAIGAN" ]; then
        if [ -n "$TAP_CMD" ]; then
            # H3 (untested hypothesis): `network mode wifisetup` as
            # explicit pre-state before `profiles add`. Documented in
            # samhobbs.co.uk and bosefirmware/Soundtouch-without-the-app
            # as a real TAP mode; STR has historically jumped straight
            # to `mode auto` after the add. On scm/spotty NetManager
            # may refuse `profiles add` unless first put into the
            # wifi-setup mode. Harmless on taigan (taigan skips M2
            # entirely upstream) and on sm2 (already accepts add in
            # the current pipeline). After the add we transition back
            # to `mode auto` as before.
            setup_log "M2: TAP CLI sequence (mode wifisetup, scan, profiles clear/add, info, setupap exit, mode auto)"
            TAP_OUT=$(
                (
                    printf 'async_responses on\n'
                    sleep 1
                    printf 'network mode wifisetup\n'
                    sleep 3
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
            # Hidden network: scan_ssid=1 makes wpa_supplicant send
            # SSID-specific probe requests; a hidden AP never carries the
            # SSID in its beacons so it is invisible to a broadcast scan.
            # The variable expands to an extra line INSIDE the single
            # network block (empty for normal networks).
            WPA_SCAN=""
            if [ "$HIDDEN" = "1" ]; then WPA_SCAN="
    scan_ssid=1"; fi
            cat > "$TMP" <<WPAEOF
ctrl_interface=DIR=/var/run/wpa_supplicant GROUP=root
update_config=1
eapol_version=1
ap_scan=1
fast_reauth=1
config_methods=virtual_display virtual_push_button keypad

network={
    ssid="$SSID"$WPA_SCAN
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
                # Hidden network: probe for the SSID directly (never beaconed).
                if [ "$HIDDEN" = "1" ]; then
                    wpa_cli -i "$_WI" set_network "$NETID" scan_ssid 1     >/dev/null 2>&1
                fi
                wpa_cli -i "$_WI" enable_network "$NETID"                  >/dev/null 2>&1; R4=$?
                wpa_cli -i "$_WI" select_network "$NETID"                  >/dev/null 2>&1; R5=$?
                wpa_cli -i "$_WI" save_config                              >/dev/null 2>&1; R6=$?
                setup_log "M4: set ssid=$R1 psk=$R2 key_mgmt=$R3 enable=$R4 select=$R5 save=$R6"
                if [ "$R1" = "0" ] && [ "$R2" = "0" ] && [ "$R4" = "0" ]; then
                    { printf 'SSID=%s\n' "$SSID"
                      printf 'PASS=%s\n' "$PASS"
                      if [ "$HIDDEN" = "1" ]; then printf 'HIDDEN=1\n'; fi
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
    fi  # end of "if [ "$WINNER" = "none" ]" guard around M0..M6

    # ${BOSE_OK:-} guard: BOSE_OK is only set inside the `if [ "$WINNER" = "none" ]`
    # block above. The M0a-prelease path skips that block entirely, so a bare
    # $BOSE_OK reference trips set -u and emits "BOSE_OK: unbound variable" to
    # stderr (observed live 2026-05-30 on a scm/spotty ST20 diagnostic).
    # BusyBox sh keeps going past the warning but the line is still a real bug.
    if [ "${BOSE_OK:-}" = "1" ] && [ "$WINNER" != "M0a-prelease" ]; then
        sleep 2
        NETINFO_AFTER=$(wget -qO- -T 3 "$BOSE_API/networkInfo" 2>/dev/null | head -c 400)
        setup_log "post-state: $NETINFO_AFTER"
    fi

    # Stick wlan.conf is kept on disk. See [[stick-is-recovery]]:
    # the stick MUST stay re-insertable as a credentials channel even
    # after a True Factory Reset has wiped NetworkProfiles.xml and
    # /mnt/nv/streborn/wlan-creds. Previous behaviour was to `rm` it
    # right here after one successful provisioning, which guaranteed
    # the next TFR+reboot left zero credential sources reachable from
    # the box (rhino ST10 diagnostic, 2026-05-30). M0a-prelease's
    # SSID-match short-circuit + M0a-refresh's password rotation
    # already handle the "same SSID, do nothing" case cheaply, so
    # carrying wlan.conf forward costs nothing and prevents a class
    # of unrecoverable Setup-AP traps.
    WLAN_T1=$(awk '{print int($1)}' /proc/uptime 2>/dev/null)
    WLAN_ELAPSED=$(( ${WLAN_T1:-0} - ${WLAN_T0:-0} ))
    FINAL_LEASE=$(current_sta_lease)
    FINAL_IFACE=${FINAL_LEASE%%|*}
    FINAL_IP=${FINAL_LEASE##*|}
    [ "$FINAL_IFACE" = "$FINAL_LEASE" ] && FINAL_IFACE="" && FINAL_IP=""
    setup_log "Approach SUMMARY: winner=$WINNER elapsed=${WLAN_ELAPSED}s iface=${FINAL_IFACE:-?} ip=${FINAL_IP:-none} bco=${BCO_MODE:-0} taigan=${IS_TAIGAN:-0} probes='wpa_cli=$HAS_WPA_CLI wpa_sup=$HAS_WPA_SUP nc=$HAS_NC tap=${TAP_CMD:+yes}'"
    # Persist wlan-creds for every real winner (any M1..M6 path), not
    # just M0a-prelease and M4-wpacli which had inline persist calls.
    # Without this, an M1 winner left wlan-creds untouched and a later
    # TFR left the box with no credential source at all.
    case "$WINNER" in
        none|ethernet-only) ;;
        *) persist_wlan_creds ;;
    esac

    # Last-resort AirplayConfiguration reboot for NON-BCO boxes: if no
    # method produced a lease but M_air wrote a profile, reboot once to
    # see if this firmware's AirPlay subsystem reads the file. rhino's
    # M3 (wpa_supplicant) normally wins WITHOUT a reboot, so this only
    # fires when the whole pipeline came up empty. Free fallback (Jens
    # 2026-06-01: "either it gets read or not"); the creds-stamp guard
    # makes it run at most once per credential set, so no boot loop.
    if [ "${AIR_WROTE:-}" = "1" ] && [ "$BCO_MODE" != "1" ] && [ -z "${IS_TAIGAN:-}" ] && [ -z "${FINAL_IP:-}" ]; then
        if airplay_reboot_guard_ok; then
            setup_log "last-resort: no STA lease and no method won; AirplayConfiguration profile written, rebooting once to try the AirPlay-config path"
            sync 2>/dev/null; sleep 1
            reboot
            exit 0
        else
            setup_log "last-resort: AirplayConfiguration written but already rebooted for these creds; not looping"
        fi
    fi

    # BCO OOB finalize: after the box joins Wi-Fi (M_air path wins via
    # M0a-prelease, which skips M1's /language + /name gates), the setup
    # state machine can stay at SETUP_LANG_NOT_SET and keep showing the
    # "download the app" hint. Run the gates in the background once
    # BoseApp is up. Self-gated on systemstate, harmless if already set.
    if [ "$BCO_MODE" = "1" ]; then
        ( finalize_oob_setup ) &
    fi

    # BCO cold-boot re-association watchdog (#157): on scm/spotty ST20 and other
    # BCO/eth0 boxes the SMSC Wi-Fi coprocessor intermittently fails to associate
    # on a cold boot (orange icon, no IP) even though a good profile is stored,
    # and the box self-heals only after a long delay. STR cannot drive
    # NetManager's associate, but goform_wlan_push re-PROGRAMS the coprocessor and
    # its GoAhead :80 handler stays up while unassociated. Nudge recovery WITHOUT
    # ever touching the stored profile: while there is no lease and we know the
    # creds, re-push the same known-good creds via goform on a slow cadence (a
    # no-op if :80 is down or the box is already fine). Self-exits the moment any
    # lease (Wi-Fi or ethernet) appears; bounded to ~12 min so it cannot spin
    # forever. Never runs `profiles clear` / wpa teardown.
    if [ "$BCO_MODE" = "1" ] && [ -n "$SSID" ] && [ -n "$PASS" ]; then
        (
            _rw=0
            while [ "$_rw" -lt 720 ]; do
                sleep 60
                _rw=$(( _rw + 60 ))
                if current_sta_lease >/dev/null 2>&1; then
                    setup_log "reassoc-watchdog: lease present at +${_rw}s, done"
                    break
                fi
                setup_log "reassoc-watchdog: no lease at +${_rw}s, non-destructive goform re-push"
                goform_wlan_push "$SSID" "$PASS"
            done
        ) &
    fi
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

# === Heal any placeholder SDK cloud URL left by a stick-free unlock ===
# A stick-free :17000 TAP unlock (desktop-app/telnet_enable_ssh.go) temporarily
# points the Bose SDK's cloud URLs at the placeholder host str-setup.invalid to
# fire an SSH injection, then restores them. If STR was installed via a path
# that skipped that restore, /mnt/nv/OverrideSdkPrivateCfg.xml still carries
# bmxRegistryUrl=https://str-setup.invalid, which NXDOMAINs, so the box can
# never reach the SoundTouch service registry and its light bar sweeps forever
# (live ST300 2026-07-07). Rewrite the placeholder inside the bmx/stats/marge
# tags back to the STOCK hostnames: STR only redirects the stock hosts
# (streaming.bose.com, content.api.bose.io) via the /etc/hosts bind mount above,
# so the box MUST carry the stock hostnames for that redirect to catch them.
# Best-effort + idempotent: the grep gate means a config with no placeholder is
# left untouched, and only tags whose value actually contains the placeholder
# are rewritten. The SDK already read the (bad) value earlier this boot, so this
# heal takes effect on the following boot; install.sh does the same before the
# post-install reboot so the very first boot is clean.
SDK_CFG="/mnt/nv/OverrideSdkPrivateCfg.xml"
if [ -f "$SDK_CFG" ] && grep -q 'str-setup\.invalid' "$SDK_CFG" 2>/dev/null; then
    if sed -e 's#\(<bmxRegistryUrl>\)[^<]*str-setup\.invalid[^<]*\(</bmxRegistryUrl>\)#\1https://content.api.bose.io/bmx/registry/v1/services\2#g' \
           -e 's#\(<statsServerUrl>\)[^<]*str-setup\.invalid[^<]*\(</statsServerUrl>\)#\1https://events.api.bosecm.com\2#g' \
           -e 's#\(<margeServerUrl>\)[^<]*str-setup\.invalid[^<]*\(</margeServerUrl>\)#\1https://streaming.bose.com\2#g' \
           "$SDK_CFG" > "$SDK_CFG.new" 2>/dev/null && mv "$SDK_CFG.new" "$SDK_CFG" 2>/dev/null; then
        setup_log "healed str-setup.invalid SDK cloud URLs in $SDK_CFG back to stock (redirect-catchable); effective next boot"
    else
        rm -f "$SDK_CFG.new" 2>/dev/null
        log "SDK cloud URL heal: sed/mv failed on $SDK_CFG (leaving it unchanged)"
    fi
fi

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
# See #60: `nc -vz <lan-ip> 8888` returns RST while `nc -vz <lan-ip>
# 8091` succeeds on an affected Series-I ST20, and the desktop's
# diagnostic-bundle field `reachable8888=false` co-occurs with a
# healthy agent bootstrap on the same bundle.
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
        if iptables -w -C INPUT -p tcp --dport "$port" \
            -m comment --comment "streborn-fw" -j ACCEPT 2>/dev/null; then
            continue  # rule already present, no-op
        fi
        if iptables -w -I INPUT 1 -p tcp --dport "$port" \
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
    # table. Probe via `iptables -w -nL INPUT` which is the cheapest
    # query that fails when the kernel module is absent or the table
    # is not yet attached.
    w=0
    while [ $w -lt 60 ]; do
        if iptables -w -nL INPUT >/dev/null 2>&1; then
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

# === Start the agent ===
# Presets live on NAND (read/write). The SD card is FAT32 and often throws
# an I/O error on writes, so the list is kept on NAND.
# First migration from the stick if NAND is still empty.
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

# Crash-visibility paths on NAND (survive the reboot that wipes /tmp,
# where $LOG lives). agent-crash.log gets the agent's own tail copied in
# only WHEN the agent exits, so a healthy long-running agent never writes
# here (no flash wear). agent-unstable.txt is the loud "the agent will
# not stay up" marker the watchdog raises after it gives up.
AGENT_CRASH_LOG="$PERSIST/logs/agent-crash.log"
AGENT_UNSTABLE_MARK="$PERSIST/logs/agent-unstable.txt"

# agent_resource_line: a compact RAM + NAND-free snapshot for the start
# and exit log lines, so an OOM kill (RAM gone) or a full NAND (write
# failures) is obvious in the bundle without a separate probe.
agent_resource_line() {
    _mf=$(awk '/^MemAvailable/{print $2"kB"}' /proc/meminfo 2>/dev/null)
    [ -z "$_mf" ] && _mf=$(awk '/^MemFree/{print $2"kB(MemFree)"}' /proc/meminfo 2>/dev/null)
    _nf=$(df -k /mnt/nv 2>/dev/null | tail -1 | awk '{print $(NF-2)"kB("$(NF-1)")"}')
    echo "memAvail=${_mf:-?} nandFree=${_nf:-?}"
}

# redeploy_agent_from_stick: self-heal for a crash-looping agent. When the NAND
# binary fast-respawns past the watchdog cap AND the recovery stick is still
# plugged in, recover the binary straight from the stick: a corrupt/short NAND
# copy (md5 != stick) is overwritten with the known-good stick binary. An intact
# copy is left as-is — the fault is then environmental (OOM / full NAND) and a
# re-copy cannot fix it. (A bound-but-slow agent is NOT treated as broken: it
# binds :8888 long before it answers /api/agent/version under boot load.) Runs at most once
# per boot (the /tmp flag clears on reboot, so a wedged box self-heals on the
# next boot without looping). Returns 0 only if it actually rewrote the binary,
# so the caller knows a fresh restart is worth attempting. "Stick is recovery."
# Self-heal budget: a per-boot guard (/tmp, clears on reboot) stops looping
# within one boot; a persistent NAND counter caps the lifetime total so an
# unsupported or unrecoverable chassis cannot redeploy on every single boot.
SELFHEAL_BOOT_FLAG="/tmp/str-selfheal-done"
SELFHEAL_COUNT="$PERSIST/logs/selfheal-count"
SELFHEAL_MAX=3
redeploy_agent_from_stick() {
    [ -f "$SELFHEAL_BOOT_FLAG" ] && return 1
    : > "$SELFHEAL_BOOT_FLAG"
    _shn=$(cat "$SELFHEAL_COUNT" 2>/dev/null || echo 0)
    case "$_shn" in ''|*[!0-9]*) _shn=0 ;; esac
    if [ "$_shn" -ge "$SELFHEAL_MAX" ]; then
        setup_log "self-heal: lifetime cap reached ($_shn/$SELFHEAL_MAX), not redeploying again (likely unsupported chassis or unrecoverable fault; a fresh stick install resets it)"
        return 1
    fi
    if [ ! -f "$STICK_BIN" ]; then
        setup_log "self-heal: recovery stick not present ($STICK_BIN), cannot redeploy agent"
        return 1
    fi
    _shm5=$(md5sum "$STICK_BIN" 2>/dev/null | awk '{print $1}')
    _nhm5=$(md5sum "$CACHED_BIN" 2>/dev/null | awk '{print $1}')
    if [ -n "$_shm5" ] && [ "$_shm5" = "$_nhm5" ]; then
        setup_log "self-heal: NAND binary already matches stick ($_nhm5); fault is not a corrupt binary (OOM / full NAND?), redeploy skipped"
        return 1
    fi
    setup_log "self-heal: redeploying agent from stick (nand=${_nhm5:-none} -> stick=$_shm5)"
    if cp "$STICK_BIN" "$CACHED_BIN.heal" 2>/dev/null \
        && [ "$(md5sum "$CACHED_BIN.heal" 2>/dev/null | awk '{print $1}')" = "$_shm5" ]; then
        chmod +x "$CACHED_BIN.heal" 2>/dev/null
        mv "$CACHED_BIN.heal" "$CACHED_BIN" 2>/dev/null
        sync
        echo $((_shn+1)) > "$SELFHEAL_COUNT" 2>/dev/null
        setup_log "self-heal: agent binary recovered from stick, md5 now $_shm5 (attempt $((_shn+1))/$SELFHEAL_MAX)"
        return 0
    fi
    rm -f "$CACHED_BIN.heal" 2>/dev/null
    setup_log "self-heal: redeploy copy/verify FAILED, NAND left as-is"
    return 1
}

# Defense-in-depth for a permanently-unstartable agent. When the agent
# crash-loops (e.g. an illegal-instruction SIGILL on an old ST CPU built
# for the wrong ISA, issue #302) it never binds :8888, and the box can be
# left sitting at a full OOB / install progress bar. The on-box OOB gates
# (finalize_oob_setup) only run on the WLAN-provision path for BCO boxes,
# so a crash-looping-agent box never reaches them. This advances the Bose
# setup state machine here too, so a dead agent does not soft-brick an
# otherwise-usable speaker: BoseApp (:8090), SSH and UPnP still work, only
# STR's own webui is gone. Same /language + /name POSTs finalize_oob_setup
# makes (live-proven 2026-06-01). Self-gated on systemstate: a pure no-op
# on any box already out of OOB (which includes every box where the agent
# has ever come up). It NEVER reboots (could loop) and NEVER re-execs
# run-override.sh. Only ever called once the agent is declared unstable.
rescue_oob_display() {
    _ro_api="http://127.0.0.1:8090"
    _ro_state=$(wget -qO- -T 5 "$_ro_api/setup" 2>/dev/null)
    # Log the raw state so the next diagnostic bundle proves whether the
    # box was ever in the OOB state this rescue can act on (the stuck bar
    # on older firmware may be a SoftwareUpdate screen, not systemstate).
    setup_log "agent-unstable rescue: /setup state = $(printf '%s' "$_ro_state" | tr -d '\n\r' | cut -c1-200)"
    case "$_ro_state" in
        *SETUP_LANG_NOT_SET*) : ;;
        *) setup_log "agent-unstable rescue: systemstate already past OOB (or /setup unavailable), no display rescue needed"; return 0 ;;
    esac
    _ro_lang=$(resolve_setup_language)
    setup_log "agent-unstable rescue: box stuck in OOB with a dead agent, POSTing /language=$_ro_lang + /name to leave the setup screen"
    wget -qO- -T 5 --header="Content-Type: text/xml" --post-data="<sysLanguage>${_ro_lang}</sysLanguage>" "$_ro_api/language" >/dev/null 2>&1
    _ro_nm=""
    [ -f "$STICK/name.conf" ] && _ro_nm=$(sed -n 's/.*"name":"\([^"]*\)".*/\1/p' "$STICK/name.conf" | head -1)
    [ -z "$_ro_nm" ] && _ro_nm=$(wget -qO- -T 5 "$_ro_api/name" 2>/dev/null | sed -n 's:.*<name>\([^<]*\)</name>.*:\1:p' | head -1)
    [ -z "$_ro_nm" ] && _ro_nm="SoundTouch"
    _ro_nme=$(printf '%s' "$_ro_nm" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g' -e 's/"/\&quot;/g')
    wget -qO- -T 5 --header="Content-Type: text/xml" --post-data="<name>${_ro_nme}</name>" "$_ro_api/name" >/dev/null 2>&1
    setup_log "agent-unstable rescue: posted /language + /name; box should leave the OOB screen and run as a plain speaker despite the dead agent"
}

start_agent() {
    START_COUNT=$((${START_COUNT:-0}+1))
    # Fingerprint the binary on every launch: a corrupt / short NAND copy
    # ties a later SIGSEGV to a bad binary rather than the environment,
    # and the resource line catches an OOM/full-NAND start condition.
    setup_log "start_agent #$START_COUNT: bin=$BIN bytes=$(wc -c < "$BIN" 2>/dev/null) md5=$(md5sum "$BIN" 2>/dev/null | awk '{print $1}') $(agent_resource_line)"
    # The agent runs inside a supervisor subshell ONLY so the shell can
    # wait() for it and record the real exit code / signal. Without this
    # the watchdog just sees the PID vanish and has no idea WHY: a Markus-
    # style "agent gets a PID then dies in seconds" loop left an empty
    # /tmp log and no exit status at all. The subshell writes the REAL
    # binary PID to $PIDFILE (what the watchdogs kill -0), logs the exit
    # code/signal, RAM/NAND, the agent's own tail, and any kernel OOM
    # evidence to NAND, then exits so the watchdog respawns.
    #
    # log-level info, not warn: earlier builds passed warn and the
    # listener bring-up logs were suppressed, so a silent :8888 bind
    # failure left no signal in the bundle. info is loud enough without
    # tick-rate spam (autopair / zeroconf are bounded).
    (
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
        _bpid=$!
        echo "$_bpid" > "$PIDFILE"
        wait "$_bpid"
        _ec=$?
        case "$_ec" in
            12[89]|1[3-9][0-9]) _sig=$((_ec-128)); _why="killed by signal $_sig (132=SIGILL/illegal-instruction 134=SIGABRT/Go-fatal 137=SIGKILL/OOM 139=SIGSEGV 143=SIGTERM); a SIGILL here means the agent binary used a CPU instruction this SoundTouch lacks (old ST unit without VFP) -> rebuild with GOARM=5" ;;
            2) _why="exit status 2 = Go runtime fatal (panic/throw, or a fatal signal such as SIGILL/illegal-instruction during os.init that the runtime reports then exits 2). On an older ST CPU this means the agent was built for an instruction set the box lacks -> rebuild with GOARM=5; see agent-crash.log for the Go traceback" ;;
            0) _why="clean exit (unexpected for a daemon)" ;;
            *) _why="exit status $_ec" ;;
        esac
        setup_log "agent EXITED (launch #$START_COUNT pid=$_bpid): code=$_ec, $_why; $(agent_resource_line)"
        # Persist the agent's own tail to NAND so the panic/fatal that the
        # /tmp $LOG loses on the next reboot is captured. Only on death.
        {
            echo "==== agent exit #$START_COUNT pid=$_bpid code=$_ec ($_why) $(date) ===="
            tail -n 40 "$LOG" 2>/dev/null
            echo ""
        } >> "$AGENT_CRASH_LOG" 2>/dev/null
        # An OOM / SIGKILL leaves nothing in the agent log; the kernel
        # ring buffer is the only witness. Grep it for the agent reap.
        _oom=$(dmesg 2>/dev/null | grep -iE 'out of memory|oom-kill|killed process|lowmemorykiller' | tail -3 | tr '\n' '|')
        [ -n "$_oom" ] && setup_log "agent EXIT kernel-oom evidence: $_oom"
    ) &
    AGENT_PID=$!   # supervisor PID; the real binary PID is in $PIDFILE
}

try_http_date_sync
start_agent
log "agent supervisor started (PID $AGENT_PID); binary PID written to $PIDFILE"

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
# sync_shim_to_nand is defined once near the top of this script (early
# shim-stage block); we only re-invoke it here. It used to be defined a
# SECOND time at this spot, an exact-duplicate definition the shell
# linter flagged (SC2218) that served no purpose, so it was removed; the
# single top-level definition covers both call sites.
sync_shim_to_nand

# === Bind-mount wrapper: hijack /opt/Bose/SoftwareUpdate non-destructively ===
#
# Previous attempt killed SoftwareUpdate AFTER Bose's init had
# already registered it in the internal IPC mesh, then re-launched
# under LD_PRELOAD. Result on Series-I: BoseApp /info returns 500
# until full power-cycle reboot. The mesh has no graceful re-
# registration path.
#
# Better: bind-mount a small wrapper script over Bose's binary path
# BEFORE Bose's init runs SoftwareUpdate. Bose's init then exec's
# /opt/Bose/SoftwareUpdate, the kernel sees `#!/bin/sh` on the
# wrapper (RAM-overlaid via mount --bind), runs the wrapper which
# in turn exec's the real binary with LD_PRELOAD set. The very
# first SoftwareUpdate process in the mesh has the shim active,
# accept() on :17008 is hijacked, no kill, no mesh disruption.
#
# Disable knob: `touch $PERSIST/state/shim-disable` to suppress the
# bind-mount + late swap on the next boot.

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

# Eligibility for the iptables PREROUTING REDIRECT path (external :8888
# reachability). It must cover EVERY chassis whose chipset blocks
# STR-owned ports, i.e. all BCO boxes, not just the ones detect_series_one
# catalogues by codename/moduleType. detect_series_one stays narrow on
# purpose (it ALSO gates the boot-hang-prone LD_PRELOAD shim, which must
# NOT run on an uncatalogued BCO box); the REDIRECT is harmless on any
# box (no-op where the chipset is permissive), so we widen it to include
# every BCO_MODE box: taigan, spotty, scm, has-bco, AND the structural
# eth0-only fallback for models not yet catalogued. Without this a
# fallback-detected BCO model provisions Wi-Fi via M_air but stays
# externally unreachable, so the desktop app never sees it as STR.
# Chipset-whitelist detection beyond detect_series_one's narrow codename/
# moduleType match. Some chassis report moduleType=sm2 yet carry the older
# SMSC2014 USB-Ethernet bridge (a SECOND networkInfo entry, type="SMSC") whose
# chipset whitelists only Bose-binary-bound listeners, so STR's :8888 is NOT
# externally reachable even though moduleType=sm2 and a wlan0 interface exists
# (so neither IS_SERIES_ONE nor BCO_MODE fired). Live 2026-06-17: a SoundTouch 10
# with moduleType=sm2 / variant=rhino but an SMSC networkInfo entry; the agent
# bound :8888 fine, yet the box's own self-probe to its LAN IP failed and the
# desktop app could not reach it. The "SMSC" marker (absent on a plain permissive
# sm2 box) is the discriminator; the universal SCM *component* is not. Widen
# REDIRECT_ELIGIBLE so the harmless, additive :17008 -> :8888 REDIRECT is
# installed (same-port REDIRECTs are no-ops on a permissive chipset, and the
# :17008 rule matches no traffic where :17008 is closed). This deliberately does
# NOT widen IS_SERIES_ONE: the LD_PRELOAD shim is boot-hang-prone on uncatalogued
# boxes and must stay gated on the narrow codename/moduleType match.
HAS_SMSC_CHIPSET=""
case "$(wget -qO- -T 3 http://127.0.0.1:8090/info 2>/dev/null)" in
    *SMSC*) HAS_SMSC_CHIPSET=1 ;;
esac

REDIRECT_ELIGIBLE=""
if [ "$IS_SERIES_ONE" = "1" ] || [ "$BCO_MODE" = "1" ] || [ "$HAS_SMSC_CHIPSET" = "1" ]; then
    REDIRECT_ELIGIBLE=1
fi
setup_log "redirect gate: eligible='${REDIRECT_ELIGIBLE:-0}' (is_series_one='${IS_SERIES_ONE:-0}' bco_mode='${BCO_MODE:-0}' smsc_chipset='${HAS_SMSC_CHIPSET:-0}')"

# ============================================================
# Cheap experiment for #90 (Series-I :8888 unreachable from LAN)
# ============================================================
# Hypothesis from a 2026-05-29 comment on #60: the scm chipset routes
# external TCP differently than internal. Self-connect to
# LAN_IP:8888 from the box itself works (proven by every spotty
# setup.log dump), but external clients on the same LAN get
# refused even though the listener is bound to 0.0.0.0:8888 and
# iptables INPUT ACCEPT is in place. That points at a chipset
# whitelist that filters by process identity rather than the
# kernel-level firewall.
#
# Cheap test path BEFORE the heavier LD_PRELOAD bind-mount shim:
# install an iptables PREROUTING REDIRECT rule for STR's listener
# ports. Inbound traffic to <LAN_IP>:PORT gets rewritten to
# 127.0.0.1:PORT before the chipset filter sees the destination,
# and STR's loopback listener accepts it.
#
#   - If the chipset filter is at the routing/socket-owner layer
#     (after iptables PREROUTING), REDIRECT bypasses it and the
#     port becomes externally reachable. WIN.
#   - If the chipset filter is in NIC hardware (before iptables),
#     REDIRECT does nothing and the box stays unreachable.
#     Still safe: PREROUTING rules are no-ops on traffic that
#     never arrives.
#
# Restricted to Series-I only because Series-II boxes already get
# external reachability via the normal listener bind. Repeats
# every 30 s same as the INPUT ACCEPT install.
REDIRECT_PORTS="8888 9080 8081 8443"
# Set to the comment match args only where the kernel's xt_comment
# module exists (probed below). Empty by default so the REDIRECT install
# never depends on a module that some Series-I kernels (taigan) lack.
RDR_COMMENT=""

# Probe whether the kernel nat table is available before attempting
# REDIRECT installs. v0.5.19 had the install code itself but every
# spotty bundle showed empty PREROUTING chain with no success log
# AND no error log, because `iptables -t nat -L` was silenced via
# 2>/dev/null. Most likely cause: iptable_nat kernel module not
# auto-loaded by Bose's init. Probe explicitly, log the stderr,
# and try a one-shot modprobe before giving up. Returns 0 if nat
# is usable, 1 otherwise.
iptables_nat_probe_and_modprobe() {
    NAT_OUT=$(iptables -w -t nat -L PREROUTING -n 2>&1)
    NAT_RC=$?
    if [ "$NAT_RC" = "0" ]; then
        setup_log "iptables nat table available (probe rc=0)"
        return 0
    fi
    setup_log "iptables nat table NOT available initially (probe rc=$NAT_RC, output='$(echo "$NAT_OUT" | tr '\n' ' ' | head -c 240)')"
    if [ -x /sbin/modprobe ] || [ -x /usr/sbin/modprobe ]; then
        MODPROBE_OUT=$(modprobe iptable_nat 2>&1)
        MODPROBE_RC=$?
        setup_log "modprobe iptable_nat rc=$MODPROBE_RC output='$(echo "$MODPROBE_OUT" | tr '\n' ' ' | head -c 240)'"
    else
        setup_log "modprobe binary not found, cannot load iptable_nat"
    fi
    NAT_OUT2=$(iptables -w -t nat -L PREROUTING -n 2>&1)
    NAT_RC2=$?
    if [ "$NAT_RC2" = "0" ]; then
        setup_log "iptables nat table available after modprobe (probe rc=0)"
        return 0
    fi
    setup_log "iptables nat table STILL unavailable after modprobe (probe rc=$NAT_RC2). REDIRECT cannot be installed on this firmware."
    return 1
}

# redirect_lan_ip resolves the box's real LAN IP for the REDIRECT rule.
# Self-contained ON PURPOSE: the REDIRECT install runs in its OWN
# backgrounded subshell (below), SEPARATE from the WLAN-provisioning
# subshell where current_sta_lease() is defined. A subshell does not
# inherit functions defined in a sibling subshell, so the REDIRECT path
# calling current_sta_lease got "command not found" -> empty -> the
# install bailed "current_sta_lease empty" for the lifetime of the box,
# even with eth0 + /networkInfo BOTH holding the lease. That is the
# long-standing taigan/spotty auto-REDIRECT failure (the box provisions
# Wi-Fi but the desktop app never sees STR), root-caused 2026-06-01.
# Resolve inline from the iface table first, then BoseApp /networkInfo
# (the BCO source). Prints "iface|ip", returns 1 if none.
redirect_lan_ip() {
    for _rif in eth0 wlan0 wlan1; do
        [ -d "/sys/class/net/$_rif" ] || continue
        _rip=$(ip -4 addr show "$_rif" 2>/dev/null | sed -n 's/.*inet \([0-9][0-9.]*\).*/\1/p' | head -1)
        case "$_rip" in
            ""|0.0.0.0|127.0.0.1|192.168.1.1|192.0.2.1|169.254.*) ;;
            *) printf '%s|%s' "$_rif" "$_rip"; return 0 ;;
        esac
    done
    _rip=$(wget -qO- -T 3 "http://127.0.0.1:8090/networkInfo" 2>/dev/null | sed -n 's/.*ipAddress="\([0-9][0-9.]*\)".*/\1/p' | head -1)
    case "$_rip" in
        ""|0.0.0.0|127.0.0.1|192.168.1.1|192.0.2.1|169.254.*) return 1 ;;
        *) printf 'eth0|%s' "$_rip"; return 0 ;;
    esac
}

iptables_install_redirect_series_one() {
    [ "$REDIRECT_ELIGIBLE" = "1" ] || return 0
    LEASE=$(redirect_lan_ip 2>/dev/null)
    # Bail-reason logging. A scm/spotty ST20 bundle 2026-05-30 showed
    # "iptables nat table available (probe rc=0)" followed by ZERO output
    # from this function across a 115s window where the watchdog must have
    # called it at least 4 times. The three silent `return 0` guards below
    # ate every call without trace. /tmp sentinel rate-limits the bail log
    # to one line per state, so a permanently-failing 30s watchdog does not
    # spam setup.log. Sentinels clear when state recovers so a transient
    # lease loss followed by recovery still produces a fresh entry log.
    if [ -z "$LEASE" ]; then
        if [ ! -f /tmp/.streborn-redirect-no-lease ]; then
            # Dump every source current_sta_lease consulted so a bundle
            # tells us WHY it came up empty: was eth0 really addressless,
            # did BoseApp /networkInfo answer, was the cache populated?
            _r_ni=$(wget -qO- -T 3 "http://127.0.0.1:8090/networkInfo" 2>/dev/null | tr -d '\r\n' | head -c 220)
            _r_eth=$(ip -4 addr show eth0 2>/dev/null | tr -d '\r' | tr '\n' ' ' | sed -n 's/.*\(inet [0-9.]*\).*/\1/p' | head -c 80)
            _r_cache=$(cat /tmp/.streborn-last-lease 2>/dev/null | head -c 60)
            setup_log "REDIRECT install: bail — current_sta_lease empty. eth0='${_r_eth:-none}' cache='${_r_cache:-none}' networkInfo='${_r_ni:-no-answer}'"
            touch /tmp/.streborn-redirect-no-lease 2>/dev/null
        fi
        return 0
    fi
    rm -f /tmp/.streborn-redirect-no-lease 2>/dev/null
    LANIP=${LEASE##*|}
    if [ -z "$LANIP" ] || [ "$LANIP" = "127.0.0.1" ]; then
        if [ ! -f /tmp/.streborn-redirect-bad-lanip ]; then
            setup_log "REDIRECT install: bail — LANIP='$LANIP' is empty or loopback (LEASE='$LEASE')"
            touch /tmp/.streborn-redirect-bad-lanip 2>/dev/null
        fi
        return 0
    fi
    rm -f /tmp/.streborn-redirect-bad-lanip 2>/dev/null
    if [ ! -f /tmp/.streborn-redirect-entered ]; then
        setup_log "REDIRECT install: enter LEASE='$LEASE' LANIP='$LANIP' ports='$REDIRECT_PORTS'"
        touch /tmp/.streborn-redirect-entered 2>/dev/null
    fi
    rc_redirect=0
    for port in $REDIRECT_PORTS; do
        # $RDR_COMMENT is "-m comment --comment streborn-redirect" only
        # where the xt_comment match module exists; empty otherwise.
        # taigan (live 2026-06-01) lacks xt_comment: any rule with
        # `-m comment` fails "No chain/target/match by that name", which
        # silently broke EVERY REDIRECT install (REDIRECT target itself is
        # fine). Unquoted so empty expands to nothing. -C idempotency
        # still works on the tuple without the cosmetic comment.
        if iptables -w -t nat -C PREROUTING -p tcp ! -i lo -d "$LANIP" --dport "$port" \
            $RDR_COMMENT -j REDIRECT --to-ports "$port" 2>/dev/null; then
            continue
        fi
        INS_OUT=$(iptables -w -t nat -I PREROUTING 1 -p tcp ! -i lo -d "$LANIP" --dport "$port" \
            $RDR_COMMENT -j REDIRECT --to-ports "$port" 2>&1)
        INS_RC=$?
        if [ "$INS_RC" = "0" ]; then
            setup_log "iptables nat PREROUTING REDIRECT tcp/$port -> loopback installed for $LANIP at uptime=$(uptime_s)s"
        else
            rc_redirect=$((rc_redirect + 1))
            # Only log the failure once per port per install pass to
            # avoid log spam from the 30s watchdog re-asserting against
            # a missing nat table for the lifetime of the box.
            setup_log "iptables nat PREROUTING REDIRECT tcp/$port FAILED rc=$INS_RC output='$(echo "$INS_OUT" | tr '\n' ' ' | head -c 240)'"
        fi
    done
    # BCO chassis whitelisted-port path. The Series-I chipset drops
    # external TCP to STR-owned :8888 (so the same-port REDIRECTs above
    # are no-ops there), but PASSES Bose-owned ports. Map the externally
    # reachable Bose port :17008 (SoftwareUpdate) to STR's loopback :8888
    # so the desktop app's probeSTR (which checks :8888 AND :17008) finds
    # STR and classifies the box correctly. Local :17008 stays
    # SoftwareUpdate (! -i lo). This is the shim-free replacement for the
    # boot-hang-prone LD_PRELOAD shim. Live-verified 2026-06-01 on a
    # taigan Portable: external :17008 returned STR JSON after this rule.
    # Gated on REDIRECT_ELIGIBLE (not BCO_MODE) so every Series-I chassis
    # whose chipset blocks :8888 gets it: taigan, spotty, scm AND the
    # eth0-only structural fallback. The function already returned early
    # for permissive Series-II boxes, so this is a no-op risk only where
    # :17008 is closed (harmless: the rule matches no traffic there).
    if [ "$REDIRECT_ELIGIBLE" = "1" ]; then
        if ! iptables -w -t nat -C PREROUTING -p tcp ! -i lo -d "$LANIP" --dport 17008 \
            $RDR_COMMENT -j REDIRECT --to-ports 8888 2>/dev/null; then
            BCO_OUT=$(iptables -w -t nat -I PREROUTING 1 -p tcp ! -i lo -d "$LANIP" --dport 17008 \
                $RDR_COMMENT -j REDIRECT --to-ports 8888 2>&1)
            BCO_RC=$?
            if [ "$BCO_RC" = "0" ]; then
                setup_log "iptables nat PREROUTING REDIRECT tcp/17008 -> loopback:8888 (BCO whitelisted-port path) installed for $LANIP at uptime=$(uptime_s)s"
            else
                rc_redirect=$((rc_redirect + 1))
                setup_log "iptables nat PREROUTING REDIRECT tcp/17008->8888 FAILED rc=$BCO_RC output='$(echo "$BCO_OUT" | tr '\n' ' ' | head -c 240)'"
            fi
        fi
    fi
    return $rc_redirect
}

# Scope-guard (C): the REDIRECT subshell below runs in its OWN ( ) &
# child and can only call functions defined at TOP LEVEL (a sibling
# subshell's functions are NOT inherited). If a future refactor moves
# one of these back inside another subshell, fail LOUDLY here instead
# of silently bailing inside the backgrounded subshell where the error
# is swallowed by 2>/dev/null — that silence is exactly what hid the
# original current_sta_lease subshell-scope bug for months.
for _need in setup_log redirect_lan_ip current_sta_lease iptables_install_redirect_series_one iptables_nat_probe_and_modprobe; do
    command -v "$_need" >/dev/null 2>&1 || \
        setup_log "FATAL scope-guard: '$_need' is not defined at top level before the REDIRECT subshell; it will be unavailable inside the backgrounded subshell. Define it at top level (see the current_sta_lease subshell-scope bug, 2026-06-01)."
done

(
    # First wait for an STA lease, then install. The LAN IP is
    # what the REDIRECT keys on, so without a lease there is no
    # destination to match. Loop forever to re-assert after any
    # NetManager flush AND re-discover the IP if DHCP rebinds.
    w=0
    while [ $w -lt 120 ]; do
        if [ -n "$(redirect_lan_ip 2>/dev/null)" ]; then
            break
        fi
        sleep 2
        w=$((w + 2))
    done
    if [ "$REDIRECT_ELIGIBLE" = "1" ]; then
        # Probe nat-table availability once at startup before the
        # watchdog loop. Logs the iptables -V output and whether
        # modprobe was needed and successful, so the next bundle
        # from a BCO/Series-I box says exactly why REDIRECT did or did
        # not land. If nat is permanently unavailable, the watchdog
        # still runs but the install function just logs failures
        # once per pass; the user will see "still unavailable" and
        # know this firmware needs the LD_PRELOAD shim path instead.
        IPTABLES_V=$(iptables -V 2>&1 | head -c 200)
        setup_log "iptables version: $IPTABLES_V"
        iptables_nat_probe_and_modprobe
        # Probe xt_comment availability once. Add a throwaway nat rule
        # with a comment; if it sticks, the module is present and we keep
        # the cosmetic comment, else we run comment-less (taigan).
        if iptables -w -t nat -A POSTROUTING -m comment --comment streborn-probe -j ACCEPT 2>/dev/null; then
            iptables -w -t nat -D POSTROUTING -m comment --comment streborn-probe -j ACCEPT 2>/dev/null
            RDR_COMMENT="-m comment --comment streborn-redirect"
            setup_log "iptables xt_comment present, REDIRECT rules will be labelled"
        else
            RDR_COMMENT=""
            setup_log "iptables xt_comment MISSING (e.g. taigan kernel), installing REDIRECT rules without -m comment"
        fi
        iptables_install_redirect_series_one
        while true; do
            sleep 30
            iptables_install_redirect_series_one
        done
    fi
) &

# SoftwareUpdate hijack logic: returns 0 when the local listener PID
# on :17008 really comes from a SoftwareUpdate process AND that
# process has already set our LD_PRELOAD env var. The
# log line emitted on every call lets a remote bundle answer "did
# the shim ever fully take hold" without staring at process-tree
# dumps for matching string fragments — particularly useful on
# scm/spotty where the SoftwareUpdate-real codepath may show but
# the LD_PRELOAD env may be absent (shepherdd re-exec without env).
shim_already_active() {
    SU_PID=$(pidof SoftwareUpdate-real 2>/dev/null | head -c 16 | awk '{print $1}')
    [ -n "$SU_PID" ] || SU_PID=$(pidof SoftwareUpdate 2>/dev/null | head -c 16 | awk '{print $1}')
    if [ -z "$SU_PID" ]; then
        setup_log "shim status check: no SoftwareUpdate process found"
        return 1
    fi
    if grep -qa "LD_PRELOAD=.*str-shim.so" "/proc/$SU_PID/environ" 2>/dev/null; then
        setup_log "shim status check: ACTIVE — PID=$SU_PID has str-shim.so in LD_PRELOAD"
        return 0
    fi
    setup_log "shim status check: NOT active — PID=$SU_PID exists but LD_PRELOAD does not contain str-shim.so"
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

# Status marker: the real swap logic lives in `shim_late_swap`
# (background, fires after Bose mesh stabilises). The Series-I
# distinction is purely advisory here — the bind-mount approach
# runs unconditionally on every box where staging produced a
# wrapper, because the late-swap is non-destructive on Series-II
# (the wrapper is invisible if /opt/Bose/SoftwareUpdate is never
# externally invoked). A 2026-05-30 rhino (Series-II ST10) bundle
# shows SoftwareUpdate-real running cleanly, proving the late-swap
# is safe on Series-II — so the previous "hijack DISABLED" message
# was wrong from the moment late-swap replaced kill+restart. Removed.
if [ -n "$IS_SERIES_ONE" ]; then
    setup_log "shim status: Series-I box (variant=${VARIANT:-?} host=${HOSTID:-?}) — late-swap is the only path to external :8888 reachability, follow shim_late_swap log lines for outcome"
else
    setup_log "shim status: Series-II box (chipset permissive, STR :8888 reachable directly) — late-swap still runs as a no-op safety net; if it succeeds the box gains :17008 as an additional reachable port"
fi

# One-shot diagnostic snapshot 90 s after start_agent. By then any
# fast respawn churn has settled and either :8888 is up or it never
# will be. The snapshot dumps listening sockets and process tree into
# setup.log (NAND-persisted, captured in full by the diagnostic),
# replacing the SSH session we cannot run on a user's box.
(
    sleep 90
    setup_log "=== one-shot post-start snapshot (uptime=$(uptime_s)s) ==="
    # Only the ports STR cares about, not the whole table. The full netstat/ss
    # dump appended tens of lines per boot to the NAND setup.log and the
    # diagnostic bundle for no diagnostic gain: the question is always "is :8888
    # bound and who else holds an STR/Bose port", which these filters answer.
    # STR: 8888/9080/8081/443. Bose: 8080/8090/8091/17008/17002/17000.
    _sock_ports=':8888 |:9080 |:8081 |:443 |:8080 |:8090 |:8091 |:17008 |:17002 |:17000 '
    if command -v ss >/dev/null 2>&1; then
        setup_log "listening sockets (ss -ltnp, STR/Bose ports only):"
        ss -ltnp 2>&1 | grep -E "$_sock_ports" | while IFS= read -r line; do setup_log "  $line"; done
    elif command -v netstat >/dev/null 2>&1; then
        setup_log "listening sockets (netstat -ltnp, STR/Bose ports only):"
        netstat -ltnp 2>&1 | grep -E "$_sock_ports" | while IFS= read -r line; do setup_log "  $line"; done
    else
        setup_log "listening sockets: ss and netstat both unavailable"
    fi
    # Process tree, STR + Bose-relevant lines only (was a full ps -ef every
    # boot). grep -v grep drops the matcher itself; head caps a runaway.
    setup_log "process tree (STR/Bose-relevant, ps -ef or busybox ps):"
    if ps -ef >/dev/null 2>&1; then
        ps -ef 2>&1 | grep -iE 'streborn|go-librespot|SoftwareUpdate|BoseApp|shepherdd|wpa_supplicant|sshd' | grep -v grep | head -40 | while IFS= read -r line; do setup_log "  $line"; done
    else
        ps 2>&1 | grep -iE 'streborn|go-librespot|SoftwareUpdate|BoseApp|shepherdd|wpa_supplicant|sshd' | grep -v grep | head -40 | while IFS= read -r line; do setup_log "  $line"; done
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
    # to a broken bind (see issue #60, 2026-05-28).
    setup_log "iptables filter INPUT:"
    iptables -w -L INPUT -n -v --line-numbers 2>&1 | while IFS= read -r line; do setup_log "  $line"; done
    setup_log "iptables nat PREROUTING:"
    iptables -w -t nat -L PREROUTING -n -v --line-numbers 2>&1 | while IFS= read -r line; do setup_log "  $line"; done
    # Try a localhost loopback connect to :8888 vs the LAN-IP connect.
    # When the two disagree (local ok, lan refused) the firewall is
    # the reason — the canonical #60 / Series-I scm/spotty pattern.
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
# Background: on slower ST20 variants reporters have observed that
# the box does reboot after the stick boot, but port 8888 never
# opens. The cause is not reproducible — suspicion falls on Bose's
# service manager (shepherdd / SCM), which during the first ~90s
# after boot runs its own cleanup work and under load drags our
# nohup process down with it, or on OOM kills (RAM pressure at
# boot). The existing 90s watchdog below catches that, but only
# AFTER the first cycle — so on a slow box the agent is dead for up
# to 90s, the user sees "Install failed" and gives up.
#
# This phase-A loop checks EVERY 5s for the first 120s whether (a)
# the agent PID is still alive AND (b) :8888 is actually bound.
# If either fails, restart immediately. Cost per check: 1 kill -0,
# 1 nc/ss lookup. With a stable agent no cp fires, no flash write.
# After 120s it hands over to the slow 90s watchdog (phase B).
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
    # bind call. Observed live in a #60 v0.5.5 setup_log at t=57s.
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
            # Bound is "good enough" here. The agent binds :8888 early but can
            # take a long time to answer /api/agent/version under boot load
            # (first-run CA gen, mDNS, the 5 s Bose /info wait, the shim swap):
            # observed >100 s live on taigan. Killing a bound-but-slow agent
            # only restarts its init and delays it, so we do NOT health-gate the
            # continue. A genuinely broken binary crash-loops DEAD/unbound (not
            # bound), and is recovered by the respawn path + the stick self-heal
            # at the fast-respawn cap below.
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
            # Last resort before declaring the agent unstable: if the NAND
            # binary is corrupt and the recovery stick is still in, recover it
            # from the stick and give the watchdog a fresh restart budget. The
            # /tmp once-per-boot guard keeps this from looping forever.
            if redeploy_agent_from_stick; then
                setup_log "boot-watchdog: self-healed agent from stick after $BOOT_RESTARTS fast respawns, resetting restart budget"
                BOOT_RESTARTS=0
                i=0
                continue
            fi
            # The agent will not stay up. Raise a loud, machine-readable
            # marker the diagnostic bundle (and a future app-side check)
            # can surface as a clear "agent unstable" error instead of a
            # silent install-failed. agent-crash.log already holds the
            # per-exit codes/signals that say WHY.
            {
                echo "agent unstable: $BOOT_RESTARTS fast respawns in 120s at $(date)"
                echo "last: pid=$CUR_PID alive=$ALIVE bound=$BOUND $(agent_resource_line)"
                echo "see agent-crash.log for the per-exit code/signal and the agent tail"
            } > "$AGENT_UNSTABLE_MARK" 2>/dev/null
            setup_log "boot-watchdog: $BOOT_RESTARTS restarts in 120s exhausted, agent UNSTABLE (marker written, $(agent_resource_line)), falling back to slow loop"
            # Defense-in-depth: the agent is definitively dead. If the box is
            # still sitting in OOB (full install/boot progress bar), advance
            # the Bose setup state off it so it is at least a usable plain
            # speaker. No-op if already out of OOB; never reboots (issue #302).
            rescue_oob_display
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
        setup_log "boot-watchdog: agent supervisor respawned (PID $AGENT_PID; binary PID in $PIDFILE)"
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
            break  # PID file gone, run.sh runs again
        fi
        CUR_PID=$(cat "$PIDFILE" 2>/dev/null || echo 0)
        if [ -n "$CUR_PID" ] && [ "$CUR_PID" -gt 0 ] && kill -0 "$CUR_PID" 2>/dev/null; then
            continue  # agent still running
        fi
        log "watchdog: agent (PID $CUR_PID) died, restarting"
        # Belt-and-suspenders: even though the agent has SO_REUSEADDR,
        # poll the listener ports to make sure no leftover process
        # holds the fd before we respawn. Caps at 30 s.
        wait_ports_clear 30
        start_agent
        log "watchdog: agent supervisor restarted (PID $AGENT_PID; binary PID in $PIDFILE)"
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
        mount --bind "$bundle" "$target" 2>/dev/null && echo "bind mount active: $bundle -> $target"
    done
fi

log "bootstrap complete"

# === Unmount the USB stick ===
# Bootstrap is done — all configs (wlan/region/name/presets/binary)
# have been copied to NAND. We no longer need the stick at runtime.
# We actively unmount it so the user can pull the stick during
# operation without a dirty FS (then Windows does not need to repair
# the FAT).
#
# SSH is deliberately NOT stopped — pre-1.0 we leave the channel
# open so the desktop app can pull its diagnostic bundle even with
# a broken agent (see ensure_sshd_running at the start of run.sh).
# The earlier logic explicitly stopped sshd here as soon as the
# agent was reachable on :8888, which closed the path to the logs
# on every later crash.
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
# Detached background block so run.sh can return immediately
# (shelby_local wants rc.local to finish quickly). Long wait +
# active check that no process still has the stick open before we
# actually unmount — otherwise we risk confusing the agent because
# its goroutines (syncRunOverride, initialBoxPresetSync) are still
# reading.
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
        exit 0  # already unmounted, nothing to do
    fi

    # Active check: who still holds a file open on the stick? We scan
    # /proc/*/fd/* for links pointing at $STICK. If someone is still
    # accessing it, we wait again — up to 90 s more. After that we do
    # not force the umount, but fall back to a read-only remount (flash
    # wear protection).
    STICK_DEV=$(mount | grep " $STICK " | awk '{print $1}')
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
    #
    # Multiple probe sources so a single failing primitive does not
    # produce a false "agent NOT bound" entry. spotty bundle
    # 2026-05-30 showed netstat reporting PID 2500 streborn-armv7l
    # LISTEN on 0.0.0.0:8888 while this probe still logged "agent NOT
    # bound" — the busybox /dev/tcp + nc -z primitives apparently
    # answered no during a momentary load spike. netstat is the
    # authoritative source: if the kernel says it's listening, it is.
    AGENT_OK=0
    if (echo > /dev/tcp/127.0.0.1/8888) >/dev/null 2>&1; then
        AGENT_OK=1
    elif command -v nc >/dev/null 2>&1 && nc -z 127.0.0.1 8888 >/dev/null 2>&1; then
        AGENT_OK=1
    elif netstat -ltn 2>/dev/null | grep -q ':8888 .*LISTEN'; then
        AGENT_OK=1
    elif pidof streborn-armv7l >/dev/null 2>&1; then
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
            && log "USB stick read-only remounted"
        exit 0
    fi
    if umount "$STICK" 2>/dev/null; then
        log "USB stick unmounted — safe to remove"
    else
        log "umount failed (a process holds the stick), trying read-only remount"
        if mount -o remount,ro "$STICK" 2>/dev/null; then
            log "USB stick read-only remounted"
        fi
    fi
) &
fi
