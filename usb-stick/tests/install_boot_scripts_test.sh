#!/bin/sh
# Functional test for how install.sh puts the two boot scripts onto NAND
# (rc.local and run-override.sh): copy to a .new file, then rename into place.
#
# The plain `cp src dst` it replaces opens dst with O_TRUNC before reading a
# byte, so a copy cut short left a truncated, still-executable boot script, and
# on the SSH install the box is usually running those very scripts at that
# moment. A rename gives the destination a NEW inode and leaves the old one to
# whoever still has it open. That is what this test proves: a hard link taken
# to the old file before the install must still read the OLD content after it
# (with an in-place cp it would read the new one).
#
# The section is cut straight out of install.sh and run against a sandbox.
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

WORK=$(mktemp -d 2>/dev/null || echo "/tmp/str-ibs-test.$$")
mkdir -p "$WORK"
cleanup() { rm -rf "$WORK"; }

# The section from the rc.local copy up to the agent binary seeding.
SECTION=$(awk '
    index($0, "echo \"Copying $RC_SRC to $RC_DST\"") { inside = 1 }
    index($0, "# Seed the NAND binary cache")        { exit }
    inside                                            { print }
' "$INSTALL")
if [ -n "$SECTION" ]; then ok; else fail "the boot-script section could not be cut out of install.sh"; fi

# Both copies must rename into place, not cp over the live file.
case "$SECTION" in
    *'mv -f "$RC_DST.new" "$RC_DST"'*) ok ;;
    *) fail "rc.local is not renamed into place" ;;
esac
case "$SECTION" in
    *'mv -f "$RUN_OVERRIDE.new" "$RUN_OVERRIDE"'*) ok ;;
    *) fail "run-override.sh is not renamed into place" ;;
esac
case "$SECTION" in
    *'cp "$RC_SRC" "$RC_DST"'[!.]*|*'cp "$RUN_SRC" "$RUN_OVERRIDE"'[!.]*) fail "a boot script is still copied in place" ;;
    *) ok ;;
esac

run_section() {
    {
        echo "RC_SRC=\"$WORK/src/rc.local\""
        echo "RC_DST=\"$WORK/nv/rc.local\""
        echo "RUN_SRC=\"$WORK/src/run.sh\""
        echo "RUN_OVERRIDE=\"$WORK/nv/streborn/run-override.sh\""
        printf '%s\n' "$SECTION"
    } > "$WORK/section.sh"
    sh "$WORK/section.sh" > "$WORK/out.txt" 2>&1
}

# ---- normal install over existing scripts -----------------------------------

mkdir -p "$WORK/src" "$WORK/nv/streborn"
# A shebang on each, so -x holds on every host the test runs on.
script() { printf '#!/bin/sh\n# %s\n' "$1" > "$2"; }
script "new rc" "$WORK/src/rc.local"
script "new run" "$WORK/src/run.sh"
script "old rc" "$WORK/nv/rc.local"
script "old run" "$WORK/nv/streborn/run-override.sh"
ln "$WORK/nv/rc.local" "$WORK/rc.held"
ln "$WORK/nv/streborn/run-override.sh" "$WORK/run.held"

run_section
rc=$?
if [ "$rc" = "0" ]; then ok; else fail "section exited $rc: $(cat "$WORK/out.txt")"; fi
if [ "$(tail -n 1 "$WORK/nv/rc.local")" = "# new rc" ]; then ok; else fail "rc.local not updated"; fi
if [ "$(tail -n 1 "$WORK/nv/streborn/run-override.sh")" = "# new run" ]; then ok; else fail "run-override.sh not updated"; fi
if [ -x "$WORK/nv/rc.local" ] && [ -x "$WORK/nv/streborn/run-override.sh" ]; then ok; else fail "boot scripts not executable"; fi
if [ "$(tail -n 1 "$WORK/rc.held")" = "# old rc" ]; then ok; else fail "rc.local was rewritten in place, not renamed"; fi
if [ "$(tail -n 1 "$WORK/run.held")" = "# old run" ]; then ok; else fail "run-override.sh was rewritten in place, not renamed"; fi
if [ -e "$WORK/nv/rc.local.new" ] || [ -e "$WORK/nv/streborn/run-override.sh.new" ]; then fail ".new left behind"; else ok; fi

# ---- unreadable rc.local source: fail loudly, keep the old one ---------------

rm -f "$WORK/src/rc.local"
script "old rc" "$WORK/nv/rc.local"
run_section
rc=$?
if [ "$rc" = "1" ]; then ok; else fail "a failed rc.local copy must exit 1, got $rc"; fi
if [ "$(tail -n 1 "$WORK/nv/rc.local")" = "# old rc" ]; then ok; else fail "a failed copy must leave the old rc.local intact"; fi
if grep -q "ERROR while copying" "$WORK/out.txt"; then ok; else fail "a failed copy must say ERROR"; fi
if [ -e "$WORK/nv/rc.local.new" ]; then fail "rc.local.new left behind after a failure"; else ok; fi

cleanup

if [ "$FAILED" -eq 0 ]; then
    echo "install boot scripts: $CASES checks, all passed"
    exit 0
fi
echo "install boot scripts: $CASES checks, $FAILED FAILED"
exit 1
