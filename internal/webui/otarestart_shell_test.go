//go:build linux

package webui

// The hand-over helpers are shell scripts generated in Go, so shellcheck in CI
// never sees them. These tests run them through the system sh against temp
// paths and a fake agent, which is the closest thing to the box short of a
// live update. They need /proc, hence linux only.

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type helperRun struct {
	dir                                string
	bin, argsOut, pidFile, log, marker string
	setupLog, crashLog                 string
}

// newHelperRun points every path the helpers touch at a temp dir and writes a
// fake agent: it records its PID and flags, logs a line and runs body.
func newHelperRun(t *testing.T, body string) *helperRun {
	t.Helper()
	if _, err := os.Stat("/proc/self"); err != nil {
		t.Skip("needs /proc")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs sh")
	}
	dir := t.TempDir()
	r := &helperRun{
		dir:      dir,
		bin:      filepath.Join(dir, "streborn-armv7l"),
		argsOut:  filepath.Join(dir, "agent-args"),
		pidFile:  filepath.Join(dir, "agent.pid"),
		log:      filepath.Join(dir, "streborn-agent.log"),
		marker:   filepath.Join(dir, "handover"),
		setupLog: filepath.Join(dir, "setup.log"),
		crashLog: filepath.Join(dir, "agent-crash.log"),
	}
	oldPID, oldLog, oldSetup, oldCrash, oldMarker := agentPIDFile, agentLogPath, agentSetupLogPath, agentCrashLogPath, agentHandOverMarker
	t.Cleanup(func() {
		agentPIDFile, agentLogPath, agentSetupLogPath, agentCrashLogPath, agentHandOverMarker = oldPID, oldLog, oldSetup, oldCrash, oldMarker
	})
	agentPIDFile, agentLogPath, agentSetupLogPath, agentCrashLogPath, agentHandOverMarker = r.pidFile, r.log, r.setupLog, r.crashLog, r.marker
	script := "#!/bin/sh\necho \"$$ $*\" > " + shQuote(r.argsOut) + "\necho \"fake agent $$ says bye\"\n" + body + "\n"
	if err := os.WriteFile(r.bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return r
}

// goneAgentPID is the PID of a process that has already exited and been
// reaped, standing in for the old agent the helper waits for.
func goneAgentPID(t *testing.T) int {
	t.Helper()
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	return c.Process.Pid
}

// run starts script under sh, claims the PID file for it unless claim is
// false (the old agent's claimAgentPIDFile), and waits for it.
func (r *helperRun) run(t *testing.T, script string, claim bool) {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	out := &strings.Builder{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if claim {
		if err := os.WriteFile(r.pidFile, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("helper failed: %v\n%s", err, out)
		}
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("helper did not finish:\n%s", out)
	}
}

func (r *helperRun) agentRan(t *testing.T) (pid string, args string) {
	t.Helper()
	got := readFileTrim(r.argsOut)
	if got == "" {
		t.Fatalf("the new agent never started")
	}
	pid, args, _ = strings.Cut(got, " ")
	return pid, args
}

// The agent the helper starts gets run.sh's flags and the PID file, and a
// crash of it lands in run.sh's NAND logs with its code and its last lines,
// the way run.sh's own supervisor records one.
func TestAgentRestartScriptSupervisesInShell(t *testing.T) {
	r := newHelperRun(t, "exit 3")
	r.run(t, agentRestartScript(goneAgentPID(t), r.bin, []string{"--listen-webui", ":8888", "--name", "it's"}), true)
	pid, args := r.agentRan(t)
	if args != "--listen-webui :8888 --name it's" {
		t.Errorf("new agent got flags %q", args)
	}
	if got := readFileTrim(r.pidFile); got != pid {
		t.Errorf("PID file holds %q, want the new agent's PID %s", got, pid)
	}
	setup := readFileTrim(r.setupLog)
	if !strings.Contains(setup, "agent EXITED (started by the update restart helper, pid="+pid+"): code=3, exit status 3") {
		t.Errorf("setup.log = %q, want the exit recorded", setup)
	}
	crash := readFileTrim(r.crashLog)
	if !strings.Contains(crash, "==== agent exit (update restart helper) pid="+pid+" code=3") ||
		!strings.Contains(crash, "fake agent "+pid+" says bye") {
		t.Errorf("agent-crash.log = %q, want the exit header and the agent's tail", crash)
	}
}

// An agent that exits for its own update hand-over is not a crash: nothing is
// written to NAND and the marker is consumed.
func TestAgentRestartScriptHandOverIsNotACrash(t *testing.T) {
	r := newHelperRun(t, "")
	// The fake agent marks its exit the way markHandOver does.
	if err := os.WriteFile(r.bin, []byte("#!/bin/sh\necho \"$$ $*\" > "+shQuote(r.argsOut)+"\necho \"$$\" > "+shQuote(r.marker)+"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.run(t, agentRestartScript(goneAgentPID(t), r.bin, nil), true)
	r.agentRan(t)
	if got := readFileTrim(r.setupLog) + readFileTrim(r.crashLog); got != "" {
		t.Errorf("a hand-over was recorded as an exit: %q", got)
	}
	if _, err := os.Stat(r.marker); err == nil {
		t.Errorf("hand-over marker left behind")
	}
}

// A stale marker from an earlier hand-over must not hide a later crash.
func TestAgentRestartScriptIgnoresStaleMarker(t *testing.T) {
	r := newHelperRun(t, "exit 2")
	if err := os.WriteFile(r.marker, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.run(t, agentRestartScript(goneAgentPID(t), r.bin, nil), true)
	pid, _ := r.agentRan(t)
	if !strings.Contains(readFileTrim(r.setupLog), "pid="+pid+"): code=2") {
		t.Errorf("crash after a stale marker not recorded: %q", readFileTrim(r.setupLog))
	}
}

// When the claim did not land, the PID file names someone else and the helper
// leaves the restart to run.sh's watchdog instead of starting a second agent.
func TestAgentRestartScriptStepsAsideWithoutTheClaim(t *testing.T) {
	r := newHelperRun(t, "exit 0")
	if err := os.WriteFile(r.pidFile, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.run(t, agentRestartScript(goneAgentPID(t), r.bin, nil), false)
	if _, err := os.Stat(r.argsOut); err == nil {
		t.Errorf("helper started an agent although the PID file was not its own")
	}
	if got := readFileTrim(r.pidFile); got != "1" {
		t.Errorf("PID file holds %q, want it left alone", got)
	}
}

// The RAM-staged swap on a restart-only speaker: copy the stage over the NAND
// binary, verify it, then start and supervise the copy instead of rebooting.
func TestSwapHelperScriptRestartOnlyRunsInShell(t *testing.T) {
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("needs sha256sum")
	}
	r := newHelperRun(t, "exit 5")
	stage := filepath.Join(r.dir, "streborn-ota.stage")
	body, err := os.ReadFile(r.bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stage, body, 0o755); err != nil {
		t.Fatal(err)
	}
	// The NAND binary before the swap is the old one.
	if err := os.WriteFile(r.bin, []byte("#!/bin/sh\necho old > "+shQuote(r.argsOut)+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	script := swapHelperScript(goneAgentPID(t), 5, stage, r.bin, len(body), hex.EncodeToString(sum[:]),
		superviseNewAgentLines(r.bin, []string{"--tls=true"}))
	r.run(t, script, true)
	pid, args := r.agentRan(t)
	if args != "--tls=true" {
		t.Errorf("swapped agent got flags %q (old binary started?)", args)
	}
	if _, err := os.Stat(stage); err == nil {
		t.Errorf("RAM stage left behind after a passing swap")
	}
	if !strings.Contains(readFileTrim(r.setupLog), "pid="+pid+"): code=5") {
		t.Errorf("swapped agent's exit not recorded: %q", readFileTrim(r.setupLog))
	}
}
