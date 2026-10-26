package main

import (
	"bytes"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/boxlog"
	"github.com/JRpersonal/streborn/internal/presets"
)

const (
	reasonSourceID   = "UpdatePresetFailureCB Update or Add preset failed with Request Failed, Marge returned: AddPreset - failed due to invalid SourceID"
	reasonWrongState = "UpdatePresetFailureCB Update or Add preset failed with MargeHSM is in the wrong state"
)

func resetRefusedRewriteBackoff(t *testing.T) {
	t.Helper()
	refusedRewriteStreak.Store(0)
	refusedRewriteAskAt.Store(0)
	t.Cleanup(func() {
		refusedRewriteStreak.Store(0)
		refusedRewriteAskAt.Store(0)
	})
}

// TestFirmwarePresetRefusalsSince: only refusals stamped at or after the sweep
// start belong to the sweep; an older one (or none, or no syslog reader at
// all) does not.
func TestFirmwarePresetRefusalsSince(t *testing.T) {
	t.Cleanup(func() { setPresetRefusalSource(nil) })
	start := time.Date(2026, 10, 22, 19, 4, 56, 0, time.UTC)

	setPresetRefusalSource(nil)
	if got := firmwarePresetRefusalsSince(start); len(got) != 0 {
		t.Fatalf("no syslog reader wired: must report no refusal, got %v", got)
	}

	all := []boxlog.PresetRefusal{
		{At: start.Add(-time.Second), Reason: reasonWrongState},
		{At: start.Add(900 * time.Millisecond), Reason: reasonWrongState}, // the bundle: 19:04:56.494 -> 19:04:57.327
	}
	setPresetRefusalSource(func(since time.Time) []boxlog.PresetRefusal {
		var out []boxlog.PresetRefusal
		for _, r := range all {
			if !r.At.Before(since) {
				out = append(out, r)
			}
		}
		return out
	})
	got := firmwarePresetRefusalsSince(start)
	if len(got) != 1 || !got[0].At.Equal(all[1].At) {
		t.Fatalf("want only the refusal during the sweep, got %v", got)
	}
}

// sweepSpecs builds a sweep the way the 2026-10-26 bundles' store wrote it:
// slots 1, 2, 3 and 6 natively, the Spotify keys in upnp in the UPnP form.
func sweepSpecs(upnp ...int) []boxcli.PresetSpec {
	isUPnP := map[int]bool{}
	for _, s := range upnp {
		isUPnP[s] = true
	}
	var specs []boxcli.PresetSpec
	for _, slot := range []int{3, 5, 4, 1, 2, 6} { // the store's own order
		spec := boxcli.PresetSpec{Slot: slot, StreamURL: "http://127.0.0.1:8888/stream/x"}
		if !isUPnP[slot] {
			spec.NativeLocation = "/station?data=x"
		}
		specs = append(specs, spec)
	}
	return specs
}

func sourceIDRefusals(n int) []boxlog.PresetRefusal {
	out := make([]boxlog.PresetRefusal, n)
	for i := range out {
		out[i] = boxlog.PresetRefusal{Reason: reasonSourceID, InvalidSourceID: true}
	}
	return out
}

// TestJudgeSweepRefusalsBundleShapes pins the three 2026-10-26 bundles: every
// refusal of the sweep is the account copy refusing a UPnP-form (Spotify) key,
// one per such key, and none of it may be pinned on the native slots.
func TestJudgeSweepRefusalsBundleShapes(t *testing.T) {
	all := []int{1, 2, 3, 4, 5, 6}

	// Two Spotify keys, two refusals (10:11:03/04 and 10:16:11/12).
	j := judgeSweepRefusals(sweepSpecs(4, 5), all, sourceIDRefusals(2))
	if j.Genuine != 0 || len(j.Suspects) != 0 {
		t.Fatalf("two refusals for two UPnP-form keys are expected, not genuine: %+v", j)
	}
	if j.Expected != 2 || !reflect.DeepEqual(j.UPnPForm, []int{4, 5}) {
		t.Fatalf("want the two UPnP-form keys to explain both refusals, got %+v", j)
	}

	// One Spotify key, one refusal (the #1526 group master flagged five slots).
	j = judgeSweepRefusals(sweepSpecs(4), []int{1, 2, 3, 4, 6}, sourceIDRefusals(1))
	if j.Genuine != 0 || len(j.Suspects) != 0 || j.Expected != 1 {
		t.Fatalf("one refusal for one UPnP-form key is expected: %+v", j)
	}

	// No refusal at all.
	j = judgeSweepRefusals(sweepSpecs(4), all, nil)
	if j.Genuine != 0 || j.Expected != 0 || j.UPnPForm != nil {
		t.Fatalf("no refusal must judge to nothing: %+v", j)
	}
}

// TestJudgeSweepRefusalsGenuine: a refusal the UPnP-form writes do not explain
// is real, and lands only on the slots it can belong to.
func TestJudgeSweepRefusalsGenuine(t *testing.T) {
	all := []int{1, 2, 3, 4, 5, 6}

	// One more "invalid SourceID" than UPnP-form writes: a native write was
	// refused for that reason too, so only the native slots are suspects.
	j := judgeSweepRefusals(sweepSpecs(4), all, sourceIDRefusals(2))
	if j.Expected != 1 || j.Genuine != 1 {
		t.Fatalf("want 1 expected + 1 genuine, got %+v", j)
	}
	if !reflect.DeepEqual(j.Suspects, []int{1, 2, 3, 5, 6}) {
		t.Fatalf("the UPnP-form key cannot carry the excess refusal, suspects %v", j.Suspects)
	}

	// The 2026-10-22 boot case: "MargeHSM is in the wrong state" can hit any
	// slot, UPnP form included.
	refusals := append(sourceIDRefusals(1), boxlog.PresetRefusal{Reason: reasonWrongState})
	j = judgeSweepRefusals(sweepSpecs(4), all, refusals)
	if j.Genuine != 1 || j.Reason != reasonWrongState || !reflect.DeepEqual(j.Suspects, all) {
		t.Fatalf("a wrong-state refusal must suspect every rewritten slot: %+v", j)
	}

	// A refusal while only freshly healed slots were written (kept empty)
	// suspects nothing: those are listed now and demonstrably landed.
	j = judgeSweepRefusals(sweepSpecs(), nil, []boxlog.PresetRefusal{{Reason: reasonWrongState}})
	if j.Genuine != 1 || len(j.Suspects) != 0 {
		t.Fatalf("healed-only sweep: %+v", j)
	}
}

// TestReportRewrittenSlotsIsHonest pins the 2026-10-22 SoundTouch 20 bundle
// (a genuine refusal must say so at WARN) and the 2026-10-26 bundles (the
// expected UPnP-form refusal must not be reported as one, nor ask for a
// re-sync).
func TestReportRewrittenSlotsIsHonest(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	all := []int{1, 2, 3, 4, 5, 6}

	if reportRewrittenSlots(logger, nil, sweepRefusals{Genuine: 1}) {
		t.Fatal("nothing rewritten: nothing to report")
	}
	if buf.Len() != 0 {
		t.Fatalf("nothing rewritten must log nothing, got %q", buf.String())
	}

	j := judgeSweepRefusals(sweepSpecs(), all, []boxlog.PresetRefusal{{Reason: reasonWrongState}})
	if !reportRewrittenSlots(logger, all, j) {
		t.Fatal("a refused rewrite must be reported as refused")
	}
	out := buf.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "refused") {
		t.Fatalf("a refused rewrite must log a WARN naming the refusal, got %q", out)
	}
	if strings.Contains(out, "it still lists them") || strings.Contains(out, "account layer was not ready") {
		t.Fatalf("old misleading wording must be gone, got %q", out)
	}

	buf.Reset()
	if reportRewrittenSlots(logger, []int{6}, sweepRefusals{}) {
		t.Fatal("no refusal seen: must not report one")
	}
	out = buf.String()
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "cannot confirm") {
		t.Fatalf("an unrefused rewrite must say the list cannot confirm it, got %q", out)
	}

	buf.Reset()
	j = judgeSweepRefusals(sweepSpecs(4, 5), all, sourceIDRefusals(2))
	if reportRewrittenSlots(logger, all, j) {
		t.Fatal("the expected UPnP-form refusal must not ask for a re-sync")
	}
	out = buf.String()
	if strings.Contains(out, "level=WARN") {
		t.Fatalf("the expected UPnP-form refusal is no warning, got %q", out)
	}
	if !strings.Contains(out, "UPnP-form") || !strings.Contains(out, "slots=\"[4 5]\"") {
		t.Fatalf("the line must name the UPnP-form keys only, got %q", out)
	}
}

func TestRefusedRewriteBackoff(t *testing.T) {
	want := map[int]time.Duration{
		0: 0, 1: 0,
		2: 5 * time.Minute, 3: 10 * time.Minute, 4: 20 * time.Minute,
		5: 40 * time.Minute, 6: time.Hour, 7: time.Hour, 50: time.Hour,
	}
	for streak, d := range want {
		if got := refusedRewriteBackoff(streak); got != d {
			t.Errorf("streak %d: backoff %v, want %v", streak, got, d)
		}
	}
}

// fakeFirmware models what the speaker does with one forced sweep: it stores
// every key (all stay listed), logs the account-copy refusal for each UPnP-form
// write, and optionally refuses some slots for real.
type fakeFirmware struct {
	upnp       []int
	refuseReal int // genuine refusals per sweep
}

func (f fakeFirmware) sweep() ([]boxcli.PresetSpec, []boxlog.PresetRefusal) {
	refusals := sourceIDRefusals(len(f.upnp))
	for i := 0; i < f.refuseReal; i++ {
		refusals = append(refusals, boxlog.PresetRefusal{Reason: reasonWrongState})
	}
	return sweepSpecs(f.upnp...), refusals
}

// runSweeps drives the post-sweep decision the reconcile makes, one sweep per
// period (the old hold ceiling was five minutes), and returns the minutes at
// which a re-sync was asked for.
func runSweeps(t *testing.T, fw fakeFirmware, n int, period time.Duration) []int {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	kept := []int{1, 2, 3, 4, 5, 6}
	start := time.Date(2026, 10, 26, 15, 11, 0, 0, time.UTC)
	var asks []int
	for i := 0; i < n; i++ {
		now := start.Add(time.Duration(i) * period)
		specs, refusals := fw.sweep()
		refused := reportRewrittenSlots(logger, kept, judgeSweepRefusals(specs, kept, refusals))
		if ask, _, _ := noteRewriteOutcome(refused, now); ask {
			asks = append(asks, int(now.Sub(start).Minutes()))
		}
	}
	return asks
}

// TestRefusalLoopEndsForUPnPFormKeys reproduces the 2026-10-26 loop: a
// firmware that "refuses" the Spotify key on every sweep. Before the fix each
// sweep asked for the next one, every five minutes, for as long as the music
// played (30 refusal lines in one bundle). Now not a single re-sync is asked.
func TestRefusalLoopEndsForUPnPFormKeys(t *testing.T) {
	resetRefusedRewriteBackoff(t)
	if asks := runSweeps(t, fakeFirmware{upnp: []int{4}}, 12, 5*time.Minute); len(asks) != 0 {
		t.Fatalf("an expected UPnP-form refusal must never re-ask, asked at minutes %v", asks)
	}
	if asks := runSweeps(t, fakeFirmware{upnp: []int{4, 5}}, 12, 5*time.Minute); len(asks) != 0 {
		t.Fatalf("two UPnP-form keys: must never re-ask, asked at minutes %v", asks)
	}
}

// TestRefusalLoopBacksOffForAGenuineRefusal: a firmware that really refuses one
// slot on every sweep still gets retries, but with growing gaps up to the cap,
// instead of one every five minutes.
func TestRefusalLoopBacksOffForAGenuineRefusal(t *testing.T) {
	resetRefusedRewriteBackoff(t)
	asks := runSweeps(t, fakeFirmware{upnp: []int{4}, refuseReal: 1}, 60, 5*time.Minute)
	want := []int{0, 5, 15, 35, 75, 135, 195, 255}
	if !reflect.DeepEqual(asks, want) {
		t.Fatalf("re-asks at minutes %v, want %v", asks, want)
	}
	// A clean sweep ends the streak: the next refusal asks at once again.
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	_ = reportRewrittenSlots(logger, []int{1}, sweepRefusals{})
	if ask, _, _ := noteRewriteOutcome(false, time.Now()); ask {
		t.Fatal("a clean sweep never asks")
	}
	if ask, streak, _ := noteRewriteOutcome(true, time.Now()); !ask || streak != 1 {
		t.Fatalf("after a clean sweep the first refusal must ask at once: ask=%v streak=%d", ask, streak)
	}
}

// TestSlotsMissingOnBox: the hold ceiling only runs a forced pass into the
// music when a key is genuinely gone from the speaker's list.
func TestSlotsMissingOnBox(t *testing.T) {
	stick := []presets.Preset{
		{Slot: 1, Type: "radio"}, {Slot: 2, Type: "queue"}, {Slot: 3, Type: "radio"},
		{Slot: 4, Type: "spotify"}, {Slot: 6, Type: "queue"},
	}
	all := []boxPresetEntry{{Slot: 1}, {Slot: 2}, {Slot: 3}, {Slot: 4}, {Slot: 6}}
	if got := slotsMissingOnBox(stick, nil, all); len(got) != 0 {
		t.Fatalf("every key listed: nothing missing, got %v", got)
	}
	if got := slotsMissingOnBox(stick, []int{5}, all); !reflect.DeepEqual(got, []int{5}) {
		t.Fatalf("an unlisted webhook-only key is missing, got %v", got)
	}
	if got := slotsMissingOnBox(stick, nil, all[:3]); !reflect.DeepEqual(got, []int{4, 6}) {
		t.Fatalf("want [4 6] missing, got %v", got)
	}
	if got := slotsMissingOnBox(stick, nil, nil); !reflect.DeepEqual(got, []int{1, 2, 3, 4, 6}) {
		t.Fatalf("an empty list misses every key, got %v", got)
	}
}
