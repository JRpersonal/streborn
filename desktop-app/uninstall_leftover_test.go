package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Remove reported "STR removed (1 entries)" on a SoundTouch 10 on 2026-11-10
// while the speaker itself had printed
//
//	rm: can't remove '/mnt/nv/streborn': Directory not empty
//
// Only rc.local was gone, the whole STR folder stayed. Two things were wrong:
// the script deleted the folder while the run script's watchdogs and the
// Spotify engine were still alive and writing into it, and it trusted rm's
// exit status per path, so one removed entry was enough for a success.
//
// The tests below run the real script text under sh against a sandbox tree
// (the paths are rewritten, the process tools are stubbed), then feed its
// output through the same parser UninstallSTR uses.

// uninstallSandbox is one fake speaker: a NAND tree, a stick mount point and a
// /proc with command lines.
type uninstallSandbox struct {
	t     *testing.T
	sh    string
	root  string // sandbox root, forward slashes
	nv    string // stands in for /mnt/nv
	media string // stands in for /media
	proc  string // stands in for /proc
}

func newUninstallSandbox(t *testing.T) *uninstallSandbox {
	t.Helper()
	// STR_TEST_SH runs the same cases under another shell, e.g. dash or an
	// ash, the speaker's own being BusyBox ash.
	name := os.Getenv("STR_TEST_SH")
	if name == "" {
		name = "sh"
	}
	sh, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("no %s on this machine, the script cannot be run here", name)
	}
	root := filepath.ToSlash(t.TempDir())
	if strings.ContainsAny(root, " \t'\"") {
		t.Skipf("temp dir %q has characters the unquoted sandbox paths cannot carry", root)
	}
	sb := &uninstallSandbox{t: t, sh: sh, root: root, nv: root + "/nv", media: root + "/media", proc: root + "/proc"}
	for _, d := range []string{sb.nv, sb.media + "/sda1", sb.proc} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return sb
}

func (sb *uninstallSandbox) write(path, content string) {
	sb.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		sb.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		sb.t.Fatal(err)
	}
}

// install lays down an STR install as the speaker has it.
func (sb *uninstallSandbox) install() {
	sb.write(sb.nv+"/rc.local", "#!/bin/sh\n")
	sb.write(sb.nv+"/streborn/run-override.sh", "#!/bin/sh\n")
	sb.write(sb.nv+"/streborn/bin/streborn-armv7l", "agent")
	sb.write(sb.nv+"/streborn/bin/go-librespot", "engine")
	sb.write(sb.nv+"/streborn/sp-cache/state.json", "{}")
	sb.write(sb.nv+"/streborn/logs/agent-crash.log", "x")
	sb.write(sb.nv+"/streborn/setup.log", "x")
	sb.write(sb.nv+"/streborn/agent.pid", "4711")
	// Bose's own state next to it must survive every run.
	sb.write(sb.nv+"/BoseApp-Persistence/1/NetworkProfiles.xml", "bose")
}

// process adds a fake /proc entry with the given argv.
func (sb *uninstallSandbox) process(pid string, argv ...string) {
	sb.write(sb.proc+"/"+pid+"/cmdline", strings.Join(argv, "\x00")+"\x00")
}

func (sb *uninstallSandbox) exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func (sb *uninstallSandbox) events() string {
	b, _ := os.ReadFile(sb.root + "/events")
	return string(b)
}

// uninstallSandboxStubs replaces what the script would do to the real machine.
// Files under $SB steer them:
//
//	running       the agent / engine "process" is alive (pidof finds it)
//	term-ignored  it ignores the polite killall and only dies to -9
//	recreate      N: a straggler writes setup.log back into the STR folder
//	              after each of the next N removals of that folder
//	rm-refuses    removing the STR folder or rc.local does nothing at all
//
// The reboot (/sbin/reboot in the script, rewritten by run) leaves the file
// "rebooted" behind. sleep returns at once, so the restart guard fires right
// away instead of after its two minutes.
const uninstallSandboxStubs = `SB=%[1]s
NV=%[2]s
sleep() { :; }
sync() { :; }
str_test_reboot() { echo reboot > "$SB/rebooted"; }
umount() { return 0; }
kill() {
  echo "kill $2" >> "$SB/events"
  command rm -rf "$SB/proc/$2"
}
killall() {
  echo "killall $*" >> "$SB/events"
  if [ "$1" = "-9" ] || [ ! -e "$SB/term-ignored" ]; then command rm -f "$SB/running"; fi
  return 0
}
pidof() { [ -e "$SB/running" ]; }
rm() {
  for _a in "$@"; do _last=$_a; done
  echo "rm $_last" >> "$SB/events"
  if [ -e "$SB/rm-refuses" ]; then
    case "$_last" in "$NV/streborn"|"$NV/rc.local") echo "rm: can't remove '$_last': Read-only file system" >&2; return 1 ;; esac
  fi
  command rm "$@"
  _rc=$?
  if [ "$_last" = "$NV/streborn" ] && [ -s "$SB/recreate" ]; then
    _n=$(cat "$SB/recreate")
    if [ "$_n" -gt 0 ]; then
      mkdir -p "$NV/streborn"
      echo late > "$NV/streborn/setup.log"
      echo $((_n-1)) > "$SB/recreate"
      echo "rm: can't remove '$_last': Directory not empty" >&2
      return 1
    fi
  fi
  return $_rc
}
`

// run executes uninstallScript inside the sandbox and returns its output.
func (sb *uninstallSandbox) run() string {
	sb.t.Helper()
	out, err := exec.Command(sb.sh, sb.scriptFile()).CombinedOutput()
	if err != nil {
		sb.t.Fatalf("the script did not run to its end: %v\n%s", err, out)
	}
	// Every run that got past the stick check has armed its restart guard,
	// and the guard is detached: wait for it here, so it is checked on every
	// run and never writes into a sandbox the test is already cleaning up.
	if !parseUninstallOutput(string(out)).stickPresent && !sb.waitRebooted() {
		sb.t.Fatalf("the script armed no restart guard, or the guard never rebooted\n%s", out)
	}
	return string(out)
}

// scriptFile writes uninstallScript into the sandbox, with the paths rewritten
// and the stubs in front, and returns the file.
func (sb *uninstallSandbox) scriptFile() string {
	sb.t.Helper()
	script := uninstallScript
	for _, r := range [][2]string{
		{"/mnt/nv", sb.nv},
		{"/media/sd", sb.media + "/sd"},
		{"/proc/", sb.proc + "/"},
		{"/sbin/reboot", "str_test_reboot"},
	} {
		script = strings.ReplaceAll(script, r[0], r[1])
	}
	// The script must never reach a real reboot on the machine running the
	// tests: every reboot in it has to be the one spelling rewritten above.
	for _, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.Contains(strings.ReplaceAll(line, "str_test_reboot", ""), "reboot") {
			sb.t.Fatalf("the script reboots in a spelling the sandbox does not stub: %q", line)
		}
	}
	stubs := strings.ReplaceAll(strings.ReplaceAll(uninstallSandboxStubs, "%[1]s", sb.root), "%[2]s", sb.nv)
	file := sb.root + "/uninstall.sh"
	if err := os.WriteFile(file, []byte(stubs+script), 0o755); err != nil {
		sb.t.Fatal(err)
	}
	return file
}

// waitRebooted reports whether the sandboxed speaker "rebooted" (the stub
// left its file), waiting a little for the detached guard.
func (sb *uninstallSandbox) waitRebooted() bool {
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, _ := os.ReadFile(sb.root + "/rebooted"); strings.Contains(string(b), "reboot") {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestUninstallScriptRemovesEverythingAndStopsTheWritersFirst(t *testing.T) {
	sb := newUninstallSandbox(t)
	sb.install()
	sb.write(sb.nv+"/sp-oauth.out", "x")
	sb.write(sb.nv+"/streborn-install/stage", "x")
	sb.write(sb.root+"/running", "")
	// The run script: its main shell and a watchdog subshell (same command
	// line), this script's own shell, and something that is none of STR's.
	sb.process("101", "/bin/sh", sb.nv+"/streborn/run-override.sh")
	sb.process("102", "/bin/sh", sb.nv+"/streborn/run-override.sh")
	sb.process("202", "sh", "-c", "echo STR_UNINSTALL_REMOVED; rm -rf "+sb.nv+"/streborn/run-override.sh")
	sb.process("303", "/opt/Bose/BoseApp")

	out := sb.run()
	rep := parseUninstallOutput(out)

	if got := rep.outcome(); got != uninstallRemoved {
		t.Fatalf("outcome = %v, want a full removal\n%s", got, out)
	}
	for _, p := range []string{"/streborn", "/rc.local", "/sp-oauth.out", "/streborn-install"} {
		if sb.exists(sb.nv + p) {
			t.Errorf("%s is still there", p)
		}
	}
	if len(rep.removed) != 4 || len(rep.leftover) != 0 {
		t.Errorf("removed = %v leftover = %v, want four removed and nothing left", rep.removed, rep.leftover)
	}
	if !sb.exists(sb.nv + "/BoseApp-Persistence/1/NetworkProfiles.xml") {
		t.Error("the Bose network state was touched; Remove must leave the speaker on its Wi-Fi")
	}
	if sb.exists(sb.proc+"/101") || sb.exists(sb.proc+"/102") {
		t.Error("a run script shell survived; its watchdogs write into the folder and restart the agent")
	}
	if !sb.exists(sb.proc + "/202") {
		t.Error("the script killed its own shell (a command line carrying its own markers)")
	}
	if !sb.exists(sb.proc + "/303") {
		t.Error("the script killed a process that is not STR's")
	}
	if sb.exists(sb.root + "/running") {
		t.Error("the agent / Spotify engine was left running")
	}

	// Stopping comes before deleting, for both the supervisors and the
	// agent + engine. This order is the fix.
	ev := sb.events()
	rmDir := strings.Index(ev, "rm "+sb.nv+"/streborn\n")
	if rmDir < 0 {
		t.Fatalf("the STR folder was never removed:\n%s", ev)
	}
	for _, first := range []string{"kill 101", "kill 102", "killall streborn-armv7l go-librespot"} {
		if i := strings.Index(ev, first); i < 0 || i > rmDir {
			t.Errorf("%q does not come before the removal of the folder:\n%s", first, ev)
		}
	}
	if !strings.Contains(ev, "go-librespot") {
		t.Error("the Spotify engine is not stopped; it keeps its state in the STR folder")
	}
}

func TestUninstallScriptKillsAnAgentThatIgnoresThePoliteStop(t *testing.T) {
	sb := newUninstallSandbox(t)
	sb.install()
	sb.write(sb.root+"/running", "")
	sb.write(sb.root+"/term-ignored", "")

	out := sb.run()

	if sb.exists(sb.root + "/running") {
		t.Fatalf("the agent survived the stop\n%s", out)
	}
	ev := sb.events()
	kill9 := strings.Index(ev, "killall -9 streborn-armv7l go-librespot")
	rmDir := strings.Index(ev, "rm "+sb.nv+"/streborn\n")
	if kill9 < 0 || rmDir < 0 || kill9 > rmDir {
		t.Errorf("the forced stop must come before the removal:\n%s", ev)
	}
	if got := parseUninstallOutput(out).outcome(); got != uninstallRemoved {
		t.Errorf("outcome = %v, want a full removal\n%s", got, out)
	}
}

func TestUninstallScriptRetriesWhenAStragglerWritesIntoTheFolder(t *testing.T) {
	sb := newUninstallSandbox(t)
	sb.install()
	// Exactly the field failure: rm ends in "Directory not empty" because a
	// file appeared while it walked the folder. Twice here, then it is quiet.
	sb.write(sb.root+"/recreate", "2")

	out := sb.run()
	rep := parseUninstallOutput(out)

	if sb.exists(sb.nv + "/streborn") {
		t.Fatalf("the folder is still there after the retries\n%s", out)
	}
	if got := rep.outcome(); got != uninstallRemoved {
		t.Errorf("outcome = %v, want a full removal\n%s", got, out)
	}
	if !strings.Contains(out, "STR_UNINSTALL_RETRIED:"+sb.nv+"/streborn 2") {
		t.Errorf("the log does not say that the removal needed retries:\n%s", out)
	}
}

func TestUninstallScriptReportsTheFolderItCouldNotRemove(t *testing.T) {
	sb := newUninstallSandbox(t)
	sb.install()
	sb.write(sb.root+"/recreate", "99") // never goes quiet

	out := sb.run()
	rep := parseUninstallOutput(out)

	if !sb.exists(sb.nv+"/streborn") || sb.exists(sb.nv+"/rc.local") {
		t.Fatalf("fixture: want the folder left and rc.local gone\n%s", out)
	}
	if len(rep.leftover) != 1 || rep.leftover[0] != sb.nv+"/streborn" {
		t.Errorf("leftover = %v, want only the STR folder", rep.leftover)
	}
	for _, p := range rep.removed {
		if p == sb.nv+"/streborn" {
			t.Error("the folder that is still there is listed as removed")
		}
	}
	if len(rep.removed) != 1 || rep.removed[0] != sb.nv+"/rc.local" {
		t.Errorf("removed = %v, want only rc.local", rep.removed)
	}
	if rep.stillStarts {
		t.Error("STR is reported as still starting although rc.local is gone")
	}
	// This is the run that used to be "STR removed (1 entries)".
	if got := rep.outcome(); got != uninstallIncomplete {
		t.Errorf("outcome = %v, want incomplete", got)
	}
	if !strings.Contains(out, "setup.log") {
		t.Errorf("the log does not show what was left in the folder:\n%s", out)
	}
}

func TestUninstallScriptSaysWhenSTRStillStarts(t *testing.T) {
	sb := newUninstallSandbox(t)
	sb.install()
	sb.write(sb.root+"/rm-refuses", "")

	out := sb.run()
	rep := parseUninstallOutput(out)

	if len(rep.leftover) != 2 || !rep.stillStarts {
		t.Fatalf("leftover = %v stillStarts = %v, want both paths left and STR still starting\n%s", rep.leftover, rep.stillStarts, out)
	}
	if len(rep.removed) != 0 {
		t.Errorf("removed = %v, want nothing", rep.removed)
	}
	if got := rep.outcome(); got != uninstallStillInstalled {
		t.Errorf("outcome = %v, want still installed", got)
	}
	if rep.alreadyClean {
		t.Error("a speaker with STR still on it was reported as clean")
	}
}

func TestUninstallScriptOnACleanSpeakerAndWithTheStickIn(t *testing.T) {
	t.Run("already clean", func(t *testing.T) {
		sb := newUninstallSandbox(t)
		out := sb.run()
		rep := parseUninstallOutput(out)
		if !rep.alreadyClean || rep.outcome() != uninstallClean {
			t.Errorf("a speaker with nothing on it was not reported as clean\n%s", out)
		}
	})
	t.Run("stick still inserted", func(t *testing.T) {
		sb := newUninstallSandbox(t)
		sb.install()
		sb.write(sb.root+"/running", "")
		sb.write(sb.media+"/sda1/run.sh", "#!/bin/sh\n")
		out := sb.run()
		if !parseUninstallOutput(out).stickPresent {
			t.Fatalf("the stick was not reported\n%s", out)
		}
		if !sb.exists(sb.nv+"/streborn/run-override.sh") || !sb.exists(sb.nv+"/rc.local") {
			t.Error("STR was removed although the stick would reinstall it on the next boot")
		}
		if sb.events() != "" || !sb.exists(sb.root+"/running") {
			t.Errorf("the script stopped or removed something before refusing:\n%s", sb.events())
		}
		// A refusal touches nothing, so it must not restart the speaker either.
		time.Sleep(300 * time.Millisecond)
		if parseUninstallOutput(out).guardArmed || sb.exists(sb.root+"/rebooted") {
			t.Errorf("the refusal armed the restart guard; the speaker would reboot for nothing\n%s", out)
		}
	})
}

// A review of the first version found the hole these tests close: the script
// kills the run script and the agent, and if the SSH call then fails or times
// out the app returned without a reboot. Nothing on the speaker starts STR
// again, so it sat there half removed and silent until a power cycle.

// The guard is armed after the stick check and before the first kill, and it
// outlasts the app's own wait for the script.
func TestUninstallScriptArmsItsRestartBeforeItStopsAnything(t *testing.T) {
	guard := strings.Index(uninstallScript, "/sbin/reboot")
	stick := strings.Index(uninstallScript, "STR_UNINSTALL_ABORT_STICK_PRESENT")
	firstStop := strings.Index(uninstallScript, "\nstop_supervisors\n")
	firstKillall := strings.Index(uninstallScript, "\nkillall ")
	if guard < 0 || stick < 0 || firstStop < 0 || firstKillall < 0 {
		t.Fatal("uninstallScript no longer has the shape this test reads")
	}
	if guard < stick {
		t.Error("the restart guard is armed before the stick check; a refusal would reboot the speaker")
	}
	if guard > firstStop || guard > firstKillall {
		t.Error("the restart guard is armed after the script starts killing; a session lost in between leaves the speaker stopped")
	}
	if strings.Count(uninstallScript, "/sbin/reboot") != 1 {
		t.Error("the script is expected to carry exactly one reboot, the guard")
	}

	line := uninstallScript[strings.LastIndex(uninstallScript[:guard], "\n")+1:]
	line = line[:strings.Index(line, "\n")]
	// Detached like rebootCmd: no handle of the SSH session, in the
	// background, and deaf to the hangup of a closing session.
	for _, want := range []string{"trap '' HUP", "sync;", "</dev/null >/dev/null 2>&1 &"} {
		if !strings.Contains(line, want) {
			t.Errorf("the guard line %q lacks %q", line, want)
		}
	}
	if want := fmt.Sprintf("sleep %d;", uninstallGuardSeconds); !strings.Contains(line, want) {
		t.Errorf("the guard line %q does not wait uninstallGuardSeconds (%q)", line, want)
	}
	if time.Duration(uninstallGuardSeconds)*time.Second <= uninstallScriptTimeout {
		t.Error("the guard fires while the app still waits for the script")
	}
	// The script's own shell is told apart from the run script by a marker in
	// its command line, and the kernel hands out only the first page of it.
	if stick > 1024 {
		t.Error("the first marker sits too deep in the script to be in its /proc cmdline")
	}

	sb := newUninstallSandbox(t)
	sb.install()
	out := sb.run() // run fails the test when no reboot follows
	if !parseUninstallOutput(out).guardArmed {
		t.Errorf("the script did not report its restart guard\n%s", out)
	}
}

// The session dies under the script: its output goes nowhere from the first
// line on. The removal still runs to its end and the speaker still restarts.
func TestUninstallScriptFinishesAndRestartsWhenTheSessionDrops(t *testing.T) {
	sb := newUninstallSandbox(t)
	sb.install()
	sb.write(sb.root+"/running", "")
	sb.process("101", "/bin/sh", sb.nv+"/streborn/run-override.sh")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close() // nobody reads: every echo of the script hits a dead pipe
	cmd := exec.Command(sb.sh, sb.scriptFile())
	cmd.Stdout = w
	cmd.Stderr = w
	runErr := cmd.Run()
	_ = w.Close()
	if runErr != nil {
		t.Logf("the script ended with %v (its last echo failed, as expected)", runErr)
	}

	if !sb.waitRebooted() {
		t.Fatalf("the speaker was left stopped: no reboot after the session dropped\nevents:\n%s", sb.events())
	}
	if sb.exists(sb.proc+"/101") || sb.exists(sb.root+"/running") {
		t.Errorf("the script died before it stopped STR:\n%s", sb.events())
	}
	for _, p := range []string{"/streborn", "/rc.local"} {
		if sb.exists(sb.nv + p) {
			t.Errorf("%s is still there: the script died with the session instead of finishing\n%s", p, sb.events())
		}
	}
}

// The app's side of the same hole, read from the source because the function
// needs a speaker behind SSH: a lost script run and a script that ended
// without its report both restart the speaker before they return.
func TestUninstallSTRRestartsTheSpeakerWhenTheScriptRunIsLost(t *testing.T) {
	b, err := os.ReadFile("uninstall_str.go")
	if err != nil {
		t.Fatal(err)
	}
	src := strings.ReplaceAll(string(b), "\r\n", "\n")
	fn := src[strings.Index(src, "func (a *App) UninstallSTR(host string)"):]
	fn = fn[:strings.Index(fn, "\nfunc ")]

	start := strings.Index(fn, "if err != nil && !rep.stickPresent {")
	if start < 0 {
		t.Fatal("UninstallSTR no longer has the shape this test reads")
	}
	lost := fn[start:]
	lost = lost[:strings.Index(lost, "\n\t}\n")]
	if !strings.Contains(lost, "boxReboot(host)") || !strings.Contains(lost, "return res") {
		t.Error("a failed or timed-out script run returns without restarting the speaker")
	}
	if strings.Contains(lost, "res.OK = true") || strings.Contains(lost, "markHostStock") {
		t.Error("a lost script run is treated as a removal")
	}

	silent := fn[strings.Index(fn, "case uninstallSilent:"):strings.Index(fn, "case uninstallStillInstalled:")]
	if !strings.Contains(silent, "boxReboot(host)") || !strings.Contains(silent, "return res") {
		t.Error("a script that ended without its report returns without restarting the speaker")
	}

	for _, armed := range []bool{true, false} {
		msg := uninstallInterruptedMessage("connection lost", armed)
		for _, want := range []string{"connection lost", "restarts by itself", "two minutes", "Remove again"} {
			if !strings.Contains(msg, want) {
				t.Errorf("armed=%v: the message %q lacks %q", armed, msg, want)
			}
		}
		if strings.Contains(msg, "STR removed") {
			t.Errorf("armed=%v: the message reads like a success: %q", armed, msg)
		}
	}
}

// The result tells the frontend which of the two leftover outcomes it is, and
// an incomplete removal mentions the kept preset keys like a full one does.
func TestUninstallSTRMarksStillInstalledAndKeepsThePresetNote(t *testing.T) {
	b, err := os.ReadFile("uninstall_str.go")
	if err != nil {
		t.Fatal(err)
	}
	src := strings.ReplaceAll(string(b), "\r\n", "\n")
	fn := src[strings.Index(src, "func (a *App) UninstallSTR(host string)"):]
	fn = fn[:strings.Index(fn, "\nfunc ")]

	still := fn[strings.Index(fn, "case uninstallStillInstalled:"):strings.Index(fn, "case uninstallIncomplete:")]
	if !strings.Contains(still, "res.StillInstalled = true") {
		t.Error("the still-installed outcome is not marked in the result")
	}
	if strings.Count(fn, "res.StillInstalled = true") != 1 {
		t.Error("StillInstalled is set outside its own outcome")
	}
	incomplete := fn[strings.Index(fn, "if res.Incomplete {"):]
	incomplete = incomplete[:strings.Index(incomplete, "return res")]
	if !strings.Contains(incomplete, "uninstallLeftoverMessage(rep) + stashedPresetsNote(stashed)") {
		t.Error("an incomplete removal does not say that the preset keys were kept")
	}

	if got := stashedPresetsNote(0); got != "" {
		t.Errorf("no keys kept, but the note says %q", got)
	}
	if got := stashedPresetsNote(6); !strings.Contains(got, "6 preset keys were kept on this PC") {
		t.Errorf("the note for six kept keys reads %q", got)
	}
}

func TestUninstallOutcome(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want uninstallOutcome
	}{
		{"everything removed", "STR_UNINSTALL_REMOVED: /mnt/nv/streborn /mnt/nv/rc.local\n", uninstallRemoved},
		{"already clean", "STR_UNINSTALL_ALREADY_CLEAN\nSTR_UNINSTALL_REMOVED:\n", uninstallClean},
		{
			// The 2026-11-10 run, as the fixed script reports it.
			"folder left, rc.local removed",
			"rm: can't remove '/mnt/nv/streborn': Directory not empty\n" +
				"STR_UNINSTALL_RETRIED:/mnt/nv/streborn 5\n" +
				"STR_UNINSTALL_LEFTOVER: /mnt/nv/streborn\nleft behind:\n-rw-r--r-- 1 root root 12 setup.log\n" +
				"STR_UNINSTALL_REMOVED: /mnt/nv/rc.local\n",
			uninstallIncomplete,
		},
		{
			"nothing came off",
			"STR_UNINSTALL_LEFTOVER: /mnt/nv/streborn /mnt/nv/rc.local\nSTR_UNINSTALL_STILL_STARTS\nSTR_UNINSTALL_REMOVED:\n",
			uninstallStillInstalled,
		},
		{"CRLF line ends", "STR_UNINSTALL_LEFTOVER: /mnt/nv/streborn\r\nSTR_UNINSTALL_REMOVED: /mnt/nv/rc.local\r\n", uninstallIncomplete},
		{"script died early", "some unrelated chatter\n", uninstallSilent},
		{"empty", "", uninstallSilent},
		{
			// A marker only counts at the start of a line.
			"marker text inside another line",
			"rm: can't remove 'STR_UNINSTALL_REMOVED: x'\nls: STR_UNINSTALL_ALREADY_CLEAN\n",
			uninstallSilent,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseUninstallOutput(tc.out).outcome(); got != tc.want {
				t.Errorf("outcome = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUninstallLeftoverMessageTellsTheUserWhatToDo(t *testing.T) {
	gone := uninstallLeftoverMessage(uninstallReport{leftover: []string{"/mnt/nv/streborn"}})
	for _, want := range []string{"/mnt/nv/streborn", "no longer starts", "could not be deleted", "install STR on it again", "remove it once more"} {
		if !strings.Contains(gone, want) {
			t.Errorf("the incomplete message lacks %q: %s", want, gone)
		}
	}
	// After the restart that speaker is a stock one: a second Remove would
	// only answer "already removed", so the message must not send the user
	// there.
	if strings.Contains(gone, "run Remove again") {
		t.Errorf("the incomplete message points at a second Remove, which cannot reach the files: %s", gone)
	}

	still := uninstallLeftoverMessage(uninstallReport{leftover: []string{"/mnt/nv/streborn", "/mnt/nv/rc.local"}, stillStarts: true})
	for _, want := range []string{"/mnt/nv/streborn, /mnt/nv/rc.local", "could not be removed", "restarting", "run Remove again"} {
		if !strings.Contains(still, want) {
			t.Errorf("the still-installed message lacks %q: %s", want, still)
		}
	}
	for _, m := range []string{gone, still} {
		if strings.Contains(m, "STR removed") {
			t.Errorf("a leftover outcome reads like a success: %s", m)
		}
	}
}

// The mapping inside UninstallSTR, read from the source because the function
// needs a speaker behind SSH: an incomplete removal is never OK, and both
// leftover outcomes still restart the speaker (the script has stopped the
// agent and its watchdogs, nothing would bring them back otherwise).
func TestUninstallSTRNeverReportsALeftoverAsSuccess(t *testing.T) {
	b, err := os.ReadFile("uninstall_str.go")
	if err != nil {
		t.Fatal(err)
	}
	src := strings.ReplaceAll(string(b), "\r\n", "\n")
	fn := src[strings.Index(src, "func (a *App) UninstallSTR(host string)"):]
	fn = fn[:strings.Index(fn, "\nfunc ")]

	still := fn[strings.Index(fn, "case uninstallStillInstalled:"):strings.Index(fn, "case uninstallIncomplete:")]
	if !strings.Contains(still, "boxReboot(host)") || !strings.Contains(still, "return res") {
		t.Error("a removal that left STR installed must restart the speaker and return without success")
	}
	if strings.Contains(still, "res.OK = true") || strings.Contains(still, "markHostStock") {
		t.Error("a speaker STR still starts on is treated as removed")
	}

	incomplete := strings.Index(fn, "if res.Incomplete {")
	okLine := strings.Index(fn, "res.Step = \"done\"\n\tres.OK = true")
	reboot := strings.Index(fn, "res.Step = \"reboot\"")
	if incomplete < 0 || okLine < 0 || reboot < 0 {
		t.Fatal("UninstallSTR no longer has the shape this test reads")
	}
	if incomplete < reboot || incomplete > okLine {
		t.Error("the incomplete outcome must return after the reboot and before the success is set")
	}
	if !strings.Contains(fn[incomplete:okLine], "return res") {
		t.Error("the incomplete outcome falls through into the success")
	}
}
