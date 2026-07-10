// Stick-free SSH unlock over the Bose :17000 TAP diagnostic shell.
//
// The stock firmware exposes a telnet-style command shell on TCP 17000 that
// accepts `envswitch boseurls set "<marge>" "<swupdate>"` and
// `sys configuration <key> "<value>"`. Writing a shell payload into the cloud
// URLs makes the firmware run it the next time it re-parses those URLs (a
// marge/boseurls check, or a boot): the payload touches the remote_services
// marker sshd gates on and starts sshd. Once :22 is open, RepairInstallViaSSH
// pushes STR over SSH with no USB stick at all - so a SoundTouch 300, Portable
// or SA-4 (none of which reliably read the stick at boot) can be converted
// without a stick or an adapter.
//
// Credit: the technique is from the gesellix/Bose-SoundTouch community
// (issues #471, #515, #519). This is an independent Go port for STR.
//
// Reliability, from live testing on ST300 (ginger) and Portable (taigan): the
// payload only fires when the box actually does a marge/boseurls check, and a
// box only checks when it has a marge account. A used box that still carries a
// Bose margeAccountUUID checks on its own; a factory-fresh box (empty UUID -
// every new Portable/ST20) never does. enableSSHViaTelnet therefore SEEDS a
// throwaway account over :17000 first (`envswitch accountid set`, see
// unlockAccountID) so every box checks, then injects and sends `sys reboot` to
// trigger the check on the next boot without the user touching the speaker. This
// account seed - firewall-free, no local responder - is what opened :22 on the
// taigan Portable, whose empty UUID had made every prior injection inert. A
// heavier local-responder variant (enableSSHViaTelnetBootstrap,
// telnet_bootstrap_marge.go) remains as a fallback for the uninstall path.

package main

import (
	"fmt"
	"net"
	"strings"
	"time"
)

// remoteServicesInjection is appended to the marge URL. When the firmware next
// shells out that URL it runs these commands: touch the remote_services marker
// sshd checks for, then start sshd. The whole value is double-quoted in the
// telnet command because it contains spaces and semicolons. The trailing " #"
// is defensive: if a firmware build shells out the URL with a path appended
// (curl $margeServerUrl/streaming/...), the "#" comments out that suffix so the
// last command stays `/etc/init.d/sshd start` instead of becoming
// `/etc/init.d/sshd start/streaming/...` (which would not start sshd). It is
// harmless on builds that append nothing.
const remoteServicesInjection = ";touch /tmp/remote_services;/etc/init.d/sshd start #"

// unlockAccountID is a throwaway marge account id STR seeds over :17000 with
// `envswitch accountid set` before injecting. It is the key to a stick-free
// unlock on a box with no residual Bose account (every factory-fresh Portable /
// ST20, whose /info shows an empty margeAccountUUID): the SSH injection only
// fires when the box actually runs its periodic marge check, and a box with no
// account never checks on its own. Seeding an account makes the box populate
// margeAccountUUID (verified live: /info reflects it immediately) and start
// checking, so on the next boot it shells out the injected margeServerUrl and
// sshd comes up. Proven on a taigan Portable: with an account seeded, :22 opened
// ~60 s after reboot; without it, the identical injection never fired. Seven
// digits stays within the id length every SoundTouch firmware accepts (gesellix
// IsValidAccountID); STR's autopair later replaces it with the real STR account,
// and it is a dead post-cloud-shutdown id regardless.
const unlockAccountID = "9999999"

// telnetEnableBase is the placeholder cloud host used while unlocking SSH on a
// box that still does marge checks on its own. A reserved .invalid name
// (RFC 6761) fails DNS instantly, so the injected `<tool> <base>` fails fast and
// the `;touch;sshd` payload runs without waiting on a network timeout.
// resetBoseURLsViaTelnet restores the real hosts after. (The factory-reset path
// instead points the base at a live local marge; see telnet_bootstrap_marge.go.)
const telnetEnableBase = "https://str-setup.invalid"

// stockBoseURLs are the factory cloud URLs restored after a stick-free unlock,
// so no command injection lingers in the box config and STR's marge interception
// (which catches streaming.bose.com via /etc/hosts) works normally. Values read
// live from a stock SoundTouch 300 and Portable on FW 27.0.6.
var stockBoseURLs = struct{ marge, stats, swUpdate, bmx string }{
	marge:    "https://streaming.bose.com",
	stats:    "https://events.api.bosecm.com",
	swUpdate: "https://worldwide.bose.com/updates/soundtouch",
	bmx:      "https://content.api.bose.io/bmx/registry/v1/services",
}

// buildEnableSSHCommands returns the ordered :17000 command list that writes the
// enable-SSH injection. It writes BOTH the envswitch persistence layer (used by
// rhino/sm2 boxes, where it reflects in /info margeURL) AND all four
// `sys configuration` runtime keys (used by ginger/taigan, where envswitch does
// not reflect in getpdo/`/info` but sys configuration does) - the #519
// full-config superset, so one sequence covers every variant. Pure and
// hardware-free so it can be unit-tested.
func buildEnableSSHCommands(base string) []string {
	inj := base + remoteServicesInjection
	swu := base + "/updates/soundtouch"
	return []string{
		`sys configuration bmxRegistryUrl "` + base + `/bmx/registry/v1/services"`,
		`sys configuration statsServerUrl "` + base + `"`,
		`sys configuration margeServerUrl "` + inj + `"`,
		`sys configuration swUpdateUrl "` + swu + `"`,
		`envswitch boseurls set "` + inj + `" "` + swu + `"`,
	}
}

// buildResetBoseURLCommands returns the :17000 command list that restores stock
// cloud URLs (removing the injection). The envswitch command is written first
// (not last) so that if the TAP session drops mid-sequence, the layer the
// running client actually loads at boot on ginger/taigan is the one most likely
// to already be clean.
func buildResetBoseURLCommands() []string {
	return []string{
		`envswitch boseurls set "` + stockBoseURLs.marge + `" "` + stockBoseURLs.swUpdate + `"`,
		`sys configuration margeServerUrl "` + stockBoseURLs.marge + `"`,
		`sys configuration statsServerUrl "` + stockBoseURLs.stats + `"`,
		`sys configuration swUpdateUrl "` + stockBoseURLs.swUpdate + `"`,
		`sys configuration bmxRegistryUrl "` + stockBoseURLs.bmx + `"`,
	}
}

// tapConn is a thin client for the Bose :17000 TAP command shell.
type tapConn struct {
	conn net.Conn
}

// dialTAP connects to :17000 and drains the initial "->" banner.
func dialTAP(host string, timeout time.Duration) (*tapConn, error) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, "17000"), timeout)
	if err != nil {
		return nil, err
	}
	t := &tapConn{conn: conn}
	t.drain(1500 * time.Millisecond)
	return t, nil
}

// drain reads until an idle gap after a "->"-terminated reply, or maxWindow
// elapses. The TAP shell echoes the command, an optional result, then "->".
func (t *tapConn) drain(maxWindow time.Duration) string {
	var sb strings.Builder
	buf := make([]byte, 4096)
	deadline := time.Now().Add(maxWindow)
	for time.Now().Before(deadline) {
		_ = t.conn.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
		n, err := t.conn.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if sb.Len() > 0 && strings.HasSuffix(strings.TrimSpace(sb.String()), "->") {
					break
				}
				continue
			}
			break
		}
	}
	return sb.String()
}

// send writes a command and returns the drained reply.
func (t *tapConn) send(cmd string) (string, error) {
	if _, err := t.conn.Write([]byte(cmd + "\r\n")); err != nil {
		return "", err
	}
	return t.drain(2500 * time.Millisecond), nil
}

func (t *tapConn) close() { _ = t.conn.Close() }

// sendAndLog runs one TAP command, appends a "-> cmd\nreply" line to log, and
// returns the transport error (nil on a clean round-trip). A firmware that does
// not expose a given key replies "Command not found" / "Invalid Command"; that
// is not a transport error, so the caller keeps going with the remaining
// commands (best-effort, fallback-first). Shared by every :17000 command loop so
// the log format and abort semantics live in one place.
func sendAndLog(t *tapConn, log *strings.Builder, cmd string) error {
	reply, err := t.send(cmd)
	fmt.Fprintf(log, "-> %s\n%s\n", cmd, strings.TrimSpace(reply))
	return err
}

// waitForSSHOpen polls TCP :22 until it accepts a connection or budget elapses.
// Shared by both unlock paths.
func (a *App) waitForSSHOpen(host string, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if tcpReachable(host, 22, 2*time.Second) {
			return true
		}
		a.emitInstallHeartbeat()
		time.Sleep(4 * time.Second)
	}
	return false
}

// enableSSHViaTelnet performs the full stick-free unlock over :17000: it seeds a
// throwaway marge account (so a box with no residual account still runs the
// marge check the injection rides on), writes the enable-SSH injection, reboots
// the box to trigger it, and polls :22 up to a model-aware budget. Returns
// whether SSH came up and a redaction-free command log for diagnostics. This one
// path now covers both used boxes (residual Bose UUID) and factory-fresh ones
// (empty UUID): the account seed is what made the previously-stuck taigan
// Portable open :22 (its /info UUID was empty, so it never checked marge, so the
// injection never fired - see unlockAccountID). The placeholder .invalid base
// means nothing on the LAN is left pointed at a live host on failure, so this
// path needs no recovery reset.
func (a *App) enableSSHViaTelnet(host, model string) (bool, string) {
	var log strings.Builder
	t, err := dialTAP(host, 5*time.Second)
	if err != nil {
		a.logger.Info("telnet-enable: :17000 not reachable, cannot unlock SSH stick-free", "host", host, "err", err)
		return false, "port 17000 not reachable: " + err.Error()
	}
	// Seed a throwaway marge account first so a box with no residual Bose account
	// still runs the marge check the injection rides on (see unlockAccountID).
	// Best-effort: a firmware that rejects `envswitch accountid` still gets the
	// injection written below, so log and keep going rather than abort.
	if serr := sendAndLog(t, &log, "envswitch accountid set "+unlockAccountID); serr != nil {
		a.logger.Warn("telnet-enable: seeding marge account failed (continuing to inject anyway)", "host", host, "err", serr)
	}
	for _, cmd := range buildEnableSSHCommands(telnetEnableBase) {
		if serr := sendAndLog(t, &log, cmd); serr != nil {
			t.close()
			a.logger.Warn("telnet-enable: command transport failed", "host", host, "cmd", cmd, "err", serr)
			return false, log.String()
		}
	}
	if v, _ := t.send("getpdo CurrentSystemConfiguration"); strings.TrimSpace(v) != "" {
		fmt.Fprintf(&log, "-> getpdo CurrentSystemConfiguration\n%s\n", strings.TrimSpace(v))
	}
	// Reboot to trigger the boot-time URL re-parse without the user touching the
	// speaker. This is STR's advantage over the manual CLI flow.
	_, _ = t.send("sys reboot")
	t.close()
	boxSSHClients.invalidateHost(host)
	a.logger.Info("telnet-enable: injection written and sys reboot sent; polling for SSH", "host", host, "model", model)

	if a.waitForSSHOpen(host, agentWaitBudget(model)) {
		a.logger.Info("telnet-enable: SSH opened stick-free via :17000 unlock", "host", host)
		return true, log.String()
	}
	a.logger.Info("telnet-enable: SSH did not open within budget (box may need a physical power cycle, or is factory-reset - the bootstrap path handles that)", "host", host)
	return false, log.String()
}

// resetBoseURLsViaTelnet restores stock cloud URLs over :17000 after a stick-free
// unlock, removing the command injection so STR's own marge interception takes
// over and no injection lingers. Retried, because leaving the box pointed at the
// injection URL is the worst outcome of this whole flow: retries take a
// just-reachable box and make the restore reliable. Returns nil once all
// commands land cleanly in one attempt, otherwise the last error.
func (a *App) resetBoseURLsViaTelnet(host string) error {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(3 * time.Second)
		}
		t, err := dialTAP(host, 5*time.Second)
		if err != nil {
			lastErr = err
			continue
		}
		var log strings.Builder
		ok := true
		for _, cmd := range buildResetBoseURLCommands() {
			if serr := sendAndLog(t, &log, cmd); serr != nil {
				lastErr = serr
				ok = false
				break
			}
		}
		t.close()
		if ok {
			return nil
		}
	}
	return lastErr
}

// restoreStockBoseURLsAndReboot restores stock cloud URLs AND reboots the box so
// the running marge client stops dialing whatever custom/dead host the unlock
// pointed it at. Best-effort recovery for a FAILED stick-free unlock: without it
// a box left with an injection URL (especially a live-LAN-IP one from the
// bootstrap path) would marge-check a now-dead responder forever and defeat
// STR's later streaming.bose.com interception. Errors are logged, not returned -
// this runs on an already-failing path.
func (a *App) restoreStockBoseURLsAndReboot(host string) {
	if err := a.resetBoseURLsViaTelnet(host); err != nil {
		a.logger.Warn("telnet-enable: failed to restore stock boseurls on the recovery path; box may keep dialing the unlock URL until a manual factory reset", "host", host, "err", err)
		return
	}
	if t, err := dialTAP(host, 5*time.Second); err == nil {
		_, _ = t.send("sys reboot")
		t.close()
		boxSSHClients.invalidateHost(host)
		a.logger.Info("telnet-enable: restored stock boseurls and rebooted after a failed unlock", "host", host)
	}
}
