// Regression tests for agent version reporting on /api/agent/version.

package main

import "testing"

// TestVersionDefaultsAreObviouslyUnstamped guards against the
// "agent reports 1.0.0 forever" regression: a local build that
// forgets to pass -X main.version=... still produces a runnable
// agent, but the desktop-app's update check then compares its own
// stamped version against the agent's default "1.0.0" and shows a
// permanent "update available" banner. The default values are kept
// obviously placeholder-ish so a quick `strings` on the released
// binary tells you immediately whether the stamping flags were
// applied.
//
// This test does not assert that the production build stamped the
// vars (we cannot tell from inside the binary). It only asserts
// that the unstamped defaults stayed unchanged so any future
// "1.0.0" sighting in production logs is unambiguously a missing
// stamp, not a refactor that aliased the var name.
//
// The matching ldflag-pair contract is:
//
//   -X main.version=<semver>      sets `version`
//   -X main.buildStamp=<yyyymmdd> sets `buildStamp`
//
// See .github/workflows/release.yml line 167 (build-agent step)
// and the Makefile's LDFLAGS for the canonical invocations.
// Local raw `go build` without these flags reproduces the
// regression that confused Pierre/Jens during the v0.5.4 dev cycle.
func TestVersionDefaultsAreObviouslyUnstamped(t *testing.T) {
	if version != "1.0.0" {
		t.Errorf("version default changed to %q; release flag contract requires %q "+
			"so an unstamped build is immediately distinguishable from a stamped one", version, "1.0.0")
	}
	if buildStamp != "dev" {
		t.Errorf("buildStamp default changed to %q; release flag contract requires %q", buildStamp, "dev")
	}
}
