// The update of a coprocessor Wi-Fi speaker whose agent still ends every
// update with a box reboot.
//
// On the scm chassis that run.sh records as wlan-mode "bco" (the first
// generation SoundTouch 20 and 30, the scm Wave, Lifestyle and CineMate
// consoles; NOT the Portable, which is "taigan-bco" and comes back fine) a
// soft `reboot` often leaves the speaker dark: the Wi-Fi coprocessor still
// answers ping, every Linux port stays closed, and only pulling the plug
// brings it back. The field logs of 2,798 updates show it plainly: 22 of 250
// updates of such speakers ended unreachable for target versions up to
// v1.3.0, none of 12 after, and one SoundTouch 20 failed 20 of 21 updates
// until an agent that restarts itself was on it. NAND headroom and the Bose
// firmware version do not correlate.
//
// The cause is the agent that RECEIVES the upload: up to v1.2.x its update
// handler always reboots the box once the new binary is written. From v1.3.0
// on it restarts only itself on wlan-mode "bco" (internal/webui/otarestart.go,
// commit ee8372f5). That fix lives in the receiving agent, so every update
// whose STARTING agent is older than v1.3.0 still ends in the risky reboot,
// whatever this app ships. Reproduced live 2026-11-15 on an scm SoundTouch 20:
// agent v0.9.84, app v1.6.2, upload accepted, old agent rebooted, box dark.
//
// So for exactly that speaker the app goes around the old agent: it asks the
// agent to open SSH (POST /api/agent/enable-ssh, there since v0.9.0, no
// reboot), reads run.sh's wlan-mode record, writes the new binary over SSH
// and restarts only the agent the way v1.3.0 does from inside: a detached
// helper takes over run.sh's PID file, ends the old agent and starts the new
// binary with the old agent's own flags. The box itself never restarts.
//
// Where that path cannot be taken (no SSH, chassis unreadable) the update runs
// as before, and the owner is told beforehand that the speaker may stay dark
// once and what to do then (BoxRestartPlan, the update confirmations), and
// afterwards, should it happen, to unplug it for 30 seconds (the unreachable
// verdict in ClassifyOTAResult).

package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/versioncmp"
)

// agentSelfRestartMinVersion is the first agent that restarts itself instead
// of rebooting the speaker after an update on wlan-mode "bco" (ee8372f5).
const agentSelfRestartMinVersion = "v1.3.0"

// agentEnableSSHMinVersion is the first agent with POST /api/agent/enable-ssh
// (43b8d49f), the way to open SSH on a running speaker without a reboot.
const agentEnableSSHMinVersion = "v0.9.0"

// The chassis classes the restart plan tells apart.
const (
	// chassisBCO is the coprocessor Wi-Fi chassis that hangs in a soft reboot
	// (run.sh wlan-mode "bco"; /info moduleType scm, not the Portable).
	chassisBCO = "bco"
	// chassisTaigan is the Portable: coprocessor Wi-Fi as well, but Verified
	// with the reboot (wlan-mode "taigan-bco").
	chassisTaigan = "taigan-bco"
	// chassisWiFi is every speaker with its own Wi-Fi module (sm2; wlan-mode
	// wlan0, wlan1 or ethernet-only). The reboot is fine there.
	chassisWiFi = "wifi"
	// chassisOther is a moduleType that is neither scm nor sm2.
	chassisOther = "other"
	// chassisUnknown: nothing safe to go on.
	chassisUnknown = "unknown"
)

// chassisFromInfo classifies a speaker from its Bose /info. The bco chassis is
// only ever an scm module, and of the scm speakers only the Portable (variant
// taigan) is known to come back from the reboot.
func chassisFromInfo(fi FirmwareInfo, err error) (chassis, why string) {
	if err != nil || !fi.Reachable {
		return chassisUnknown, "the speaker's Bose /info did not answer"
	}
	mt := strings.ToLower(strings.TrimSpace(fi.ModuleType))
	variant := strings.ToLower(strings.TrimSpace(fi.Variant))
	model := strings.ToLower(fi.Model)
	desc := fmt.Sprintf("/info moduleType=%s variant=%s type=%q", mt, variant, fi.Model)
	switch mt {
	case "":
		return chassisUnknown, "the speaker's /info names no moduleType"
	case "sm2":
		return chassisWiFi, desc
	case "scm":
		if variant == "taigan" || strings.Contains(model, "portable") {
			return chassisTaigan, desc
		}
		return chassisBCO, desc
	}
	return chassisOther, desc
}

// chassisFromWlanMode classifies a speaker from run.sh's wlan-mode record
// (/mnt/nv/streborn/wlan-mode). ok is false for an empty or unknown record.
// This is the record the v1.3.0 agent itself decides by, so where it is
// readable it settles the question.
func chassisFromWlanMode(mode string) (chassis string, ok bool) {
	switch strings.TrimSpace(mode) {
	case "bco":
		return chassisBCO, true
	case "taigan-bco":
		return chassisTaigan, true
	case "wlan0", "wlan1", "ethernet-only":
		return chassisWiFi, true
	}
	return chassisUnknown, false
}

// agentRebootsAfterUpdate reports whether an agent of version v ends an update
// with a box reboot whatever its chassis, i.e. predates v1.3.0. An empty or
// unparsable version, and the unstamped dev default "1.0.0", are unknown and
// never steer anything.
func agentRebootsAfterUpdate(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || strings.TrimPrefix(v, "v") == "1.0.0" || len(versioncmp.Parts(v)) == 0 {
		return false
	}
	return versioncmp.Less(v, agentSelfRestartMinVersion)
}

// The three ways an update can end, as far as the speaker's restart goes.
const (
	// restartPlanReboot: the update ends as it always did. Either the agent
	// restarts only itself (v1.3.0 and later on bco), or the chassis comes
	// back from the reboot, or nothing is known that says otherwise.
	restartPlanReboot = "reboot"
	// restartPlanAgentOnly: the app updates over SSH and restarts only the
	// agent, so the old agent's reboot never runs.
	restartPlanAgentOnly = "agent-restart"
	// restartPlanWarn: the old agent's reboot is likely and cannot be avoided
	// safely, so the owner is told beforehand.
	restartPlanWarn = "warn"
)

// RestartPlan is how this app expects a speaker's update to end. Bound: the
// update confirmations show a warning when Mode is "warn".
type RestartPlan struct {
	Mode         string `json:"mode"`
	Chassis      string `json:"chassis"`
	AgentVersion string `json:"agentVersion"`
	Reason       string `json:"reason"`
}

// planAgentRestart is the decision behind BoxRestartPlan and the no-reboot
// detour in UpdateBoxAgent, from the agent's version and the chassis.
func planAgentRestart(agentVersion, chassis string) RestartPlan {
	p := RestartPlan{Mode: restartPlanReboot, Chassis: chassis, AgentVersion: strings.TrimSpace(agentVersion)}
	if !agentRebootsAfterUpdate(p.AgentVersion) {
		if p.AgentVersion == "" || len(versioncmp.Parts(p.AgentVersion)) == 0 || strings.TrimPrefix(p.AgentVersion, "v") == "1.0.0" {
			p.Reason = "agent version unknown"
		} else {
			p.Reason = "agent " + p.AgentVersion + " restarts itself after an update on the coprocessor chassis"
		}
		return p
	}
	switch chassis {
	case chassisBCO:
		if versioncmp.Less(p.AgentVersion, agentEnableSSHMinVersion) {
			p.Mode = restartPlanWarn
			p.Reason = "agent " + p.AgentVersion + " reboots after an update and predates enable-ssh, so SSH cannot be opened without a reboot"
			return p
		}
		p.Mode = restartPlanAgentOnly
		p.Reason = "agent " + p.AgentVersion + " reboots after an update and this chassis can hang in a soft reboot: updating over SSH and restarting only the agent"
	case chassisUnknown:
		p.Mode = restartPlanWarn
		p.Reason = "agent " + p.AgentVersion + " reboots after an update and the chassis could not be read"
	case chassisTaigan:
		p.Reason = "the Portable comes back from the agent's reboot"
	case chassisWiFi:
		p.Reason = "a speaker with its own Wi-Fi module comes back from the agent's reboot"
	default:
		p.Reason = "not the coprocessor Wi-Fi chassis"
	}
	return p
}

// otaChassisLedger remembers, per speaker address, the chassis the last update
// found, so the verdict at the end of the verify window can still say which
// speaker it is when the speaker no longer answers anything.
var otaChassisLedger sync.Map

func noteOTAChassis(host, chassis string) {
	if host != "" && chassis != "" && chassis != chassisUnknown {
		otaChassisLedger.Store(host, chassis)
	}
}

func otaChassisOf(host string) string {
	if v, ok := otaChassisLedger.Load(host); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return chassisUnknown
}

// readChassis asks the speaker's Bose /info for its chassis and remembers it
// for the verdict. Best effort: unknown on any failure.
func (a *App) readChassis(host string) (chassis, why string) {
	fi, err := a.GetBoxFirmware(host)
	chassis, why = chassisFromInfo(fi, err)
	noteOTAChassis(host, chassis)
	return chassis, why
}

// BoxRestartPlan tells the update confirmation how this speaker's update will
// end. Mode "warn" is the one the owner is told about: an agent older than
// v1.3.0 on a coprocessor speaker (or one whose chassis cannot be read) where
// the app cannot route around the agent's reboot. The caller swallows the
// error: a speaker that cannot be asked is never blocked or warned.
func (a *App) BoxRestartPlan(host string, port int) (RestartPlan, error) {
	ver, err := a.BoxAgentVersion(host, port)
	if err != nil {
		return RestartPlan{Mode: restartPlanReboot, Chassis: chassisUnknown, Reason: "agent version unknown"}, err
	}
	bv := strings.TrimSpace(ver["version"])
	if !agentRebootsAfterUpdate(bv) {
		return planAgentRestart(bv, chassisUnknown), nil
	}
	chassis, why := a.readChassis(host)
	p := planAgentRestart(bv, chassis)
	p.Reason += " (" + why + ")"
	a.logger.Info("restart plan for the update confirmation", "host", host, "agent", bv, "chassis", chassis, "mode", p.Mode, "reason", p.Reason)
	return p, nil
}

// wlanModePath is run.sh's wlan-mode record on the speaker.
const wlanModePath = "/mnt/nv/streborn/wlan-mode"

// wlanModeReadCmd prints the wlan-mode record and an end marker, so an empty
// record and a failed command can be told apart.
const wlanModeReadCmd = "cat " + wlanModePath + " 2>/dev/null; echo; echo STR_WLAN_MODE_END"

// parseWlanModeOutput takes the record out of wlanModeReadCmd's output: the
// last non-empty line before the marker. ok is false without the marker.
func parseWlanModeOutput(out string) (mode string, ok bool) {
	i := strings.Index(out, "STR_WLAN_MODE_END")
	if i < 0 {
		return "", false
	}
	lines := strings.Split(strings.ReplaceAll(out[:i], "\r", ""), "\n")
	for j := len(lines) - 1; j >= 0; j-- {
		if s := strings.TrimSpace(lines[j]); s != "" {
			return s, true
		}
	}
	return "", true
}

// The on-box paths the agent-only restart works with. They are the ones every
// run.sh since v0.9.0 uses (PIDFILE, $LOG, setup_log, AGENT_CRASH_LOG).
const (
	agentPIDFilePath   = "/mnt/nv/streborn/agent.pid"
	agentLogPath       = "/tmp/streborn-agent.log"
	agentSetupLogPath  = "/mnt/nv/streborn/setup.log"
	agentCrashLogPath  = "/mnt/nv/streborn/logs/agent-crash.log"
	otaRestartMarker   = "/mnt/nv/streborn/ota-reboot"
	agentRestartHelper = "/tmp/streborn-app-agent-restart.sh"
	// agentRestartStarted is written by the helper as its first act, so the
	// SSH session can tell that it runs.
	agentRestartStarted = "/tmp/streborn-app-agent-restart.started"
)

// agentRestartMinUptimeSec keeps the hand-over out of reach of run.sh's boot
// watchdog (Phase A). For about 120 s after run.sh starts the agent it checks
// every 5 s and respawns an agent that is alive but not bound, and the gap
// between the old agent closing its listeners and the new one binding them
// looks exactly like that. Past it only the slow watchdog runs, which checks
// kill -0 alone, and the helper owns the PID file it reads. Measured from box
// boot, so it also covers a run.sh that started late. The app holds updates
// back until a box is 150 s up and the upload takes about a minute, so this
// rarely waits at all.
const agentRestartMinUptimeSec = 300

// agentRestartHelperScript is the detached helper that replaces the old
// agent's box reboot. It mirrors what v1.3.0 does from inside the agent
// (otarestart.go agentRestartScript and superviseNewAgentLines), driven from
// outside because the old agent does not know how:
//
//  1. Wait until the box is past run.sh's boot watchdog window.
//  2. Read the running agent's PID from run.sh's PID file and its flags from
//     /proc/<pid>/cmdline, so the new binary starts exactly as run.sh started
//     the old one (the flags of the run.sh that is actually on this speaker,
//     not of the one this app ships).
//  3. Claim the PID file with the helper's own PID before the old agent goes,
//     so run.sh's slow watchdog sees a live PID throughout and never starts a
//     second agent next to the new one.
//  4. SIGTERM the old agent (its own clean wind-down: hosts file, mDNS
//     goodbye, panel), SIGKILL after 30 s, then stop the Spotify engine it
//     supervised so the new agent can start its own on the port.
//  5. Start the new NAND binary, put its PID in the PID file, and wait for it
//     the way run.sh's start_agent subshell does, recording an exit in
//     setup.log and agent-crash.log. Once it is gone the PID file names a
//     dead PID and run.sh's watchdog respawns from the same NAND path.
//
// When no PID or no flags can be read, the helper only ends the old agent and
// leaves the start to run.sh's watchdog, which respawns from the NAND binary
// within its 90 s cadence. No TIME_WAIT sleep as in the old self-restart
// fallback: Go's listeners set SO_REUSEADDR and the old process is gone
// before the new one binds.
func agentRestartHelperScript() string {
	return agentRestartPaths{
		pidFile: agentPIDFilePath, bin: agentBinaryPath, log: agentLogPath,
		setupLog: agentSetupLogPath, crashLog: agentCrashLogPath, started: agentRestartStarted,
		minUptimeSec: agentRestartMinUptimeSec,
	}.helperScript()
}

// agentRestartPaths are the on-box paths and the uptime gate of the helper, a
// struct so a test can run the very script against a temp dir.
type agentRestartPaths struct {
	pidFile, bin, log, setupLog, crashLog, started string
	minUptimeSec                                   int
}

func (p agentRestartPaths) helperScript() string {
	return fmt.Sprintf(`echo "$$" > %[7]s
PIDFILE=%[1]s
BIN=%[2]s
LOG=%[3]s
SETUP=%[4]s
CRASH=%[5]s
note() { echo "[up=$(awk '{print int($1)}' /proc/uptime 2>/dev/null)s] $(date): $*" >> "$SETUP" 2>/dev/null; }
up=$(awk '{print int($1)}' /proc/uptime 2>/dev/null)
case "$up" in ''|*[!0-9]*) up=%[6]d ;; esac
if [ "$up" -lt %[6]d ]; then sleep $((%[6]d-up)); fi
old=$(cat "$PIDFILE" 2>/dev/null)
case "$old" in ''|*[!0-9]*) old="" ;; esac
set --
if [ -n "$old" ] && [ -r "/proc/$old/cmdline" ]; then
  args=$(tr '\000' '\n' < "/proc/$old/cmdline" 2>/dev/null)
  first=1
  while IFS= read -r a; do
    if [ "$first" = 1 ]; then first=0; continue; fi
    [ -n "$a" ] && set -- "$@" "$a"
  done <<STR_ARGS_EOF
$args
STR_ARGS_EOF
fi
if [ -z "$old" ] || [ "$#" -eq 0 ]; then
  note "app update: no agent flags to hand over (pid=${old:-none}), ending the old agent and leaving the start to run.sh's watchdog"
  [ -n "$old" ] && kill -TERM "$old" 2>/dev/null
  exit 0
fi
note "app update: restarting the agent (pid=$old) on the new binary, no box restart"
echo "$$" > "$PIDFILE"
kill -TERM "$old" 2>/dev/null
i=0
while [ -d "/proc/$old" ] && [ "$i" -lt 30 ]; do sleep 1; i=$((i+1)); done
if [ -d "/proc/$old" ]; then kill -9 "$old" 2>/dev/null; sleep 3; fi
killall go-librespot 2>/dev/null
sleep 2
cur=$(cat "$PIDFILE" 2>/dev/null)
if [ -n "$cur" ] && [ "$cur" != "$$" ]; then note "app update: run.sh's watchdog took over (pid=$cur), not starting a second agent"; exit 0; fi
"$BIN" "$@" >> "$LOG" 2>&1 &
bpid=$!
echo "$bpid" > "$PIDFILE"
note "app update: new agent started (pid=$bpid bin=$BIN)"
wait "$bpid"
ec=$?
note "app update: agent EXITED (started by the app's update restart helper, pid=$bpid): code=$ec"
{
  echo "==== agent exit (app update restart helper) pid=$bpid code=$ec $(date) ===="
  tail -n 40 "$LOG" 2>/dev/null
  echo ""
} >> "$CRASH" 2>/dev/null
`, shQuoteWord(p.pidFile), shQuoteWord(p.bin), shQuoteWord(p.log), shQuoteWord(p.setupLog), shQuoteWord(p.crashLog), p.minUptimeSec, shQuoteWord(p.started))
}

// shQuoteWord quotes one word for sh.
func shQuoteWord(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// agentRestartNoRebootCmd is the SSH command run once the new binary is
// verified and in place: write the helper, write the update marker the new
// agent reads at start (it then stands the power-on resume down once, exactly
// as after the old agent's update reboot), sync, and start the helper detached
// from the session (setsid where BusyBox has it, nohup either way, all stdio
// away from the session so it returns at once). The helper's first act is to
// write agentRestartStarted; STR_RESTART_ARMED says it did within a few
// seconds. Without it nothing else is attempted: ending the old agent from
// here while a slow helper may still start would race two agents for the
// listener ports.
func agentRestartNoRebootCmd() string {
	return "rm -f " + agentRestartStarted + "; " +
		"cat > " + agentRestartHelper + " <<'STR_HELPER_EOF'\n" +
		agentRestartHelperScript() +
		"STR_HELPER_EOF\n" +
		"date -u +%Y-%m-%dT%H:%M:%SZ > " + otaRestartMarker + " 2>/dev/null; sync; " +
		"if command -v setsid >/dev/null 2>&1; then setsid nohup sh " + agentRestartHelper + " </dev/null >/dev/null 2>&1 & " +
		"else nohup sh " + agentRestartHelper + " </dev/null >/dev/null 2>&1 & fi; " +
		"i=0; while [ ! -f " + agentRestartStarted + " ] && [ \"$i\" -lt 8 ]; do sleep 1; i=$((i+1)); done; " +
		"if [ -f " + agentRestartStarted + " ]; then echo STR_RESTART_ARMED; else echo STR_RESTART_NOT_STARTED; fi"
}

// errAgentRestartNotArmed: the new binary is verified and in place, but the
// restart could not be confirmed. The update must not fall through to the
// old agent's HTTP path then: that would push the same binary again and end
// in the very reboot this path exists to avoid.
var errAgentRestartNotArmed = errors.New("the new agent is on the speaker, but its restart could not be started")

// sshAgentPushPreamble is the upload session's space preamble for the agent
// binary over SSH, shared by the rebooting and the no-reboot path: drop the
// stranded SSH-repair staging dir and the regenerable junk the agent's
// reclaimNAND clears, and drop the Spotify engine too when the NAND is still
// tighter than needKB (sshPushNeedKB) after that. Mirrors reclaimNAND + run.sh
// cleanup_nand; see updateAgentViaSSH.
func sshAgentPushPreamble(needKB int64) string {
	return fmt.Sprintf("rm -rf /mnt/nv/streborn-install /mnt/nv/streborn/streborn-install 2>/dev/null; "+
		"rm -f /mnt/nv/sp-oauth.out /mnt/nv/streborn/cap*.ogg /mnt/nv/streborn/bin/*.new 2>/dev/null; "+
		"free=$(df -k /mnt/nv 2>/dev/null | tail -1 | awk '{print $(NF-2)}'); "+
		"if [ \"${free:-0}\" -lt \"%d\" ]; then rm -f /mnt/nv/streborn/bin/go-librespot /mnt/nv/streborn/bin/go-librespot.sha256 2>/dev/null; %s fi; ", needKB, sshStopEngineCmd)
}

// updateAgentViaSSHNoReboot writes the new agent over SSH (stream to .new,
// byte count, sha256 verify, rename, sync: sshPushFile) and restarts only the
// agent. SSH must already be open. Unlike updateAgentViaSSH there is no :17000
// escalation here, because that unlock REBOOTS a coprocessor speaker when it
// fails, and no reboot at the end.
func (a *App) updateAgentViaSSHNoReboot(host string, bin []byte) error {
	preamble := sshAgentPushPreamble(sshPushNeedKB(int64(len(bin))))
	if err := sshPushFile(host, agentBinaryPath, bin, sshPushOptions{preamble: preamble}); err != nil {
		return agentSSHPushError(err, len(bin))
	}
	out, err := boxSSHOutput(host, agentRestartNoRebootCmd(), 30*time.Second)
	if err == nil && strings.Contains(out, "STR_RESTART_ARMED") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %w (%s)", errAgentRestartNotArmed, err, strings.TrimSpace(out))
	}
	return fmt.Errorf("%w (%s)", errAgentRestartNotArmed, strings.TrimSpace(out))
}

// agentRestartSSHCloseWindow bounds how long the app waits for the new agent
// before it gives up closing the SSH port it opened. It covers the helper's
// uptime wait, the old agent's 30 s wind-down and run.sh's 90 s watchdog
// fallback.
const agentRestartSSHCloseWindow = 9 * time.Minute

// closeSSHAfterAgentRestart takes back the SSH port the no-reboot path opened,
// once the new agent answers: the reboot that used to close it does not
// happen, and the old agent has no endpoint to close it. The new agent's
// /api/agent/ssh {"closeNow":true} does, and it keeps a port the owner opted
// into open.
func (a *App) closeSSHAfterAgentRestart(host string, port int, preVersion, preBuild string) {
	deadline := time.Now().Add(agentRestartSSHCloseWindow)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Second)
		v, err := a.BoxAgentVersion(host, port)
		if err != nil || (v["version"] == preVersion && v["build"] == preBuild) {
			continue
		}
		a.closeAgentSSH(host, port)
		a.recordOTA(host, "no-reboot path: the new agent answers ("+describeAgentReading(v)+"); asked it to close the SSH port opened for the update")
		return
	}
	a.recordOTA(host, fmt.Sprintf("no-reboot path: the new agent did not answer within %s; the SSH port opened for the update closes at the speaker's next restart", agentRestartSSHCloseWindow))
	a.logger.Warn("no-reboot update: the new agent did not answer in time, SSH stays open until the next restart", "host", host)
}

// readWlanModeSSH reads run.sh's wlan-mode record over an open SSH channel.
func readWlanModeSSH(host string) (string, bool) {
	out, err := boxSSHOutput(host, wlanModeReadCmd, 15*time.Second)
	if err != nil {
		return "", false
	}
	return parseWlanModeOutput(out)
}

// restartOnlyOldBCOAgent is the detour for an agent older than v1.3.0 on the
// coprocessor Wi-Fi chassis: update over SSH and restart only the agent, so
// the old agent's update reboot never runs. It reports whether the update was
// delivered that way; false means the detour does not apply or could not run,
// and the normal path follows (which reboots, as it always did: the owner was
// warned beforehand where the app could tell, and the verdict says what to
// do). chassis is what /info said at the start of the update.
func (p *agentPush) restartOnlyOldBCOAgent(ver map[string]string, chassis, why string) bool {
	a, host, port, bin := p.a, p.host, p.port, p.bin
	bv := strings.TrimSpace(ver["version"])
	if !agentRebootsAfterUpdate(bv) {
		return false
	}
	plan := planAgentRestart(bv, chassis)
	a.recordOTA(host, "restart plan: chassis "+chassis+" ("+why+") -> "+plan.Mode+": "+plan.Reason)
	a.logger.Info("update agent: restart plan", "host", host, "agent", bv, "chassis", chassis, "mode", plan.Mode, "reason", plan.Reason)
	if plan.Mode == restartPlanReboot {
		return false
	}
	// SSH through the agent only. The :17000 unlock updateAgentViaSSH falls
	// back on reboots a coprocessor speaker when it fails, the very thing
	// this path avoids.
	if !versioncmp.Less(bv, agentEnableSSHMinVersion) {
		a.enableAgentSSH(host, port)
	}
	if hello, err := sshHandshake(host, 4); err != nil || !strings.Contains(hello, "STR_SSH_OK") {
		herr := sshHandshakeFailure(err, hello)
		a.recordOTA(host, "no-reboot path: SSH could not be opened ("+herr.Error()+"); the agent's own update follows, and it REBOOTS this speaker, which can leave a coprocessor Wi-Fi speaker dark until it is unplugged")
		a.logger.Warn("update agent: no-reboot path impossible, SSH did not open; the old agent's rebooting update follows", "host", host, "agent", bv, "err", herr)
		return false
	}
	mode, ok := readWlanModeSSH(host)
	c, known := chassisFromWlanMode(mode)
	a.recordOTA(host, fmt.Sprintf("no-reboot path: wlan-mode on the speaker reads %q (readable=%v)", mode, ok))
	if !known || c != chassisBCO {
		// The record is what decides on the speaker as well: the new agent
		// skips its stacked bootstrap reboot only on a coprocessor wlan-mode,
		// so an agent-only restart without it would reboot the box anyway.
		if known {
			noteOTAChassis(host, c)
		}
		a.recordOTA(host, "no-reboot path: not taken, run.sh's wlan-mode is not \"bco\"; the agent's own update follows")
		a.logger.Info("update agent: wlan-mode is not bco, the normal update path runs", "host", host, "wlanMode", mode)
		return false
	}
	noteOTAChassis(host, chassisBCO)
	a.recordOTA(host, "no-reboot path: uploading over SSH, then restarting only the agent; the speaker itself is not restarted")
	a.logger.Info("update agent: old agent on the coprocessor chassis, updating over SSH and restarting only the agent", "host", host, "agent", bv, "bytes", len(bin))
	err := a.updateAgentViaSSHNoReboot(host, bin)
	switch {
	case err == nil:
		a.recordOTA(host, "no-reboot path: new binary verified and in place, agent restart started (run.sh's PID file handed to the restart helper); no box reboot")
		go a.closeSSHAfterAgentRestart(host, port, bv, strings.TrimSpace(ver["build"]))
		return true
	case errors.Is(err, errAgentRestartNotArmed):
		a.recordOTA(host, "no-reboot path: "+err.Error()+"; not pushing again over HTTP, whose old agent would reboot the speaker")
		_ = p.unconfirmed("new binary in place over SSH, agent restart not confirmed; deferred to the version poll",
			"update agent: no-reboot restart not confirmed, deferring to the version poll", "host", host, "err", err)
		go a.closeSSHAfterAgentRestart(host, port, bv, strings.TrimSpace(ver["build"]))
		return true
	}
	a.recordOTA(host, "no-reboot path: SSH upload failed ("+err.Error()+"); the agent's own update follows, and it REBOOTS this speaker")
	a.logger.Warn("update agent: no-reboot SSH upload failed, falling back to the normal path", "host", host, "err", err)
	return false
}

// darkProbePorts are the speaker's own ports the verdict checks when nothing
// answered: Bose's web API, UPnP, the gabbo bus, the setup ports and SSH. A
// coprocessor speaker that hung in its restart has all of them closed while
// its Wi-Fi chip still answers ping.
var darkProbePorts = []int{8090, 8091, 8080, 17008, 17000, 22}

// openPorts returns which of ports accept a TCP connection, probed in parallel.
func openPorts(host string, ports []int, timeout time.Duration) []int {
	var mu sync.Mutex
	var wg sync.WaitGroup
	var open []int
	for _, pt := range ports {
		wg.Add(1)
		go func(pt int) {
			defer wg.Done()
			if tcpReachable(host, pt, timeout) {
				mu.Lock()
				open = append(open, pt)
				mu.Unlock()
			}
		}(pt)
	}
	wg.Wait()
	return open
}

// unplugAdviceLedger marks the speakers whose last update ended unreachable
// on the coprocessor chassis with every port closed: the owner is told to
// unplug them for 30 seconds.
var unplugAdviceLedger sync.Map

// unplugAdvised reports whether the last verdict for host advised unplugging.
func unplugAdvised(host string) bool {
	_, ok := unplugAdviceLedger.Load(host)
	return ok
}

// unreachableVerdictLine is the journal's outcome line for a speaker that
// answered nothing after the verify window. On the coprocessor chassis with
// every port closed it says what the field data says: the speaker hung in its
// restart and a power cut brings it back.
func unreachableVerdictLine(bcoAllClosed bool, cause string) string {
	if bcoAllClosed {
		return "outcome: NOT CONFIRMED - box unreachable after the verify window, and it is a coprocessor Wi-Fi speaker (scm) with every port closed (" +
			portList(darkProbePorts) + "): this chassis can hang in its own restart; unplug it for 5 minutes, plug it back in and wait 3 minutes (a later discovery sighting on the new build adds a corrective line): " + cause
	}
	return "outcome: NOT CONFIRMED - box unreachable after the verify window (a later discovery sighting on the new build adds a corrective line): " + cause
}

func portList(ports []int) string {
	s := make([]string, len(ports))
	for i, p := range ports {
		s[i] = fmt.Sprint(p)
	}
	return strings.Join(s, " ")
}
