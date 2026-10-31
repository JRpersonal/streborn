package webui

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	engineOff int
}

func installOTASeams(t *testing.T, mode, running string) (*otaSeams, *Server) {
	t.Helper()
	rec := &otaSeams{}
	oldMode, oldRun, oldReboot, oldStart, oldExit, oldPID := otaWLANMode, otaRunningBinary, otaReboot, otaStartDetached, otaExit, agentPIDFile
	t.Cleanup(func() {
		otaWLANMode, otaRunningBinary, otaReboot, otaStartDetached, otaExit, agentPIDFile = oldMode, oldRun, oldReboot, oldStart, oldExit, oldPID
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
	agentPIDFile = filepath.Join(t.TempDir(), "agent.pid")
	if err := os.WriteFile(agentPIDFile, []byte("1234\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{logger: slog.New(slog.DiscardHandler)}
	s.spotifyStop = func() bool { rec.engineOff++; return true }
	return rec, s
}

func readPIDFile(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(agentPIDFile)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// The coprocessor Wi-Fi ST20: no reboot. A detached helper starts the new
// binary, holds run.sh's PID file so the watchdog does not start a second
// agent, the Spotify engine is stopped so it is not orphaned, and the agent
// exits.
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
		"exec '" + testAgentBin + "'",
		"agent.pid", // a temp path here; %q doubles Windows backslashes
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
	if got := readPIDFile(t); got != "31337" {
		t.Errorf("PID file holds %q, want the helper's PID 31337", got)
	}
	if rec.engineOff != 1 {
		t.Errorf("engine stopped %d times, want 1", rec.engineOff)
	}
	if len(rec.exited) != 1 || rec.exited[0] != 0 {
		t.Errorf("exits = %v, want one exit(0)", rec.exited)
	}
}

// A helper that cannot start still ends the process: run.sh's watchdog then
// respawns the agent from the same NAND path, and the PID file is left to it.
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
	if len(rec.exited) != 1 {
		t.Errorf("exits = %v, want one", rec.exited)
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
			s.finishAgentOTA(testAgentBin)
			if rec.rebooted != 1 {
				t.Errorf("rebooted %d times, want 1", rec.rebooted)
			}
			if len(rec.started) != 0 || len(rec.exited) != 0 || rec.engineOff != 0 {
				t.Errorf("reboot path also started %d helpers, exited %v, stopped the engine %d times",
					len(rec.started), rec.exited, rec.engineOff)
			}
			if got := readPIDFile(t); got != "1234" {
				t.Errorf("PID file holds %q, want it untouched", got)
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

// The helper's own guard: should the watchdog have started an agent after all,
// the helper steps aside instead of starting a second one; otherwise it claims
// the PID file and execs the new binary with run.sh's flags, quoted.
func TestExecNewAgentLines(t *testing.T) {
	old := agentPIDFile
	t.Cleanup(func() { agentPIDFile = old })
	agentPIDFile = "/mnt/nv/streborn/agent.pid"
	got := execNewAgentLines(testAgentBin, []string{"--presets", "/mnt/nv/streborn/presets.json", "--name", "it's"})
	for _, want := range []string{
		`cur=$(cat "/mnt/nv/streborn/agent.pid" 2>/dev/null)`,
		`[ "$cur" != "$$" ] && [ -d "/proc/$cur" ]; then exit 0; fi`,
		`echo "$$" > "/mnt/nv/streborn/agent.pid"`,
		`exec '` + testAgentBin + `' '--presets' '/mnt/nv/streborn/presets.json' '--name' 'it'\''s' >> "/tmp/streborn-agent.log" 2>&1`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
