package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stickRig runs mountStickScript under the host's sh against a fake box: the
// device nodes, /proc/mounts and the mount point live in a temp dir, and
// mount, umount, fsck.vfat, dosfsck and sleep are stubs that record every call.
// The fake mount "mounts" a device by copying content/<dev> into the mount
// point and adding a /proc/mounts line, so has_marker sees what a real stick
// would carry.
type stickRig struct {
	t    *testing.T
	root string
}

func newStickRig(t *testing.T) *stickRig {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX sh and a colon-safe PATH")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	r := &stickRig{t: t, root: t.TempDir()}
	for _, d := range []string{"dev", "proc", "stubs", "content", "media", "str-stick", "sys/class/scsi_host"} {
		if err := os.MkdirAll(filepath.Join(r.root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r.write("proc/mounts", "")
	stubs := map[string]string{
		"mount": `#!/bin/sh
echo "mount $*" >> "$FAKE/calls"
ro=rw
[ "$3" = "-o" ] && [ "$4" = "ro" ] && ro=ro
for a; do dev=$mp; mp=$a; done
name=${dev##*/}
[ -d "$FAKE/content/$name" ] || exit 32
[ -e "$FAKE/fail-$ro-$name" ] && exit 32
cp -R "$FAKE/content/$name/." "$mp/"
echo "$dev $mp vfat $ro 0 0" >> "$FAKE/proc/mounts"
exit 0
`,
		"umount": `#!/bin/sh
echo "umount $*" >> "$FAKE/calls"
grep -q " $1 " "$FAKE/proc/mounts" || exit 1
grep -v " $1 " "$FAKE/proc/mounts" > "$FAKE/proc/mounts.new"
mv "$FAKE/proc/mounts.new" "$FAKE/proc/mounts"
[ "$1" = "$FAKE/str-stick" ] && rm -rf "$FAKE/str-stick" && mkdir "$FAKE/str-stick"
exit 0
`,
		"fsck.vfat": "#!/bin/sh\necho \"fsck.vfat $*\" >> \"$FAKE/calls\"\nexit 0\n",
		"dosfsck":   "#!/bin/sh\necho \"dosfsck $*\" >> \"$FAKE/calls\"\nexit 0\n",
		"sleep":     "#!/bin/sh\nexit 0\n",
	}
	for name, body := range stubs {
		p := filepath.Join(r.root, "stubs", name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func (r *stickRig) write(rel, body string) {
	r.t.Helper()
	p := filepath.Join(r.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// device creates the fake device node dev (e.g. "sda1") whose file system
// holds files.
func (r *stickRig) device(dev string, files ...string) {
	r.write("dev/"+dev, "")
	if err := os.MkdirAll(filepath.Join(r.root, "content", dev), 0o755); err != nil {
		r.t.Fatal(err)
	}
	for _, f := range files {
		r.write(filepath.Join("content", dev, f), "x")
	}
}

// mountedAt records dev as already mounted (by Bose's udev) at media/<dir>,
// with its files visible there.
func (r *stickRig) mountedAt(dev, dir string) string {
	mp := filepath.Join(r.root, "media", dir)
	if err := os.MkdirAll(mp, 0o755); err != nil {
		r.t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(r.root, "content", dev))
	for _, e := range entries {
		r.write(filepath.Join("media", dir, e.Name()), "x")
	}
	f, err := os.OpenFile(filepath.Join(r.root, "proc/mounts"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		r.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(filepath.Join(r.root, "dev", dev) + " " + mp + " vfat rw 0 0\n"); err != nil {
		r.t.Fatal(err)
	}
	return mp
}

// run executes the script and returns its output and the recorded calls.
func (r *stickRig) run() (out string, calls []string) {
	r.t.Helper()
	s := mountStickScript
	firstLine := "PATH=/usr/sbin:/sbin:/usr/bin:/bin:$PATH"
	if !strings.HasPrefix(s, firstLine) {
		r.t.Fatalf("mountStickScript no longer starts with %q; update the rig", firstLine)
	}
	s = "PATH=" + filepath.Join(r.root, "stubs") + ":$PATH" + strings.TrimPrefix(s, firstLine)
	s = strings.NewReplacer(
		"/sys/class/scsi_host", filepath.Join(r.root, "sys/class/scsi_host"),
		"/dev/sd", filepath.Join(r.root, "dev")+"/sd",
		"/proc/mounts", filepath.Join(r.root, "proc/mounts"),
		"/tmp/str-stick", filepath.Join(r.root, "str-stick"),
		// The fake device nodes are plain files.
		"[ -b ", "[ -e ",
	).Replace(s)
	cmd := exec.Command("sh", "-c", s)
	cmd.Env = append(os.Environ(), "FAKE="+r.root)
	b, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("script failed: %v\n%s", err, b)
	}
	raw, _ := os.ReadFile(filepath.Join(r.root, "calls"))
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if l != "" {
			calls = append(calls, strings.ReplaceAll(l, r.root, ""))
		}
	}
	return string(b), calls
}

func hasCall(calls []string, prefix string) bool {
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// The bug: a music stick Bose had mounted was unmounted, fscked and mounted a
// second time before the script noticed it was not STR's.
func TestMountStickLeavesAMountedForeignStickAlone(t *testing.T) {
	r := newStickRig(t)
	r.device("sda1", "song.mp3", "cap1.ogg")
	r.mountedAt("sda1", "sda1")
	out, calls := r.run()
	if !strings.Contains(out, "STR_STICK_NONE") {
		t.Errorf("a music stick was taken for the STR stick:\n%s", out)
	}
	for _, c := range calls {
		t.Errorf("the music stick was touched: %q", c)
	}
	raw, _ := os.ReadFile(filepath.Join(r.root, "proc/mounts"))
	if !strings.Contains(string(raw), "/media/sda1 ") {
		t.Errorf("the music stick is no longer mounted where Bose put it:\n%s", raw)
	}
}

func TestMountStickNeverFscksAnUnmountedForeignStick(t *testing.T) {
	r := newStickRig(t)
	r.device("sda1", "song.mp3")
	out, calls := r.run()
	if !strings.Contains(out, "STR_STICK_NONE") {
		t.Errorf("a music stick was taken for the STR stick:\n%s", out)
	}
	if hasCall(calls, "fsck.vfat") || hasCall(calls, "dosfsck") {
		t.Errorf("the music stick was fscked: %q", calls)
	}
	for _, c := range calls {
		if strings.HasPrefix(c, "mount ") && !strings.Contains(c, "-o ro") {
			t.Errorf("the music stick was mounted read-write: %q", c)
		}
	}
	if !hasCall(calls, "mount -t vfat -o ro") {
		t.Errorf("the read-only look never happened: %q", calls)
	}
}

// The STR stick keeps today's path: mounted by Bose, unmounted, fscked and
// mounted fresh at /tmp/str-stick.
func TestMountStickStillRemountsAMountedSTRStick(t *testing.T) {
	r := newStickRig(t)
	r.device("sda1", "run.sh", "install.sh", "streborn-armv7l", "version.txt")
	r.mountedAt("sda1", "sda1")
	out, calls := r.run()
	if !strings.Contains(out, "STR_STICK_MP="+filepath.Join(r.root, "str-stick")) {
		t.Fatalf("the STR stick was not mounted at str-stick:\n%s\ncalls %q", out, calls)
	}
	want := []string{"umount /media/sda1", "fsck.vfat -a /dev/sda1", "mount -t vfat /dev/sda1 /str-stick"}
	var got []string
	for _, c := range calls {
		if c != "umount /str-stick" {
			got = append(got, c)
		}
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("STR stick calls changed:\n got %q\nwant %q", got, want)
	}
}

func TestMountStickStillFscksAnUnmountedSTRStick(t *testing.T) {
	r := newStickRig(t)
	r.device("sda1", "run.sh", "streborn-armv7l")
	out, calls := r.run()
	if !strings.Contains(out, "STR_STICK_DEV="+filepath.Join(r.root, "dev/sda1")) {
		t.Fatalf("the STR stick was not found:\n%s\ncalls %q", out, calls)
	}
	if !hasCall(calls, "fsck.vfat -a /dev/sda1") {
		t.Errorf("the STR stick was not fscked before the rw mount: %q", calls)
	}
	if !hasCall(calls, "mount -t vfat /dev/sda1 /str-stick") {
		t.Errorf("the STR stick was not mounted read-write: %q", calls)
	}
	raw, _ := os.ReadFile(filepath.Join(r.root, "proc/mounts"))
	if n := strings.Count(string(raw), "/dev/sda1 "); n != 1 {
		t.Errorf("the STR stick is mounted %d times, want once:\n%s", n, raw)
	}
}

// A stick that cannot even be mounted read-only is the dirty-FAT case the fsck
// exists for, so it still gets the fsck + rw mount.
func TestMountStickFallsBackWhenTheReadOnlyLookFails(t *testing.T) {
	r := newStickRig(t)
	r.device("sda1", "install.sh", "run.sh")
	r.write("fail-ro-sda1", "")
	out, calls := r.run()
	if !strings.Contains(out, "STR_STICK_DEV=") {
		t.Fatalf("the STR stick was not found:\n%s\ncalls %q", out, calls)
	}
	if !hasCall(calls, "fsck.vfat -a /dev/sda1") {
		t.Errorf("no fsck after a failed read-only look: %q", calls)
	}
}

// A music stick in one slot must not hide the STR stick in another.
func TestMountStickFindsTheSTRStickBesideAForeignOne(t *testing.T) {
	r := newStickRig(t)
	r.device("sda1", "song.mp3")
	r.mountedAt("sda1", "sda1")
	r.device("sdb1", "run.sh")
	out, calls := r.run()
	if !strings.Contains(out, "STR_STICK_DEV="+filepath.Join(r.root, "dev/sdb1")) {
		t.Fatalf("the STR stick on sdb1 was not found:\n%s\ncalls %q", out, calls)
	}
	for _, c := range calls {
		if strings.Contains(c, "sda1") {
			t.Errorf("the music stick on sda1 was touched: %q", c)
		}
	}
}
