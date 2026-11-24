package boxws

import (
	"context"
	"time"
)

// A power-on the bus reports as the speaker trying STR's source itself.
//
// The power-on resume binds to two bus signals: a powerStateUpdated frame
// (firmware that sends one) and the INVALID_SOURCE + DO_NOT_RESUME selection
// restore (handleRestoredSelection). Discussion #1771 (an scm SoundTouch 20 on a
// network cable, v1.9.1) brought a third shape that is neither. Switched on by
// the remote after an overnight standby, the speaker went straight
//
//	STANDBY -> UPNP            the firmware resumes its last source itself
//	1036 NOT_LOGGED_IN         UpnpRcvdContentItemInWrongState: STR cleared the
//	                           transport at power-off (#197), so there is nothing
//	UPNP -> INVALID_SOURCE     ... and it gives up
//
// with no DO_NOT_RESUME restore and no powerStateUpdated. STR never heard a
// power-on, so the folder it had kept for exactly this moment (queueresume.go)
// stayed where it was, and the speaker sat on with no sound and the last title
// on its display until a key was pressed. The syslog ring did see the wake
// (scmmond SCMMOND_WAKEUP_NETWORK_STANDBY), but that path only re-syncs keys.
//
// So that sequence counts as the power-on too, under two conditions:
//
//   - UPNP was entered from STANDBY moments ago (selfResumeGiveUpWindow): the
//     give-up belongs to the speaker's own resume, not to a stream STR started.
//   - The speaker had been in STANDBY for a while (selfResumeMinStandby). The
//     firmware's own power-off oscillations (#197 on scm ST20s, #419) flip
//     UPNP <-> STANDBY within milliseconds to a few seconds; a person switching
//     a speaker on comes after a real standby.
//
// Everything else the resume must respect is decided downstream, exactly as for
// the other two signals: the per-speaker opt-out, the crash-loop guard, the
// #197 bounce guard, the zone / stereo-pair self-wake guard (ResumeLastPlay).
// A power-on that also produces one of the other two signals resumes once:
// whichever comes first fires, and the others inside powerWakeDedupWindow are
// dropped.

const (
	// selfResumeGiveUpWindow bounds STANDBY->UPNP to UPNP->INVALID_SOURCE.
	// Measured: 221 ms.
	selfResumeGiveUpWindow = 3 * time.Second
	// selfResumeMinStandby is how long the speaker must have been in standby
	// for its STANDBY->UPNP to read as a power-on.
	selfResumeMinStandby = 30 * time.Second
	// powerWakeDedupWindow keeps one power-on from resuming twice.
	powerWakeDedupWindow = 5 * time.Second
)

// noteSelfResumeTransition follows the source transitions this detection
// needs and reports whether this one completes the give-up shape. It takes
// c.mu itself; the caller must not hold it.
func (c *Client) noteSelfResumeTransition(prev, src string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case src == "STANDBY":
		c.standbyEnteredAt = now
		c.upnpFromStandbyAt = time.Time{}
	case prev == "STANDBY" && src == "UPNP":
		if !c.standbyEnteredAt.IsZero() && now.Sub(c.standbyEnteredAt) >= selfResumeMinStandby {
			c.upnpFromStandbyAt = now
		} else {
			c.upnpFromStandbyAt = time.Time{}
		}
	case prev == "UPNP" && src == "INVALID_SOURCE":
		at := c.upnpFromStandbyAt
		c.upnpFromStandbyAt = time.Time{}
		return !at.IsZero() && now.Sub(at) <= selfResumeGiveUpWindow
	default:
		c.upnpFromStandbyAt = time.Time{}
	}
	return false
}

// firePowerWake delivers OnPowerWake once per power-on, whichever bus signal
// reported it first. why goes into the log line.
func (c *Client) firePowerWake(ctx context.Context, why string) {
	now := time.Now()
	c.mu.Lock()
	dup := !c.lastPowerWakeAt.IsZero() && now.Sub(c.lastPowerWakeAt) < powerWakeDedupWindow
	if !dup {
		c.lastPowerWakeAt = now
	}
	c.mu.Unlock()
	if dup {
		c.logger.Debug("box ws: power-on already delivered moments ago, not resuming twice", "signal", why)
		return
	}
	if c.handler != nil {
		c.handler.OnPowerWake(ctx)
	}
}
