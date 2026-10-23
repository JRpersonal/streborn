// The stick-free SSH unlock rung shared by the install, the SSH-OTA and the
// uninstall. Each of the three reaches it from a different ladder (the install
// from a :22 that is TCP-closed in the preflight, the OTA from a failed
// handshake, the uninstall from a failed handshake plus a failed
// agent-enable), but the rung itself is one sequence: :17000 answers, the
// unlock runs, and the box is left either with SSH open and its stock cloud
// URLs restored, or with its stock URLs restored and restarted. Before this
// file the sequence was written out three times and the three copies had
// started to differ in details nobody had decided on.

package main

import (
	"errors"
	"time"

	"github.com/JRpersonal/streborn/discovery"
)

// sshUnlockOptions is the per-caller shape of the rung. The differences are
// deliberate and each has an owner, so they are options rather than one
// unified behaviour.
type sshUnlockOptions struct {
	// tag prefixes the log lines so str.log still says which flow was running
	// ("install_str", "SSH-OTA", "uninstall_str").
	tag string
	// phase is the install progress phase to emit once :17000 has answered and
	// the unlock is about to run ("access" for the install). Empty emits
	// nothing; the OTA and the uninstall have no install checklist to advance.
	phase string
	// bootstrap: when the plain unlock does not open SSH, try the
	// factory-reset bootstrap (enableSSHViaTelnetBootstrap) before giving up.
	// Only the uninstall sets it. The install dropped that rung on purpose
	// (see the account-seed note in installSTROnBox: seeding the marge account
	// made the plain unlock cover factory-fresh boxes too), and the OTA never
	// had it.
	bootstrap bool
	// rememberReboot records the restart a FAILED unlock costs the speaker in
	// unlockReboots, so a second press of Install within unlockRebootGrace
	// reports "still restarting" instead of unlocking again. Only the install
	// reads that memory, so only the install sets it; the OTA and the
	// uninstall never recorded their restarts there and still do not.
	rememberReboot bool
}

// errSetupPortClosed: :17000 did not accept a connection, so no unlock was
// attempted and the speaker was not touched.
var errSetupPortClosed = errors.New(":17000 setup port closed, no stick-free unlock attempted")

// errUnlockDidNotOpenSSH: the unlock ran and :22 did not open within its
// budget. The speaker's stock cloud URLs have been restored and it has been
// restarted (best-effort), so it is booting when this is returned.
var errUnlockDidNotOpenSSH = errors.New("the :17000 stick-free unlock did not open SSH; stock URLs restored and the speaker restarted")

// ensureSSHOpen opens root SSH on a speaker without a USB stick through its
// :17000 setup shell, the same unlock every caller used to spell out itself.
//
// Returns opened (SSH came up), note (the unlock's redaction-free command log,
// for diagnostics; the install stores it in the result's Log) and err. The
// error has two meanings, which the callers rely on:
//
//   - opened == true: err is the stock-cloud-URL restore's failure, or nil.
//     The restore is best-effort (resetBoseURLsViaTelnet) and the install
//     tells the user about it when the install itself succeeds, because a box
//     left on the injected .invalid host misbehaves on radio and presets.
//   - opened == false: errSetupPortClosed when :17000 never answered (nothing
//     was done to the speaker), errUnlockDidNotOpenSSH when the unlock ran and
//     failed (the speaker has been restored and restarted).
//
// Why the unlock is followed by a URL restore on BOTH outcomes: the injection
// writes a dead .invalid URL into the box's cloud config. Once SSH is open,
// restoring it while :17000 is still the plain Bose TAP keeps the box from
// marge-checking a dead host (and STR's install may REDIRECT :17008
// afterwards). When SSH did not open, the restore plus a reboot keeps the box
// from being left dialing that host forever, which would also defeat STR's
// later streaming.bose.com interception on a subsequent stick install.
func (a *App) ensureSSHOpen(host, model string, o sshUnlockOptions) (opened bool, note string, err error) {
	if !tcpReachable(host, 17000, 2*time.Second) {
		return false, "", errSetupPortClosed
	}
	if o.phase != "" {
		a.emitPhase(o.phase)
	}
	a.logger.Info(o.tag+": plain SSH closed, opening it stick-free via :17000", "host", host, "model", model)
	// One stick-free unlock path covers every box: enableSSHViaTelnet seeds a
	// throwaway marge account (unlockAccountID) so even a factory-fresh box
	// with an empty margeAccountUUID runs the marge check the injection rides
	// on, then injects, reboots and waits for :22.
	opened, note = a.enableSSHViaTelnet(host, model)
	if !opened && o.bootstrap {
		var blog string
		opened, blog = a.enableSSHViaTelnetBootstrap(host, model)
		note += blog
	}
	if opened {
		// Restore stock cloud URLs while :17000 is still the plain Bose TAP.
		// Best-effort; the caller decides what to tell the user.
		if rerr := a.resetBoseURLsViaTelnet(host); rerr != nil {
			a.logger.Warn(o.tag+": could not restore stock boseurls after the :17000 unlock", "host", host, "err", rerr)
			return true, note, rerr
		}
		return true, note, nil
	}
	// The unlock did not open SSH. It may have written a dead .invalid
	// injection URL into the box config; restore stock URLs and reboot so the
	// box is never left marge-checking a dead host. Best-effort.
	a.restoreStockBoseURLsAndReboot(host)
	if o.rememberReboot {
		markUnlockReboot(host)
	}
	a.logger.Warn(o.tag+": the :17000 stick-free unlock did not open SSH; stock URLs restored and the speaker restarted",
		"host", host, "log", discovery.LastN(note, 200))
	return false, note, errUnlockDidNotOpenSSH
}
