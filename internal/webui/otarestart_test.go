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
	t.Parallel()
	cases := []struct {
		mode, running string
		want          bool
	}{
		{"bco", testAgentBin, true},
		{"bco", "/mnt/nv/streborn/bin/../bin/streborn-armv7l", true},
		{"bco", "/dev/shm/streborn-armv7l", false},
		// The rescue agent a failed swap started from RAM restarts onto the
		// NAND copy like the agent it stands in for.
		{"bco", "/dev/shm/streborn-rescue/streborn-armv7l", true},
		{"wlan0", "/dev/shm/streborn-rescue/streborn-armv7l", false},
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
		if got := selfRestartAfterOTA(c.mode, c.running, testAgentBin, testRescueBin); got != c.want {
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

// newOTASeamServer is a server whose update runs on recorded effects and its
// own state: nothing it does reaches the process or another test.
func newOTASeamServer(t *testing.T, mode, running string) (*otaSeams, *Server) {
	t.Helper()
	rec := &otaSeams{}
	dir := t.TempDir()
	fx := productionOTAEffects
	fx.paths.agentPIDFile = filepath.Join(dir, "agent.pid")
	fx.paths.agentHandOverMarker = filepath.Join(dir, "handover")
	fx.wlanMode = func() string { return mode }
	fx.runningBinary = func() string { return running }
	fx.reboot = func() error { rec.rebooted++; return rec.rebootErr }
	fx.startDetached = func(script string) (int, error) {
		rec.started = append(rec.started, script)
		if rec.startErr != nil {
			return 0, rec.startErr
		}
		return 31337, nil
	}
	fx.exit = func(code int) { rec.exited = append(rec.exited, code) }
	fx.agentAge = func() time.Duration { return time.Hour }
	fx.sleep = func(d time.Duration) { rec.slept = append(rec.slept, d) }
	if err := os.WriteFile(fx.paths.agentPIDFile, []byte("1234\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fx.shutdown = func() {
		rec.shutdowns++
		rec.pidAtShutdown = readFileTrim(fx.paths.agentPIDFile)
		rec.markerAtShutdown = readFileTrim(fx.paths.agentHandOverMarker)
	}
	s := &Server{logger: slog.New(slog.DiscardHandler), ota: &otaState{}, otaEffects: &fx}
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

func readPIDFile(s *Server) string {
	return readFileTrim(s.otaEffects.paths.agentPIDFile)
}

// The coprocessor Wi-Fi ST20: no reboot. A detached helper starts the new
// binary, holds run.sh's PID file so the watchdog does not start a second
// agent, the exit is marked as a hand-over, the Spotify engine is stopped so
// it is not orphaned, and the agent shuts down cleanly (not os.Exit, which
// would skip the Recently played flush and the mDNS goodbye).
func TestFinishAgentOTARestartsAgentOnBCO(t *testing.T) {
	t.Parallel()
	rec, s := newOTASeamServer(t, "bco", testAgentBin)
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
		productionOTAEffects.paths.agentLogPath,
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
	t.Parallel()
	rec, s := newOTASeamServer(t, "bco", testAgentBin)
	s.otaEffects.agentAge = func() time.Duration { return 100 * time.Second }
	s.otaEffects.sleep = func(d time.Duration) {
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
	t.Parallel()
	_, s := newOTASeamServer(t, "bco", testAgentBin)
	for _, c := range []struct{ age, want time.Duration }{
		{0, otaHandOverMinAgentAge},
		{60 * time.Second, otaHandOverMinAgentAge - 60*time.Second},
		{otaHandOverMinAgentAge, 0},
		{time.Hour, 0},
	} {
		s.otaEffects.agentAge = func() time.Duration { return c.age }
		if got := s.handOverDelay(); got != c.want {
			t.Errorf("handOverDelay at age %v = %v, want %v", c.age, got, c.want)
		}
	}
}

// A helper that cannot start still ends the process: run.sh's watchdog then
// respawns the agent from the same NAND path, and the PID file is left to it.
// No hand-over marker: nothing supervises a hand-over that did not happen.
func TestFinishAgentOTAHelperFailureLeavesItToTheWatchdog(t *testing.T) {
	t.Parallel()
	rec, s := newOTASeamServer(t, "bco", testAgentBin)
	rec.startErr = errors.New("fork failed")
	s.finishAgentOTA(testAgentBin)
	if rec.rebooted != 0 {
		t.Fatalf("rebooted %d times, want none", rec.rebooted)
	}
	if got := readPIDFile(s); got != "1234" {
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
			t.Parallel()
			rec, s := newOTASeamServer(t, c.mode, c.running)
			s.otaEffects.agentAge = func() time.Duration { return 0 }
			s.finishAgentOTA(testAgentBin)
			if rec.rebooted != 1 {
				t.Errorf("rebooted %d times, want 1", rec.rebooted)
			}
			if len(rec.started) != 0 || len(rec.exited) != 0 || rec.shutdowns != 0 || rec.engineOff != 0 || len(rec.slept) != 0 {
				t.Errorf("reboot path also started %d helpers, exited %v, shut down %d times, stopped the engine %d times, slept %v",
					len(rec.started), rec.exited, rec.shutdowns, rec.engineOff, rec.slept)
			}
			if got := readPIDFile(s); got != "1234" {
				t.Errorf("PID file holds %q, want it untouched", got)
			}
			if _, err := os.Stat(s.otaEffects.paths.agentHandOverMarker); err == nil {
				t.Errorf("reboot path wrote the hand-over marker")
			}
		})
	}
}

// A refused reboot keeps the old fallback: the detached self-restart after
// the TIME_WAIT sleep.
func TestFinishAgentOTARebootFailureFallsBack(t *testing.T) {
	t.Parallel()
	rec, s := newOTASeamServer(t, "wlan0", testAgentBin)
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
	t.Parallel()
	p := otaPaths{
		agentPIDFile:        "/mnt/nv/streborn/agent.pid",
		agentLogPath:        "/tmp/streborn-agent.log",
		agentSetupLogPath:   "/mnt/nv/streborn/setup.log",
		agentCrashLogPath:   "/mnt/nv/streborn/logs/agent-crash.log",
		agentHandOverMarker: "/tmp/streborn-agent-handover",
	}
	got := p.superviseNewAgentLines(testAgentBin, []string{"--presets", "/mnt/nv/streborn/presets.json", "--name", "it's"})
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

// installOTAAbandonSeams points the OTA-reboot marker at a temp file and sets
// the in-flight mark the way a running update has it.
func installOTAAbandonSeams(t *testing.T, s *Server) {
	t.Helper()
	oldMarker := otaRebootMarkerPath
	otaRebootMarkerPath = filepath.Join(t.TempDir(), "ota-reboot")
	if err := os.WriteFile(otaRebootMarkerPath, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.ota.beginUpdate()
	t.Cleanup(func() { otaRebootMarkerPath = oldMarker })
}

// A refused reboot whose self-restart cannot start either leaves this agent
// running. It must go back into service: before, the in-flight mark stayed set
// and the speaker refused engine deliveries and self-updates until it was
// unplugged.
func TestOTAHandOverThatCannotRestartPutsTheAgentBackIntoService(t *testing.T) {
	rec, s := newOTASeamServer(t, "wlan0", testAgentBin)
	installOTAAbandonSeams(t, s)
	rec.rebootErr = errors.New("reboot: not found")
	rec.startErr = errors.New("fork failed")
	s.selfUpd.set("rebooting", "")
	splashStopped := 0

	h := s.finishAgentOTA(testAgentBin)
	if h != otaHandOverFailed {
		t.Fatalf("hand-over = %v, want otaHandOverFailed", h)
	}
	if ref := s.engineBlockedByUpdate(); ref == nil {
		t.Fatal("precondition: the engine delivery is not refused while the update is in flight")
	}
	s.settleOTAHandOver(h, func() { splashStopped++ })

	if s.ota.updating() {
		t.Error("the update is still marked in flight: the speaker would refuse engine deliveries and updates until a power cycle")
	}
	if ref := s.engineBlockedByUpdate(); ref != nil {
		t.Errorf("engine delivery still refused: %+v", ref)
	}
	if splashStopped != 1 {
		t.Errorf("update screen handed back %d times, want 1", splashStopped)
	}
	if _, err := os.Stat(otaRebootMarkerPath); err == nil {
		t.Error("the OTA-reboot marker survived a reboot that never happened")
	}
	if len(rec.exited) != 0 {
		t.Errorf("exits = %v, want none: nothing would start the agent again", rec.exited)
	}
	s.selfUpd.mu.Lock()
	state, msg := s.selfUpd.state, s.selfUpd.err
	s.selfUpd.mu.Unlock()
	if state != "failed" || !strings.Contains(msg, "Unplug") {
		t.Errorf("self-update state = %q (%q), want failed with the unplug advice", state, msg)
	}
}

// An accepted reboot normally ends the process inside the grace. An agent
// that outlives it falls back to the self-restart, and stays in flight
// because it is on its way out.
func TestOTAHandOverRebootThatNeverComesFallsBackToSelfRestart(t *testing.T) {
	rec, s := newOTASeamServer(t, "wlan0", testAgentBin)
	installOTAAbandonSeams(t, s)
	h := s.finishAgentOTA(testAgentBin)
	if h != otaHandOverRebooting {
		t.Fatalf("hand-over = %v, want otaHandOverRebooting", h)
	}
	s.settleOTAHandOver(h, func() { t.Error("update screen handed back although the agent is restarting") })
	if len(rec.slept) != 1 || rec.slept[0] != otaRebootGrace {
		t.Errorf("waited %v, want one wait of %v", rec.slept, otaRebootGrace)
	}
	if len(rec.started) != 1 || !strings.HasPrefix(rec.started[0], "sleep 70 && exec ") {
		t.Fatalf("fallback helpers = %q, want one sleep 70 self-restart", rec.started)
	}
	if len(rec.exited) != 1 {
		t.Errorf("exits = %v, want one", rec.exited)
	}
	if !s.ota.updating() {
		t.Error("the in-flight mark was cleared although the agent is exiting for the new binary")
	}
}

// The same reboot that never comes, with no way to self-restart: back into
// service.
func TestOTAHandOverRebootThatNeverComesAndNoFallback(t *testing.T) {
	rec, s := newOTASeamServer(t, "wlan0", testAgentBin)
	installOTAAbandonSeams(t, s)
	rec.startErr = errors.New("fork failed")
	s.settleOTAHandOver(s.finishAgentOTA(testAgentBin), nil)
	if s.ota.updating() {
		t.Error("the update is still marked in flight")
	}
}

// A hand-over that ends the process settles nothing.
func TestOTAHandOverExitingIsLeftAlone(t *testing.T) {
	rec, s := newOTASeamServer(t, "bco", testAgentBin)
	installOTAAbandonSeams(t, s)
	s.settleOTAHandOver(s.finishAgentOTA(testAgentBin), func() { t.Error("update screen handed back") })
	if len(rec.slept) != 0 || !s.ota.updating() {
		t.Errorf("slept %v, in flight %v; want no wait and the flag kept", rec.slept, s.ota.updating())
	}
}

// A desktop push has no self-update to fail.
func TestFailIfRebootingLeavesOtherStatesAlone(t *testing.T) {
	t.Parallel()
	var u selfUpdater
	u.set("idle", "")
	u.failIfRebooting("x")
	if u.state != "idle" || u.err != "" {
		t.Errorf("state = %q err = %q, want idle untouched", u.state, u.err)
	}
}
