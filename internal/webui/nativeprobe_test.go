package webui

import (
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// The probe's read-back line, off the typed /presets reader: the slot asked
// for, its location entity-decoded, nothing for a slot the list lacks.
func TestProbeSlotSummary(t *testing.T) {
	doc := boxapi.ParsePresets([]byte(`<presets>` +
		`<preset id="1"><ContentItem source="UPNP" type="audio" location="http://192.0.2.1:8888/stream/raw?u=abc&amp;x=1" sourceAccount="UPnPUserName"><itemName>One</itemName></ContentItem></preset>` +
		`<preset id="2"><ContentItem source="LOCAL_INTERNET_RADIO" type="stationurl" location="/station?data=abc" sourceAccount=""><itemName>Two</itemName></ContentItem></preset>` +
		`<preset id="3"></preset>` +
		`</presets>`))
	for slot, want := range map[int]string{
		1: "UPNP audio account=UPnPUserName http://192.0.2.1:8888/stream/raw?u=abc&x=1",
		2: "LOCAL_INTERNET_RADIO stationurl account= /station?data=abc",
		3: "  account= ",
	} {
		got, ok := probeSlotSummary(doc, slot)
		if !ok || got != want {
			t.Errorf("slot %d = %q, %v, want %q", slot, got, ok, want)
		}
	}
	if got, ok := probeSlotSummary(doc, 4); ok || got != "" {
		t.Errorf("slot 4 = %q, %v, want nothing", got, ok)
	}
}
