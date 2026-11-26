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
	"sync"
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
	// bootstrap: when the plain unlock does not open SSH, ALWAYS try the
	// factory-reset bootstrap (enableSSHViaTelnetBootstrap) before giving up.
	// Only the uninstall sets it.
	bootstrap bool
	// bootstrapIfNoAccount: like bootstrap, but conditional. The install sets
	// it so that a factory-fresh box the account seed did not reach (an sm2 Wave
	// with no Bose account, 2026-11-15) still falls through to the bootstrap -
	// but ONLY when /info positively reports an empty margeAccountUUID. A used
	// box that carries an account keeps the plain-unlock-only behaviour: it
	// already runs the marge check on its own, and the bootstrap would POST a
	// dummy account over its real one (project_bootstrap_used_box_deadend). The
	// OTA sets neither and never falls through.
	bootstrapIfNoAccount bool
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

// errBootstrapResponderUnreachable: the factory-reset bootstrap ran, the
// speaker came back on the network, but it never called this PC's local marge
// responder (:19080) - the Windows Firewall is the usual cause. A more specific
// flavour of errUnlockDidNotOpenSSH (the speaker was still restored and
// restarted) so the install can name the firewall in its advice. errors.Is
// against errUnlockDidNotOpenSSH stays false; callers that only care that the
// unlock failed check `!opened` instead.
var errBootstrapResponderUnreachable = errors.New("the factory-reset bootstrap ran but the speaker never reached this PC's marge responder; stock URLs restored and the speaker restarted (likely a firewall block)")

// errBootstrapAccountRefused: the factory-reset bootstrap ran and the speaker
// turned the stand-in account down itself (SET_MARGE_ACCOUNT_ERROR 1038 in a
// tenth of a second, no call to this PC), before and after a restart onto the
// local responder. Seen on a Wave that was reset and never set up again
// (2026-11-18 to 11-23, four releases of fixes did not move it). Another flavour
// of errUnlockDidNotOpenSSH, so the install can send the owner to the Bose
// SoundTouch app for one setup instead of to the firewall or the stick.
var errBootstrapAccountRefused = errors.New("the factory-reset bootstrap ran but the speaker refused the stand-in account itself (error 1038) and never called this PC; stock URLs restored and the speaker restarted")

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
	start := time.Now()
	if o.phase != "" {
		a.emitPhase(o.phase)
	}
	a.logger.Info(o.tag+": plain SSH closed, opening it stick-free via :17000", "host", host, "model", model)
	// One stick-free unlock path covers every box: enableSSHViaTelnet seeds a
	// throwaway marge account (unlockAccountID) so even a factory-fresh box
	// with an empty margeAccountUUID runs the marge check the injection rides
	// on, then injects, reboots and waits for :22.
	opened, note = a.enableSSHViaTelnet(host, model)
	failErr := errUnlockDidNotOpenSSH
	// Second rung: the factory-reset bootstrap (local marge responder +
	// setMargeAccount). It runs unconditionally for the uninstall (o.bootstrap),
	// and for the install only on a box that positively reports NO marge account
	// (o.bootstrapIfNoAccount - the account seed in the plain unlock did not
	// cover it, and it has no account for the dummy account to overwrite). Each
	// of enableSSHViaTelnet and enableSSHViaTelnetBootstrap reboots the box at
	// most once, and restoreStockBoseURLsAndReboot below reboots at most once
	// more on the failing exit: the reboot count stays bounded and every one is
	// logged.
	if !opened && a.shouldTryBootstrapUnlock(host, o) {
		a.logger.Info(o.tag+": plain unlock did not open SSH and the box carries no account; trying the factory-reset bootstrap", "host", host, "model", model)
		var blog string
		var kind error
		opened, blog, kind = a.enableSSHViaTelnetBootstrap(host, model)
		note += blog
		if !opened && kind != nil {
			failErr = kind
		}
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
	// The unlock did not open SSH. It may have written a dead .invalid (or a
	// live local-IP, from the bootstrap) injection URL into the box config;
	// restore stock URLs and reboot so the box is never left marge-checking a
	// dead/reassignable host. Best-effort.
	a.restoreStockBoseURLsAndReboot(host)
	// Only a restart that actually went out is remembered and logged as one.
	// Both feed advice that is true only after a restart (the preflight's
	// "still starting up", and the failure report's hours without power): an
	// unlock whose :17000 connection broke before any "sys reboot" left the
	// speaker running.
	restarted := tapRebootSentSince(host, start)
	if !restarted {
		a.logger.Warn(o.tag+": the :17000 stick-free unlock did not open SSH, and no restart reached the speaker",
			"host", host, "log", discovery.LastN(note, 6000))
		return false, note, failErr
	}
	if o.rememberReboot {
		markUnlockReboot(host)
	}
	// The whole transcript, not its last 200 characters: the account-id seed,
	// the getpdo reads around it, and the margeAccountUUID line after the reboot
	// sit at the start, and a bundle from a factory-fresh Wave that failed five
	// unlocks in a row could not say whether the seed took (2026-11-15). The
	// bundle export anonymises it.
	a.logger.Warn(o.tag+": the :17000 stick-free unlock did not open SSH; stock URLs restored and the speaker restarted",
		"host", host, "firewallSuspected", errors.Is(failErr, errBootstrapResponderUnreachable), "accountRefused", errors.Is(failErr, errBootstrapAccountRefused), "log", discovery.LastN(note, 6000))
	return false, note, failErr
}

// tapReboots remembers, per host, when a "sys reboot" last went out over the
// :17000 setup port. The unlock's restarts are sent there and not over SSH, so
// boxReboot's ledger (rebootLedger) never sees them.
var tapReboots sync.Map // host -> time.Time

// noteTAPReboot records that "sys reboot" just went to host over :17000.
func noteTAPReboot(host string) { tapReboots.Store(host, time.Now()) }

// tapRebootSentSince reports whether a "sys reboot" went to host over :17000
// at or after t.
func tapRebootSentSince(host string, t time.Time) bool {
	v, ok := tapReboots.Load(host)
	if !ok {
		return false
	}
	at, ok := v.(time.Time)
	return ok && !at.Before(t)
}

// sendTAPReboot sends "sys reboot" over an open :17000 connection and records
// it when the command was written. Every :17000 restart goes through here, so
// the advice that is only true after a restart can rely on tapReboots.
func sendTAPReboot(t *tapConn, host string) {
	if _, err := t.send("sys reboot"); err == nil {
		noteTAPReboot(host)
	}
}

// shouldTryBootstrapUnlock decides whether, after the plain :17000 unlock failed
// to open SSH, the heavier factory-reset bootstrap should run. The uninstall
// (o.bootstrap) always falls through. The install (o.bootstrapIfNoAccount) falls
// through only when the box positively reports an empty margeAccountUUID - so a
// used box that still carries a Bose account is never handed the dummy account
// the bootstrap POSTs over it (project_bootstrap_used_box_deadend), and it does
// not need the bootstrap anyway (it runs the marge check on its own). The OTA
// sets neither option and never falls through.
func (a *App) shouldTryBootstrapUnlock(host string, o sshUnlockOptions) bool {
	if o.bootstrap {
		return true
	}
	if !o.bootstrapIfNoAccount {
		return false
	}
	return a.boxMargeAccountEmpty(host)
}
