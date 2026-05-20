// Regression test for the winformat embed slot: ensure the
// released binary actually ships a PE32 helper, not the empty stub.

package sticksetup

import "testing"

// winformatStubMaxBytes is the size below which we treat the
// embedded blob as "empty stub or truncated build". A real
// winformat.exe is roughly 1.7 MB; the checked-in stub is 0 bytes.
// Pick a generous middle ground so a future binary that shrinks
// significantly (UPX, debug-stripped variant) does not false-fail.
const winformatStubMaxBytes = 100 * 1024

// TestWinformatEmbedSizeLooksReal guards against #44's "Format
// failed: winformat Helper fehlt im Build" regression. On PR CI
// the checked-in stub is empty and the test skips; on release CI
// the winformat-build step writes a real PE32 binary into the
// embed slot before this test runs (release.yml's "Build winformat
// helper" step) so a truncated or missing build is caught loudly.
func TestWinformatEmbedSizeLooksReal(t *testing.T) {
	sz := len(winformatBinary)
	if sz == 0 {
		t.Skip("winformat embed is empty (likely PR CI without the winformat-build step). " +
			"The release workflow runs 'Build winformat helper' before this test; on a fresh " +
			"clean checkout, run `make winformat-embed` first.")
	}
	if sz < winformatStubMaxBytes {
		t.Fatalf("winformat embed is %d bytes, expected a real PE32 (~1.7 MB). "+
			"A half-populated stub indicates the build step truncated the file. "+
			"This is the exact regression that caused #44.", sz)
	}
}
