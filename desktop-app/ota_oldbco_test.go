package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The chassis an update is decided on. The trap MODEL-VARIANTS.md shouts
// about: the ST20, ST30 and Wave each exist as sm2 AND scm, and only the scm
// ones (the Portable excepted) hang in a soft reboot.
func TestChassisFromInfo(t *testing.T) {
	info := func(model, mt, variant string) FirmwareInfo {
		return FirmwareInfo{Reachable: true, Model: model, ModuleType: mt, Variant: variant}
	}
	cases := []struct {
		name string
		fi   FirmwareInfo
		err  error
		want string
	}{
		{"scm SoundTouch 20 (spotty)", info("SoundTouch 20", "scm", "spotty"), nil, chassisBCO},
		{"scm SoundTouch 30 (mojo)", info("SoundTouch 30", "scm", "mojo"), nil, chassisBCO},
		{"scm Wave (lisa)", info("Wave SoundTouch", "scm", "lisa"), nil, chassisBCO},
		{"scm Lifestyle (lisa)", info("Lifestyle", "scm", "lisa"), nil, chassisBCO},
		{"module type in capitals", info("SoundTouch 20", " SCM ", "spotty"), nil, chassisBCO},
		{"Portable by variant", info("SoundTouch Portable", "scm", "taigan"), nil, chassisTaigan},
		{"Portable by name, variant missing", info("SoundTouch Portable", "scm", ""), nil, chassisTaigan},
		{"sm2 SoundTouch 20", info("SoundTouch 20", "sm2", "spotty"), nil, chassisWiFi},
		{"sm2 SoundTouch 10", info("SoundTouch 10", "sm2", "rhino"), nil, chassisWiFi},
		{"sm2 Wave", info("Wave SoundTouch", "sm2", "lisa"), nil, chassisWiFi},
		{"unknown module type", info("SoundTouch 300", "riva", "ginger"), nil, chassisOther},
		{"no module type", info("SoundTouch 20", "", ""), nil, chassisUnknown},
		{"/info failed", FirmwareInfo{}, errors.New("timeout"), chassisUnknown},
		{"/info not reachable", FirmwareInfo{ModuleType: "scm"}, nil, chassisUnknown},
	}
	for _, c := range cases {
		if got, _ := chassisFromInfo(c.fi, c.err); got != c.want {
			t.Errorf("%s: chassis %q, want %q", c.name, got, c.want)
		}
	}
}

func TestChassisFromWlanMode(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"bco":           {chassisBCO, true},
		" bco\n":        {chassisBCO, true},
		"taigan-bco":    {chassisTaigan, true},
		"wlan0":         {chassisWiFi, true},
		"wlan1":         {chassisWiFi, true},
		"ethernet-only": {chassisWiFi, true},
		"":              {chassisUnknown, false},
		"garbage":       {chassisUnknown, false},
	}
	for in, c := range cases {
		got, ok := chassisFromWlanMode(in)
		if got != c.want || ok != c.ok {
			t.Errorf("wlan-mode %q: (%q, %v), want (%q, %v)", in, got, ok, c.want, c.ok)
		}
	}
}

// Below, at and above v1.3.0, on every chassis. Only an agent older than
// v1.3.0 on the coprocessor chassis is routed around; where that cannot be
// done safely the owner is warned; everything else keeps today's path.
func TestPlanAgentRestart(t *testing.T) {
	cases := []struct {
		version, chassis, want string
	}{
		// The live case of 2026-11-15.
		{"v0.9.84", chassisBCO, restartPlanAgentOnly},
		{"v1.2.1", chassisBCO, restartPlanAgentOnly},
		{"v0.9.0", chassisBCO, restartPlanAgentOnly},
		{"v0.9.14", chassisBCO, restartPlanAgentOnly},
		// From v1.3.0 the agent restarts itself on this chassis.
		{"v1.3.0", chassisBCO, restartPlanReboot},
		{"v1.3.2", chassisBCO, restartPlanReboot},
		{"v1.6.2", chassisBCO, restartPlanReboot},
		// No enable-ssh before v0.9.0: SSH cannot be opened without a reboot.
		{"v0.8.40", chassisBCO, restartPlanWarn},
		// Chassis unknown on an old agent: warn.
		{"v0.9.84", chassisUnknown, restartPlanWarn},
		{"v1.2.1", chassisUnknown, restartPlanWarn},
		// ...but not on a new one.
		{"v1.3.0", chassisUnknown, restartPlanReboot},
		// The Portable and the sm2 speakers come back from the reboot.
		{"v0.9.84", chassisTaigan, restartPlanReboot},
		{"v0.9.84", chassisWiFi, restartPlanReboot},
		{"v0.9.84", chassisOther, restartPlanReboot},
		// Unknown agent versions never steer anything.
		{"", chassisBCO, restartPlanReboot},
		{"1.0.0", chassisBCO, restartPlanReboot},
		{"garbage", chassisBCO, restartPlanReboot},
	}
	for _, c := range cases {
		p := planAgentRestart(c.version, c.chassis)
		if p.Mode != c.want {
			t.Errorf("agent %q on %s: mode %q (%s), want %q", c.version, c.chassis, p.Mode, p.Reason, c.want)
		}
		if p.Reason == "" {
			t.Errorf("agent %q on %s: no reason given", c.version, c.chassis)
		}
	}
}

func TestParseWlanModeOutput(t *testing.T) {
	cases := []struct {
		out    string
		want   string
		wantOK bool
	}{
		{"bco\n\nSTR_WLAN_MODE_END\n", "bco", true},
		{"bco\r\n\r\nSTR_WLAN_MODE_END\r\n", "bco", true},
		{"taigan-bco\nSTR_WLAN_MODE_END", "taigan-bco", true},
		// The record is missing: cat prints nothing, the marker still comes.
		{"\nSTR_WLAN_MODE_END\n", "", true},
		{"", "", false},
		{"bco", "", false},
	}
	for _, c := range cases {
		got, ok := parseWlanModeOutput(c.out)
		if got != c.want || ok != c.wantOK {
			t.Errorf("parse %q: (%q, %v), want (%q, %v)", c.out, got, ok, c.want, c.wantOK)
		}
	}
}

// The command that replaces the old agent's reboot must never reboot, and it
// must do the hand-over the way run.sh's watchdogs expect.
func TestAgentRestartCommandRestartsOnlyTheAgent(t *testing.T) {
	cmd := agentRestartNoRebootCmd()
	// "ota-reboot" is the marker file the new agent reads; nothing else may
	// say reboot.
	if strings.Contains(strings.ReplaceAll(cmd, otaRestartMarker, ""), "reboot") {
		t.Fatalf("the no-reboot command mentions reboot:\n%s", cmd)
	}
	for _, want := range []string{
		// The helper is written and started detached from the SSH session.
		"cat > " + agentRestartHelper + " <<'STR_HELPER_EOF'\n",
		"\nSTR_HELPER_EOF\n",
		"setsid nohup sh " + agentRestartHelper + " </dev/null >/dev/null 2>&1 &",
		"nohup sh " + agentRestartHelper + " </dev/null >/dev/null 2>&1 &",
		"echo STR_RESTART_ARMED",
		// The new agent stands its power-on resume down, as after an OTA reboot.
		"date -u +%Y-%m-%dT%H:%M:%SZ > " + otaRestartMarker,
		// The helper itself.
		`echo "$$" > '` + agentRestartStarted + `'`,
		"PIDFILE='" + agentPIDFilePath + "'",
		"BIN='" + agentBinaryPath + "'",
		`tr '\000' '\n' < "/proc/$old/cmdline"`,
		// Claim the PID file before the old agent goes.
		`echo "$$" > "$PIDFILE"` + "\nkill -TERM \"$old\"",
		`kill -9 "$old"`,
		"killall go-librespot",
		`"$BIN" "$@" >> "$LOG" 2>&1 &`,
		`echo "$bpid" > "$PIDFILE"`,
		`wait "$bpid"`,
		"-lt 300 ]",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("the no-reboot command lacks %q", want)
		}
	}
	// The helper's heredoc must end on its own line, or the rest of the
	// command would be swallowed into the helper.
	if !strings.HasSuffix(agentRestartHelperScript(), "\n") {
		t.Error("the helper script does not end with a newline before its heredoc delimiter")
	}
}

// Both SSH agent pushes share one space preamble, the one sshPushFile has
// always been handed for the agent.
func TestSSHAgentPushPreambleDropsTheEngineOnlyWhenTight(t *testing.T) {
	pre := sshAgentPushPreamble(14336)
	for _, want := range []string{
		"rm -rf /mnt/nv/streborn-install /mnt/nv/streborn/streborn-install",
		"/mnt/nv/streborn/bin/*.new",
		`-lt "14336" ]; then rm -f /mnt/nv/streborn/bin/go-librespot`,
		sshStopEngineCmd,
	} {
		if !strings.Contains(pre, want) {
			t.Errorf("preamble lacks %q:\n%s", want, pre)
		}
	}
	if !strings.HasSuffix(pre, "; ") {
		t.Errorf("preamble must end with \"; \" for the mkdir that follows: %q", pre)
	}
}

// The helper run for real against a temp dir: a fake old agent with run.sh's
// flags, a fake new binary that records how it was started. Linux only, as
// on the speaker (/proc/<pid>/cmdline).
func TestAgentRestartHelperHandsOverWithTheOldFlags(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs /proc/<pid>/cmdline")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	p := agentRestartPaths{
		pidFile:  filepath.Join(dir, "agent.pid"),
		bin:      filepath.Join(dir, "new-agent"),
		log:      filepath.Join(dir, "agent.log"),
		setupLog: filepath.Join(dir, "setup.log"),
		crashLog: filepath.Join(dir, "crash.log"),
		started:  filepath.Join(dir, "started"),
	}
	argsOut := filepath.Join(dir, "args")
	// The new binary writes its flags, one per line, and exits.
	if err := os.WriteFile(p.bin, []byte("#!/bin/sh\nfor a in \"$@\"; do echo \"$a\"; done > "+shQuoteWord(argsOut)+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The old agent: a script that idles, started with run.sh-style flags.
	// Its cmdline is "/bin/sh <script> <flags>", so what the helper hands on
	// is the script path and then the flags.
	oldBin := filepath.Join(dir, "old-agent")
	if err := os.WriteFile(oldBin, []byte("#!/bin/sh\nwhile :; do sleep 1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldAgent := exec.Command(oldBin, "--presets", "/mnt/nv/streborn/presets.json", "--listen-webui", ":8888")
	if err := oldAgent.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = oldAgent.Process.Kill() }()
	exited := make(chan struct{})
	go func() { _ = oldAgent.Wait(); close(exited) }()
	if err := os.WriteFile(p.pidFile, []byte(strconv.Itoa(oldAgent.Process.Pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(sh, "-c", p.helperScript()).CombinedOutput()
	if err != nil {
		t.Fatalf("helper failed: %v\n%s", err, out)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the old agent is still running")
	}
	got, err := os.ReadFile(argsOut)
	if err != nil {
		t.Fatalf("the new binary was not started: %v", err)
	}
	if want := oldBin + "\n--presets\n/mnt/nv/streborn/presets.json\n--listen-webui\n:8888\n"; string(got) != want {
		t.Errorf("new agent flags %q, want %q", got, want)
	}
	pid, _ := os.ReadFile(p.pidFile)
	if strings.TrimSpace(string(pid)) == strconv.Itoa(oldAgent.Process.Pid) {
		t.Error("the PID file still names the old agent")
	}
	setup, _ := os.ReadFile(p.setupLog)
	for _, want := range []string{"restarting the agent", "new agent started", "agent EXITED"} {
		if !strings.Contains(string(setup), want) {
			t.Errorf("setup.log lacks %q:\n%s", want, setup)
		}
	}
	if _, err := os.Stat(p.started); err != nil {
		t.Error("the helper did not mark itself started")
	}
}

// The whole SSH command as the session runs it, against a temp dir: it writes
// the helper, starts it detached, says ARMED once the helper runs, and the
// helper then hands over to the new binary.
func TestAgentRestartCommandArmsTheHelper(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs /proc/<pid>/cmdline")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	at := func(n string) string { return filepath.Join(dir, n) }
	cmd := agentRestartNoRebootCmd()
	// The uptime gate: a CI box may have booted moments ago.
	if n := strings.Count(cmd, strconv.Itoa(agentRestartMinUptimeSec)); n != 3 {
		t.Fatalf("expected the uptime gate three times in the command, found %d", n)
	}
	cmd = strings.ReplaceAll(cmd, strconv.Itoa(agentRestartMinUptimeSec), "0")
	cmd = strings.NewReplacer(
		agentPIDFilePath, at("agent.pid"),
		agentBinaryPath, at("new-agent"),
		agentLogPath, at("agent.log"),
		agentSetupLogPath, at("setup.log"),
		agentCrashLogPath, at("crash.log"),
		otaRestartMarker, at("ota-marker"),
		agentRestartHelper, at("helper.sh"),
		agentRestartStarted, at("started"),
	).Replace(cmd)
	if strings.Contains(cmd, "/mnt/nv") {
		t.Fatalf("a speaker path was left in the test command:\n%s", cmd)
	}
	argsOut := at("args")
	if err := os.WriteFile(at("new-agent"), []byte("#!/bin/sh\necho \"$*\" > "+shQuoteWord(argsOut)+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(at("old-agent"), []byte("#!/bin/sh\nwhile :; do sleep 1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldAgent := exec.Command(at("old-agent"), "--listen-webui", ":8888")
	if err := oldAgent.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = oldAgent.Process.Kill() }()
	go func() { _ = oldAgent.Wait() }()
	if err := os.WriteFile(at("agent.pid"), []byte(strconv.Itoa(oldAgent.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(sh, "-c", cmd).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "STR_RESTART_ARMED") {
		t.Fatalf("command: %v\n%s", err, out)
	}
	if _, err := os.Stat(at("ota-marker")); err != nil {
		t.Error("the update marker for the new agent was not written")
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(argsOut); err == nil {
			if got, want := strings.TrimSpace(string(b)), at("old-agent")+" --listen-webui :8888"; got != want {
				t.Errorf("new agent started with %q, want %q", got, want)
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the helper never started the new binary")
}

// The whole SSH command, heredoc and all, parses in a POSIX sh.
func TestAgentRestartCommandParses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX sh guaranteed")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	if out, err := exec.Command(sh, "-n", "-c", agentRestartNoRebootCmd()).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v\n%s", err, out)
	}
	if out, err := exec.Command(sh, "-n", "-c", agentRestartHelperScript()).CombinedOutput(); err != nil {
		t.Fatalf("sh -n helper: %v\n%s", err, out)
	}
}

func TestUnreachableVerdictLine(t *testing.T) {
	generic := unreachableVerdictLine(false, "dial tcp: i/o timeout")
	if !strings.HasPrefix(generic, "outcome: NOT CONFIRMED - box unreachable after the verify window") || strings.Contains(generic, "unplug") {
		t.Errorf("generic verdict: %q", generic)
	}
	bco := unreachableVerdictLine(true, "dial tcp: i/o timeout")
	for _, want := range []string{"NOT CONFIRMED", "coprocessor Wi-Fi speaker", "8090 8091 8080 17008 17000 22", "unplug it for 30 seconds", "wait 3 minutes", "dial tcp: i/o timeout"} {
		if !strings.Contains(bco, want) {
			t.Errorf("coprocessor verdict lacks %q: %q", want, bco)
		}
	}
}

// The outcome tells the frontend to lead with unplugging only for the
// classifier's coprocessor verdict.
func TestUnplugFirstOnlyForTheCoprocessorVerdict(t *testing.T) {
	for _, c := range []struct {
		verdict string
		advised bool
		want    bool
	}{
		{"unreachable", true, true},
		{"unreachable", false, false},
		{"agent-gone", true, false},
		{"not-landed", true, false},
	} {
		fb := newOtaFakeBox()
		fb.rebootsTo(time.Hour, func(time.Time) map[string]string { return newVersion("present") })
		fb.verdicts = []string{c.verdict}
		s := fb.seams()
		s.unplugAdvised = func(string) bool { return c.advised }
		out, err := orchestrateSpeaker(s, otaProductionPolicy, "192.0.2.1", 8888, "device-id-here", "Kitchen", nil)
		if err != nil {
			t.Fatal(err)
		}
		if out.Confirmed {
			t.Fatalf("%s: confirmed", c.verdict)
		}
		if out.UnplugFirst != c.want {
			t.Errorf("verdict %s advised=%v: UnplugFirst=%v, want %v", c.verdict, c.advised, out.UnplugFirst, c.want)
		}
	}
}

func TestUnplugAdviceLedger(t *testing.T) {
	const host = "192.0.2.77"
	if unplugAdvised(host) {
		t.Fatal("advised before anything was recorded")
	}
	unplugAdviceLedger.Store(host, time.Now())
	if !unplugAdvised(host) {
		t.Fatal("not advised after recording")
	}
	unplugAdviceLedger.Delete(host)
	noteOTAChassis(host, chassisUnknown)
	if otaChassisOf(host) != chassisUnknown {
		t.Error("an unknown chassis was remembered")
	}
	noteOTAChassis(host, chassisBCO)
	if otaChassisOf(host) != chassisBCO {
		t.Error("the coprocessor chassis was not remembered")
	}
	otaChassisLedger.Delete(host)
}
