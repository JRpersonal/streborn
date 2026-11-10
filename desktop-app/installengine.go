package main

import (
	"errors"
	"fmt"
	"time"
)

// A network install never puts the Spotify engine on the speaker by itself.
// install.sh copies rc.local, run.sh, the agent and the presets to NAND; the
// engine is not even staged any more (repairFileSet leaves it out, because the
// ~16 MB it cost in the speaker's RAM was dead weight there), and run.sh's
// engine sync reads only a real USB stick. So after a network install the engine arrives only if something
// pushes it, and until now that was the setup wizard's verify loop alone: it
// takes the first "present" it sees, swallows push errors, and is skipped by
// every path that does not run it (the repair button, a window closed early).
// Fleet run 2026-10-04: a fresh network install came up without the engine
// twice, and EnsureSpotifyEngine run by hand brought it back.
//
// The install now ends the way the update flow does: verify the engine and
// re-push it when it is missing or not the embedded build.

// installEngineAttempts bounds the final engine step. The agent has just come
// up, so the first push can meet a speaker that is still settling; with
// installEngineBackoff the waits add up to 5+10+20 = 35 s.
const installEngineAttempts = 4

func installEngineBackoff(attempt int) time.Duration {
	return 5 * time.Second << (attempt - 1)
}

// engineWillNeverFit reports a push refused for space: retrying cannot help.
// The verdict travels as a nandFullError from the gate, the agent's 507 or
// the SSH df refusal, so this no longer reads the text.
func engineWillNeverFit(err error) bool {
	return errors.Is(err, errInsufficientNAND)
}

// ensureEngineAfterInstall is the install's last step: the speaker ends with
// the embedded Spotify engine on it, or the reason it could not is returned.
// An empty string means the engine is there, or there is none to give (a
// build with the engine stub, a speaker STR is being removed from again).
func (a *App) ensureEngineAfterInstall(host string) string {
	seams := a.installSeams()
	var last error
	for attempt := 1; attempt <= installEngineAttempts; attempt++ {
		r, err := seams.ensureEngine(host)
		if err == nil {
			// No error is not "the engine is there": the step also answers
			// without one when it sent nothing, and when what it sent is not
			// running. Only an engine that is in place counts as verified.
			switch {
			case r.inPlace():
				a.recordOTA(host, "install: Spotify engine checked after the install: "+r.String())
				a.logger.Info("install_str: Spotify engine verified after the install", "host", host, "result", r.String())
				return ""
			case r == engineNotEmbedded, r == engineSkipped:
				// Nothing to give (a build with the engine stub), or nothing
				// to be given on purpose (STR is being removed again). Not a
				// failure the user can act on, and not a verified engine.
				a.recordOTA(host, "install: Spotify engine not delivered after the install: "+r.String())
				a.logger.Info("install_str: no Spotify engine delivered after the install", "host", host, "result", r.String())
				return ""
			case r == engineStaged:
				// On the speaker's disk, not running: the restart that starts
				// it could not be triggered. Asking again would only find the
				// file already there and answer "current".
				a.recordOTA(host, "install: Spotify engine is on the speaker but not running, the activating restart could not be triggered: "+r.String())
				a.logger.Warn("install_str: Spotify engine staged after the install, the activating restart failed", "host", host)
				return "it is on the speaker and starts with the speaker's next restart"
			}
			// Refused without anything sent (the speaker's update latch): the
			// same as a failed attempt, asked again after the backoff.
			err = errors.New(r.String())
			if r == engineNone {
				err = errors.New("the engine step gave no result")
			}
		}
		last = err
		a.recordOTA(host, fmt.Sprintf("install: Spotify engine attempt %d/%d failed: %v", attempt, installEngineAttempts, err))
		a.logger.Warn("install_str: Spotify engine delivery after the install failed", "host", host, "attempt", attempt, "err", err)
		if engineWillNeverFit(err) || attempt == installEngineAttempts {
			break
		}
		seams.sleep(installEngineBackoff(attempt))
	}
	return last.Error()
}
