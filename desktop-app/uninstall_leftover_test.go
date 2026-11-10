package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
const uninstallSandboxStubs = `SB=%[1]s
NV=%[2]s
sleep() { :; }
sync() { :; }
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
	script := uninstallScript
	for _, r := range [][2]string{
		{"/mnt/nv", sb.nv},
		{"/media/sd", sb.media + "/sd"},
		{"/proc/", sb.proc + "/"},
	} {
		script = strings.ReplaceAll(script, r[0], r[1])
	}
	stubs := strings.ReplaceAll(strings.ReplaceAll(uninstallSandboxStubs, "%[1]s", sb.root), "%[2]s", sb.nv)
	file := sb.root + "/uninstall.sh"
	if err := os.WriteFile(file, []byte(stubs+script), 0o755); err != nil {
		sb.t.Fatal(err)
	}
	out, err := exec.Command(sb.sh, file).CombinedOutput()
	if err != nil {
		sb.t.Fatalf("the script did not run to its end: %v\n%s", err, out)
	}
	return string(out)
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
	})
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
