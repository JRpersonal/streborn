package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/presets"
)

func nativeTestPreset(slot int) presets.Preset {
	p, _ := presets.NewNativePreset(slot, presets.NativeItem{
		Source: "PANDORA", SourceAccount: "listener@example.com", Location: "4071226281950183516",
		ItemType: "stationurl", ItemName: "Little Big Town Radio",
	}, "")
	return p
}

type recordedWrite struct {
	slot                                 int
	source, typ, location, name, account string
}

func recordNativeWrites(t *testing.T, fail error) *[]recordedWrite {
	t.Helper()
	var got []recordedWrite
	origAccounts := boxSourceAccountsFn
	boxSourceAccountsFn = func(string) map[string][]string { return map[string][]string{} }
	orig := addPresetContentItemFn
	addPresetContentItemFn = func(_ context.Context, _ string, slot int, source, typ, location, name, account string) error {
		got = append(got, recordedWrite{slot, source, typ, location, name, account})
		return fail
	}
	t.Cleanup(func() {
		addPresetContentItemFn = orig
		boxSourceAccountsFn = origAccounts
		nativeSlotState.Lock()
		nativeSlotState.refusedAt = map[string]time.Time{}
		nativeSlotState.skipped = map[int]string{}
		nativeSlotState.Unlock()
	})
	return &got
}

func TestPlanNativeSlotsWritesTheItemOnlyWhenTheBoxLacksIt(t *testing.T) {
	stick := []presets.Preset{
		{Slot: 1, Name: "1LIVE", Type: "radio", StreamURL: "https://example.com/1live.mp3"},
		nativeTestPreset(2),
	}
	asked := 0
	ready := func(string) bool { asked++; return true }

	// The box holds exactly the stored item (location escaped the way /presets
	// reports attribute values): nothing to write, the reconcile is idempotent,
	// and the source list is not even read.
	held := []boxPresetEntry{{Slot: 2, Source: "PANDORA", Location: "4071226281950183516"}}
	if w, sk := planNativeSlots(stick, held, ready); len(w) != 0 || len(sk) != 0 || asked != 0 {
		t.Fatalf("box already holds the item: writes=%v skips=%v asked=%d", w, sk, asked)
	}

	// Missing on the box, or overwritten by STR's stream form: written back.
	for _, entries := range [][]boxPresetEntry{
		nil,
		{{Slot: 2, Source: "UPNP", Location: "http://127.0.0.1:8888/stream/2"}},
	} {
		w, sk := planNativeSlots(stick, entries, ready)
		if len(w) != 1 || w[0].Slot != 2 || len(sk) != 0 {
			t.Fatalf("entries %v: writes=%v skips=%v", entries, w, sk)
		}
	}
}

func TestPlanNativeSlotsSkipsAServiceTheSpeakerDoesNotOffer(t *testing.T) {
	stick := []presets.Preset{nativeTestPreset(3)}
	w, sk := planNativeSlots(stick, nil, func(string) bool { return false })
	if len(w) != 0 || len(sk) != 1 || sk[0].Slot != 3 || sk[0].Source != "PANDORA" {
		t.Fatalf("writes=%v skips=%v", w, sk)
	}
}

func TestWriteNativeSlotsSendsTheStoredContentItem(t *testing.T) {
	got := recordNativeWrites(t, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := nativeTestPreset(2)
	p.Name = "Country"
	written := writeNativeSlots("192.0.2.1", []presets.Preset{p}, logger)
	if len(written) != 1 || written[0] != 2 {
		t.Fatalf("written = %v", written)
	}
	want := recordedWrite{2, "PANDORA", "stationurl", "4071226281950183516", "Country", "listener@example.com"}
	if len(*got) != 1 || (*got)[0] != want {
		t.Fatalf("writes = %+v, want %+v", *got, want)
	}
}

// A refused write backs off instead of being retried on every pass.
func TestWriteNativeSlotsBacksOffAfterARefusal(t *testing.T) {
	got := recordNativeWrites(t, errors.New("box refused"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := nativeTestPreset(2)
	if w := writeNativeSlots("192.0.2.1", []presets.Preset{p}, logger); len(w) != 0 {
		t.Fatalf("a refused write reported as written: %v", w)
	}
	writeNativeSlots("192.0.2.1", []presets.Preset{p}, logger)
	if len(*got) != 1 {
		t.Fatalf("refused write retried immediately: %d attempts", len(*got))
	}
}

func TestParseSourceStatus(t *testing.T) {
	body := []byte(`<sources deviceID="device-id-here">` +
		`<sourceItem source="PANDORA" sourceAccount="listener@example.com" status="UNAVAILABLE">Pandora</sourceItem>` +
		`<sourceItem source="PANDORA" sourceAccount="other@example.com" status="READY">Pandora</sourceItem>` +
		`<sourceItem source="IHEARTRADIO" status="READY">iHeartRadio</sourceItem>` +
		`<sourceItem source="AUX" sourceAccount="AUX" status="READY">AUX</sourceItem></sources>`)
	st := parseSourceStatus(body)
	if st["PANDORA"] != "READY" || st["IHEART"] != "READY" || st["AUX"] != "READY" {
		t.Fatalf("status = %v", st)
	}
	if len(parseSourceStatus([]byte("not xml"))) != 0 {
		t.Fatal("garbage must read as no sources")
	}
}

func TestLazySourceReadyReadsOnce(t *testing.T) {
	calls := 0
	orig := boxSourceStatusFn
	boxSourceStatusFn = func(string) map[string]string { calls++; return map[string]string{"PANDORA": "READY"} }
	t.Cleanup(func() { boxSourceStatusFn = orig })
	ready := lazySourceReady("192.0.2.1")
	if !ready("pandora") || ready("IHEART") || !ready("PANDORA") {
		t.Fatal("readiness answers wrong")
	}
	if calls != 1 {
		t.Fatalf("source list read %d times, want 1", calls)
	}
}

// #1101: a key held on the speaker was stored with the station's name as its
// account. The write-back uses the account the speaker lists for the service
// instead, so the key comes back after a reboot.
func TestWriteNativeSlotsUsesTheSpeakersOwnAccount(t *testing.T) {
	got := recordNativeWrites(t, nil)
	boxSourceAccountsFn = func(string) map[string][]string {
		return map[string][]string{"PANDORA": {"listener@example.com"}}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := nativeTestPreset(2)
	p.Native.SourceAccount = "Chris Stapleton Radio"
	if w := writeNativeSlots("192.0.2.1", []presets.Preset{p}, logger); len(w) != 1 {
		t.Fatalf("written = %v", w)
	}
	if len(*got) != 1 || (*got)[0].account != "listener@example.com" {
		t.Fatalf("writes = %+v", *got)
	}
}
