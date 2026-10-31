package webui

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testAgentBin = "/mnt/nv/streborn/bin/streborn-armv7l"

// Only wlan-mode "bco" restarts the agent instead of rebooting, and only while
// the agent runs from the binary the update replaced. The Portable
// (taigan-bco), the Linux Wi-Fi chassis and an unreadable mode keep the reboot.
func TestSelfRestartAfterOTA(t *testing.T) {
	cases := []struct {
		mode, running string
		want          bool
	}{
		{"bco", testAgentBin, true},
		{"bco", "/mnt/nv/streborn/bin/../bin/streborn-armv7l", true},
		{"bco", "/dev/shm/streborn-armv7l", false},
		{"bco", "/media/sda1/streborn-armv7l", false},
		{"bco", "", false},
		{"taigan-bco", testAgentBin, false},
		{"wlan0", testAgentBin, false},
		{"wlan1", testAgentBin, false},
		{"ethernet-only", testAgentBin, false},
		{"", testAgentBin, false},
		{"BCO", testAgentBin, false},
	}
	for _, c := range cases {
		if got := selfRestartAfterOTA(c.mode, c.running, testAgentBin); got != c.want {
			t.Errorf("selfRestartAfterOTA(%q, %q) = %v, want %v", c.mode, c.running, got, c.want)
		}
	}
}

// otaSeams records what the hand-over did instead of doing it.
type otaSeams struct {
	rebooted  int
	rebootErr error
	started   []string
	startErr  error
	exited    []int
	shutdowns int
	engineOff int
	slept     []time.Duration
	// pidAtShutdown and markerAtShutdown are what run.sh's PID file and the
	// hand-over marker held when the clean shutdown began.
	pidAtShutdown    string
	markerAtShutdown string
}

func installOTASeams(t *testing.T, mode, running string) (*otaSeams, *Server) {
	t.Helper()
	rec := &otaSeams{}
	oldMode, oldRun, oldReboot, oldStart, oldExit := otaWLANMode, otaRunningBinary, otaReboot, otaStartDetached, otaExit
	oldShutdown, oldAge, oldSleep := otaShutdown, otaAgentAge, otaSleep
	oldPID, oldMarker := agentPIDFile, agentHandOverMarker
	t.Cleanup(func() {
		otaWLANMode, otaRunningBinary, otaReboot, otaStartDetached, otaExit = oldMode, oldRun, oldReboot, oldStart, oldExit
		otaShutdown, otaAgentAge, otaSleep = oldShutdown, oldAge, oldSleep
		agentPIDFile, agentHandOverMarker = oldPID, oldMarker
	})
	otaWLANMode = func() string { return mode }
	otaRunningBinary = func() string { return running }
	otaReboot = func() error { rec.rebooted++; return rec.rebootErr }
	otaStartDetached = func(script string) (int, error) {
		rec.started = append(rec.started, script)
		if rec.startErr != nil {
			return 0, rec.startErr
		}
		return 31337, nil
	}
	otaExit = func(code int) { rec.exited = append(rec.exited, code) }
	otaAgentAge = func() time.Duration { return time.Hour }
	otaSleep = func(d time.Duration) { rec.slept = append(rec.slept, d) }
	dir := t.TempDir()
	agentPIDFile = filepath.Join(dir, "agent.pid")
	agentHandOverMarker = filepath.Join(dir, "handover")
	if err := os.WriteFile(agentPIDFile, []byte("1234\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	otaShutdown = func() {
		rec.shutdowns++
		rec.pidAtShutdown = readFileTrim(agentPIDFile)
		rec.markerAtShutdown = readFileTrim(agentHandOverMarker)
	}
	s := &Server{logger: slog.New(slog.DiscardHandler)}
	s.spotifyStop = func() bool { rec.engineOff++; return true }
	return rec, s
}

func readFileTrim(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readPIDFile(t *testing.T) string {
	t.Helper()
	return readFileTrim(agentPIDFile)
}

// The coprocessor Wi-Fi ST20: no reboot. A detached helper starts the new
// binary, holds run.sh's PID file so the watchdog does not start a second
// agent, the exit is marked as a hand-over, the Spotify engine is stopped so
// it is not orphaned, and the agent shuts down cleanly (not os.Exit, which
// would skip the Recently played flush and the mDNS goodbye).
func TestFinishAgentOTARestartsAgentOnBCO(t *testing.T) {
	rec, s := installOTASeams(t, "bco", testAgentBin)
	s.finishAgentOTA(testAgentBin)
	if rec.rebooted != 0 {
		t.Fatalf("rebooted %d times on a bco speaker, want none", rec.rebooted)
	}
	if len(rec.started) != 1 {
		t.Fatalf("started %d helpers, want 1", len(rec.started))
	}
	script := rec.started[0]
	for _, want := range []string{
		"/proc/",  // waits for this agent to exit
		"kill -9", // and does not wait forever
		"'" + testAgentBin + "'",
		`wait "$bpid"`, // supervises the new agent instead of exec'ing it
		"agent.pid",    // a temp path here; %q doubles Windows backslashes
		"handover",
		agentLogPath,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("restart helper is missing %q:\n%s", want, script)
		}
	}
	for _, forbidden := range []string{"reboot", "sleep 70", "run-override", "[[", "function ", "local "} {
		if strings.Contains(script, forbidden) {
			t.Errorf("restart helper contains %q:\n%s", forbidden, script)
		}
	}
	if rec.pidAtShutdown != "31337" {
		t.Errorf("PID file held %q at shutdown, want the helper's PID 31337", rec.pidAtShutdown)
	}
	if want := strconv.Itoa(os.Getpid()); rec.markerAtShutdown != want {
		t.Errorf("hand-over marker held %q at shutdown, want this process's PID %s", rec.markerAtShutdown, want)
	}
	if rec.engineOff != 1 {
		t.Errorf("engine stopped %d times, want 1", rec.engineOff)
	}
	if rec.shutdowns != 1 || len(rec.exited) != 0 {
		t.Errorf("clean shutdowns = %d, hard exits = %v; want one clean shutdown and no os.Exit", rec.shutdowns, rec.exited)
	}
	if len(rec.slept) != 0 {
		t.Errorf("an agent an hour old waited %v before the hand-over, want no wait", rec.slept)
	}
}

// An agent younger than run.sh's boot watchdog window keeps serving until it
// is out of it, and only then starts the helper: a helper alive but unbound
// inside that window would make the watchdog start a second agent.
func TestFinishAgentOTAWaitsOutTheBootWatchdog(t *testing.T) {
	rec, s := installOTASeams(t, "bco", testAgentBin)
	otaAgentAge = func() time.Duration { return 100 * time.Second }
	otaSleep = func(d time.Duration) {
		if len(rec.started) != 0 {
			t.Errorf("helper started before the wait")
		}
		rec.slept = append(rec.slept, d)
	}
	s.finishAgentOTA(testAgentBin)
	if len(rec.slept) != 1 || rec.slept[0] != otaHandOverMinAgentAge-100*time.Second {
		t.Errorf("waited %v, want one wait of %v", rec.slept, otaHandOverMinAgentAge-100*time.Second)
	}
	if len(rec.started) != 1 || rec.shutdowns != 1 {
		t.Errorf("after the wait: %d helpers, %d shutdowns; want 1 and 1", len(rec.started), rec.shutdowns)
	}
}

func TestHandOverDelay(t *testing.T) {
	old := otaAgentAge
	t.Cleanup(func() { otaAgentAge = old })
	for _, c := range []struct{ age, want time.Duration }{
		{0, otaHandOverMinAgentAge},
		{60 * time.Second, otaHandOverMinAgentAge - 60*time.Second},
		{otaHandOverMinAgentAge, 0},
		{time.Hour, 0},
	} {
		otaAgentAge = func() time.Duration { return c.age }
		if got := handOverDelay(); got != c.want {
			t.Errorf("handOverDelay at age %v = %v, want %v", c.age, got, c.want)
		}
	}
}

// A helper that cannot start still ends the process: run.sh's watchdog then
// respawns the agent from the same NAND path, and the PID file is left to it.
// No hand-over marker: nothing supervises a hand-over that did not happen.
func TestFinishAgentOTAHelperFailureLeavesItToTheWatchdog(t *testing.T) {
	rec, s := installOTASeams(t, "bco", testAgentBin)
	rec.startErr = errors.New("fork failed")
	s.finishAgentOTA(testAgentBin)
	if rec.rebooted != 0 {
		t.Fatalf("rebooted %d times, want none", rec.rebooted)
	}
	if got := readPIDFile(t); got != "1234" {
		t.Errorf("PID file holds %q, want it untouched", got)
	}
	if rec.markerAtShutdown != "" {
		t.Errorf("hand-over marker %q written without a helper", rec.markerAtShutdown)
	}
	if rec.shutdowns != 1 {
		t.Errorf("shutdowns = %d, want one", rec.shutdowns)
	}
}

// Every other chassis, and a bco speaker whose agent does not run from the
// replaced binary, reboots as before and starts no helper.
func TestFinishAgentOTARebootsElsewhere(t *testing.T) {
	for _, c := range []struct{ mode, running string }{
		{"wlan0", testAgentBin},
		{"wlan1", testAgentBin},
		{"taigan-bco", testAgentBin},
		{"ethernet-only", testAgentBin},
		{"", testAgentBin},
		{"bco", "/dev/shm/streborn-armv7l"},
	} {
		t.Run(c.mode+"|"+c.running, func(t *testing.T) {
			rec, s := installOTASeams(t, c.mode, c.running)
			otaAgentAge = func() time.Duration { return 0 }
			s.finishAgentOTA(testAgentBin)
			if rec.rebooted != 1 {
				t.Errorf("rebooted %d times, want 1", rec.rebooted)
			}
			if len(rec.started) != 0 || len(rec.exited) != 0 || rec.shutdowns != 0 || rec.engineOff != 0 || len(rec.slept) != 0 {
				t.Errorf("reboot path also started %d helpers, exited %v, shut down %d times, stopped the engine %d times, slept %v",
					len(rec.started), rec.exited, rec.shutdowns, rec.engineOff, rec.slept)
			}
			if got := readPIDFile(t); got != "1234" {
				t.Errorf("PID file holds %q, want it untouched", got)
			}
			if _, err := os.Stat(agentHandOverMarker); err == nil {
				t.Errorf("reboot path wrote the hand-over marker")
			}
		})
	}
}

// A refused reboot keeps the old fallback: the detached self-restart after
// the TIME_WAIT sleep.
func TestFinishAgentOTARebootFailureFallsBack(t *testing.T) {
	rec, s := installOTASeams(t, "wlan0", testAgentBin)
	rec.rebootErr = errors.New("reboot: not found")
	s.finishAgentOTA(testAgentBin)
	if len(rec.started) != 1 || !strings.HasPrefix(rec.started[0], "sleep 70 && exec ") {
		t.Fatalf("fallback helpers = %q, want one sleep 70 self-restart", rec.started)
	}
	if len(rec.exited) != 1 {
		t.Errorf("exits = %v, want one", rec.exited)
	}
}

// The helper's own guard: unless run.sh's PID file names the helper, it steps
// aside for the watchdog; otherwise it starts the new binary with run.sh's
// flags, quoted, as its child, hands the PID file to that child and records an
// exit that is not a hand-over in run.sh's NAND logs.
func TestSuperviseNewAgentLines(t *testing.T) {
	oldPID, oldLog, oldSetup, oldCrash, oldMarker := agentPIDFile, agentLogPath, agentSetupLogPath, agentCrashLogPath, agentHandOverMarker
	t.Cleanup(func() {
		agentPIDFile, agentLogPath, agentSetupLogPath, agentCrashLogPath, agentHandOverMarker = oldPID, oldLog, oldSetup, oldCrash, oldMarker
	})
	agentPIDFile = "/mnt/nv/streborn/agent.pid"
	agentLogPath = "/tmp/streborn-agent.log"
	agentSetupLogPath = "/mnt/nv/streborn/setup.log"
	agentCrashLogPath = "/mnt/nv/streborn/logs/agent-crash.log"
	agentHandOverMarker = "/tmp/streborn-agent-handover"
	got := superviseNewAgentLines(testAgentBin, []string{"--presets", "/mnt/nv/streborn/presets.json", "--name", "it's"})
	for _, want := range []string{
		`cur=$(cat "/mnt/nv/streborn/agent.pid" 2>/dev/null)`,
		`if [ -n "$cur" ] && [ "$cur" != "$$" ]; then exit 0; fi`,
		`'` + testAgentBin + `' '--presets' '/mnt/nv/streborn/presets.json' '--name' 'it'\''s' >> "/tmp/streborn-agent.log" 2>&1 &`,
		`echo "$bpid" > "/mnt/nv/streborn/agent.pid"`,
		`wait "$bpid"`,
		`if [ "$(cat "/tmp/streborn-agent-handover" 2>/dev/null)" = "$bpid" ]; then rm -f "/tmp/streborn-agent-handover"; exit 0; fi`,
		`>> "/mnt/nv/streborn/setup.log"`,
		`tail -n 40 "/tmp/streborn-agent.log"`,
		`>> "/mnt/nv/streborn/logs/agent-crash.log"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "exec ") {
		t.Errorf("the helper must supervise the new agent, not exec it:\n%s", got)
	}
}
