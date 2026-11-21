#!/bin/sh
# Functional test for how the boot scripts treat a USB stick that is NOT an
# STR (or Bose) stick, e.g. a music stick left in the speaker.
#
# What it fixes: rc.local and run.sh took any mounted stick for STR's. A music
# stick at boot cost about 55s (rc.local waited 30s for it to turn into an STR
# stick, run.sh another 25s), then run.sh fscked it and mounted it a second
# time over Bose's own mount, deleted its cap*.ogg files and wrote setup.log
# onto it every 25s.
#
# The helpers stick_has_str_marker, usb_stick_mounts and foreign_stick_only
# live verbatim in BOTH usb-stick/rc.local and usb-stick/run.sh (rc.local runs
# before anything is sourceable), so the first check is that the copies have
# not drifted. Then the helpers, then the shipped wait/recovery sections run
# in a sandbox with the box paths rewritten into $WORK and mount, umount,
# fsck.vfat and sleep stubbed, and finally a guard that every write to the
# stick in run.sh sits behind the marker check.
#
# Runs under dash, busybox ash and bash. No arrays, no [[ ]], no local.

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
check() { if [ "$2" = "$3" ]; then ok; else fail "$1 (want $2, got $3)"; fi; }

extract() {
    awk -v fn="$2" '
        $0 == fn "() {"     { inside = 1 }
        inside              { print }
        inside && $0 == "}" { exit }
    ' "$1"
}

WORK=$(mktemp -d 2>/dev/null || echo "/tmp/foreign_stick_test.$$")
mkdir -p "$WORK"
trap 'rm -rf "$WORK"' EXIT INT TERM

# Box paths into the sandbox.
sandbox() {
    sed -e "s#/media/#$WORK/media/#g" \
        -e "s#/sys/block/#$WORK/sys/block/#g" \
        -e "s#/dev/sd#$WORK/dev/sd#g" \
        -e "s#/dev/mmcblk#$WORK/dev/mmcblk#g" \
        -e 's#\[ -b #[ -e #g'
}

HELPERS="stick_has_str_marker usb_stick_mounts foreign_stick_only"

# ---- the copies must be identical ------------------------------------------

for fn in $HELPERS; do
    A=$(extract "$RC" "$fn")
    B=$(extract "$RUN" "$fn")
    if [ -z "$A" ]; then fail "$fn not found in rc.local"; continue; fi
    if [ -z "$B" ]; then fail "$fn not found in run.sh"; continue; fi
    if [ "$A" = "$B" ]; then
        ok
    else
        fail "the two $fn copies have DRIFTED; they must stay byte-identical"
        printf '%s\n' "$A" > "$WORK/a.sh"
        printf '%s\n' "$B" > "$WORK/b.sh"
        diff "$WORK/a.sh" "$WORK/b.sh" || true
    fi
done

# ---- defined BEFORE the first call -----------------------------------------
#
# sh runs a file top to bottom; a call above the definition is "not found"
# (rc 127), which for foreign_stick_only would read as "not foreign" and hide
# the fix, and for the write guards as "not ours" and stop every write to a
# real STR stick.

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
    _d=$(def_line "$1" "$2")
    _c=$(first_call_line "$1" "$2")
    if [ -z "$_d" ]; then fail "$2 is not defined in $1"; return; fi
    if [ -z "$_c" ]; then fail "$2 is never called in $1 (anchor moved?)"; return; fi
    if [ "$_d" -lt "$_c" ]; then ok; else fail "$1 calls $2 at line $_c but only defines it at line $_d"; fi
}
for fn in $HELPERS; do
    check_order "$RC" "$fn"
    check_order "$RUN" "$fn"
done
# The first write to the stick is the setup.log mirror, started near the top.
_d=$(def_line "$RUN" stick_has_str_marker)
_m=$(grep -n '^start_stick_log_mirror$' "$RUN" | head -1 | cut -d: -f1)
if [ -n "$_m" ] && [ -n "$_d" ] && [ "$_d" -lt "$_m" ]; then ok; else fail "stick_has_str_marker must be defined above the setup.log mirror start (def $_d, start $_m)"; fi

# ---- the helpers -----------------------------------------------------------

for fn in $HELPERS; do
    eval "$(extract "$RUN" "$fn" | sandbox)"
done

M="$WORK/media"
reset_media() {
    rm -rf "$M" "$WORK/sys" "$WORK/dev" "$WORK/content" "$WORK/calls" "$WORK/sleeps"
    mkdir -p "$M/sda1" "$M/sdb1" "$WORK/dev" "$WORK/content"
    : > "$WORK/mounts"
    PROC_MOUNTS="$WORK/mounts"
    export PROC_MOUNTS
}
reset_media

stick_has_str_marker ""
check "an empty path is not an STR stick" 1 $?
stick_has_str_marker "$WORK/does-not-exist"
check "a missing directory is not an STR stick" 1 $?
stick_has_str_marker "$M/sda1"
check "an empty stick is not an STR stick" 1 $?

mkdir -p "$M/sda1/Music/run.sh.d"
: > "$M/sda1/song.mp3"
: > "$M/sda1/cap0001.ogg"
: > "$M/sda1/setup.log"
: > "$M/sda1/Music/run.sh"
stick_has_str_marker "$M/sda1"
check "music, cap*.ogg, setup.log and markers below the top level do not make an STR stick" 1 $?

for m in run.sh install.sh rc.local streborn-armv7l streborn version.txt wlan.conf \
    wlan.ssid wlan.pass str-shim.so go-librespot remote_services keep-open \
    presets.json lang.conf region.conf name.conf name.raw Update.stu UPDATE.STU; do
    rm -rf "$M/sdb1"; mkdir -p "$M/sdb1"
    : > "$M/sdb1/$m"
    stick_has_str_marker "$M/sdb1"
    check "$m at the top level marks an STR/Bose stick" 0 $?
done

# usb_stick_mounts: only mount points under /media/sd*.
reset_media
{
    echo "/dev/root / ext4 rw 0 0"
    echo "/dev/sdb1 /mnt ext4 rw 0 0"
    echo "/dev/sda1 $M/sda1 vfat rw,relatime 0 0"
    echo "tmpfs /tmp tmpfs rw 0 0"
} > "$PROC_MOUNTS"
check "only /media/sd* mounts are sticks" "$M/sda1" "$(usb_stick_mounts)"
PROC_MOUNTS="$WORK/no-such-file" usb_stick_mounts > "$WORK/out"
check "an unreadable mounts file lists nothing" "" "$(cat "$WORK/out")"

reset_media
foreign_stick_only
check "nothing mounted is not 'foreign only' (a stick may still be mounting)" 1 $?
echo "/dev/sda1 $M/sda1 vfat rw 0 0" > "$PROC_MOUNTS"
: > "$M/sda1/song.mp3"
foreign_stick_only
check "a mounted music stick is 'foreign only'" 0 $?
: > "$M/sdb1/run.sh"
echo "/dev/sdb1 $M/sdb1 vfat rw 0 0" >> "$PROC_MOUNTS"
foreign_stick_only
check "a music stick beside a mounted STR stick is not 'foreign only'" 1 $?
reset_media
: > "$M/sda1/install.sh"
echo "/dev/sda1 $M/sda1 vfat rw 0 0" > "$PROC_MOUNTS"
foreign_stick_only
check "a mounted STR stick is not 'foreign only'" 1 $?

# ---- stubs for the sandboxed sections --------------------------------------
#
# mount "mounts" a device by copying content/<dev> into the mount point and
# adding a mounts line; umount drops the line and empties the mount point.
# fail-ro-<dev> makes the read-only mount of that device fail (the dirty FAT
# case the run.sh recovery exists for).

STUBS="$WORK/stubs"
mkdir -p "$STUBS"
cat > "$STUBS/mount" <<'EOF'
#!/bin/sh
echo "mount $*" >> "$FAKE/calls"
mode=rw
[ "${3:-}" = "-o" ] && [ "${4:-}" = "ro" ] && mode=ro
dev=""; mp=""
for a in "$@"; do dev=$mp; mp=$a; done
name=${dev##*/}
[ -d "$FAKE/content/$name" ] || exit 32
[ -e "$FAKE/fail-$mode-$name" ] && exit 32
mkdir -p "$mp"
cp -R "$FAKE/content/$name/." "$mp/"
echo "$dev $mp vfat $mode 0 0" >> "$PROC_MOUNTS"
exit 0
EOF
cat > "$STUBS/umount" <<'EOF'
#!/bin/sh
echo "umount $*" >> "$FAKE/calls"
grep -q " $1 " "$PROC_MOUNTS" || exit 1
grep -v " $1 " "$PROC_MOUNTS" > "$PROC_MOUNTS.new"
mv "$PROC_MOUNTS.new" "$PROC_MOUNTS"
rm -rf "$1" && mkdir -p "$1"
exit 0
EOF
printf '#!/bin/sh\necho "fsck.vfat $*" >> "$FAKE/calls"\nexit 0\n' > "$STUBS/fsck.vfat"
chmod +x "$STUBS"/*
FAKE="$WORK"
export FAKE
# sleep, mount and umount are shell functions in the sandboxed sections, not
# only PATH stubs: busybox ash can run its own applets ahead of anything on
# PATH (it does for sleep). Counting the sleep calls is how the tests see a
# wait. fsck.vfat is no busybox applet, so the PATH stub is enough for it.
SLEEP_STUB='sleep() { echo x >> "$WORK/sleeps"; }
mount() { "$WORK/stubs/mount" "$@"; }
umount() { "$WORK/stubs/umount" "$@"; }'

# A device node (a plain file; the sandbox turns [ -b ] into [ -e ]) whose
# file system carries the given files.
device() {
    _dv="$1"; shift
    : > "$WORK/dev/$_dv"
    mkdir -p "$WORK/content/$_dv"
    for _f in "$@"; do : > "$WORK/content/$_dv/$_f"; done
}
# Bose's udev has mounted $1 at /media/$1.
mounted() {
    cp -R "$WORK/content/$1/." "$M/$1/"
    echo "$WORK/dev/$1 $M/$1 vfat rw 0 0" >> "$PROC_MOUNTS"
}
sleeps() { if [ -r "$WORK/sleeps" ]; then wc -l < "$WORK/sleeps" | tr -d ' \t'; else echo 0; fi; }
called() { grep -qE "$1" "$WORK/calls" 2>/dev/null; }

# ---- rc.local: the stick wait loop -----------------------------------------

{
    for fn in $HELPERS; do extract "$RC" "$fn"; done
    echo "$SLEEP_STUB"
    echo 'LOGDIR="$WORK/nv"; STICK_WAIT=30'
    awk '$0 == "STICK_PATH=\"\"" { s = 1 } s { print } s && $0 == "done" { exit }' "$RC"
    echo 'echo "STICK_PATH=$STICK_PATH"'
} | sandbox > "$WORK/rc_loop.sh"
if grep -q 'foreign_stick_only' "$WORK/rc_loop.sh" && grep -q '^done$' "$WORK/rc_loop.sh"; then ok; else fail "the rc.local wait loop could not be cut out"; fi
rc_loop() {
    mkdir -p "$WORK/nv"; : > "$WORK/nv/boot.log"
    WORK="$WORK" PATH="$STUBS:$PATH" sh "$WORK/rc_loop.sh" 2>&1
}

reset_media
device sda1 song.mp3 cap1.ogg
mounted sda1
OUT=$(rc_loop)
check "rc.local: a mounted music stick is not waited on" 0 "$(sleeps)"
check "rc.local: a music stick is not taken for STR's" "STICK_PATH=" "$OUT"
if grep -q "only a non-STR USB stick is mounted ($M/sda1 ), going on without a stick after 0s" "$WORK/nv/boot.log"; then ok; else fail "rc.local did not log why it went on: $(cat "$WORK/nv/boot.log")"; fi

reset_media
device sda1 run.sh install.sh streborn-armv7l version.txt
mounted sda1
OUT=$(rc_loop)
check "rc.local: a mounted STR stick is found at once" "STICK_PATH=$M/sda1" "$OUT"
check "rc.local: a mounted STR stick costs no wait" 0 "$(sleeps)"

reset_media
OUT=$(rc_loop)
check "rc.local: nothing mounted yet is still waited on to the cap" 30 "$(sleeps)"

# An STR stick that is mounted but has only a config file left: no run.sh, so
# rc.local does not adopt it, but it is ours, so the wait runs as before.
reset_media
device sda1 wlan.conf
mounted sda1
OUT=$(rc_loop)
check "rc.local: a marked stick without run.sh is waited on as before" 30 "$(sleeps)"

# ---- run.sh: the stick wait and the fsck recovery --------------------------

{
    for fn in $HELPERS; do extract "$RUN" "$fn"; done
    echo "$SLEEP_STUB"
    echo 'setup_log() { echo "$*" >> "$WORK/setup.log"; }'
    echo 'STICK="/media/sda1"; STICK_BIN="$STICK/streborn-armv7l"; STICK_VER_FILE="$STICK/version.txt"'
    awk '$0 == "_stick_foreign=\"\"" { s = 1 } s { print } s && $0 == "fi" { exit }' "$RUN"
    echo 'echo "STICK=$STICK foreign=$_stick_foreign"'
} | sandbox > "$WORK/run_wait.sh"
if grep -q 'trying fsck+manual mount' "$WORK/run_wait.sh" && grep -q 'foreign_stick_only' "$WORK/run_wait.sh"; then ok; else fail "the run.sh stick wait section could not be cut out"; fi
run_wait() {
    : > "$WORK/setup.log"
    mkdir -p "$WORK/sys/block/sda"
    WORK="$WORK" PATH="$STUBS:$PATH" sh "$WORK/run_wait.sh" 2>&1
}
logged() { grep -q "$1" "$WORK/setup.log"; }

# T1 in short: a music stick Bose has mounted.
reset_media
device sda1 song.mp3 cap1.ogg
mounted sda1
OUT=$(run_wait)
check "run.sh: a mounted music stick is not waited on" 0 "$(sleeps)"
if called "fsck"; then fail "run.sh fscked a mounted music stick: $(cat "$WORK/calls")"; else ok; fi
if called "mount"; then fail "run.sh mounted or unmounted a music stick: $(cat "$WORK/calls")"; else ok; fi
check "run.sh: the music stick is still mounted once" 1 "$(grep -c " $M/sda1 " "$PROC_MOUNTS")"
if logged "only a non-STR USB stick is mounted"; then ok; else fail "run.sh did not log the foreign stick: $(cat "$WORK/setup.log")"; fi
case "$OUT" in *"foreign=1"*) ok ;; *) fail "run.sh did not flag the foreign stick: $OUT" ;; esac

# T2 in short: the STR stick Bose has mounted.
reset_media
device sda1 run.sh install.sh streborn-armv7l version.txt
mounted sda1
OUT=$(run_wait)
check "run.sh: a mounted STR stick costs no wait" 0 "$(sleeps)"
if called "mount|fsck"; then fail "run.sh touched a mounted STR stick it had found: $(cat "$WORK/calls")"; else ok; fi

# The recovery path. Nothing auto-mounted, a music stick in: the read-only
# look finds no marker, so no fsck and no rw mount.
reset_media
device sda1 song.mp3
OUT=$(run_wait)
check "run.sh: an unmounted stick is still waited on" 25 "$(sleeps)"
if called "mount -t vfat -o ro $WORK/dev/sda1"; then ok; else fail "run.sh did not take a read-only look: $(cat "$WORK/calls" 2>/dev/null)"; fi
if called "fsck"; then fail "run.sh fscked an unmounted music stick: $(cat "$WORK/calls")"; else ok; fi
if called "mount -t vfat -o rw"; then fail "run.sh mounted a music stick rw: $(cat "$WORK/calls")"; else ok; fi
if logged "dev/sda1 is not an STR stick"; then ok; else fail "run.sh did not log the skipped device: $(cat "$WORK/setup.log")"; fi
check "run.sh: nothing is left mounted after the look" 0 "$(grep -c . "$PROC_MOUNTS")"

# T3 in short: a dirty STR stick the kernel did not mount. Same as before:
# fsck, rw mount, recovered.
reset_media
device sda1 run.sh install.sh streborn-armv7l
OUT=$(run_wait)
if called "fsck.vfat -a $WORK/dev/sda1"; then ok; else fail "run.sh did not fsck the STR stick: $(cat "$WORK/calls" 2>/dev/null)"; fi
if called "mount -t vfat -o rw $WORK/dev/sda1 $M/sda1"; then ok; else fail "run.sh did not mount the STR stick rw: $(cat "$WORK/calls" 2>/dev/null)"; fi
if logged "recovered .*dev/sda1 via fsck+manual mount"; then ok; else fail "run.sh did not recover the STR stick: $(cat "$WORK/setup.log")"; fi
check "run.sh: the recovered STR stick is mounted once" 1 "$(grep -c " $M/sda1 " "$PROC_MOUNTS")"

# The read-only look itself fails (the FAT is too broken even for that): the
# fsck + rw mount run exactly as before.
reset_media
device sda1 run.sh
: > "$WORK/fail-ro-sda1"
OUT=$(run_wait)
if called "fsck.vfat -a $WORK/dev/sda1"; then ok; else fail "run.sh skipped the fsck after a failed read-only look: $(cat "$WORK/calls" 2>/dev/null)"; fi
if logged "recovered .*dev/sda1 via fsck+manual mount"; then ok; else fail "run.sh did not recover after a failed read-only look: $(cat "$WORK/setup.log")"; fi

# A music stick that is mounted, but not under /media/sd* (e.g. left at the
# desktop app's /tmp/str-stick), so the wait does not see it: the recovery
# finds the device mounted and leaves it alone instead of fscking it and
# mounting it a second time.
reset_media
device sda1 song.mp3
mkdir -p "$WORK/elsewhere"
echo "$WORK/dev/sda1 $WORK/elsewhere vfat rw 0 0" >> "$PROC_MOUNTS"
OUT=$(run_wait)
if called "fsck|mount"; then fail "run.sh fscked or mounted an already mounted music stick: $(cat "$WORK/calls")"; else ok; fi
if logged "dev/sda1 is mounted at $WORK/elsewhere and is not an STR stick"; then ok; else fail "run.sh did not log the mounted foreign device: $(cat "$WORK/setup.log")"; fi

# A music stick Bose mounted at /media/sdb1 rather than sda1: still seen as
# the only stick, so nothing is waited on, fscked or mounted over it.
reset_media
device sdb1 song.mp3
mounted sdb1
OUT=$(run_wait)
if called "fsck|mount"; then fail "run.sh touched a music stick on sdb1: $(cat "$WORK/calls")"; else ok; fi
check "run.sh: a music stick on sdb1 is not waited on" 0 "$(sleeps)"

# ---- every write to the stick sits behind the marker check -----------------
#
# A line that writes setup.log onto the stick must be inside an `if` on
# "$STICK" whose test includes stick_has_str_marker.

grep -nE '"\$SETUP_LOG"|"\$SETUP_LOG\.mirror"' "$RUN" | grep -v '^[0-9]*:[ \t]*#' | grep -v 'SETUP_LOG="' > "$WORK/writes"
if [ -s "$WORK/writes" ]; then ok; else fail "no setup.log write sites found (anchor moved?)"; fi
while IFS=: read -r _ln _rest; do
    _guard=$(awk -v n="$_ln" 'NR < n && /if / && /"\$STICK"/ { g = $0 } NR == n { print g; exit }' "$RUN")
    case "$_guard" in
        *stick_has_str_marker*) ok ;;
        *) fail "run.sh line $_ln writes setup.log to the stick without the marker check (guard: '$_guard')" ;;
    esac
done < "$WORK/writes"

if grep -n '"\$STICK"/cap\*\.ogg' "$RUN" | grep -v 'stick_has_str_marker' | grep -v '^[0-9]*:[ \t]*#' | grep -q .; then
    fail "run.sh deletes cap*.ogg on the stick without the marker check"
else
    ok
fi
# Run the shipped line: a user's cap*.ogg survives, STR's goes.
CAPLINE=$(grep '"\$STICK"/cap\*\.ogg' "$RUN" | grep -v '^[ \t]*#' | head -1)
reset_media
: > "$M/sda1/cap1.ogg"; : > "$M/sda1/song.mp3"
STICK="$M/sda1"; eval "$CAPLINE"
if [ -e "$M/sda1/cap1.ogg" ]; then ok; else fail "the cap*.ogg cleanup deleted a file on a music stick"; fi
: > "$M/sda1/run.sh"
eval "$CAPLINE"
if [ -e "$M/sda1/cap1.ogg" ]; then fail "the cap*.ogg cleanup no longer cleans an STR stick"; else ok; fi

# The write probe in initial_snapshot creates a file on the stick.
if grep -A3 'for p in /etc /mnt/nv /tmp /media/sda1; do' "$RUN" | grep -q 'stick_has_str_marker'; then ok; else fail "initial_snapshot's write probe runs on a stick without the marker check"; fi

# The post-bootstrap unmount must not unmount or remount a user's stick.
_um=$(grep -n '^    if umount "\$STICK" 2>/dev/null; then' "$RUN" | head -1 | cut -d: -f1)
_g=$(grep -n '^elif ! stick_has_str_marker "\$STICK"; then' "$RUN" | head -1 | cut -d: -f1)
if [ -n "$_um" ] && [ -n "$_g" ] && [ "$_g" -lt "$_um" ]; then ok; else fail "the post-bootstrap unmount is not behind the marker check (guard $_g, umount $_um)"; fi

if [ "$FAILED" -eq 0 ]; then
    echo "foreign stick: $CASES checks, all passed"
    exit 0
fi
echo "foreign stick: $CASES checks, $FAILED FAILED"
exit 1
