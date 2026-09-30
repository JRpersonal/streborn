#!/bin/sh
# Functional test for install_boot_script, the atomic boot-script installer.
#
# What it replaced: a plain `cp ... 2>/dev/null` followed by an unconditional
# `chmod +x` and an unconditional success log, in three places. cp opens the
# destination with O_TRUNC before reading a byte of the source, so an
# interrupted read left a truncated file that was still executable, rc.local's
# `[ -x ... ]` passed, and the boot exec'd it. One of the three sites also
# rewrote the very script the shell was executing from.
#
# The function lives verbatim in BOTH usb-stick/rc.local and usb-stick/run.sh,
# because they cannot share a copy: rc.local runs before anything is sourceable.
# The first test here is therefore that the two have not drifted, which is the
# same defect class the audit that produced this file was about.
#
# Runs under dash, busybox ash and bash.

set -u

HERE=$(dirname "$0")
RC="$HERE/../rc.local"
RUN="$HERE/../run.sh"
for f in "$RC" "$RUN"; do
    [ -r "$f" ] || { echo "cannot read $f"; exit 1; }
done

FAILED=0
CASES=0
ok() { CASES=$((CASES + 1)); }
fail() {
    CASES=$((CASES + 1))
    FAILED=$((FAILED + 1))
    echo "FAIL: $1"
}
check() { if [ "$2" = "$3" ]; then ok; else fail "$1 (want rc=$2, got rc=$3)"; fi; }

extract() {
    awk '
        /^install_boot_script\(\) \{/ { inside = 1 }
        inside                        { print }
        inside && $0 == "}"           { exit }
    ' "$1"
}

# ---- the two copies must be identical --------------------------------------

A=$(extract "$RC")
B=$(extract "$RUN")
if [ -z "$A" ]; then fail "install_boot_script not found in rc.local"; else ok; fi
if [ -z "$B" ]; then fail "install_boot_script not found in run.sh"; else ok; fi
if [ "$A" = "$B" ]; then
    ok
else
    fail "the two install_boot_script copies have DRIFTED; they must stay byte-identical"
    printf '%s\n' "$A" > "$HERE/.a.sh"
    printf '%s\n' "$B" > "$HERE/.b.sh"
    diff "$HERE/.a.sh" "$HERE/.b.sh" || true
    rm -f "$HERE/.a.sh" "$HERE/.b.sh"
fi

eval "$A"

WORK=$(mktemp -d 2>/dev/null || echo "$HERE/.work")
mkdir -p "$WORK"
cleanup() { rm -rf "$WORK"; }

# A plausible boot script: parses, and long enough for the size floor to mean
# something.
make_script() {
    # $1 = path, $2 = how many filler lines
    {
        echo '#!/bin/sh'
        echo 'echo hello'
        i=0
        while [ "$i" -lt "${2:-40}" ]; do
            echo "# filler line $i ------------------------------------------"
            i=$((i + 1))
        done
        echo 'exit 0'
    } > "$1"
}

# ---- the happy path --------------------------------------------------------

make_script "$WORK/src.sh" 60
install_boot_script "$WORK/src.sh" "$WORK/dst.sh"
check "a good copy succeeds" 0 $?
if cmp -s "$WORK/src.sh" "$WORK/dst.sh"; then ok; else fail "the destination does not match the source"; fi
if [ -x "$WORK/dst.sh" ]; then ok; else fail "the installed script is not executable"; fi
if [ -e "$WORK/dst.sh.new" ]; then fail "the staging file was left behind on success"; else ok; fi

# ---- rename, not truncate --------------------------------------------------
#
# The inode must CHANGE. That is the whole reason it is safe to replace a
# script that is currently being executed: the running shell keeps the old
# inode alive through its open fd.

INO_BEFORE=$(ls -i "$WORK/dst.sh" 2>/dev/null | awk '{print $1}')
make_script "$WORK/src2.sh" 70
install_boot_script "$WORK/src2.sh" "$WORK/dst.sh"
check "replacing an existing script succeeds" 0 $?
INO_AFTER=$(ls -i "$WORK/dst.sh" 2>/dev/null | awk '{print $1}')
if [ -n "$INO_BEFORE" ] && [ -n "$INO_AFTER" ] && [ "$INO_BEFORE" != "$INO_AFTER" ]; then
    ok
else
    fail "the inode did not change ($INO_BEFORE -> $INO_AFTER): this was an in-place write, not a rename"
fi

# The old inode must still be readable through an already-open descriptor,
# which is what protects a running boot script.
make_script "$WORK/dst3.sh" 50
OLDSUM=$(cksum < "$WORK/dst3.sh" | awk '{print $1}')
# Hold it open on fd 9 for the duration of the replacement.
exec 9< "$WORK/dst3.sh"
make_script "$WORK/src3.sh" 90
install_boot_script "$WORK/src3.sh" "$WORK/dst3.sh"
check "replacing a file that is held open succeeds" 0 $?
HELDSUM=$(cksum <&9 | awk '{print $1}')
exec 9<&-
if [ "$HELDSUM" = "$OLDSUM" ]; then
    ok
else
    fail "the already-open descriptor saw changed content: a running boot script would have been corrupted"
fi

# ---- refusals --------------------------------------------------------------

make_script "$WORK/keep.sh" 60
KEEPSUM=$(cksum < "$WORK/keep.sh" | awk '{print $1}')

install_boot_script "$WORK/does-not-exist.sh" "$WORK/keep.sh"
check "an unreadable source is refused" 1 $?

install_boot_script "" "$WORK/keep.sh"
check "an empty source path is refused" 1 $?

# A source that does not parse: a stick file cut off mid-statement.
{ echo '#!/bin/sh'; echo 'if [ 1 = 1 ]; then'; echo '  echo half'; } > "$WORK/broken.sh"
install_boot_script "$WORK/broken.sh" "$WORK/keep.sh"
check "a source that does not parse is refused" 1 $?

# Parseable but drastically shorter than the working script.
{ echo '#!/bin/sh'; echo 'exit 0'; } > "$WORK/tiny.sh"
install_boot_script "$WORK/tiny.sh" "$WORK/keep.sh"
check "a drastically shorter replacement is refused with rc=2" 2 $?

# Every refusal must have left the working script untouched and no staging
# file behind.
if [ "$(cksum < "$WORK/keep.sh" | awk '{print $1}')" = "$KEEPSUM" ]; then
    ok
else
    fail "a refused install still modified the working script"
fi
if [ -e "$WORK/keep.sh.new" ]; then fail "a refused install left its staging file behind"; else ok; fi

# ---- a legitimate shrink is still allowed ----------------------------------
#
# The floor is there for truncation, not to freeze the script's size. Slightly
# smaller must go through, or a real edit that removes code could never ship.

make_script "$WORK/big.sh" 100
install_boot_script "$WORK/big.sh" "$WORK/shrink.sh" >/dev/null 2>&1
make_script "$WORK/smaller.sh" 70
install_boot_script "$WORK/smaller.sh" "$WORK/shrink.sh"
check "a moderately smaller replacement is allowed" 0 $?
if cmp -s "$WORK/smaller.sh" "$WORK/shrink.sh"; then ok; else fail "the smaller script was not installed"; fi

# ---- a destination that does not exist yet ---------------------------------

make_script "$WORK/fresh.sh" 40
install_boot_script "$WORK/fresh.sh" "$WORK/brandnew.sh"
check "installing where nothing existed succeeds" 0 $?
if [ -x "$WORK/brandnew.sh" ]; then ok; else fail "the fresh install is not executable"; fi

cleanup

if [ "$FAILED" -eq 0 ]; then
    echo "boot install: $CASES checks, all passed"
    exit 0
fi
echo "boot install: $CASES checks, $FAILED FAILED"
exit 1
