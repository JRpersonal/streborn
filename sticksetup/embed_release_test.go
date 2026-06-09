// Release-asserting variant of the winformat embed test. Runs only
// when the `release_assert` build tag is set so PR CI does not
// fail on the empty stub. Release CI MUST pass `-tags=release_assert`
// to its `go test` invocation after the winformat-build step has
// populated the embed slot, otherwise a future broken release that
// ships an empty stub will not be caught here.

//go:build release_assert
// +build release_assert

package sticksetup

import "testing"

func TestWinformatEmbedIsNonEmptyInReleaseBuild(t *testing.T) {
	if len(winformatBinary) == 0 {
		t.Fatal("winformat embed is EMPTY in a release_assert test run. " +
			"The 'Build winformat helper' step in .github/workflows/release.yml " +
			"must run BEFORE 'Build Windows desktop app'. Without that, the " +
			"released STR-Windows.exe ships with the empty stub and in-app " +
			"stick formatting fails with the German error message a user hit " +
			"in #44.")
	}
}
