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
    awk -v fn="${2:-install_boot_script}" '
        $0 == fn "() {"     { inside = 1 }
        inside              { print }
        inside && $0 == "}" { exit }
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

# ---- pid_is_our_agent: never signal a reused PID -------------------------
#
# The pidfile lives on NAND and survives the reboot, and nothing clears it at
# shutdown (the watchdogs use a failing kill -0 as their restart trigger). So
# every boot reads the previous boot's number while Linux hands the same
# numbers out again from 1. kill -0 alone was taken as proof of identity before
# escalating to TERM and KILL.

eval "$(extract "$RUN" pid_is_our_agent)"

FAKEPROC="$WORK/proc"
mk_pid() {
    # $1 = pid, $2 = cmdline (spaces stand in for the NUL separators)
    mkdir -p "$FAKEPROC/$1"
    # /proc/<pid>/cmdline is NUL-separated, and a NUL cannot travel through
    # argv, so this is built with awk rather than handed to tr.
    printf '%s' "$2" | awk 'BEGIN{RS=" "} {printf "%s%c", $0, 0}' > "$FAKEPROC/$1/cmdline"
}

mk_pid 111 "/mnt/nv/streborn/bin/streborn-armv7l"
mk_pid 222 "/mnt/nv/streborn/run-override.sh"
mk_pid 333 "/opt/Bose/NetManager --autoswitching=true"
mk_pid 444 "/mnt/nv/streborn/bin/streborn-armv7l -listen :8888"
mk_pid 555 "/media/sda1/streborn"

PROC_ROOT="$FAKEPROC" pid_is_our_agent 111 "/mnt/nv/streborn/bin/streborn-armv7l"
check "our own agent is recognised" 0 $?

# The one that matters: the boot script itself lives under a path containing
# "streborn", so a looser match would send KILL to our own supervisor.
PROC_ROOT="$FAKEPROC" pid_is_our_agent 222 "/mnt/nv/streborn/bin/streborn-armv7l"
check "the boot script is NOT mistaken for the agent" 1 $?

PROC_ROOT="$FAKEPROC" pid_is_our_agent 333 "/mnt/nv/streborn/bin/streborn-armv7l"
check "a firmware daemon is not mistaken for the agent" 1 $?

PROC_ROOT="$FAKEPROC" pid_is_our_agent 444 "/mnt/nv/streborn/bin/streborn-armv7l"
check "the agent with arguments is recognised" 0 $?

PROC_ROOT="$FAKEPROC" pid_is_our_agent 555 "/media/sda1/streborn"
check "the stick binary name is recognised when that is what we run" 0 $?

PROC_ROOT="$FAKEPROC" pid_is_our_agent 999 "/mnt/nv/streborn/bin/streborn-armv7l"
check "a pid with no /proc entry is refused" 1 $?

PROC_ROOT="$FAKEPROC" pid_is_our_agent "" "/mnt/nv/streborn/bin/streborn-armv7l"
check "an empty pid is refused" 1 $?

PROC_ROOT="$FAKEPROC" pid_is_our_agent 0 "/mnt/nv/streborn/bin/streborn-armv7l"
check "pid 0 is refused" 1 $?

PROC_ROOT="$FAKEPROC" pid_is_our_agent "notanumber" "/mnt/nv/streborn/bin/streborn-armv7l"
check "a non-numeric pid is refused" 1 $?

# With no expected binary given it falls back to the agent's usual name, so the
# check still works rather than matching everything.
PROC_ROOT="$FAKEPROC" pid_is_our_agent 111 ""
check "the fallback name still recognises the agent" 0 $?
PROC_ROOT="$FAKEPROC" pid_is_our_agent 222 ""
check "the fallback name still rejects the boot script" 1 $?

# ---- defined BEFORE the first call ---------------------------------------
#
# Everything above extracts the functions and tests them in isolation, which is
# exactly why it missed the real defect: both files CALLED install_boot_script
# at top level before the line that defines it. sh runs a file top to bottom,
# so the call found no such command (rc 127), fell into the "did not validate"
# branch, and redeploying the boot scripts from a stick never worked from
# v0.9.94 on. run.sh had the same defect with pid_is_our_agent.

# First line that defines $2 in $1, and first line that calls it (any line that
# names it as a word, outside comments and outside its own definition line).
def_line() {
    awk -v fn="$2" '$0 == fn "() {" { print NR; exit }' "$1"
}
first_call_line() {
    awk -v fn="$2" '
        $0 == fn "() {"   { next }
        /^[ \t]*#/        { next }
        {
            line = $0
            sub(/[ \t]#.*$/, "", line)
            if (match(line, "(^|[^A-Za-z0-9_])" fn "([^A-Za-z0-9_]|$)")) { print NR; exit }
        }
    ' "$1"
}
check_order() {
    # $1 = file, $2 = function
    _d=$(def_line "$1" "$2")
    _c=$(first_call_line "$1" "$2")
    if [ -z "$_d" ]; then fail "$2 is not defined in $1"; return; fi
    if [ -z "$_c" ]; then fail "$2 is never called in $1 (anchor moved?)"; return; fi
    if [ "$_d" -lt "$_c" ]; then
        ok
    else
        fail "$1 calls $2 at line $_c but only defines it at line $_d: the call runs before the function exists"
    fi
}
check_order "$RC" install_boot_script
check_order "$RUN" install_boot_script
check_order "$RUN" pid_is_our_agent

# ---- the real files, run as they run on the speaker ------------------------
#
# The order check above is a proxy. These run the shipped code paths in a
# sandbox, with the box paths rewritten into $WORK, and assert the outcome.

SANDBOX="$WORK/box"
sandbox_paths() {
    sed -e "s#/media/sda1#$SANDBOX/stick#g" \
        -e "s#/media/sd[b-d]1#$SANDBOX/nostick#g" \
        -e "s#/mnt/nv#$SANDBOX/nv#g"
}
reset_sandbox() {
    rm -rf "$SANDBOX"
    mkdir -p "$SANDBOX/stick" "$SANDBOX/nv/streborn"
    cp "$RC" "$SANDBOX/stick/rc.local"
    cp "$RUN" "$SANDBOX/stick/run.sh"
}

# rc.local: everything from the top up to the hand-off to the NAND override,
# verbatim. That is the stick wait loop, then the redeploy.
reset_sandbox
awk '$0 ~ /^# The NAND override wins\./ { exit } { print }' "$RC" | sandbox_paths > "$WORK/rc_part.sh"
if grep -q '^    install_boot_script ' "$WORK/rc_part.sh"; then ok; else fail "the rc.local redeploy section could not be cut out"; fi
sh "$WORK/rc_part.sh" >/dev/null 2>&1
check "the rc.local boot prefix runs cleanly" 0 $?
if cmp -s "$RC" "$SANDBOX/nv/rc.local"; then ok; else fail "rc.local did not redeploy /mnt/nv/rc.local from the stick"; fi
if cmp -s "$RUN" "$SANDBOX/nv/streborn/run-override.sh"; then ok; else fail "rc.local did not redeploy run-override.sh from the stick"; fi
if grep -q "nv/rc.local redeployed from stick" "$SANDBOX/nv/streborn/boot.log" 2>/dev/null; then ok; else fail "rc.local did not log the rc.local redeploy as a success"; fi
if grep -q "run-override.sh redeployed from stick" "$SANDBOX/nv/streborn/boot.log" 2>/dev/null; then ok; else fail "rc.local did not log the run-override.sh redeploy as a success"; fi

# run.sh is far too box-bound to run from the top. Instead: every top-level
# function run.sh has DEFINED by the time it reaches a section (which is what
# the shell has at that point), then that section verbatim.
run_section() {
    # $1 = first line of the section, $2 = the line that ends it (excluded)
    awk -v s="$1" -v e="$2" '
        !started && $0 == s                       { started = 1 }
        started && $0 == e                        { exit }
        started                                   { print; next }
        !infn && /^[A-Za-z_][A-Za-z0-9_]*\(\) \{$/ { infn = 1 }
        infn                                      { print }
        infn && $0 == "}"                         { infn = 0 }
    ' "$RUN"
}
# run.sh's own log() appends to $LOG; point that into the sandbox.
RUN_STUBS='LOG="'"$SANDBOX"'/run.log"'

reset_sandbox
{
    printf '%s\n' "$RUN_STUBS"
    echo "STICK=\"$SANDBOX/stick\""
    run_section "# Defense in depth: redeploy rc.local + run-override.sh from stick" \
        "# === Early low-power Wi-Fi one-shot (BEFORE the heavy stick->NAND copy) ==="
} | sandbox_paths > "$WORK/run_redeploy.sh"
if grep -q '^if \[ -f "\$STICK/rc.local" \]' "$WORK/run_redeploy.sh"; then ok; else fail "the run.sh redeploy section could not be cut out"; fi
sh "$WORK/run_redeploy.sh" >/dev/null 2>&1
check "the run.sh redeploy section runs cleanly" 0 $?
if cmp -s "$RC" "$SANDBOX/nv/rc.local"; then ok; else fail "run.sh did not redeploy /mnt/nv/rc.local from the stick"; fi
if cmp -s "$RUN" "$SANDBOX/nv/streborn/run-override.sh"; then ok; else fail "run.sh did not redeploy run-override.sh from the stick"; fi
if grep -q "^.*: redeployed .*run-override.sh from stick" "$SANDBOX/run.log" 2>/dev/null; then ok; else fail "run.sh did not log the run-override.sh redeploy as a success"; fi

# run.sh, "agent already running? then stop it": a live process that /proc
# says is our agent must be stopped.
reset_sandbox
sleep 60 &
VICTIM=$!
FAKEPROC2="$SANDBOX/proc"
mkdir -p "$FAKEPROC2/$VICTIM"
printf '/mnt/nv/streborn/bin/streborn-armv7l\000' > "$FAKEPROC2/$VICTIM/cmdline"
echo "$VICTIM" > "$SANDBOX/agent.pid"
{
    printf '%s\n' "$RUN_STUBS"
    echo "PIDFILE=\"$SANDBOX/agent.pid\""
    echo "BIN=/mnt/nv/streborn/bin/streborn-armv7l"
    echo "PROC_ROOT=\"$FAKEPROC2\""
    run_section "# === Agent already running? Then stop it. ===" \
        "# The stick-side update.sh hook was removed in v0.9.28: it pulled a binary"
} | sandbox_paths > "$WORK/run_stop.sh"
if grep -q 'pid_is_our_agent "\$OLDPID"' "$WORK/run_stop.sh"; then ok; else fail "the run.sh agent-stop section could not be cut out"; fi
# Its real sleep 2 is kept: this must behave as on the box.
sh "$WORK/run_stop.sh" >/dev/null 2>&1
check "the run.sh agent-stop section runs cleanly" 0 $?
if kill -0 "$VICTIM" 2>/dev/null; then
    fail "run.sh did not stop a running previous agent"
    kill -KILL "$VICTIM" 2>/dev/null
else
    ok
fi
wait "$VICTIM" 2>/dev/null
if grep -q "previous agent still running (PID $VICTIM), stopping it" "$SANDBOX/run.log" 2>/dev/null; then ok; else fail "run.sh did not recognise the running agent as its own"; fi
if [ -e "$SANDBOX/agent.pid" ]; then fail "run.sh did not remove the pidfile"; else ok; fi

cleanup

if [ "$FAILED" -eq 0 ]; then
    echo "boot install: $CASES checks, all passed"
    exit 0
fi
echo "boot install: $CASES checks, $FAILED FAILED"
exit 1
