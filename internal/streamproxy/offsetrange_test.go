// #844: a library FLAC that never played once, restarted six times in two
// minutes.
//
// The box asked the proxy for `bytes=253170-` while reading the file's header.
// The proxy forwarded no Range at all, so the upstream answered from byte 0, the
// box read a few kilobytes of the wrong part of the file, gave up with
// AUDIO_ERROR_DECODER and retried five seconds later.
//
// Forwarding every Range would be wrong: "bytes=0-" is what a plain player sends
// for any stream, radio included, and a live stream handed a Range can answer in
// ways nobody here wants. A non-zero start is the thing no radio listener ever
// asks for, so that is the only shape that travels.

package streamproxy

import "testing"

func TestOnlyAnOffsetRangeTravelsUpstream(t *testing.T) {
	forwarded := map[string]string{
		// The exact header from the #844 bundle.
		"bytes=253170-":   "bytes=253170-",
		"bytes=1024-2047": "bytes=1024-2047",
		"BYTES=500-":      "BYTES=500-", // case is the caller's business
		"  bytes=900-  ":  "bytes=900-", // trimmed, value preserved
	}
	for in, want := range forwarded {
		if got := offsetRange(in); got != want {
			t.Fatalf("offsetRange(%q) = %q, want %q", in, got, want)
		}
	}

	// Everything a radio player sends, and everything malformed, stays behind.
	for _, in := range []string{
		"",           // no header at all, the overwhelming majority
		"bytes=0-",   // every plain player, for every stream
		"bytes=0",    //
		"bytes=-500", // a suffix range: not an offset into the file
		"bytes=",     // malformed
		"items=5-10", // not a byte range
		"0-100",      // no unit
	} {
		if got := offsetRange(in); got != "" {
			t.Fatalf("offsetRange(%q) = %q, want it held back", in, got)
		}
	}
}
