#!/bin/sh
# Functional test for install.sh's remove_rival_mod_leftovers: the install takes
# AfterTouch's NAND leftovers out itself, and touches nothing else on NAND.
#
# The function is cut straight out of install.sh and run against a sandbox.
# Runs under dash, busybox ash and bash.

set -u

HERE=$(dirname "$0")
INSTALL="$HERE/../install.sh"
[ -r "$INSTALL" ] || { echo "cannot read $INSTALL"; exit 1; }

FAILED=0
CASES=0
ok() { CASES=$((CASES + 1)); }
fail() {
    CASES=$((CASES + 1))
    FAILED=$((FAILED + 1))
    echo "FAIL: $1"
}

WORK=$(mktemp -d 2>/dev/null || echo "/tmp/str-rmc-test.$$")
mkdir -p "$WORK"
trap 'rm -rf "$WORK"' EXIT

FUNC=$(awk '
    /^remove_rival_mod_leftovers\(\) \{/ { inside = 1 }
    inside { print }
    inside && /^\}/ { exit }
' "$INSTALL")
if [ -n "$FUNC" ]; then ok; else fail "remove_rival_mod_leftovers could not be cut out of install.sh"; fi

# phase1_install must call it.
if awk '/^phase1_install\(\) \{/ { inside = 1 } inside && /remove_rival_mod_leftovers/ { found = 1 } inside && /^\}/ { exit } END { exit !found }' "$INSTALL"; then
    ok
else
    fail "phase1_install does not call remove_rival_mod_leftovers"
fi

eval "$FUNC"

# Case 1: a box with AfterTouch leftovers next to STR and Bose files.
NV="$WORK/nv1"
mkdir -p "$NV/aftertouch/bin" "$NV/streborn/bin" "$NV/BoseApp-Persistence/1"
echo x > "$NV/aftertouch/bin/aftertouch"
echo "nameserver 192.0.2.1" > "$NV/aftertouch.resolv.conf"
echo keep > "$NV/streborn/bin/streborn-armv7l"
echo keep > "$NV/BoseApp-Persistence/1/Sources.xml"
echo keep > "$NV/OverrideSdkPrivateCfg.xml"
echo keep > "$NV/rc.local"
OUT=$(remove_rival_mod_leftovers "$NV")
[ ! -e "$NV/aftertouch" ] && ok || fail "aftertouch/ was not removed"
[ ! -e "$NV/aftertouch.resolv.conf" ] && ok || fail "aftertouch.resolv.conf was not removed"
for f in streborn/bin/streborn-armv7l BoseApp-Persistence/1/Sources.xml OverrideSdkPrivateCfg.xml rc.local; do
    [ -f "$NV/$f" ] && ok || fail "$f was removed but must stay"
done
case "$OUT" in
    *"Removed AfterTouch leftovers: $NV/aftertouch"*) ok ;;
    *) fail "no log line for the removed directory: $OUT" ;;
esac

# Case 2: a clean box. Nothing changes, nothing is logged, exit status 0.
NV="$WORK/nv2"
mkdir -p "$NV/streborn"
echo keep > "$NV/streborn/presets.json"
OUT=$(remove_rival_mod_leftovers "$NV"); RC=$?
[ "$RC" = 0 ] && ok || fail "clean box: exit status $RC"
[ -z "$OUT" ] && ok || fail "clean box: unexpected output: $OUT"
[ -f "$NV/streborn/presets.json" ] && ok || fail "clean box: presets.json removed"

# Case 3: only the resolv override is left.
NV="$WORK/nv3"
mkdir -p "$NV"
echo "nameserver 192.0.2.1" > "$NV/aftertouch.resolv.conf"
remove_rival_mod_leftovers "$NV" >/dev/null
[ ! -e "$NV/aftertouch.resolv.conf" ] && ok || fail "resolv-only box: override not removed"

echo "rival_mod_cleanup_test: $CASES cases, $FAILED failed"
[ "$FAILED" = 0 ]
