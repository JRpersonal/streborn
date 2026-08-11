// Teardown classifier: telling the box's own UPnP-source teardown apart from
// a deliberate user stop, and the own-transport-command stamps that feed it.

package boxws

import (
	"time"
)

// presetTeardownWindow / invalidSourceTeardownWindow bound how soon after a
// hardware preset press or an INVALID_SOURCE flap a STOP_STATE still counts as
// the box's own teardown rather than a deliberate user stop. Kept short: the
// teardown STOP_STATE arrives within a fraction of a second of the flap/press on
// the observed scm/mojo firmware, while a real stop the user makes seconds later
// is well outside these windows and still honoured.
const (
	presetTeardownWindow        = 4 * time.Second
	invalidSourceTeardownWindow = 2 * time.Second
	// ownCmdTeardownWindow bounds how soon after one of STR's OWN transport
	// commands (SOAP Stop / SetURI / Play) a STOP_STATE still counts as the
	// box's reaction to that command rather than a user stop. The echo arrives
	// within a fraction of a second of the SOAP round-trip; kept short so a
	// real stop the user makes a moment later is still honoured.
	ownCmdTeardownWindow = 3 * time.Second
	// upnpFlapWindow bounds how long after UPNP stopped being the active
	// source a STANDBY entry still counts as an STR-driven power-off (the
	// UPNP -> INVALID_SOURCE -> STANDBY give-up completes within ~1-2s).
	upnpFlapWindow = 5 * time.Second
	// nativeDropWindow bounds how long after the box left a native radio
	// station a jump to INVALID_SOURCE still counts as the box abandoning that
	// station rather than an unrelated later event. The observed failure took
	// about a second from leaving the source to INVALID_SOURCE; the window is
	// generous because the alternative, missing the evidence, leaves a speaker
	// stuck on a form it cannot keep.
	nativeDropWindow = 10 * time.Second
	// nativeStandbyDropWindow bounds the OTHER way a speaker abandons a native
	// station: straight to STANDBY without ever touching INVALID_SOURCE.
	//
	// Deliberately much shorter than the window above, because STANDBY is
	// ambiguous and INVALID_SOURCE is not: a user switching the speaker off
	// produces the identical transition. Only the time tells them apart, and
	// nobody powers a speaker off within a breath of starting a station. The
	// reported failure lasted 862 ms; four seconds leaves room for a slow box
	// while staying far below any human reaction to music starting.
	nativeStandbyDropWindow = 4 * time.Second
	// ownCmdKeyVeto: a physical key press this close to the STOP_STATE means
	// the stop came from the user (remote/box stop key), NOT from STR's own
	// command, even inside ownCmdTeardownWindow. The firmware sends a
	// userActivityUpdate alongside real key presses; STR's SOAP commands never
	// produce one.
	ownCmdKeyVeto = 1500 * time.Millisecond
	// ownCmdKeylessTeardownWindow replaces ownCmdTeardownWindow on firmware
	// that has never emitted a userActivityUpdate: the key veto has no signal
	// there, so the full 3s window would excuse EVERY stop within 3s of any of
	// STR's own SOAP commands - and during a struggling recall STR pushes on a
	// ~5s cadence, so a deliberate remote stop was near-guaranteed to be
	// swallowed and the re-push overrode it. The box's echo to our own command
	// arrives within a fraction of a second of the SOAP round-trip, so a tight
	// window still excuses the genuine echo while a user stop a second later
	// latches.
	ownCmdKeylessTeardownWindow = 750 * time.Millisecond
	// standbyFlapTeardownWindow bounds how soon after a UPNP<->STANDBY flap a
	// STOP_STATE still counts as the spontaneous-off oscillation's teardown
	// (#419) rather than a deliberate stop. The observed bounce completes in
	// ~100-150 ms; 3 s covers a slow flap without swallowing a real stop the user
	// makes seconds after a genuine power event.
	standbyFlapTeardownWindow = 3 * time.Second
)

// stopStateIsTeardown reports whether a STOP_STATE nowPlaying frame is the box's
// own UPnP-source teardown (a preset switch or an involuntary stream drop, both
// of which flap the source through INVALID_SOURCE) rather than a deliberate user
// stop. Only a genuine stop must fire OnUserStop; a teardown must not, or the
// latched user-stop suppresses the drop recovery and the recall retry and the
// preset buttons look dead (#ST30 2026-07-11). Returns the reason for the log.
func (c *Client) stopStateIsTeardown(np *wsNowPlaying) (bool, string) {
	// The frame itself admits the box could not hold STR's source: either the
	// nowPlaying source attribute or its nested ContentItem reads INVALID_SOURCE
	// (the failed self-activation) or STANDBY (a power-off teardown, already
	// covered elsewhere but never a "user stopped the stream").
	if np != nil {
		for _, src := range []string{np.Source, np.ContentItem.Source} {
			if src == "INVALID_SOURCE" || src == "STANDBY" {
				return true, "nowPlaying source=" + src
			}
		}
	}
	c.mu.Lock()
	sincePress := time.Since(c.lastPresetPressAt)
	sinceInvalid := time.Since(c.lastInvalidSourceAt)
	sinceStandbyFlap := time.Since(c.lastStandbyFlapAt)
	sinceOwnCmd := time.Since(c.lastOwnCmdAt)
	pressSet := !c.lastPresetPressAt.IsZero()
	invalidSet := !c.lastInvalidSourceAt.IsZero()
	standbyFlapSet := !c.lastStandbyFlapAt.IsZero()
	ownCmdSet := !c.lastOwnCmdAt.IsZero()
	c.mu.Unlock()
	if pressSet && sincePress < presetTeardownWindow {
		return true, "preset selected " + sincePress.Round(time.Millisecond).String() + " ago"
	}
	if invalidSet && sinceInvalid < invalidSourceTeardownWindow {
		return true, "source flapped to INVALID_SOURCE " + sinceInvalid.Round(time.Millisecond).String() + " ago"
	}
	// The spontaneous-off oscillation (#419) flips UPNP->STANDBY->UPNP and emits a
	// STOP_STATE on the way back up whose source reads UPNP, so the two checks
	// above miss it. A STOP_STATE within a moment of a STANDBY flap is that
	// bounce, not a deliberate stop: reading it as a user stop re-latched
	// lastUserStop and defeated the #419 spontaneous-off exemption on the next leg
	// of the same oscillation, so #197 cleared the transport and the box went
	// silent until a power pull (bundle 17, three sm2 boxes on v0.9.15). A real
	// power press is unaffected: on this firmware it emits a userActivityUpdate,
	// so HandleEnterStandby still classifies and latches it via its own path.
	if standbyFlapSet && sinceStandbyFlap < standbyFlapTeardownWindow {
		return true, "source flapped through STANDBY " + sinceStandbyFlap.Round(time.Millisecond).String() + " ago"
	}
	// STR itself just drove the transport (the wrong-state repair's Stop+ClearURI,
	// a verify re-push's SetURI+Play, a re-push/resume from the webui): the box
	// answers those with a STOP_STATE that is not a user stop. The veto: a
	// physical key press right next to the frame means the user pressed stop on
	// the remote/box even inside this window - the firmware accompanies real key
	// presses with a userActivityUpdate, which STR's SOAP commands never cause.
	// Firmware that has NEVER emitted a userActivityUpdate gives the veto no
	// signal, so only the much tighter keyless window applies there (see the
	// constant): a real remote stop on such a box must still latch.
	if ownCmdSet {
		c.thumbMu.Lock()
		sinceKey := time.Since(c.lastUserActivityAt)
		keySet := !c.lastUserActivityAt.IsZero()
		c.thumbMu.Unlock()
		window := ownCmdTeardownWindow
		if !keySet {
			window = ownCmdKeylessTeardownWindow
		}
		if sinceOwnCmd < window && (!keySet || sinceKey > ownCmdKeyVeto) {
			return true, "STR's own transport command " + sinceOwnCmd.Round(time.Millisecond).String() + " ago"
		}
	}
	return false, ""
}

// NoteOwnTransportCommand stamps that STR itself just issued a transport-
// mutating SOAP command. Wired to upnp.Renderer.OnTransportCommand (for every
// renderer that drives THIS box) so stopStateIsTeardown can excuse the box's
// STOP_STATE reaction to STR's own commands. Safe for concurrent use.
func (c *Client) NoteOwnTransportCommand() {
	c.mu.Lock()
	c.lastOwnCmdAt = time.Now()
	c.mu.Unlock()
}

// LastOwnTransportCommand returns when STR last issued a transport-mutating
// SOAP command (zero time if never). The webui's standby classifier reads it
// to recognise a source flip that merely answers STR's OWN push (the firmware
// rejecting a wake-resume/recall) instead of reading it as a user power-off.
func (c *Client) LastOwnTransportCommand() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastOwnCmdAt
}
