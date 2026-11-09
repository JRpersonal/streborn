package webui

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// EROFS must be recognized both as a wrapped syscall errno and by message,
// because it usually arrives buried in fs.PathError -> fmt.Errorf chains.
func TestIsReadOnlyFSErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain EROFS", syscall.EROFS, true},
		{"wrapped EROFS", fmt.Errorf("write tmp: %w", syscall.EROFS), true},
		{"message only", errors.New("open /mnt/nv/x: read-only file system"), true},
		{"ENOSPC is not it", syscall.ENOSPC, false},
		{"unrelated", errors.New("connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isReadOnlyFSErr(tc.err); got != tc.want {
				t.Errorf("isReadOnlyFSErr(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

const (
	testStage     = "/dev/shm/streborn-ota.stage"
	testRescueBin = "/dev/shm/streborn-rescue/streborn-armv7l"
)

func testSwapHelper(waitSec int, finish string) swapHelper {
	return swapHelper{
		agentPID: 4242, waitSec: waitSec,
		stage: testStage, dst: testAgentBin, size: 12345678, sha: "abc123",
		finish:    finish,
		rescueBin: testRescueBin, args: []string{"--listen-webui", ":8888"},
	}
}

// failureBranch returns the lines a swap runs when the binary could not be
// placed: from the ok check to its exit.
func failureBranch(t *testing.T, script string) string {
	t.Helper()
	const open, end = "if [ \"$ok\" != 1 ]; then\n", "\n  exit 1\nfi\n"
	i := strings.Index(script, open)
	if i < 0 {
		t.Fatalf("helper has no failure branch:\n%s", script)
	}
	j := strings.Index(script[i:], end)
	if j < 0 {
		t.Fatalf("failure branch does not end in exit 1:\n%s", script)
	}
	return script[i : i+j+len(end)]
}

// The swap helper must wait for the agent PID with kill -9 escalation, ABORT
// without touching the NAND when the agent will not die (cp over a live binary
// fails ETXTBSY and leaves the old version in place), place stage->dst with the
// cp exit status checked, verify the CONTENT on flash (sha256sum, else cmp
// against the stage; size is only the last resort because consecutive releases
// are byte-equal in size), retry once, and only reboot after a passing verify.
// Guard the script's load-bearing pieces so a refactor cannot silently drop
// one.
func TestSwapHelperScript(t *testing.T) {
	script := testSwapHelper(swapHelperWaitSec, "reboot").script()
	for _, want := range []string{
		"/proc/4242",
		`[ "$i" -lt 90 ]`,
		"kill -9 4242",
		"swap aborted", // agent still alive -> abort marker, no reboot
		// Atomic when two copies fit (df gate: 12057 KB + 1024 KB slack).
		`-ge 13081 ]`,
		`cp "/dev/shm/streborn-ota.stage" "/mnt/nv/streborn/bin/streborn-armv7l.new" && chmod 755 "/mnt/nv/streborn/bin/streborn-armv7l.new" && verify_flash "/mnt/nv/streborn/bin/streborn-armv7l.new" && mv -f "/mnt/nv/streborn/bin/streborn-armv7l.new" "/mnt/nv/streborn/bin/streborn-armv7l"`,
		// In place otherwise: free the old blocks, then a checked copy.
		"  rm -f \"/mnt/nv/streborn/bin/streborn-armv7l\"\n  sync\n  cp \"/dev/shm/streborn-ota.stage\" \"/mnt/nv/streborn/bin/streborn-armv7l\" || return 1\n",
		"drop_caches", // verify must read flash, not page cache
		"sha256sum",
		"abc123",
		`cmp -s "/dev/shm/streborn-ota.stage" "$1"`,
		`wc -c < "$1"`,
		"12345678",
		"killall -9 streborn-armv7l", // retry must clear a watchdog-respawned agent
		"swap failed",                // failed verify -> marker, no reboot
		swapFailMarker,
		`rm -f "/dev/shm/streborn-ota.stage" "` + swapFailMarker + `"`, // success only
		"reboot",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("helper script is missing %q:\n%s", want, script)
		}
	}
	// The abort paths must exit before the reboot line.
	if strings.LastIndex(script, "exit 1") > strings.Index(script, "reboot") {
		t.Errorf("abort paths must come before the reboot:\n%s", script)
	}
	// BusyBox sh only: no bashisms.
	for _, forbidden := range []string{"[[", "function ", "$((i++))", "local "} {
		if strings.Contains(script, forbidden) {
			t.Errorf("helper script contains non-POSIX construct %q", forbidden)
		}
	}
}

// A swap whose binary could not be placed keeps the speaker on STR: it never
// deletes the stage (the only good copy once the in-place copy started), never
// reaches finish, removes the unverified NAND copy and starts the staged build
// from RAM under the helper's supervision with run.sh's flags.
func TestSwapHelperFailureKeepsAnAgent(t *testing.T) {
	for _, finish := range []string{"reboot", superviseNewAgentLines(testAgentBin, []string{"--tls=true"})} {
		script := testSwapHelper(swapHelperWaitSec, finish).script()
		fail := failureBranch(t, script)
		if strings.Contains(fail, "reboot") {
			t.Errorf("the failure branch reboots:\n%s", fail)
		}
		if strings.Contains(fail, `rm -f "`+testStage+`"`) {
			t.Errorf("the failure branch deletes the stage:\n%s", fail)
		}
		for _, want := range []string{
			swapFailedWhy,
			`rm -f "` + testAgentBin + `" "` + testAgentBin + `.new"`,
			`run="` + testStage + `"`,
			`mkdir -p "/dev/shm/streborn-rescue"`,
			`mv -f "` + testStage + `" "` + testRescueBin + `" 2>/dev/null; then run="` + testRescueBin + `"; fi`,
			`{ echo "$$" > "` + agentPIDFile + `"; } 2>/dev/null`,
			`"$run" '--listen-webui' ':8888' >> `,
			`echo "$bpid" > "` + agentPIDFile + `"`,
			`wait "$bpid"`,
		} {
			if !strings.Contains(fail, want) {
				t.Errorf("failure branch is missing %q:\n%s", want, fail)
			}
		}
		// finish comes after the failure branch, never inside it.
		if f := strings.LastIndex(script, finish); f < strings.Index(script, fail)+len(fail) {
			t.Errorf("finish is reachable from the failure branch:\n%s", script)
		}
	}
}

// On a speaker that restarts only the agent after an update, the swap helper
// starts the new binary after a passing verify and never reboots; the abort
// paths still come first.
func TestSwapHelperScriptRestartOnly(t *testing.T) {
	dst := testAgentBin
	finish := superviseNewAgentLines(dst, []string{"--listen-webui", ":8888"})
	script := testSwapHelper(150, finish).script()
	if strings.Contains(script, "reboot") {
		t.Errorf("restart-only helper must not reboot:\n%s", script)
	}
	if !strings.Contains(script, `[ "$i" -lt 150 ]`) {
		t.Errorf("helper does not wait the given 150 s for the agent:\n%s", script)
	}
	start := strings.Index(script, "'"+dst+"' '--listen-webui' ':8888' >> ")
	if start < 0 {
		t.Fatalf("restart-only helper does not start the new agent:\n%s", script)
	}
	if strings.LastIndex(script, "exit 1") > start {
		t.Errorf("abort paths must come before the agent start:\n%s", script)
	}
}

// The rescue agent's binary stays in tmpfs while the rescue agent (or the
// helper supervising it) runs, and goes once neither does, so a later agent
// does not leave ~18 MB of RAM pinned until the next reboot.
func TestCleanStaleRAMStagesRescueBinary(t *testing.T) {
	_ = newRAMFirstRig(t, 60<<20, 60<<10)
	oldBin, oldProc, oldRun := ramRescueBin, procRoot, otaRunningBinary
	t.Cleanup(func() { ramRescueBin, procRoot, otaRunningBinary = oldBin, oldProc, oldRun })
	ramRescueBin = filepath.Join(t.TempDir(), "streborn-rescue", "streborn-armv7l")
	otaRunningBinary = func() string { return testAgentBin }
	if err := os.MkdirAll(filepath.Dir(ramRescueBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ramRescueBin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// The rescue agent runs: its command line names the file.
	procRoot = t.TempDir()
	if err := os.MkdirAll(filepath.Join(procRoot, "4242"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procRoot, "4242", "cmdline"), []byte(ramRescueBin+"\x00--tls=true"), 0o600); err != nil {
		t.Fatal(err)
	}
	CleanStaleRAMStages(logger)
	if _, err := os.Stat(ramRescueBin); err != nil {
		t.Errorf("the rescue binary must stay while it runs: %v", err)
	}

	// The rescue agent itself never removes it.
	procRoot = t.TempDir()
	otaRunningBinary = func() string { return ramRescueBin }
	CleanStaleRAMStages(logger)
	if _, err := os.Stat(ramRescueBin); err != nil {
		t.Errorf("the rescue agent removed its own binary: %v", err)
	}

	// Nobody runs it any more: it goes.
	otaRunningBinary = func() string { return testAgentBin }
	CleanStaleRAMStages(logger)
	if _, err := os.Stat(ramRescueBin); err == nil {
		t.Errorf("the orphaned rescue binary was left in tmpfs")
	}
}
