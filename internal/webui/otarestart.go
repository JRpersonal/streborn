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
//
// Three things the reboot did for free are done by hand here:
//
//   - A clean shutdown. init's SIGTERM ran the agent's own wind-down (flush of
//     Recently played, mDNS goodbye, panel hand-back, hosts file restore); the
//     restart raises the same SIGTERM against itself instead of os.Exit.
//   - Supervision. run.sh's start_agent subshell waits on the agent it started
//     and logs its exit; the agent the helper starts is not its child, so the
//     helper supervises it the same way (superviseNewAgentLines), and the
//     hand-over itself is marked so a supervisor can tell it from a crash.
//   - Staying out of run.sh's boot watchdog. For its first ~120 s after
//     start_agent that watchdog respawns an agent that is alive but not bound,
//     and the helper's gap between the old agent closing its listeners and
//     the new one binding them looks exactly like that. The hand-over waits
//     until this agent is older than that window (waitOutBootWatchdog).

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// otaState is what an agent knows about its own update: whether one is in
// flight, and the bookkeeping of the receive routes around it. Production has
// exactly one per process (processOTAState), because the facts are the
// process's: one running binary, one NAND, one RAM stage directory. A Server
// reaches it through otaSt; a test hands its Server a fresh one (Server.ota),
// so no test sees the update another one started.
//
// It is not a plain field of Server because cmd/agent asks about the update
// before and beside the Server, through package functions (RebootUnsafe,
// StartRescueRetry, CleanStaleRAMStages); those and the Server have to read
// the same state.
type otaState struct {
	// inFlight is set while an agent update is writing the new binary and
	// heading for its restart. Only beginUpdate and endUpdate write it.
	//
	// It exists because the update DELETES the Spotify engine when NAND is
	// tight: it stops the engine and reclaims its ~16 MB to make room for the
	// new binary. An engine delivered into that window is therefore thrown away
	// moments later, and the speaker comes back without one. That is not
	// hypothetical, it cost a SoundTouch 30 its engine twice in one day, both
	// times because a client pushed the engine while the update it had just
	// started was still running.
	//
	// A client cannot see this from the outside: the old agent keeps answering
	// normally right up to the restart, so every check it can make says the
	// speaker is ready. So the agent has to say no.
	//
	// Atomic: it is read by requests, by the self-update's goroutines and by
	// the rescue agent's retry while an upload's own goroutine writes it.
	inFlight atomic.Bool
}

// processOTAState is the update state of this agent process.
var processOTAState = &otaState{}

// otaSt is the update state this server works on.
func (s *Server) otaSt() *otaState {
	if s.ota != nil {
		return s.ota
	}
	return processOTAState
}

// beginUpdate marks an agent update as in flight. From here the engine may be
// reclaimed at any moment and the process may exit.
func (st *otaState) beginUpdate() { st.inFlight.Store(true) }

// endUpdate takes the mark back. Every path that gives up WITHOUT restarting
// the speaker calls it, so a failed update does not leave the speaker refusing
// engine deliveries and updates until its next reboot: the receive that
// failed, the RAM-staged swap that could not be armed, the flash verify that
// did not pass (all installAgentUpload) and the hand-over nothing took
// (abandonOTAHandOver). A path that does restart never calls it; the process
// ends with the mark set.
func (st *otaState) endUpdate() { st.inFlight.Store(false) }

// updating reports whether an agent update is in flight.
func (st *otaState) updating() bool { return st.inFlight.Load() }

// otaRestartOnlyWLANMode is the one wlan-mode whose speakers restart the agent
// instead of rebooting after an update.
const otaRestartOnlyWLANMode = "bco"

// The on-box paths the hand-over touches, vars so tests can point them at a
// temp dir. agentPIDFile is where run.sh keeps the PID its watchdogs kill -0;
// the restart helper claims it before this process exits, so the watchdog sees
// a live agent throughout and never starts a second one next to it.
// agentLogPath is run.sh's $LOG (the agent's stdout); agentSetupLogPath and
// agentCrashLogPath are the NAND files run.sh's start_agent subshell records
// an agent's exit in. agentHandOverMarker holds the PID of an agent that exits
// for an update restart, on tmpfs, so its supervisor can tell that exit from a
// crash.
var (
	agentPIDFile        = "/mnt/nv/streborn/agent.pid"
	agentLogPath        = "/tmp/streborn-agent.log"
	agentSetupLogPath   = "/mnt/nv/streborn/setup.log"
	agentCrashLogPath   = "/mnt/nv/streborn/logs/agent-crash.log"
	agentHandOverMarker = "/tmp/streborn-agent-handover"
)

// otaHandOverMinAgentAge is how old this agent must be before it hands over
// without a reboot. run.sh's boot watchdog (Phase A) checks every 5 s for 24
// rounds after start_agent and respawns an agent that is alive but unbound
// once its 30 s grace is over; during a hand-over the helper is exactly that.
// 24 rounds plus their probes end well inside 180 s of this agent's start, so
// past it only the slow watchdog runs, which checks kill -0 alone. Desktop
// pushes practically never wait here (the app holds back until the box is
// 150 s up and a push takes about a minute); a self-update started right
// after a boot can.
const otaHandOverMinAgentAge = 180 * time.Second

// otaShutdownBackstop bounds the clean shutdown. A wind-down that has not
// finished by then ends with os.Exit(0); the helper's kill -9 after its own
// wait stays the last resort behind it.
const otaShutdownBackstop = 20 * time.Second

// processStart is when this agent started, close enough to run.sh's
// start_agent for otaHandOverMinAgentAge.
var processStart = time.Now()

// The outside effects of the hand-over, swapped out in tests.
var (
	otaWLANMode      = readWlanMode
	otaRunningBinary = runningAgentBinary
	otaReboot        = func() error { return exec.Command("reboot").Run() }
	otaStartDetached = startDetachedShell
	otaExit          = os.Exit
	otaShutdown      = shutdownForRestart
	otaAgentAge      = func() time.Duration { return time.Since(processStart) }
	otaSleep         = time.Sleep
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

// shutdownForRestart ends this process the way init's SIGTERM ended it on a
// reboot: the signal context in cmd/agent cancels, and agent.wait() winds
// everything down before main returns. Should that hang, or the signal not be
// deliverable, it exits anyway.
func shutdownForRestart() {
	if p, err := os.FindProcess(os.Getpid()); err == nil && p.Signal(syscall.SIGTERM) == nil {
		time.Sleep(otaShutdownBackstop)
	}
	os.Exit(0)
}

// selfRestartAfterOTA reports whether the update hands over by restarting the
// agent process instead of rebooting the speaker. Only on wlan-mode "bco", and
// only while this agent runs from the NAND binary the update just replaced:
// that is the binary run.sh's watchdog starts again later, so the restarted
// agent and any later respawn agree. An agent run.sh started from a RAM copy
// or the stick reboots as before, so the next boot picks its binary afresh.
//
// The rescue agent a failed RAM-staged swap left running from RAM
// (ota_rescue.go) counts as running from dst as well: it is the build that
// could not be written there, run.sh's watchdog would start dst after it, and
// rebooting this chassis to get there is exactly what the restart avoids.
func selfRestartAfterOTA(wlanMode, running, dst string) bool {
	if wlanMode != otaRestartOnlyWLANMode || running == "" {
		return false
	}
	running = filepath.Clean(running)
	return running == filepath.Clean(dst) || running == filepath.Clean(ramRescueBin)
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

// handOverDelay is how much longer this agent has to keep running before a
// hand-over is out of reach of run.sh's boot watchdog.
func handOverDelay() time.Duration {
	if d := otaHandOverMinAgentAge - otaAgentAge(); d > 0 {
		return d
	}
	return 0
}

// waitOutBootWatchdog holds the hand-over until handOverDelay has passed. The
// old agent keeps serving meanwhile, so the watchdog keeps seeing it bound.
func (s *Server) waitOutBootWatchdog() {
	d := handOverDelay()
	if d <= 0 {
		return
	}
	s.logger.Info("post-OTA: agent started moments ago, waiting before the restart so run.sh's boot watchdog does not start a second agent",
		"waitSec", int(d.Round(time.Second).Seconds()))
	otaSleep(d)
}

// shQuote quotes one word for sh.
func shQuote(a string) string {
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

// superviseNewAgentLines is the tail of a hand-over helper: start the new
// agent as the helper's child, put its PID in run.sh's PID file (which the old
// agent handed to the helper), and wait for it the way run.sh's start_agent
// subshell waits for the agents it starts. An exit is recorded in the same
// NAND files with its code or signal and the agent's last 40 log lines, unless
// the agent marked it as its own update hand-over.
//
// The helper only starts an agent while the PID file names the helper itself.
// Any other PID means the claim did not land (a full NAND refuses even this
// one line), and then run.sh's watchdog owns the restart: it either already
// runs an agent of its own, and two agents cannot share the listener ports,
// or it sees the old agent's PID dead and respawns from the same NAND path.
// An empty or missing file starts the agent regardless, because the slow
// watchdog stops for good once the file is gone.
func superviseNewAgentLines(bin string, args []string) string {
	return fmt.Sprintf(`cur=$(cat %q 2>/dev/null)
if [ -n "$cur" ] && [ "$cur" != "$$" ]; then exit 0; fi
`, agentPIDFile) + superviseAgentLines(shQuote(bin), args)
}

// superviseAgentLines starts the agent named by binWord (a word already quoted
// for sh, so a caller can pass a variable such as "$run") with args, writes its
// PID into run.sh's PID file and waits for it, recording an exit that is not a
// hand-over as superviseNewAgentLines describes. It has no guard of its own.
func superviseAgentLines(binWord string, args []string) string {
	words := make([]string, 0, len(args)+1)
	words = append(words, binWord)
	for _, a := range args {
		words = append(words, shQuote(a))
	}
	return fmt.Sprintf(`rm -f %[2]q
%[3]s >> %[4]q 2>&1 &
bpid=$!
echo "$bpid" > %[1]q
wait "$bpid"
ec=$?
if [ "$(cat %[2]q 2>/dev/null)" = "$bpid" ]; then rm -f %[2]q; exit 0; fi
case "$ec" in
  0) why="clean exit (unexpected for a daemon)" ;;
  12[89]|1[3-9][0-9]) why="killed by signal $((ec-128))" ;;
  *) why="exit status $ec" ;;
esac
echo "[up=$(awk '{print int($1)}' /proc/uptime 2>/dev/null)s] $(date): agent EXITED (started by the update restart helper, pid=$bpid): code=$ec, $why" >> %[5]q 2>/dev/null
{
  echo "==== agent exit (update restart helper) pid=$bpid code=$ec ($why) $(date) ===="
  tail -n 40 %[4]q 2>/dev/null
  echo ""
} >> %[6]q 2>/dev/null`,
		agentPIDFile, agentHandOverMarker, strings.Join(words, " "),
		agentLogPath, agentSetupLogPath, agentCrashLogPath)
}

// agentRestartScript is the helper that replaces the box reboot on a
// coprocessor Wi-Fi speaker: wait for this agent to exit (kill -9 after 30 s),
// then start and supervise the new binary. No 70 s TIME_WAIT sleep as in the
// reboot fallback: Go's listeners set SO_REUSEADDR, and the old process is
// gone (its listeners closed) before the new one binds.
func agentRestartScript(agentPID int, bin string, args []string) string {
	return fmt.Sprintf(`i=0
while [ -d /proc/%d ] && [ "$i" -lt 30 ]; do sleep 1; i=$((i+1)); done
if [ -d /proc/%d ]; then kill -9 %d 2>/dev/null; sleep 3; fi
sleep 2
%s`, agentPID, agentPID, agentPID, superviseNewAgentLines(bin, args))
}

// claimAgentPIDFile writes the helper's PID into run.sh's PID file. Best
// effort: without it the helper steps aside and run.sh's watchdog starts the
// new binary within 90 s (superviseNewAgentLines).
func (s *Server) claimAgentPIDFile(pid int) {
	if err := os.WriteFile(agentPIDFile, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		s.logger.Warn("post-OTA: could not hand run.sh's PID file to the restart helper; its watchdog starts the agent instead",
			"helperPID", pid, "err", err)
	}
}

// markHandOver records that this process is about to exit for an update
// restart, for a helper that supervises it (superviseNewAgentLines). run.sh's
// own start_agent subshell does not read it: it still records this exit as
// "agent EXITED ... code=0" with the agent's tail in agent-crash.log, and that
// tail ends with handOverAndExit's "exiting for an agent update, not a crash"
// and the clean shutdown after it. Teaching run.sh to read the marker waits
// for a release that changes run.sh anyway: any run.sh change costs every
// other chassis a bootstrap reboot (cmd/agent maybeRebootAfterBootstrapSync).
func markHandOver() {
	_ = os.WriteFile(agentHandOverMarker, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
}

// stopEngineForRestart stops the supervised Spotify engine before the agent
// exits. A reboot ended it with everything else; a process restart would
// leave it orphaned and holding its port when the new agent starts its own.
func (s *Server) stopEngineForRestart() {
	if s.spotifyStop != nil {
		s.spotifyStop()
	}
}

// handOverAndExit ends this agent for a helper that starts the new one: hand
// run.sh's PID file to helperPID and mark the exit as a hand-over (helperPID
// 0: there is no helper and run.sh's watchdog takes over), stop the engine,
// and shut down cleanly.
func (s *Server) handOverAndExit(helperPID int, then string) {
	if helperPID > 0 {
		s.claimAgentPIDFile(helperPID)
		markHandOver()
	}
	s.stopEngineForRestart()
	s.logger.Info("exiting for an agent update, not a crash; "+then, "helperPID", helperPID)
	otaShutdown()
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
	s.waitOutBootWatchdog()
	pid, err := otaStartDetached(agentRestartScript(os.Getpid(), dst, agentArgs()))
	if err != nil {
		// The watchdog in run.sh respawns an agent that is gone, from the same
		// NAND path, within its 90 s cadence.
		s.logger.Error("post-OTA: could not start the restart helper, leaving the restart to run.sh's watchdog", "err", err)
		s.handOverAndExit(0, "run.sh's watchdog starts the new binary")
		return
	}
	s.handOverAndExit(pid, "the restart helper starts the new binary, no box reboot")
}

// otaHandOver is how finishAgentOTA left the speaker.
type otaHandOver int

const (
	// otaHandOverExiting: this process is ending, a helper or the fallback
	// starts the new binary.
	otaHandOverExiting otaHandOver = iota
	// otaHandOverRebooting: reboot accepted the request; init ends this
	// process within seconds.
	otaHandOverRebooting
	// otaHandOverFailed: nothing took over, this agent keeps running the
	// previous build.
	otaHandOverFailed
)

// otaRebootGrace is how long an accepted reboot gets to end this process. A
// reboot takes this agent down within seconds; one still running after two
// minutes is not rebooting.
const otaRebootGrace = 2 * time.Minute

// finishAgentOTA hands the speaker over to the binary just written to dst,
// once the answer has gone out, the stick is refreshed, the OTA marker is
// written and everything is synced.
func (s *Server) finishAgentOTA(dst string) otaHandOver {
	if s.otaRestartOnly(dst) {
		s.restartAgentAfterOTA(dst)
		return otaHandOverExiting
	}
	err := otaReboot()
	if err == nil {
		return otaHandOverRebooting
	}
	s.logger.Error("post-OTA reboot failed, falling back to process self-restart", "err", err)
	if s.selfRestartFallback() {
		return otaHandOverExiting
	}
	return otaHandOverFailed
}

// selfRestartFallback is the hand-over when reboot is missing or refused: a
// detached process self-restart, so the speaker at least runs the new binary.
// The sleep 70 covers the :8081/:9080 TIME_WAIT window (60 s tcp_fin_timeout
// on this kernel) so the new binary does not crash-loop on "address already
// in use" (seen 2026-05-17). False when the helper could not be started; this
// agent then keeps running.
func (s *Server) selfRestartFallback() bool {
	quoted := make([]string, 0, len(os.Args))
	for _, a := range os.Args {
		quoted = append(quoted, shQuote(a))
	}
	shCmd := "sleep 70 && exec " + strings.Join(quoted, " ") + " >> " + agentLogPath + " 2>&1"
	if _, serr := otaStartDetached(shCmd); serr != nil {
		s.logger.Error("self-restart fallback also failed", "err", serr)
		return false
	}
	time.Sleep(100 * time.Millisecond)
	otaExit(0)
	return true
}

// settleOTAHandOver follows a hand-over to its end. Nothing to do while this
// process is on its way out. An accepted reboot is given otaRebootGrace; an
// agent that is still running after it tries the self-restart instead. When
// nothing takes over, the update is abandoned (abandonOTAHandOver), because
// an agent that stays up must not keep saying an update is installing.
func (s *Server) settleOTAHandOver(h otaHandOver, stopSplash func()) {
	switch h {
	case otaHandOverExiting:
		return
	case otaHandOverRebooting:
		otaSleep(otaRebootGrace)
		s.logger.Error("post-OTA: the reboot was accepted but this agent still runs, falling back to process self-restart",
			"graceSec", int(otaRebootGrace/time.Second))
		if s.selfRestartFallback() {
			return
		}
	}
	s.abandonOTAHandOver(stopSplash)
}

// abandonOTAHandOver puts this agent back into service after an update that
// was written but could not restart the speaker. Before, the in-flight mark
// stayed set for the life of the process, so the speaker refused every Spotify engine
// delivery (409) and every self-update until someone pulled the plug, and the
// Portable's display kept showing the update screen.
//
// The new binary stays on the NAND: the next power cycle starts it, and the
// self-update marker stays too, so that start finishes the update (engine
// included). The OTA-reboot marker goes, because no OTA reboot happened and
// the next start is a genuine power-on.
func (s *Server) abandonOTAHandOver(stopSplash func()) {
	s.logger.Error("post-OTA: the speaker could not be restarted; it keeps running the previous version until it is power-cycled, and accepts updates and engine deliveries again")
	_ = os.Remove(otaRebootMarkerPath)
	if stopSplash != nil {
		stopSplash()
	}
	s.selfUpd.failIfRebooting("The update is on the speaker, but the speaker could not restart itself. Unplug it for ten seconds and plug it back in.")
	s.otaSt().endUpdate()
}
