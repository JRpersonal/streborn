package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/boxmem"
	"golang.org/x/crypto/ssh"
)

// TestRepairFileSetLeavesTheEngineOut pins the first half of the 2026-10-26
// ST20 fix: the SSH repair staged the ~16 MB go-librespot engine into the
// speaker's RAM although install.sh never uses it, and that dead weight is
// what tipped a 122 MB speaker over while install.sh copied the agent.
func TestRepairFileSetLeavesTheEngineOut(t *testing.T) {
	files, err := repairFileSet([]byte("agent"), "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["go-librespot"]; ok {
		t.Fatal("the repair file set must not carry the go-librespot engine")
	}
	for _, want := range []string{"install.sh", "run.sh", "rc.local", "streborn-armv7l", "version.txt"} {
		if _, ok := files[want]; !ok {
			t.Errorf("repair file set lost %s", want)
		}
	}
}

func TestParseMemAvailable(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"MemAvailable:      49448 kB\n", 49448 * 1024, true},
		{"Warning: Permanently added 'h' (RSA) to the list of known hosts.\nMemAvailable: 100 kB", 100 * 1024, true},
		{"", 0, false},
		{"MemFree: 100 kB", 0, false},
		{"MemAvailable: x kB", 0, false},
	}
	for _, c := range cases {
		got, ok := parseMemAvailable(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseMemAvailable(%q) = %d,%v want %d,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// repairSeams scripts the staging chooser's view of a speaker.
type repairSeams struct {
	mem      []int64 // successive MemAvailable reads; the last one repeats
	reads    int
	stopped  int
	events   []string
	free     map[string]int64
	stageErr map[string]error
	noSTR    bool  // the speaker runs no STR (a stock speaker's first install)
	stopErr  error // the stop's SSH call failed
}

func (s *repairSeams) install(t *testing.T) {
	t.Helper()
	oFree, oMem, oStop, oUp := repairFreeBytes, repairMemAvailable, repairStopSTR, repairStageUpload
	t.Cleanup(func() {
		repairFreeBytes, repairMemAvailable, repairStopSTR, repairStageUpload = oFree, oMem, oStop, oUp
	})
	repairFreeBytes = func(_, path string) int64 { return s.free[path] }
	repairMemAvailable = func(string) (int64, bool) {
		i := s.reads
		if i >= len(s.mem) {
			i = len(s.mem) - 1
		}
		s.reads++
		s.events = append(s.events, "mem")
		return s.mem[i], true
	}
	repairStopSTR = func(string) (strStopResult, error) {
		s.stopped++
		s.events = append(s.events, "stop")
		return strStopResult{found: !s.noSTR && s.stopErr == nil}, s.stopErr
	}
	repairStageUpload = func(_ *App, _, dir string, _ map[string][]byte) error {
		s.events = append(s.events, "stage "+dir)
		base := strings.TrimSuffix(dir, "/streborn-install")
		return s.stageErr[base]
	}
}

const mb = int64(1 << 20)

func roomyTmpfs() map[string]int64 {
	return map[string]int64{"/dev/shm": 60 * mb, "/run": 60 * mb, "/tmp": 15 * mb, "/mnt/nv": 20 * mb}
}

// With plenty of RAM nothing is stopped and /dev/shm wins, as before.
func TestRepairStageRoomyRAMStagesInShmWithoutStopping(t *testing.T) {
	s := &repairSeams{mem: []int64{80 * mb}, free: roomyTmpfs()}
	s.install(t)
	stage, stopped, err := quietApp().stageRepairFilesBestEffort("h", 20*mb, nil)
	if err != nil || stage != "/dev/shm/streborn-install" || stopped {
		t.Fatalf("got stage=%q stopped=%v err=%v", stage, stopped, err)
	}
	if s.stopped != 0 {
		t.Fatal("a speaker with room must keep its agent running")
	}
}

// The 2026-10-26 ST20: MemAvailable too low for the stage. The agent stop has
// to run BEFORE anything is staged, and once it freed enough RAM the stage
// goes to RAM as usual.
func TestRepairStageLowRAMStopsAgentBeforeStaging(t *testing.T) {
	s := &repairSeams{mem: []int64{30 * mb, 60 * mb}, free: roomyTmpfs()}
	s.install(t)
	stage, stopped, err := quietApp().stageRepairFilesBestEffort("h", 20*mb, nil)
	if err != nil || stage != "/dev/shm/streborn-install" || !stopped {
		t.Fatalf("got stage=%q stopped=%v err=%v", stage, stopped, err)
	}
	if s.stopped != 1 {
		t.Fatalf("agent stop ran %d times, want 1", s.stopped)
	}
	want := []string{"mem", "stop", "mem", "stage /dev/shm/streborn-install"}
	if fmt.Sprint(s.events) != fmt.Sprint(want) {
		t.Fatalf("order = %v, want %v", s.events, want)
	}
}

// Still short after the stop: no RAM target may be tried at all, NAND is the
// last resort, and when that fails too the error says it was memory, not the
// network (which would send the user after a truncation that never happened).
func TestRepairStageStillShortRejectsRAM(t *testing.T) {
	s := &repairSeams{
		mem:      []int64{25 * mb, 30 * mb},
		free:     roomyTmpfs(),
		stageErr: map[string]error{"/mnt/nv": &uploadTruncatedError{msg: "short"}},
	}
	s.install(t)
	_, stopped, err := quietApp().stageRepairFilesBestEffort("h", 20*mb, nil)
	if !stopped {
		t.Fatal("the agent stop must have been tried first")
	}
	for _, e := range s.events {
		if strings.HasPrefix(e, "stage /dev/shm") || strings.HasPrefix(e, "stage /run") || strings.HasPrefix(e, "stage /tmp") {
			t.Fatalf("RAM target tried although memory is short: %v", s.events)
		}
	}
	var ramShort *repairRAMShortError
	if !errors.As(err, &ramShort) {
		t.Fatalf("want repairRAMShortError, got %T %v", err, err)
	}
	var allTrunc *allTargetsTruncatedError
	if errors.As(err, &allTrunc) {
		t.Fatal("a memory rejection must not read as an all-targets-truncated network failure")
	}
}

// A stock speaker on its first install runs no STR: the stop finds nothing,
// so nothing was stopped and a later failure must not restart the speaker
// (that restart would skip the SDK URL heal on a :17000-unlocked speaker).
func TestRepairStageStockSpeakerIsNotMarkedStopped(t *testing.T) {
	s := &repairSeams{mem: []int64{30 * mb, 60 * mb}, free: roomyTmpfs(), noSTR: true}
	s.install(t)
	_, stopped, err := quietApp().stageRepairFilesBestEffort("h", 20*mb, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.stopped != 1 {
		t.Fatalf("the stop must still run, ran %d times", s.stopped)
	}
	if stopped {
		t.Fatal("a speaker that ran no STR must not be reported as stopped")
	}
}

// A stop whose SSH call failed may have killed the agent anyway: it counts as
// stopped, so a failed install restarts the speaker rather than strand it.
func TestRepairStageUnknownStopCountsAsStopped(t *testing.T) {
	s := &repairSeams{mem: []int64{30 * mb, 60 * mb}, free: roomyTmpfs(), stopErr: errors.New("ssh: handshake failed")}
	s.install(t)
	_, stopped, _ := quietApp().stageRepairFilesBestEffort("h", 20*mb, nil)
	if !stopped {
		t.Fatal("an unknown stop outcome must count as stopped")
	}
}

func TestParseSTRStop(t *testing.T) {
	if r := parseSTRStop("STR_STOPPED\n"); r.found || r.respawned || r.stillRunning {
		t.Errorf("bare STR_STOPPED = %+v, want nothing found", r)
	}
	r := parseSTRStop("STR_RESPAWN_KILLED\nSTR_RESPAWN_KILLED\nSTR_STILL_RUNNING\nSTR_FOUND\nSTR_STOPPED\n")
	if !r.found || !r.respawned || !r.stillRunning {
		t.Errorf("all markers = %+v, want all set", r)
	}
}

func TestRepairRAMRoomForUsesSharedFloor(t *testing.T) {
	need := 20 * mb
	if !repairRAMRoomFor(need+boxmem.RAMStageFloor, true, need) {
		t.Error("exactly the floor left must pass")
	}
	if repairRAMRoomFor(need+boxmem.RAMStageFloor-1, true, need) {
		t.Error("one byte under the floor must fail")
	}
	if !repairRAMRoomFor(0, false, need) {
		t.Error("an unknown MemAvailable must not block (old behaviour)")
	}
}

func TestStopSTRScriptEndsWatchdogsFirst(t *testing.T) {
	rm := strings.Index(stopSTRScript, "rm -f /mnt/nv/streborn/agent.pid")
	kill := strings.Index(stopSTRScript, "killall")
	if rm < 0 || kill < 0 || rm > kill {
		t.Fatal("the pidfile must go before the kill, or a run.sh watchdog respawns the agent")
	}
	for _, p := range []string{"streborn-armv7l", "go-librespot", "STR_STOPPED", "STR_FOUND", "STR_RESPAWN_KILLED", "STR_STILL_RUNNING"} {
		if !strings.Contains(stopSTRScript, p) {
			t.Errorf("stop script lost %q", p)
		}
	}
	// STR_FOUND must come from a check taken BEFORE the first kill, or a
	// speaker that ran STR would read as stock.
	if found, firstKill := strings.Index(stopSTRScript, "found=1"), strings.Index(stopSTRScript, "killall"); found < 0 || found > firstKill {
		t.Error("the STR_FOUND check must run before the first kill")
	}
	// The sweep that catches a watchdog respawn has to remove the pidfile
	// again and kill inside a loop after the second pass.
	loop := strings.Index(stopSTRScript, "while [")
	if loop < 0 {
		t.Fatal("stop script lost its respawn sweep")
	}
	sweep := stopSTRScript[loop:]
	if !strings.Contains(sweep, "rm -f /mnt/nv/streborn/agent.pid") || !strings.Contains(sweep, "killall -9") {
		t.Error("the respawn sweep must remove the pidfile and kill again")
	}
}

// Source checks for the paths a unit test cannot reach without a speaker.
func TestRepairRestartPathsBringTheAgentBack(t *testing.T) {
	src := readInstallSrc(t)
	repair := funcBody(t, src, "func (a *App) RepairInstallViaSSH(")
	i := strings.Index(repair, `res.Code = "repair-box-restarted"`)
	if i < 0 {
		t.Fatal("repair-box-restarted branch is gone; if it moved, move this test with it")
	}
	branch := repair[i:]
	if j := strings.Index(branch, "return res, nil"); j > 0 {
		branch = branch[:j]
	}
	if !strings.Contains(branch, "restartIfStopped()") {
		t.Error("the restart branch must restart a speaker whose agent this repair stopped (an sshd-only OOM kill leaves the speaker up)")
	}
	restart := repair[strings.Index(repair, "restartIfStopped := func()"):]
	heal, reboot := strings.Index(restart, "healSDKCloudURLs"), strings.Index(restart, "boxReboot")
	if heal < 0 || reboot < 0 || heal > reboot {
		t.Error("restartIfStopped must heal the SDK cloud URLs before its reboot")
	}

	helper := funcBody(t, src, "func (a *App) repairAndAwaitAgent(")
	short := strings.Index(helper, `rr.Code == "repair-box-restarted"`)
	diag := strings.Index(helper, "diagOf := func()")
	if short < 0 || diag < 0 || short > diag {
		t.Error("repairAndAwaitAgent must end the install on a speaker restart before any further probing, on every site")
	}
}

func TestBoxRestartedMidCommand(t *testing.T) {
	if !boxRestartedMidCommand(fmt.Errorf("run: %w", &ssh.ExitMissingError{})) {
		t.Error("a wrapped ExitMissingError is a restarted speaker")
	}
	if !boxRestartedMidCommand(errors.New("wait: remote command exited without exit status or exit signal")) {
		t.Error("the flattened text must match too")
	}
	for _, e := range []error{nil, errors.New("Process exited with status 1"), errors.New("ssh native timeout after 5m0s")} {
		if boxRestartedMidCommand(e) {
			t.Errorf("%v must not read as a restart", e)
		}
	}
}

// The failure report for this case: the restart paragraph leads, the
// post-install "after the install rebooted it" paragraph and its ST30/Portable
// stick sentence do not appear.
func TestReportForRepairRestartDropsPostInstallAndStickAdvice(t *testing.T) {
	r := failureReport{
		Phase:   "install:repair-box-restarted",
		History: "repair_ssh: staged install files",
		ErrMsg:  "The speaker restarted in the middle of the network install, while ST Reborn was being copied onto it.\n\n" + repairBoxRestartedAdvice,
		Facts:   installFacts{PingRan: true, PingAlive: true},
	}
	out := formatFailureReport(r)
	i := strings.Index(out, "what to try")
	if i < 0 {
		t.Fatalf("no advice section:\n%s", out)
	}
	advice := out[i:]
	if !strings.Contains(advice, repairBoxRestartedAdvice) {
		t.Error("the restart advice must be in the advice section")
	}
	if strings.Contains(out, "after the install rebooted it") {
		t.Error("must not claim the install rebooted the speaker")
	}
	if strings.Contains(out, "ST30 or Portable") {
		t.Error("must not give ST30/Portable stick advice here")
	}
}
