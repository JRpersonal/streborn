#!/bin/sh
# Functional test for the filters of run.sh's one-shot post-start snapshot.
#
# The snapshot writes listening sockets, a process list and the AirPlay syslog
# lines into setup.log, which is all a diagnostic bundle has in place of an SSH
# session. Each filter decides what survives. A port or process the filter drops
# is invisible in every bundle, which is how an ST10 that lost its AirPlay
# receiver produced a bundle with no trace of :7000 at all. These cases pin
# what must be kept and what must stay out, so the bundle stays small.
#
# Runs under dash, busybox ash and bash. No arrays, no [[ ]], no local.
#
# Deliberately NOT in usb-stick/ itself: files.go embeds *.sh from that
# directory onto every stick, and a test script has no business shipping to a
# speaker.

set -u

HERE=$(dirname "$0")
RUNSH="$HERE/../run.sh"
[ -r "$RUNSH" ] || { echo "cannot read $RUNSH"; exit 1; }

FAILED=0
CASES=0

ok() {
    CASES=$((CASES + 1))
}
fail() {
    CASES=$((CASES + 1))
    FAILED=$((FAILED + 1))
    echo "FAIL: $1"
}

# Pull the functions under test straight out of run.sh, so the test can never
# drift from the shipped code. Each is defined at column 0 and closed by a
# lone brace at column 0.
extract() {
    awk -v fn="$1" '
        $0 == fn "() {" { inside = 1 }
        inside          { print }
        inside && $0 == "}" { exit }
    ' "$RUNSH"
}

EXTRACTED="$HERE/.extracted_snapshot.sh"
: > "$EXTRACTED"
for fn in snapshot_socket_filter snapshot_process_filter snapshot_airplay_log_filter; do
    extract "$fn" >> "$EXTRACTED"
    if ! grep -q "^$fn() {" "$EXTRACTED"; then
        echo "FAIL: could not extract $fn from run.sh"
        rm -f "$EXTRACTED"
        exit 1
    fi
done

# shellcheck source=/dev/null
. "$EXTRACTED"
rm -f "$EXTRACTED"

# keeps FILTER INPUT NEEDLE DESC: the filter's output contains NEEDLE.
keeps() {
    if printf '%s\n' "$2" | "$1" | grep -qF -- "$3"; then ok; else fail "$4"; fi
}
# drops FILTER INPUT DESC: the filter's output is empty.
drops() {
    if [ -z "$(printf '%s\n' "$2" | "$1")" ]; then ok; else fail "$3"; fi
}

# ---- sockets ---------------------------------------------------------------

keeps snapshot_socket_filter \
    'tcp        0      0 0.0.0.0:7000            0.0.0.0:*               LISTEN      812/AirPlay' \
    ':7000 ' "netstat: AirPlay listener on :7000 kept"
keeps snapshot_socket_filter \
    'udp        0      0 0.0.0.0:5353            0.0.0.0:*                           640/mdnsd' \
    ':5353 ' "netstat: mDNS on udp :5353 kept"
keeps snapshot_socket_filter \
    'tcp   LISTEN 0      128          0.0.0.0:7000       0.0.0.0:*    users:(("AirPlay",pid=812,fd=9))' \
    ':7000 ' "ss: AirPlay listener on :7000 kept"
keeps snapshot_socket_filter \
    'tcp        0      0 :::8888                 :::*                    LISTEN      1201/streborn' \
    ':8888 ' "netstat: STR :8888 still kept"
keeps snapshot_socket_filter \
    'tcp        0      0 0.0.0.0:17008           0.0.0.0:*               LISTEN      455/SoftwareUpdate' \
    ':17008 ' "netstat: Bose :17008 still kept"
drops snapshot_socket_filter \
    'tcp        0      0 0.0.0.0:70001           0.0.0.0:*               LISTEN      1/x' \
    "a port that only starts with 7000 is dropped"
drops snapshot_socket_filter \
    'udp        0      0 0.0.0.0:1900            0.0.0.0:*                           700/upnp' \
    "an unrelated port is dropped"

# ---- processes -------------------------------------------------------------

keeps snapshot_process_filter '  812 root      2048 S    /opt/Bose/AirPlay' \
    'AirPlay' "AirPlay process kept"
keeps snapshot_process_filter '  813 root      1024 S    raopd --name x' \
    'raopd' "RAOP process kept"
keeps snapshot_process_filter '  640 root       512 S    /usr/sbin/mDNSResponder' \
    'mDNSResponder' "mDNSResponder kept, case-insensitive"
keeps snapshot_process_filter '  641 avahi      512 S    avahi-daemon: running' \
    'avahi-daemon' "avahi kept"
keeps snapshot_process_filter ' 1201 root      9000 S    /mnt/nv/streborn/streborn' \
    'streborn' "STR agent still kept"
drops snapshot_process_filter '  200 root       100 S    /sbin/udhcpc -i wlan0' \
    "an unrelated process is dropped"
drops snapshot_process_filter ' 3000 root       100 S    grep -iE airplay' \
    "the grep matcher itself is dropped"

MANY=""
i=0
while [ "$i" -lt 100 ]; do
    MANY="$MANY$i root 1 S BoseApp
"
    i=$((i + 1))
done
N=$(printf '%s' "$MANY" | snapshot_process_filter | wc -l)
if [ "$N" -eq 40 ]; then ok; else fail "process list capped at 40 lines (got $N)"; fi

# ---- AirPlay syslog --------------------------------------------------------

keeps snapshot_airplay_log_filter \
    'Jan  1 00:00:12 spotty user.err AirPlay[812]: failed to start receiver' \
    'failed to start receiver' "AirPlay error line kept"
keeps snapshot_airplay_log_filter \
    'Jan  1 00:00:12 spotty user.info mdnsd: register _airplay._tcp' \
    '_airplay._tcp' "_airplay service registration kept"
keeps snapshot_airplay_log_filter \
    'Jan  1 00:00:13 spotty user.warn raop: rtsp setup refused' \
    'rtsp setup refused' "RAOP line kept"
keeps snapshot_airplay_log_filter \
    'Jan  1 00:00:13 spotty user.err auth: MFi chip not answering' \
    'MFi chip' "MFi line kept"
drops snapshot_airplay_log_filter \
    'Jan  1 00:00:14 spotty user.info kernel: ramfile cache flushed' \
    "a word that merely contains mfi is dropped"
drops snapshot_airplay_log_filter \
    'Jan  1 00:00:14 spotty user.info BoseApp: volume 12' \
    "an unrelated syslog line is dropped"

LOG=""
i=0
while [ "$i" -lt 200 ]; do
    LOG="${LOG}line $i AirPlay tick
noise $i
"
    i=$((i + 1))
done
OUT=$(printf '%s' "$LOG" | snapshot_airplay_log_filter)
N=$(printf '%s\n' "$OUT" | wc -l)
if [ "$N" -eq 60 ]; then ok; else fail "AirPlay syslog capped at 60 lines (got $N)"; fi
if printf '%s\n' "$OUT" | tail -1 | grep -qF 'line 199 AirPlay'; then ok; else fail "AirPlay syslog keeps the LAST matches"; fi

LONG="AirPlay $(printf '%0400d' 0)"
W=$(printf '%s\n' "$LONG" | snapshot_airplay_log_filter | awk '{ print length($0) }')
if [ "$W" -eq 300 ]; then ok; else fail "AirPlay syslog line cut to 300 bytes (got $W)"; fi

echo "snapshot_filter_test: $CASES cases, $FAILED failed"
[ "$FAILED" -eq 0 ]
