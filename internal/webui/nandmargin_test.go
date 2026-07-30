package webui

import "testing"

// The margin decides whether the Spotify engine is reclaimed BEFORE the agent
// binary is written. It refuses nothing, so being mean with it only means a
// speaker attempts a second full copy of the binary on a volume with almost
// nothing left, which is where updates were stalling: the body arrives, the
// write never returns, and the speaker reboots onto the old binary.
//
// Live evidence (2026-07-30): two ST10s with 13,680 KB and 13,652 KB free took
// the same 12.78 MB update; the first replied in 15 s, the second was never
// heard from again. Both must now be told they are tight.
func TestBorderlineSpeakerIsTreatedAsTight(t *testing.T) {
	const agentSize = 12_779_682

	cases := []struct {
		name      string
		freeBytes int64
		wantRoom  bool
	}{
		{"the speaker that stalled", 13_652 * 1024, false},
		{"its twin that got away with it", 13_680 * 1024, false},
		{"clearly tight, always reclaimed", 7_600 * 1024, false},
		{"genuinely roomy", 22_900 * 1024, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.freeBytes >= agentSize+nandWriteMargin
			if got != c.wantRoom {
				t.Fatalf("free=%d bytes: room=%v, want %v (need=%d + margin=%d)",
					c.freeBytes, got, c.wantRoom, agentSize, nandWriteMargin)
			}
		})
	}
}

// Leaving under a megabyte free after writing a second copy of the binary is
// the state that stalled, so the margin must never allow it again.
func TestMarginLeavesRoomAfterTheSecondCopy(t *testing.T) {
	const agentSize = 12_779_682
	free := int64(13_652 * 1024)
	if free >= agentSize+nandWriteMargin {
		t.Fatalf("a write leaving %d bytes free must not count as having room", free-agentSize)
	}
	if nandWriteMargin < 2*1024*1024 {
		t.Fatalf("margin %d is too small to keep a journaling filesystem out of trouble", nandWriteMargin)
	}
}
