package webui

// The tier-3 swap helper is a shell script generated in Go, so shellcheck in CI
// never sees it. These tests run it through the system sh against temp paths
// with faked sha256sum, df, killall and (in one case) cp, which is the closest
// thing to a speaker whose NAND is too full short of a live update. They need
// no /proc (the agent PID they hand over can never exist), so they also run
// under Git Bash on Windows.

import (
	"context"
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

// noSuchPID is above any Linux pid_max, so the helper's wait for the old agent
// ends at once.
const noSuchPID = 2147483646

type swapRig struct {
	dir                                  string
	stage, dst, rescue, marker, finished string
	pidFile, argsOut, setupLog, killalls string
	body                                 []byte
	sum                                  string
	env                                  []string
	// paths are run.sh's files in the temp dir, as the helper is given them.
	paths otaPaths
}

// slash is p as the shell sees it (Git Bash takes C:/... paths).
func slash(p string) string { return filepath.ToSlash(p) }

// newSwapRig lays out a speaker in a temp dir: the new build in the RAM stage,
// the old one at the NAND path, and the run.sh files the rescue agent's
// supervision writes. The new build is a fake agent that records its PID and
// flags. shaFails is how many verify calls the fake sha256sum fails before it
// answers truthfully; dfAvailKB is what the fake df reports as free.
func newSwapRig(t *testing.T, shaFails, dfAvailKB int) *swapRig {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs sh")
	}
	realSha, err := exec.LookPath("sha256sum")
	if err != nil {
		t.Skip("needs sha256sum")
	}
	d := t.TempDir()
	r := &swapRig{
		dir:      d,
		stage:    slash(filepath.Join(d, "shm", "streborn-ota.stage")),
		dst:      slash(filepath.Join(d, "nv", "bin", "streborn-armv7l")),
		rescue:   slash(filepath.Join(d, "shm", "streborn-rescue", "streborn-armv7l")),
		marker:   slash(filepath.Join(d, "nv", "ota-swap-failed")),
		finished: slash(filepath.Join(d, "finished")),
		pidFile:  slash(filepath.Join(d, "nv", "agent.pid")),
		argsOut:  slash(filepath.Join(d, "agent-args")),
		setupLog: slash(filepath.Join(d, "nv", "setup.log")),
		killalls: slash(filepath.Join(d, "killall.log")),
	}
	for _, sub := range []string{"shm", filepath.Join("nv", "bin"), "fakebin"} {
		if err := os.MkdirAll(filepath.Join(d, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r.body = []byte("#!/bin/sh\n# new build\necho \"$$ $*\" > " + shQuote(r.argsOut) + "\n")
	s := sha256.Sum256(r.body)
	r.sum = hex.EncodeToString(s[:])
	mustWrite(t, r.stage, r.body, 0o755)
	mustWrite(t, r.dst, []byte("#!/bin/sh\n# old build\n"), 0o755)

	calls := slash(filepath.Join(d, "sha-calls"))
	fake := map[string]string{
		"sha256sum": `n=$(cat ` + shQuote(calls) + ` 2>/dev/null || echo 0)
n=$((n+1))
echo "$n" > ` + shQuote(calls) + `
if [ "$n" -le ` + strconv.Itoa(shaFails) + ` ]; then echo "0000000000000000000000000000000000000000000000000000000000000000  $1"; exit 0; fi
exec ` + shQuote(slash(realSha)) + ` "$@"`,
		"df": `echo "Filesystem 1K-blocks Used Available Use% Mounted on"
echo "ubi0:nv 26000 25000 ` + strconv.Itoa(dfAvailKB) + ` 96% /mnt/nv"`,
		// A real killall would hit any streborn-armv7l on the test host.
		"killall": `echo "$*" >> ` + shQuote(r.killalls),
	}
	for name, body := range fake {
		mustWrite(t, filepath.Join(d, "fakebin", name), []byte("#!/bin/sh\n"+body+"\n"), 0o755)
	}
	r.env = append(os.Environ(), "PATH="+filepath.Join(d, "fakebin")+string(os.PathListSeparator)+os.Getenv("PATH"))

	r.paths = otaPaths{
		agentPIDFile:        r.pidFile,
		agentLogPath:        slash(filepath.Join(d, "agent.log")),
		agentSetupLogPath:   r.setupLog,
		agentCrashLogPath:   slash(filepath.Join(d, "nv", "agent-crash.log")),
		agentHandOverMarker: slash(filepath.Join(d, "handover")),
	}
	oldMarker := swapFailMarker
	t.Cleanup(func() { swapFailMarker = oldMarker })
	swapFailMarker = r.marker
	return r
}

func mustWrite(t *testing.T, p string, b []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(p, b, mode); err != nil {
		t.Fatal(err)
	}
}

func (r *swapRig) helper() swapHelper {
	return swapHelper{
		agentPID: noSuchPID, waitSec: 5,
		stage: r.stage, dst: r.dst, size: int64(len(r.body)), sha: r.sum,
		finish:    "echo finished > " + shQuote(r.finished),
		rescueBin: r.rescue, args: []string{"--listen-webui", ":8888"},
		paths: r.paths,
	}
}

// run runs script under sh and returns its exit code.
func (r *swapRig) run(t *testing.T, script string) int {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = r.env
	out := &strings.Builder{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		t.Logf("helper output:\n%s", out)
		if err == nil {
			return 0
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		t.Fatalf("helper failed to run: %v\n%s", err, out)
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("helper did not finish:\n%s", out)
	}
	return -1
}

func (r *swapRig) assertSwapped(t *testing.T) {
	t.Helper()
	if got, _ := os.ReadFile(r.dst); string(got) != string(r.body) {
		t.Errorf("NAND binary = %q, want the new build", got)
	}
	for _, gone := range []string{r.stage, r.dst + ".new", r.marker, r.rescue} {
		if _, err := os.Stat(gone); err == nil {
			t.Errorf("%s left behind after a passing swap", gone)
		}
	}
	if readFileTrim(r.finished) != "finished" {
		t.Errorf("finish did not run after a passing swap")
	}
	if _, err := os.Stat(r.argsOut); err == nil {
		t.Errorf("a passing swap started the rescue agent")
	}
}

// The verify passes at once; the NAND has no room for a second copy, so the
// binary is written in place.
func TestSwapHelperInShellVerifyPasses(t *testing.T) {
	r := newSwapRig(t, 0, 100)
	if ec := r.run(t, r.helper().script()); ec != 0 {
		t.Errorf("exit %d, want 0", ec)
	}
	r.assertSwapped(t)
	// One verify: the in-place copy, no dst.new.
	if n := r.verifies(); n != 1 {
		t.Errorf("%d verifies, want 1 (in place)", n)
	}
}

// With room for both copies the binary goes through dst.new and a rename.
func TestSwapHelperInShellAtomicWhenItFits(t *testing.T) {
	r := newSwapRig(t, 0, 1<<20)
	if ec := r.run(t, r.helper().script()); ec != 0 {
		t.Errorf("exit %d, want 0", ec)
	}
	r.assertSwapped(t)
	// Two verifies: dst.new before the rename, dst after it.
	if n := r.verifies(); n != 2 {
		t.Errorf("%d verifies, want 2 (dst.new, then dst)", n)
	}
}

// verifies is how often the fake sha256sum ran.
func (r *swapRig) verifies() int {
	n, _ := strconv.Atoi(readFileTrim(filepath.Join(r.dir, "sha-calls")))
	return n
}

// The first verify fails (page cache, #302), the retry after the cache drop
// passes.
func TestSwapHelperInShellVerifyFailsThenPasses(t *testing.T) {
	r := newSwapRig(t, 1, 100)
	if ec := r.run(t, r.helper().script()); ec != 0 {
		t.Errorf("exit %d, want 0", ec)
	}
	r.assertSwapped(t)
	if !strings.Contains(readFileTrim(r.killalls), "-9 streborn-armv7l") {
		t.Errorf("the retry did not clear watchdog respawns first")
	}
}

// The field case: both verifies fail. The stage must survive, the speaker must
// get the new build started from RAM, and nothing reboots.
func TestSwapHelperInShellVerifyFailsTwiceKeepsAnAgent(t *testing.T) {
	r := newSwapRig(t, 99, 100)
	if ec := r.run(t, r.helper().script()); ec != 1 {
		t.Errorf("exit %d, want 1", ec)
	}
	r.assertRescued(t)
}

// cp reporting failure counts as a failed copy even when the bytes it left
// would verify (the old script ignored cp's exit status).
func TestSwapHelperInShellCopyErrorKeepsAnAgent(t *testing.T) {
	r := newSwapRig(t, 0, 100)
	realCp, err := exec.LookPath("cp")
	if err != nil {
		t.Skip("needs cp")
	}
	mustWrite(t, filepath.Join(r.dir, "fakebin", "cp"),
		[]byte("#!/bin/sh\n"+shQuote(slash(realCp))+" \"$@\"\nexit 1\n"), 0o755)
	if ec := r.run(t, r.helper().script()); ec != 1 {
		t.Errorf("exit %d, want 1", ec)
	}
	r.assertRescued(t)
}

func (r *swapRig) assertRescued(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(r.finished); err == nil {
		t.Errorf("finish (the reboot) ran after a failed swap")
	}
	if got, err := os.ReadFile(r.rescue); err != nil || string(got) != string(r.body) {
		t.Errorf("the staged build did not survive at the rescue path: %q, %v", got, err)
	}
	if _, err := os.Stat(r.dst); err == nil {
		t.Errorf("the unverified NAND copy was left where run.sh starts the agent from")
	}
	if !strings.Contains(readFileTrim(r.marker), "swap failed") {
		t.Errorf("marker = %q, want the swap failure", readFileTrim(r.marker))
	}
	got := readFileTrim(r.argsOut)
	pid, args, _ := strings.Cut(got, " ")
	if got == "" || args != "--listen-webui :8888" {
		t.Fatalf("the rescue agent did not start with run.sh's flags: %q", got)
	}
	if readFileTrim(r.pidFile) != pid {
		t.Errorf("PID file = %q, want the rescue agent's PID %s", readFileTrim(r.pidFile), pid)
	}
	// The fake agent exits at once; its supervisor records that like run.sh.
	if !strings.Contains(readFileTrim(r.setupLog), "pid="+pid+"): code=0") {
		t.Errorf("the rescue agent's exit was not recorded: %q", readFileTrim(r.setupLog))
	}
}

// The rescue agent's own retry runs the same copy: a NAND copy that verifies
// clears the marker and lifts the reboot block.
func TestRescueRetryWritesTheBuild(t *testing.T) {
	r := newSwapRig(t, 0, 100)
	rescueTestSeams(t, r)
	if err := os.MkdirAll(filepath.Dir(r.rescue), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, r.rescue, r.body, 0o755)
	mustWrite(t, r.marker, []byte(swapFailedWhy+"\n"), 0o644)
	if RebootUnsafe() == "" {
		t.Fatal("the rescue agent must refuse its own reboots before the retry")
	}
	if !rescueRetry(t.Context(), discardLogger()) {
		t.Fatal("retry reported failure")
	}
	if got, _ := os.ReadFile(r.dst); string(got) != string(r.body) {
		t.Errorf("NAND binary = %q, want the running build", got)
	}
	if _, err := os.Stat(r.marker); err == nil {
		t.Errorf("marker left after a verified retry")
	}
	if why := RebootUnsafe(); why != "" {
		t.Errorf("reboots still blocked after a verified retry: %s", why)
	}
}

// A retry that fails again keeps the marker (with the retry named), keeps
// reboots blocked and leaves no unverified copy behind.
func TestRescueRetryFailsAgain(t *testing.T) {
	r := newSwapRig(t, 99, 100)
	rescueTestSeams(t, r)
	if err := os.MkdirAll(filepath.Dir(r.rescue), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, r.rescue, r.body, 0o755)
	if rescueRetry(t.Context(), discardLogger()) {
		t.Fatal("retry reported success")
	}
	if _, err := os.Stat(r.dst); err == nil {
		t.Errorf("the unverified NAND copy was left behind")
	}
	if !strings.Contains(readFileTrim(r.marker), "own retry") {
		t.Errorf("marker = %q, want the failed retry named", readFileTrim(r.marker))
	}
	if RebootUnsafe() == "" {
		t.Errorf("reboots allowed although the NAND copy is not verified")
	}
	if _, err := os.Stat(r.rescue); err != nil {
		t.Errorf("the running build's file is gone: %v", err)
	}
}

// An update the app is pushing right now owns the write.
func TestRescueRetryStandsBackForAnUpdate(t *testing.T) {
	r := newSwapRig(t, 0, 100)
	rescueTestSeams(t, r)
	processOTAState.beginUpdate()
	t.Cleanup(processOTAState.endUpdate)
	if rescueRetry(t.Context(), discardLogger()) {
		t.Fatal("retry ran during an update")
	}
	if got, _ := os.ReadFile(r.dst); string(got) == string(r.body) {
		t.Errorf("retry wrote during an update")
	}
}

// rescueTestSeams makes this test process the rescue agent of rig r, with the
// retry script run under the rig's PATH.
func rescueTestSeams(t *testing.T, r *swapRig) {
	t.Helper()
	oldBin, oldDst, oldRun, oldSum, oldShell := ramRescueBin, rescueNANDDst, productionOTAEffects.runningBinary, rescueSum, rescueShell
	oldProc := productionOTAEffects.paths.procRoot
	t.Cleanup(func() {
		ramRescueBin, rescueNANDDst, productionOTAEffects.runningBinary, rescueSum, rescueShell = oldBin, oldDst, oldRun, oldSum, oldShell
		productionOTAEffects.paths.procRoot = oldProc
		nandCopyVerified.Store(false)
	})
	nandCopyVerified.Store(false)
	ramRescueBin, rescueNANDDst = r.rescue, r.dst
	productionOTAEffects.runningBinary = func() string { return r.rescue }
	rescueSum = func() string { return r.sum }
	productionOTAEffects.paths.procRoot = filepath.Join(r.dir, "no-proc")
	rescueShell = func(ctx context.Context, script string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "sh", "-c", script)
		cmd.Env = r.env
		return cmd.CombinedOutput()
	}
}
