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
	repairStopSTR = func(string) error {
		s.stopped++
		s.events = append(s.events, "stop")
		return nil
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
	for _, p := range []string{"streborn-armv7l", "go-librespot", "STR_STOPPED"} {
		if !strings.Contains(stopSTRScript, p) {
			t.Errorf("stop script lost %q", p)
		}
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
		ErrMsg:  "The speaker restarted in the middle of the network install, before ST Reborn was copied onto it.\n\n" + repairBoxRestartedAdvice,
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
