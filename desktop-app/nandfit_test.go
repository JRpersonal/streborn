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

func TestNANDNeedCompressed(t *testing.T) {
	const mb = int64(1024 * 1024)
	// The credit: raw size scaled by the UBIFS-LZO fraction plus the fixed margin.
	raw := 16 * mb
	if got, want := nandNeedCompressed(raw), int64(float64(raw)*ubifsCompressedFraction)+nandNeedMargin; got != want {
		t.Errorf("nandNeedCompressed(16MB) = %d, want %d", got, want)
	}
	// Zero raw bytes still reserve the fixed margin.
	if got := nandNeedCompressed(0); got != nandNeedMargin {
		t.Errorf("nandNeedCompressed(0) = %d, want the bare margin %d", got, nandNeedMargin)
	}
	// For any realistic binary the credited need must stay BELOW the raw size:
	// gating on more than the raw size would re-introduce the over-refusal.
	if got := nandNeedCompressed(10 * mb); got >= 10*mb {
		t.Errorf("nandNeedCompressed(10MB) = %d, must be below the raw size", got)
	}
}

// The regression the compression credit fixes: a ~16 MB engine aimed at a box
// whose pessimistic UBIFS free figure reads 13 MB. The old gate (raw size +
// 2 MB margin = 18 MB) refused it, although the write lands at ~10-11 MB on
// UBIFS-LZO and agent + engine already fit a 26.7 MB ST20 NAND. The gate is
// only a cheap pre-filter now (the agent-side write is authoritative), so it
// passes tight-but-possible pushes and still refuses clearly hopeless ones.
func TestNANDGateCompressionCredit(t *testing.T) {
	const mb = int64(1024 * 1024)
	if !nandFits(13*mb, 0, nandNeedCompressed(16*mb)) {
		t.Errorf("a 16MB engine onto 13MB pessimistic free must pass with the compression credit")
	}
	// Clearly hopeless: 5 MB free cannot take a 16 MB engine even compressed.
	if nandFits(5*mb, 0, nandNeedCompressed(16*mb)) {
		t.Errorf("a 16MB engine onto 5MB free is hopeless and must stay refused")
	}
	// The stage gate sums agent + engine before crediting: 12 MB + 16 MB raw
	// onto 21 MB free passes (credited ~20.6 MB), while the raw sum (30 MB)
	// would have deferred the staging.
	if !nandFits(21*mb, 0, nandNeedCompressed(12*mb+16*mb)) {
		t.Errorf("staging agent+engine onto 21MB pessimistic free must pass with the compression credit")
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
