package main

import (
	"errors"
	"fmt"
	"strconv"
	"testing"
)

func TestNANDFits(t *testing.T) {
	const mb = int64(1024 * 1024)
	cases := []struct {
		name                    string
		free, reclaimable, need int64
		want                    bool
	}{
		// Fail-open: a box that reports no usable free figure never blocks.
		{"unknown free (ParseInt failure -> 0)", 0, 0, 18 * mb, true},
		{"negative free", -1, 0, 18 * mb, true},
		// The push gate boundary: free + reclaimable must cover the need.
		{"exactly enough", 18 * mb, 0, 18 * mb, true},
		{"one byte short", 18*mb - 1, 0, 18 * mb, false},
		// A present old engine counts as headroom (the reclaim cascade).
		{"short on raw free, engine swap fits", 8 * mb, 10 * mb, 18 * mb, true},
		{"short even with the reclaim", 4 * mb, 10 * mb, 18 * mb, false},
	}
	for _, c := range cases {
		if got := nandFits(c.free, c.reclaimable, c.need); got != c.want {
			t.Errorf("%s: nandFits(%d, %d, %d) = %v, want %v", c.name, c.free, c.reclaimable, c.need, got, c.want)
		}
	}
}

func TestReclaimableEngineBytes(t *testing.T) {
	const embedded = int64(16 * 1024 * 1024)
	cases := []struct {
		name string
		ver  map[string]string
		want int64
	}{
		{"no engine on the box", map[string]string{}, 0},
		{"engine absent explicitly", map[string]string{"goLibrespot": "absent"}, 0},
		{"engine present with size", map[string]string{"goLibrespot": "present", "goLibrespotSizeBytes": "10485760"}, 10485760},
		// Agent predates the size report: assume the embedded engine's size.
		{"engine present, no size field", map[string]string{"goLibrespot": "present"}, embedded},
		{"engine present, garbled size", map[string]string{"goLibrespot": "present", "goLibrespotSizeBytes": "lots"}, embedded},
		{"engine present, zero size", map[string]string{"goLibrespot": "present", "goLibrespotSizeBytes": "0"}, embedded},
	}
	for _, c := range cases {
		if got := reclaimableEngineBytes(c.ver, embedded); got != c.want {
			t.Errorf("%s: reclaimableEngineBytes = %d, want %d", c.name, got, c.want)
		}
	}
}

// The two sidecar gates parse nandFreeBytes with ParseInt and feed the result
// straight into nandFits. Replicate that exact sequence for the field shapes
// agents actually send so the fail-open contract (missing field, old agent)
// is pinned at the integration seam, not just in the helper.
func TestNANDGateFailsOpenOnUnparsableFree(t *testing.T) {
	for _, raw := range []string{"", "unknown", "-3", "12.5"} {
		freeN, _ := strconv.ParseInt(raw, 10, 64)
		if !nandFits(freeN, 0, 1<<30) {
			t.Errorf("nandFreeBytes=%q must fail open, got a blocked push", raw)
		}
	}
}

// The insufficient-NAND sentinel must survive the fmt.Errorf %w wrapping used
// by pushSidecarIfNeeded, because stageSidecarBeforeReboot's retry loop keys
// on it to stop retrying a shortfall that cannot resolve within the window.
func TestInsufficientNANDSentinelWraps(t *testing.T) {
	err := fmt.Errorf("sidecar: %w (free=1 reclaimable=2 need=3)", errInsufficientNAND)
	if !errors.Is(err, errInsufficientNAND) {
		t.Errorf("wrapped gate error must match errInsufficientNAND")
	}
	if errors.Is(errors.New("sidecar upload failed: EOF"), errInsufficientNAND) {
		t.Errorf("a transport failure must not match the space sentinel")
	}
}
