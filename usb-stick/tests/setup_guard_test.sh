#!/bin/sh
# box_in_setup decides whether STR may touch a speaker's Wi-Fi at all.
#
# The rescue watchdog pushes the stored credentials at a speaker that has no
# lease, once a minute for twelve minutes, through the goform endpoint. That
# endpoint is the provisioning channel, so the push is exactly what ends a setup
# AP. A speaker the user has deliberately put into setup also has no lease, so
# until this guard existed STR fought the owner's own setup attempt once a
# minute, from inside the box.
#
# Getting this wrong in either direction is costly, which is why it is tested:
# too eager and a genuinely stranded speaker is never rescued, too lax and the
# speaker is dragged back out of setup.

set -u

HERE=$(dirname "$0")
RUNSH="$HERE/../run.sh"
[ -r "$RUNSH" ] || { echo "cannot read $RUNSH"; exit 1; }

FAILED=0
CASES=0
ok() { CASES=$((CASES + 1)); }
fail() { CASES=$((CASES + 1)); FAILED=$((FAILED + 1)); echo "FAIL: $1"; }
check() { if [ "$2" = "$3" ]; then ok; else fail "$1 (want rc=$2, got rc=$3)"; fi; }

extract() {
    awk -v fn="$1" '
        $0 == fn "() {"     { inside = 1 }
        inside              { print }
        inside && $0 == "}" { exit }
    ' "$RUNSH"
}

eval "$(extract box_in_setup)"

WORK=$(mktemp -d 2>/dev/null || echo "$HERE/.setupwork")
mkdir -p "$WORK/bin"
PATH="$WORK/bin:$PATH"
export PATH

# wget stands in for the box's own web server, ps for its process list.
set_now_playing() {
    cat > "$WORK/bin/wget" <<STUB
#!/bin/sh
printf '%s' '$1'
STUB
    chmod +x "$WORK/bin/wget"
}
set_processes() {
    cat > "$WORK/bin/ps" <<STUB
#!/bin/sh
cat <<'OUT'
$1
OUT
STUB
    chmod +x "$WORK/bin/ps"
}

NORMAL_PS='  123 root     /opt/Bose/NetManager --autoswitching=true
  456 root     /usr/sbin/wpa_supplicant -i wlan0'
SETUP_PS="$NORMAL_PS
  789 root     /usr/sbin/hostapd /tmp/hostapd.conf"

# ---- the firmware source, the answer that works on every chassis ----

set_processes "$NORMAL_PS"

set_now_playing '<nowPlaying deviceID="X" source="SETUP"><ContentItem/></nowPlaying>'
box_in_setup; check "source=SETUP is setup" 0 $?

set_now_playing '<nowPlaying deviceID="X" source="SETUP_INACTIVE"></nowPlaying>'
box_in_setup; check "the SETUP family counts too" 0 $?

set_now_playing '<nowPlaying deviceID="X" source="LOCAL_INTERNET_RADIO"></nowPlaying>'
box_in_setup; check "a playing speaker is not in setup" 1 $?

set_now_playing '<nowPlaying deviceID="X" source="STANDBY"></nowPlaying>'
box_in_setup; check "standby is not setup" 1 $?

# A stranded speaker: no lease, not in setup. This is the case the watchdog
# exists for and it must still be rescued.
set_now_playing '<nowPlaying deviceID="X" source="STANDBY"></nowPlaying>'
box_in_setup; check "a stranded speaker is still rescuable" 1 $?

# ---- the AP processes, the fallback for a chassis with no usable source ----

set_now_playing ''
set_processes "$SETUP_PS"
box_in_setup; check "hostapd running means setup" 0 $?

set_processes "$NORMAL_PS"
box_in_setup; check "no AP processes and no answer means not setup" 1 $?

for p in udhcpd dnsmasq nodogsplash; do
    set_processes "$NORMAL_PS
  790 root     /usr/sbin/$p"
    box_in_setup; check "$p running means setup" 0 $?
done

# grep -v grep: the grep that does the looking must not match itself, or every
# speaker on earth reads as being in setup and nothing is ever rescued.
set_processes "$NORMAL_PS"
box_in_setup; check "the search itself does not count as a hit" 1 $?

# ---- and that the watchdog actually ASKS ------------------------------------
#
# Testing the function alone is not enough: switching the call site off leaves
# every case above green while the speaker is dragged out of setup exactly as
# before. Pin the call, in the loop, before the push.

RESCUE=$(awk '/hands-off: no lease after 90s/,/^            fi$/' "$RUNSH")
case "$RESCUE" in
    *box_in_setup*) ok ;;
    *) fail "the rescue watchdog does not call box_in_setup, so nothing protects a speaker in setup" ;;
esac
case "$RESCUE" in
    *"goform_wlan_push"*) ok ;;
    *) fail "the rescue watchdog no longer pushes at all; this test is pinned to the wrong block" ;;
esac
# The guard has to come BEFORE the push, or it decides nothing.
GUARD_AT=$(printf '%s' "$RESCUE" | grep -n "box_in_setup" | head -1 | cut -d: -f1)
PUSH_AT=$(printf '%s' "$RESCUE" | grep -n "goform_wlan_push" | head -1 | cut -d: -f1)
if [ -n "$GUARD_AT" ] && [ -n "$PUSH_AT" ] && [ "$GUARD_AT" -lt "$PUSH_AT" ]; then
    ok
else
    fail "box_in_setup is checked after the push (guard=$GUARD_AT push=$PUSH_AT)"
fi

# Both timer-driven writers to the goform channel must ask. The second one was
# found by an audit after the first was fixed: same construct, same cadence,
# same budget, same single exit condition, no guard. Pin BOTH, by name, so a
# third one cannot be added quietly either.

for block in "hands-off: no lease after 90s" "BCO cold-boot re-association watchdog"; do
    LOOP=$(awk -v start="$block" 'index($0, start) { on = 1 } on { print } on && /^    fi$/ { exit }' "$RUNSH")
    case "$(printf '%s' "$LOOP" | grep -v "^[[:space:]]*#")" in
        *goform_wlan_push*) ok ;;
        *) fail "could not find the push loop under '$block'; this test is pinned to the wrong place" ;;
    esac
    case "$(printf '%s' "$LOOP" | grep -v "^[[:space:]]*#")" in
        *box_in_setup*) ok ;;
        *) fail "the loop under '$block' pushes at a speaker without asking whether it is in SETUP" ;;
    esac
    # Comments in this file explain goform_wlan_push by name, so look at code
    # lines only or the prose counts as a call and the order reads backwards.
    CODE=$(printf '%s' "$LOOP" | grep -v "^[[:space:]]*#")
    G=$(printf '%s' "$CODE" | grep -n "box_in_setup" | head -1 | cut -d: -f1)
    P=$(printf '%s' "$CODE" | grep -n "goform_wlan_push" | head -1 | cut -d: -f1)
    if [ -n "$G" ] && [ -n "$P" ] && [ "$G" -lt "$P" ]; then
        ok
    else
        fail "'$block' asks after it pushes (guard=$G push=$P)"
    fi
done

# Nothing else may push on a timer without asking. Counts the calls: two loops,
# plus the early one-shot and the failover seed, which are single deliberate
# pushes and not loops.
PUSHES=$(grep -v "^[[:space:]]*#" "$RUNSH" | grep -c "goform_wlan_push ")
if [ "$PUSHES" -le 6 ]; then
    ok
else
    fail "there are now $PUSHES goform_wlan_push call sites; a new one may need the same guard"
fi

rm -rf "$WORK"

if [ "$FAILED" -eq 0 ]; then
    echo "setup guard: $CASES checks, all passed"
    exit 0
fi
echo "setup guard: $CASES checks, $FAILED FAILED"
exit 1
