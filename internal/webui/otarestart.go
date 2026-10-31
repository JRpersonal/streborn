package webui

// How a delivered agent update takes over: a box reboot on most speakers, a
// restart of the agent process alone on the coprocessor Wi-Fi chassis.
//
// The SoundTouch 20 with the scm module (and, by the same chassis, the scm
// SoundTouch 30 and Wave) runs its Wi-Fi on the BCO coprocessor, which run.sh
// records as wlan-mode "bco". A soft `reboot` there does not reliably bring
// the speaker back: a field bundle from 2026-10-30 showed such an ST20 whose
// boot log has no soft-reboot entry after any of five updates since
// 2026-10-01. Each time the Bose stack (:8090, SSDP) stayed down until the
// owner pulled the plug, while the sm2 speakers in the same house came back
// every time. cmd/agent's maybeRebootAfterBootstrapSync already spares this
// chassis its stacked bootstrap reboot for the same reason; the update's own
// reboot was never gated the same way.
//
// What the reboot used to buy (2026-06-01, agentupload.go) no longer needs
// one: the start-up preset push (initialBoxPresetSync) and the first forced
// full re-sync (periodicPresetReconcile starts with fullDone=false) run on
// EVERY agent start, not only after a box boot. The refreshed run.sh and
// rc.local take effect at the speaker's next natural boot, which is what the
// bootstrap gate already settled for this chassis. The OTA marker is written
// before the restart exactly as before the reboot, so the new agent still
// stands the power-on resume down once.
//
// The Portable ("taigan-bco") stays on the reboot: it is Verified with it and
// nothing says it hangs. Every other or unreadable mode keeps the reboot too.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// otaRestartOnlyWLANMode is the one wlan-mode whose speakers restart the agent
// instead of rebooting after an update.
const otaRestartOnlyWLANMode = "bco"

// agentPIDFile is where run.sh keeps the PID its watchdogs kill -0. The
// restart helper claims it before this process exits, so the watchdog sees a
// live agent throughout and never starts a second one next to it.
var agentPIDFile = "/mnt/nv/streborn/agent.pid"

// agentLogPath is run.sh's $LOG, where the agent's stdout goes.
const agentLogPath = "/tmp/streborn-agent.log"

// The outside effects of the hand-over, swapped out in tests.
var (
	otaWLANMode      = readWlanMode
	otaRunningBinary = runningAgentBinary
	otaReboot        = func() error { return exec.Command("reboot").Run() }
	otaStartDetached = startDetachedShell
	otaExit          = os.Exit
)

// runningAgentBinary is the path this process was started from. Linux reports
// a replaced binary as "<path> (deleted)", which is exactly the state after an
// update renamed the new file over it, so the suffix is dropped.
func runningAgentBinary() string {
	p, err := os.Executable()
	if err != nil || p == "" {
		if len(os.Args) == 0 {
			return ""
		}
		p = os.Args[0]
	}
	return strings.TrimSuffix(p, " (deleted)")
}

// startDetachedShell runs script under sh in its own session, so it survives
// this process, and returns its PID without waiting for it.
func startDetachedShell(script string) (int, error) {
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = sysProcAttrSetsid()
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}

// selfRestartAfterOTA reports whether the update hands over by restarting the
// agent process instead of rebooting the speaker. Only on wlan-mode "bco", and
// only while this agent runs from the NAND binary the update just replaced:
// that is the binary run.sh's watchdog starts again later, so the restarted
// agent and any later respawn agree. An agent run.sh started from a RAM copy
// or the stick reboots as before, so the next boot picks its binary afresh.
func selfRestartAfterOTA(wlanMode, running, dst string) bool {
	if wlanMode != otaRestartOnlyWLANMode || running == "" {
		return false
	}
	return filepath.Clean(running) == filepath.Clean(dst)
}

// otaRestartOnly decides the hand-over for this speaker and logs why.
func (s *Server) otaRestartOnly(dst string) bool {
	mode, running := otaWLANMode(), otaRunningBinary()
	if selfRestartAfterOTA(mode, running, dst) {
		s.logger.Info("post-OTA: restarting the agent only, no box reboot (a soft reboot can leave this chassis dark until it is unplugged)",
			"wlanMode", mode)
		return true
	}
	if mode == otaRestartOnlyWLANMode {
		s.logger.Warn("post-OTA: rebooting this coprocessor Wi-Fi speaker anyway, the agent does not run from the binary the update replaced",
			"wlanMode", mode, "running", running, "updated", dst)
		return false
	}
	s.logger.Info("post-OTA: rebooting the box", "wlanMode", mode)
	return false
}

// shQuote quotes one word for sh.
func shQuote(a string) string {
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

// execNewAgentLines is the tail of a hand-over helper: start the new agent in
// the helper's own process, so the PID the agent claimed for it in run.sh's
// PID file stays the agent's PID. Should the watchdog have started an agent
// after all (the claim failed), that one is left to run and the helper bows
// out, because two agents cannot share the listener ports.
func execNewAgentLines(bin string, args []string) string {
	words := make([]string, 0, len(args)+1)
	words = append(words, shQuote(bin))
	for _, a := range args {
		words = append(words, shQuote(a))
	}
	return fmt.Sprintf(`cur=$(cat %q 2>/dev/null)
if [ -n "$cur" ] && [ "$cur" != "$$" ] && [ -d "/proc/$cur" ]; then exit 0; fi
echo "$$" > %q
exec %s >> %q 2>&1`, agentPIDFile, agentPIDFile, strings.Join(words, " "), agentLogPath)
}

// agentRestartScript is the helper that replaces the box reboot on a
// coprocessor Wi-Fi speaker: wait for this agent to exit (kill -9 after 30 s),
// then start the new binary. No 70 s TIME_WAIT sleep as in the reboot
// fallback: Go's listeners set SO_REUSEADDR, and the old process is gone (its
// listeners closed) before the new one binds.
func agentRestartScript(agentPID int, bin string, args []string) string {
	return fmt.Sprintf(`i=0
while [ -d /proc/%d ] && [ "$i" -lt 30 ]; do sleep 1; i=$((i+1)); done
if [ -d /proc/%d ]; then kill -9 %d 2>/dev/null; sleep 3; fi
sleep 2
%s`, agentPID, agentPID, agentPID, execNewAgentLines(bin, args))
}

// claimAgentPIDFile writes the helper's PID into run.sh's PID file. Best
// effort: without it the watchdog starts an agent of its own within 90 s and
// the helper sees that and steps aside (execNewAgentLines).
func (s *Server) claimAgentPIDFile(pid int) {
	if err := os.WriteFile(agentPIDFile, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		s.logger.Warn("post-OTA: could not hand run.sh's PID file to the restart helper; its watchdog starts the agent instead",
			"helperPID", pid, "err", err)
	}
}

// stopEngineForRestart stops the supervised Spotify engine before the agent
// exits. A reboot ended it with everything else; a process restart would
// leave it orphaned and holding its port when the new agent starts its own.
func (s *Server) stopEngineForRestart() {
	if s.spotifyStop != nil {
		s.spotifyStop()
	}
}

// agentArgs are the flags run.sh started this agent with, which the new one
// gets as well.
func agentArgs() []string {
	if len(os.Args) > 1 {
		return os.Args[1:]
	}
	return nil
}

// restartAgentAfterOTA is the hand-over without a box reboot.
func (s *Server) restartAgentAfterOTA(dst string) {
	pid, err := otaStartDetached(agentRestartScript(os.Getpid(), dst, agentArgs()))
	if err != nil {
		// The watchdog in run.sh respawns an agent that is gone, from the same
		// NAND path, within its 90 s cadence.
		s.logger.Error("post-OTA: could not start the restart helper, leaving the restart to run.sh's watchdog", "err", err)
	} else {
		s.claimAgentPIDFile(pid)
		s.logger.Info("post-OTA: exiting, the helper starts the new agent", "helperPID", pid)
	}
	s.stopEngineForRestart()
	otaExit(0)
}

// finishAgentOTA hands the speaker over to the binary just written to dst,
// once the answer has gone out, the stick is refreshed, the OTA marker is
// written and everything is synced.
func (s *Server) finishAgentOTA(dst string) {
	if s.otaRestartOnly(dst) {
		s.restartAgentAfterOTA(dst)
		return
	}
	err := otaReboot()
	if err == nil {
		return
	}
	// reboot binary missing or refused: fall back to the detached process
	// self-restart so we at least run the new binary. The sleep 70 covers the
	// :8081/:9080 TIME_WAIT window (60 s tcp_fin_timeout on this kernel) so
	// the new binary does not crash-loop on "address already in use" (seen
	// 2026-05-17).
	s.logger.Error("post-OTA reboot failed, falling back to process self-restart", "err", err)
	quoted := make([]string, 0, len(os.Args))
	for _, a := range os.Args {
		quoted = append(quoted, shQuote(a))
	}
	shCmd := "sleep 70 && exec " + strings.Join(quoted, " ") + " >> " + agentLogPath + " 2>&1"
	if _, serr := otaStartDetached(shCmd); serr != nil {
		s.logger.Error("self-restart fallback also failed", "err", serr)
		return
	}
	time.Sleep(100 * time.Millisecond)
	otaExit(0)
}
