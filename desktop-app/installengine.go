package main

import (
	"fmt"
	"strings"
	"time"
)

// A network install never puts the Spotify engine on the speaker by itself.
// install.sh copies rc.local, run.sh, the agent and the presets to NAND; the
// staged engine sits in the staging directory, which RepairInstallViaSSH
// removes before the reboot, and run.sh's engine sync reads only a real USB
// stick. So after a network install the engine arrives only if something
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

// installEnsureEngine and installEngineSleep are the seams for the tests.
var (
	installEnsureEngine = func(a *App, host string) (string, error) { return a.EnsureSpotifyEngine(host, 0) }
	installEngineSleep  = time.Sleep
)

// engineWillNeverFit reports a push refused for space: retrying cannot help.
func engineWillNeverFit(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "507") || strings.Contains(m, "insufficient nand") || strings.Contains(m, "no space")
}

// ensureEngineAfterInstall is the install's last step: the speaker ends with
// the embedded Spotify engine on it, or the reason it could not is returned.
// An empty string means the engine is there (or this build has none to give).
func (a *App) ensureEngineAfterInstall(host string) string {
	var last error
	for attempt := 1; attempt <= installEngineAttempts; attempt++ {
		r, err := installEnsureEngine(a, host)
		if err == nil {
			a.recordOTA(host, "install: Spotify engine checked after the install: "+r)
			a.logger.Info("install_str: Spotify engine verified after the install", "host", host, "result", r)
			return ""
		}
		last = err
		a.recordOTA(host, fmt.Sprintf("install: Spotify engine attempt %d/%d failed: %v", attempt, installEngineAttempts, err))
		a.logger.Warn("install_str: Spotify engine delivery after the install failed", "host", host, "attempt", attempt, "err", err)
		if engineWillNeverFit(err) || attempt == installEngineAttempts {
			break
		}
		installEngineSleep(installEngineBackoff(attempt))
	}
	return last.Error()
}
