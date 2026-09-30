#!/bin/sh
# Functional test for run.sh's Wi-Fi provisioning verdict.
#
# Why this file exists. The repo's shell gates are static: shellcheck plus a
# busybox parse. Both pass happily on a verdict function that answers the wrong
# question, which is exactly the defect this tests for: every provisioning stage
# used to judge itself with "does the box hold an address", never "is that
# address on the network we were told to join". On a deliberate network change
# with the old AP still reachable, the first stage won instantly, the only stage
# that can program a rhino ST10 was skipped, and the speaker stayed on the old
# network for good while setup.log said it had succeeded.
#
# It also caught a second thing during development, which is the better argument
# for having it: a patch that converted the HANDS-OFF replay probe by mistake
# instead of the M_jukebox stage. That branch is deliberately hands-off (#270),
# and converting it would have started goform pushes against boxes that are
# already online. A static gate cannot see that; a test that asserts the
# no-SSID form still behaves the old way can.
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
check() {
    # $1 = description, $2 = expected rc, $3 = actual rc
    if [ "$2" = "$3" ]; then ok; else fail "$1 (want rc=$2, got rc=$3)"; fi
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

EXTRACTED="$HERE/.extracted.sh"
: > "$EXTRACTED"
for fn in current_sta_ssid forget_sta_lease lease_is_on_ssid wait_for_sta_lease wpa_globals_only; do
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

# ---- stubs, defined AFTER sourcing so they win -----------------------------
#
# STUB_LEASE empty means the box holds no address. STUB_SSID empty means
# nothing on the box can name the current network, which is the case that must
# stay lenient rather than strand a speaker.
STUB_LEASE=""
STUB_SSID=""
LOGGED=""

current_sta_lease() {
    [ -n "$STUB_LEASE" ] || return 1
    printf '%s' "$STUB_LEASE"
}
current_sta_ssid() {
    [ -n "$STUB_SSID" ] || return 1
    printf '%s' "$STUB_SSID"
}
setup_log() {
    LOGGED="$LOGGED
$1"
}
# Budgets are in real seconds in production; a test must not spend them.
sleep() {
    :
}
forget_sta_lease() {
    :
}

reset_case() {
    STUB_LEASE=""
    STUB_SSID=""
    LOGGED=""
    WLAN_VERDICT_UNVERIFIED=""
}

# ---- wait_for_sta_lease, with a wanted network ------------------------------

reset_case
STUB_LEASE="wlan0|10.0.0.5"; STUB_SSID="WantedNet"
wait_for_sta_lease 30 "WantedNet"; check "lease on the wanted network passes" 0 $?
[ -z "${WLAN_VERDICT_UNVERIFIED:-}" ] && ok || fail "a confirmed win must not be marked unverified"

# The core of the fix: an address on the OLD network is not a win.
reset_case
STUB_LEASE="wlan0|10.0.0.5"; STUB_SSID="OldNet"
wait_for_sta_lease 30 "WantedNet"; check "lease on a DIFFERENT network fails" 1 $?
[ -z "${WLAN_VERDICT_UNVERIFIED:-}" ] && ok || fail "a wrong-network verdict is a failure, not an unverified pass"

reset_case
wait_for_sta_lease 30 "WantedNet"; check "no lease at all fails" 1 $?

# Nothing can name the network: pass, but say so and mark the run, so the
# summary does not go on to re-rank the firmware's profiles.
reset_case
STUB_LEASE="eth0|10.0.0.9"; STUB_SSID=""
wait_for_sta_lease 30 "WantedNet"; check "unnameable network passes leniently" 0 $?
[ -n "${WLAN_VERDICT_UNVERIFIED:-}" ] && ok || fail "an unconfirmed win must be marked unverified"
case "$LOGGED" in
    *UNVERIFIED*) ok ;;
    *) fail "the unconfirmed verdict was not written to setup.log" ;;
esac
case "$LOGGED" in
    *WantedNet*) fail "the network NAME leaked into setup.log" ;;
    *) ok ;;
esac

# ---- wait_for_sta_lease, the no-SSID form ----------------------------------
#
# This is the hands-off replay probe. Its behaviour must be exactly what it
# always was: any lease is a pass, and nothing is ever marked unverified.

reset_case
STUB_LEASE="wlan0|10.0.0.5"; STUB_SSID="AnyOtherNet"
wait_for_sta_lease 30; check "no wanted network: any lease passes" 0 $?
[ -z "${WLAN_VERDICT_UNVERIFIED:-}" ] && ok || fail "the hands-off form must never mark a run unverified"

reset_case
STUB_LEASE="wlan0|10.0.0.5"; STUB_SSID=""
wait_for_sta_lease 30; check "no wanted network, unnameable: still passes" 0 $?

reset_case
wait_for_sta_lease 30; check "no wanted network, no lease: fails" 1 $?

# An empty second argument must behave like no argument, because that is what
# an unset SSID expands to at a call site.
reset_case
STUB_LEASE="wlan0|10.0.0.5"; STUB_SSID="AnyOtherNet"
wait_for_sta_lease 30 ""; check "empty wanted network behaves like the old form" 0 $?

# ---- names with awkward characters -----------------------------------------

reset_case
STUB_LEASE="wlan0|10.0.0.5"; STUB_SSID="My Net 2.4"
wait_for_sta_lease 30 "My Net 2.4"; check "a name with spaces matches exactly" 0 $?

reset_case
STUB_LEASE="wlan0|10.0.0.5"; STUB_SSID="My Net"
wait_for_sta_lease 30 "My Net 2.4"; check "a name that is a PREFIX of the wanted one fails" 1 $?

reset_case
STUB_LEASE="wlan0|10.0.0.5"; STUB_SSID='Weird$Net"x'
wait_for_sta_lease 30 'Weird$Net"x'; check "a name with a dollar and a quote matches" 0 $?

# ---- lease_is_on_ssid, the no-budget form used after M6's teardown ---------

reset_case
STUB_LEASE="wlan0|10.0.0.5"; STUB_SSID="WantedNet"
lease_is_on_ssid "WantedNet"; check "lease_is_on_ssid: match" 0 $?

reset_case
STUB_LEASE="wlan0|10.0.0.5"; STUB_SSID="OldNet"
lease_is_on_ssid "WantedNet"; check "lease_is_on_ssid: wrong network" 1 $?

reset_case
lease_is_on_ssid "WantedNet"; check "lease_is_on_ssid: no lease" 1 $?

reset_case
STUB_LEASE="eth0|10.0.0.9"; STUB_SSID=""
lease_is_on_ssid "WantedNet"; check "lease_is_on_ssid: unnameable stays lenient" 0 $?

reset_case
STUB_LEASE="wlan0|10.0.0.5"; STUB_SSID="Whatever"
lease_is_on_ssid ""; check "lease_is_on_ssid: no wanted network accepts any lease" 0 $?

# ---- current_sta_ssid's own parse, against a real wpa_cli status block ------
#
# The anchor matters: a status block contains bssid= and p2p_device_address=
# alongside ssid=, and an unanchored match picks up the wrong one.

STUBDIR=$(mktemp -d 2>/dev/null || echo "$HERE/.stubbin")
mkdir -p "$STUBDIR"
cat > "$STUBDIR/wpa_cli" <<'STUB'
#!/bin/sh
cat <<'OUT'
bssid=aa:bb:cc:dd:ee:ff
freq=5220
ssid=RealNet
id=0
mode=station
pairwise_cipher=CCMP
wpa_state=COMPLETED
ip_address=10.0.0.5
p2p_device_address=aa:bb:cc:dd:ee:00
OUT
STUB
chmod +x "$STUBDIR/wpa_cli"

# Restore the real reader for this one case and point it at the stub.
mkdir -p "$STUBDIR/sysfs/wlan0"
eval "$(extract current_sta_ssid)"
GOT=$(PATH="$STUBDIR:$PATH" STR_SYSFS_ROOT="$STUBDIR/sysfs" current_sta_ssid 2>/dev/null || true)
if [ "$GOT" = "RealNet" ]; then ok; else fail "wpa_cli status parse got '$GOT', want RealNet"; fi
rm -rf "$STUBDIR"

# ---- wpa_globals_only: the conf rewrite must keep the vendor globals -------
#
# The vendor conf carries thirteen global directives and NO network block. Any
# fixed preamble loses seven of them, including the box's WPS/P2P identity.

CONFDIR=$(mktemp -d 2>/dev/null || echo "$HERE/.confs")
mkdir -p "$CONFDIR"

cat > "$CONFDIR/vendor.conf" <<'VENDOR'
# Bose wpa_supplicant configuration
ctrl_interface=/var/run/wpa_supplicant
update_config=1
eapol_version=1
ap_scan=1
fast_reauth=1
disassoc_low_ack=1
driver_param=placeholder
device_name=SoundTouch
manufacturer=Bose Corporation
model_name=SoundTouch 10
model_number=placeholder
serial_number=placeholder
config_methods=virtual_push_button
VENDOR

GOT=$(wpa_globals_only "$CONFDIR/vendor.conf")
MISSING=""
for d in ctrl_interface update_config eapol_version ap_scan fast_reauth          disassoc_low_ack driver_param device_name manufacturer model_name          model_number serial_number config_methods; do
    case "$GOT" in
        *"$d="*) ;;
        *) MISSING="$MISSING $d" ;;
    esac
done
if [ -z "$MISSING" ]; then ok; else fail "globals dropped:$MISSING"; fi
case "$GOT" in
    *"# Bose wpa_supplicant configuration"*) ok ;;
    *) fail "the leading comment was dropped" ;;
esac
case "$GOT" in
    *"manufacturer=Bose Corporation"*) ok ;;
    *) fail "a preserved global lost its value" ;;
esac

# STR's own previous write: globals plus one network block. The block goes, the
# globals stay, so a second switch cannot accumulate dead networks.
cat > "$CONFDIR/prev.conf" <<'PREV'
ctrl_interface=DIR=/var/run/wpa_supplicant GROUP=root
update_config=1
device_name=SoundTouch

network={
    ssid="OldNet"
    psk="oldpassword"
    key_mgmt=WPA-PSK
    priority=10
}
PREV
GOT=$(wpa_globals_only "$CONFDIR/prev.conf")
case "$GOT" in
    *OldNet*|*oldpassword*) fail "the old network survived the rewrite" ;;
    *) ok ;;
esac
case "$GOT" in
    *device_name=SoundTouch*) ok ;;
    *) fail "a global was lost alongside the network block" ;;
esac

# Two blocks, a one-line block, and a cred block that is NOT ours to touch.
cat > "$CONFDIR/mixed.conf" <<'MIXED'
ap_scan=1
cred={
    realm="example"
}
network={
    ssid="A"
}
device_name=SoundTouch
network = {
    ssid="B"
}
fast_reauth=1
MIXED
GOT=$(wpa_globals_only "$CONFDIR/mixed.conf")
case "$GOT" in
    *'ssid="A"'*|*'ssid="B"'*) fail "a network block leaked into the globals" ;;
    *) ok ;;
esac
case "$GOT" in
    *'cred={'*) ok ;;
    *) fail "the cred block was dropped; it is not ours to remove" ;;
esac
case "$GOT" in
    *'realm="example"'*) ok ;;
    *) fail "the cred block lost its contents" ;;
esac
# The cred block's closing brace must survive, or the conf will not parse.
BRACES=$(printf '%s' "$GOT" | tr -cd '}' | wc -c | tr -d ' ')
if [ "$BRACES" = "1" ]; then ok; else fail "want exactly 1 closing brace kept, got $BRACES"; fi
case "$GOT" in
    *ap_scan=1*) case "$GOT" in *fast_reauth=1*) ok ;; *) fail "a global after the last block was lost" ;; esac ;;
    *) fail "a global before the first block was lost" ;;
esac

wpa_globals_only "$CONFDIR/does-not-exist.conf" >/dev/null 2>&1
check "an unreadable conf reports failure" 1 $?

wpa_globals_only "" >/dev/null 2>&1
check "an empty path reports failure" 1 $?

rm -rf "$CONFDIR"

# ---- result ----------------------------------------------------------------

if [ "$FAILED" -eq 0 ]; then
    echo "wlan verdict: $CASES checks, all passed"
    exit 0
fi
echo "wlan verdict: $CASES checks, $FAILED FAILED"
exit 1
