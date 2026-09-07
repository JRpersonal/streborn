package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/recent"
	"github.com/JRpersonal/streborn/internal/webui"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// stubPruneBox puts the empty-store prune past its gates (recovery settled,
// box out of OOB) and captures its RemovePreset writes instead of sending
// them to a speaker. Restored on cleanup.
func stubPruneBox(t *testing.T) *[]int {
	t.Helper()
	origOOB, origRemove, origSrc, origFetch := boxInSetupOOBFn, removeBoxPresetFn, boxNowPlayingSourceFn, fetchBoxPresetsFullFn
	t.Cleanup(func() {
		boxInSetupOOBFn, removeBoxPresetFn, boxNowPlayingSourceFn, fetchBoxPresetsFullFn = origOOB, origRemove, origSrc, origFetch
		presetRecoverySettled.Store(false)
	})
	presetRecoverySettled.Store(true)
	boxInSetupOOBFn = func(string) bool { return false }
	boxNowPlayingSourceFn = func(string) string { return "STANDBY" }
	removed := &[]int{}
	removeBoxPresetFn = func(_ context.Context, _ string, slot int) error {
		*removed = append(*removed, slot)
		return nil
	}
	return removed
}

func emptyStore(t *testing.T) *presets.Store {
	t.Helper()
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func ownNativeEntry(slot int, name string) boxPresetEntry {
	return boxPresetEntry{Slot: slot, Name: name, Source: "LOCAL_INTERNET_RADIO", Type: "stationurl",
		Location: webui.OrionStationLocation(fmt.Sprintf("http://127.0.0.1:8888/stream/%d", slot), name, "")}
}

// staleStrSlots is the reconcile's deletion list: exactly the slots STR
// itself wrote that the store no longer backs, in every form the speaker
// reports them (#882 added the relative native form), and never a foreign
// slot or one the store or a webhook key claims.
func TestStaleStrSlots(t *testing.T) {
	native3 := webui.OrionStationLocation("http://127.0.0.1:8888/stream/3", "WDCB Jazz", "")
	// The speaker's own hold-to-store gesture keeps the playing item as is,
	// so a key stored while key 2 played points at key 2's proxy. marge hands
	// that slot to the store a moment later; it is not for the prune.
	held5 := webui.OrionStationLocation("http://127.0.0.1:8888/stream/2", "Held", "")
	foreignNative := webui.OrionStationLocation("https://stream.example.com/live.mp3", "Foreign", "")
	boxLocs := map[int]string{
		1: "http://127.0.0.1:8888/stream/1",
		2: "https://api.deezer.com/user/me/flow",
		3: native3,
		4: foreignNative,
		5: held5,
		6: "/core02/svc-bmx-adapter-orion/prod/orion/station?data=abc",
	}
	if got := staleStrSlots(boxLocs, nil); !reflect.DeepEqual(got, []int{1, 3, 6}) {
		t.Fatalf("empty store: want the STR-origin slots [1 3 6], got %v", got)
	}
	if got := staleStrSlots(boxLocs, map[int]bool{1: true, 3: true}); !reflect.DeepEqual(got, []int{6}) {
		t.Fatalf("slots the store backs must survive, got %v", got)
	}
	if got := staleStrSlots(map[int]string{}, nil); len(got) != 0 {
		t.Fatalf("an empty box list prunes nothing, got %v", got)
	}
}

// The store recovery identifies a lost NATIVE key by name from the
// recently-played history (#882: the relative native form was never matched,
// so an all-native box recovered nothing), seeds the cache with the StrOrigin
// flag, and leaves a verdict a bundle can read.
func TestSeedRecoversNativeSlotFromHistory(t *testing.T) {
	presetRecoverySettled.Store(false)
	orig := fetchBoxPresetsFullFn
	t.Cleanup(func() { fetchBoxPresetsFullFn = orig; presetRecoverySettled.Store(false) })
	loc := webui.OrionStationLocation("http://127.0.0.1:8888/stream/3", "WDCB Jazz", "")
	fetchBoxPresetsFullFn = func(string) ([]boxPresetEntry, error) {
		return []boxPresetEntry{
			{Slot: 3, Location: loc, Name: "WDCB Jazz", Source: "LOCAL_INTERNET_RADIO", Type: "stationurl"},
			{Slot: 4, Location: "https://api.deezer.com/user/me/flow", Name: "Flow", Source: "DEEZER", Type: "tracklistRadio"},
		}, nil
	}
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	rs := recent.New()
	rs.Add(recent.Entry{Source: "radio", CardKey: "radio:wdcb", CardName: "WDCB Jazz",
		CardURL: "https://wdcb.example/stream", CardArt: "https://wdcb.example/logo.png", Homepage: "https://wdcb.example"})
	// The seed is what the composition root's sink sees: it stamps the Lost
	// verdict against the store as it is at that moment.
	var seeded [][]webui.BoxPreset
	fired := 0
	seedBoxPresetsAndRecoverStore(store, rs, "192.0.2.1",
		func(b []webui.BoxPreset) { seeded = append(seeded, stampLostBoxPresets(b, store, nil)) },
		quietLogger(), func() { fired++ })
	if fired != 1 {
		t.Fatalf("first-attempt signal must fire once, got %d", fired)
	}
	// Seeded twice: once from the read (against the empty store, so the
	// native slot is lost at that point), once more after the recovery so
	// the cache does not call the recovered key dead until the next pass.
	if len(seeded) != 2 {
		t.Fatalf("the list must be published again after a recovery, got %d publishes", len(seeded))
	}
	first, last := seeded[0], seeded[1]
	if len(first) != 2 || !first[0].StrOrigin || first[1].StrOrigin {
		t.Fatalf("seed must carry StrOrigin for the native STR slot only, got %+v", first)
	}
	if !first[0].Lost || first[1].Lost {
		t.Fatalf("before the recovery the native slot is lost and the foreign one is not, got %+v", first)
	}
	if last[0].Lost || last[1].Lost {
		t.Fatalf("after the recovery nothing is lost, got %+v", last)
	}
	p, ok := store.Get(3)
	if !ok || p.Type != "radio" || p.StreamURL != "https://wdcb.example/stream" || p.Art != "https://wdcb.example/logo.png" {
		t.Fatalf("slot 3 must be recovered from the history, got %+v ok=%v", p, ok)
	}
	if _, ok := store.Get(4); ok {
		t.Fatal("the foreign Deezer slot must never be turned into a store preset")
	}
	v := presetRecoveryStatus().(presetRecoveryVerdict)
	if v.BoxSlots != 2 || v.StrOriginSlots != 1 || v.HistoryEntries != 1 || v.Recovered != 1 || v.Reason != "ok" {
		t.Fatalf("verdict: %+v", v)
	}
	if !presetRecoverySettled.Load() {
		t.Fatal("the recovery must report itself settled so the empty-store prune may run")
	}
}

// The reinstall signature (#882): an empty store, STR's own keys on the box,
// and NO history to recover from (the removal took recent.json too). Nothing
// is recovered, the verdict says why, and the store stays empty.
func TestSeedReportsNoHistoryOnReinstall(t *testing.T) {
	presetRecoverySettled.Store(false)
	orig := fetchBoxPresetsFullFn
	t.Cleanup(func() { fetchBoxPresetsFullFn = orig; presetRecoverySettled.Store(false) })
	fetchBoxPresetsFullFn = func(string) ([]boxPresetEntry, error) {
		var out []boxPresetEntry
		for slot, name := range map[int]string{1: "101 SMOOTH JAZZ", 2: "Jazz Radio Classic Jazz", 3: "WDCB Jazz"} {
			out = append(out, boxPresetEntry{Slot: slot, Name: name, Source: "LOCAL_INTERNET_RADIO", Type: "stationurl",
				Location: webui.OrionStationLocation("http://127.0.0.1:8888/stream/"+string(rune('0'+slot)), name, "")})
		}
		return out, nil
	}
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	seedBoxPresetsAndRecoverStore(store, recent.New(), "192.0.2.1", nil, quietLogger(), nil)
	if n := len(store.All()); n != 0 {
		t.Fatalf("nothing to recover from, store must stay empty, got %d", n)
	}
	v := presetRecoveryStatus().(presetRecoveryVerdict)
	if v.BoxSlots != 3 || v.StrOriginSlots != 3 || v.HistoryEntries != 0 || v.Recovered != 0 || v.Reason != "no-history" {
		t.Fatalf("verdict: %+v", v)
	}
	if !presetRecoverySettled.Load() {
		t.Fatal("settled must be set even when nothing was recovered")
	}
}

// A non-empty store is left alone and says so.
func TestSeedLeavesNonEmptyStoreAlone(t *testing.T) {
	orig := fetchBoxPresetsFullFn
	t.Cleanup(func() { fetchBoxPresetsFullFn = orig; presetRecoverySettled.Store(false) })
	fetchBoxPresetsFullFn = func(string) ([]boxPresetEntry, error) {
		return []boxPresetEntry{{Slot: 1, Name: "X", Location: webui.OrionStationLocation("http://127.0.0.1:8888/stream/1", "X", "")}}, nil
	}
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSlot(presets.Preset{Slot: 2, Name: "Kept", Type: "radio", StreamURL: "https://kept.example/s"}); err != nil {
		t.Fatal(err)
	}
	rs := recent.New()
	rs.Add(recent.Entry{Source: "radio", CardKey: "radio:x", CardName: "X", CardURL: "https://x.example/s"})
	seedBoxPresetsAndRecoverStore(store, rs, "192.0.2.1", nil, quietLogger(), nil)
	if _, ok := store.Get(1); ok {
		t.Fatal("a non-empty store must not be refilled from the box list")
	}
	if v := presetRecoveryStatus().(presetRecoveryVerdict); v.Reason != "store-not-empty" {
		t.Fatalf("verdict: %+v", v)
	}
}

// The empty-store prune waits for the recovery: until it has settled the
// pass must not even read the box, or it could delete the slots the recovery
// identifies the lost presets by.
func TestEmptyStorePruneWaitsForRecovery(t *testing.T) {
	presetRecoverySettled.Store(false)
	orig := fetchBoxPresetsFullFn
	t.Cleanup(func() { fetchBoxPresetsFullFn = orig })
	reads := 0
	fetchBoxPresetsFullFn = func(string) ([]boxPresetEntry, error) { reads++; return nil, nil }
	if pruneStaleKeysOnEmptyStore(emptyStore(t), nil, "192.0.2.1", quietLogger()) {
		t.Fatal("an unsettled recovery must keep the pass a no-op")
	}
	if reads != 0 {
		t.Fatalf("the box must not be read before the recovery settled, got %d reads", reads)
	}
}

// The positive control for the give-way test below: a store that IS still
// empty after the read gets its dead keys pruned, STR-origin slots only, and
// the pass reports the box done so the loop idles at the maintenance cadence.
func TestEmptyStorePrunesDeadKeysWhenStoreStaysEmpty(t *testing.T) {
	removed := stubPruneBox(t)
	fetchBoxPresetsFullFn = func(string) ([]boxPresetEntry, error) {
		return []boxPresetEntry{
			{Slot: 1, Name: "A", Location: "http://127.0.0.1:8888/stream/1"},
			{Slot: 2, Name: "Flow", Source: "DEEZER", Location: "https://api.deezer.com/user/me/flow"},
			ownNativeEntry(3, "C"),
		}, nil
	}
	if !pruneStaleKeysOnEmptyStore(emptyStore(t), nil, "192.0.2.1", quietLogger()) {
		t.Fatal("a pruned box must be reported done")
	}
	if !reflect.DeepEqual(*removed, []int{1, 3}) {
		t.Fatalf("want the two STR-origin slots removed, got %v", *removed)
	}
}

// "Empty" is decided after the box read, not on entry (review finding on
// #882). The desktop's post-install restore PUTs the stashed slots and syncs
// them onto the box within seconds of the agent binding its port; a pass that
// entered on an empty store and then read the box would see those six fresh
// keys, all in STR's own form, and prune every one as stale. The seam plays
// the restore: it fills the store, then hands back the list the restore
// registered. Nothing may be removed, and the pass hands the keys to the next
// pass (false, so the loop retries in seconds, not minutes).
func TestEmptyStorePruneGivesWayToRestore(t *testing.T) {
	removed := stubPruneBox(t)
	store := emptyStore(t)
	fetchBoxPresetsFullFn = func(string) ([]boxPresetEntry, error) {
		var out []boxPresetEntry
		for slot := 1; slot <= 6; slot++ {
			name := fmt.Sprintf("Station %d", slot)
			if err := store.SetSlot(presets.Preset{Slot: slot, Name: name, Type: "radio", StreamURL: "https://s.example/" + name}); err != nil {
				t.Fatal(err)
			}
			out = append(out, ownNativeEntry(slot, name))
		}
		return out, nil
	}
	if pruneStaleKeysOnEmptyStore(store, nil, "192.0.2.1", quietLogger()) {
		t.Fatal("a store that filled during the read must hand the pass back to the normal path")
	}
	if len(*removed) != 0 {
		t.Fatalf("the keys the restore just registered must survive, got removals %v", *removed)
	}
	if n := len(store.All()); n != 6 {
		t.Fatalf("the restored store must be untouched, got %d slots", n)
	}
}

// The same give-way for a webhook-only key configured while the list was out:
// its placeholder is STR-origin and store-less by design, and the normal pass
// is the one that knows to keep it.
func TestEmptyStorePruneGivesWayToWebhookConfig(t *testing.T) {
	removed := stubPruneBox(t)
	wh := hookStore(t, nil, nil)
	fetchBoxPresetsFullFn = func(string) ([]boxPresetEntry, error) {
		cfg := wh.Get()
		cfg.Buttons[presetButtonID(4)] = webhooksReplaceTrigger()
		if err := wh.Set(cfg); err != nil {
			t.Fatal(err)
		}
		return []boxPresetEntry{{Slot: 4, Name: webhookPlaceholderName, Location: "http://127.0.0.1:8888/stream/4"}}, nil
	}
	if pruneStaleKeysOnEmptyStore(emptyStore(t), wh, "192.0.2.1", quietLogger()) {
		t.Fatal("a webhook key configured during the read must hand the pass back to the normal path")
	}
	if len(*removed) != 0 {
		t.Fatalf("the webhook placeholder must survive, got removals %v", *removed)
	}
}

// The Lost verdict (what the desktop draws a dead key from) is made on the
// agent because only the agent sees the store AND the webhook config: a
// webhook-only key (#536) holds a placeholder in STR's own form and is kept
// out of the store on purpose, so judged by the location alone it is a dead
// key. It must not be.
func TestLostVerdictSparesStoredAndHookedSlots(t *testing.T) {
	store := emptyStore(t)
	if err := store.SetSlot(presets.Preset{Slot: 2, Name: "Kept", Type: "radio", StreamURL: "https://kept.example/s"}); err != nil {
		t.Fatal(err)
	}
	wh := hookStore(t, []int{4}, []int{5})
	bps := boxPresetsFromEntries([]boxPresetEntry{
		{Slot: 1, Name: "Dead", Location: "http://127.0.0.1:8888/stream/1"},
		ownNativeEntry(2, "Kept"),
		{Slot: 3, Name: "Flow", Source: "DEEZER", Location: "https://api.deezer.com/user/me/flow"},
		{Slot: 4, Name: webhookPlaceholderName, Location: "http://127.0.0.1:8888/stream/4"},
		// Additional mode gets no placeholder (a real preset plays as well),
		// so a store-less STR-origin slot there IS dead.
		{Slot: 5, Name: "Old", Location: "http://127.0.0.1:8888/stream/5"},
	})
	stampLostBoxPresets(bps, store, wh)
	want := map[int][2]bool{ // slot -> {StrOrigin, Lost}
		1: {true, true}, 2: {true, false}, 3: {false, false}, 4: {true, false}, 5: {true, true},
	}
	for _, bp := range bps {
		if w := want[bp.Slot]; bp.StrOrigin != w[0] || bp.Lost != w[1] {
			t.Errorf("slot %d: strOrigin=%v lost=%v, want %v/%v", bp.Slot, bp.StrOrigin, bp.Lost, w[0], w[1])
		}
	}
	// No store and no webhook config: every STR-origin slot is lost.
	bare := stampLostBoxPresets(boxPresetsFromEntries([]boxPresetEntry{{Slot: 6, Location: "http://127.0.0.1:8888/stream/6"}}), nil, nil)
	if !bare[0].Lost {
		t.Fatalf("without a store every STR-origin slot is lost, got %+v", bare[0])
	}
}
