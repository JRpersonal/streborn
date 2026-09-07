package webui

import (
	"encoding/base64"
	"strings"
	"testing"
)

// StationLocationOwnSlot is the predicate the reconcile prune keys DELETION
// off for native slots (#882), so it has to recognise every spelling the
// speaker can hand back of a location STR wrote, and nothing else.
func TestStationLocationOwnSlot(t *testing.T) {
	payload := []byte(`{"streamUrl":"http://127.0.0.1:8888/stream/3","name":"WDCB Jazz","imageUrl":""}`)
	own := map[string]string{
		"as written":        OrionStationLocation("http://127.0.0.1:8888/stream/3", "WDCB Jazz", ""),
		"orion prefix":      "/core02/svc-bmx-adapter-orion/prod/orion" + OrionStationLocation("http://127.0.0.1:8888/stream/3", "WDCB Jazz", ""),
		"absolute prefix":   "https://content.api.bose.io/core02/svc-bmx-adapter-orion/prod/orion" + OrionStationLocation("http://127.0.0.1:8888/stream/3", "WDCB Jazz", ""),
		"std alphabet":      "/station?data=" + base64.StdEncoding.EncodeToString(payload),
		"url alphabet":      "/station?data=" + base64.URLEncoding.EncodeToString(payload),
		"escaped padding":   "/station?data=" + strings.ReplaceAll(base64.URLEncoding.EncodeToString(payload), "=", "%3D"),
		"redirect port":     OrionStationLocation("http://127.0.0.1:17008/stream/3", "WDCB Jazz", ""),
		"with real artwork": OrionStationLocation("http://127.0.0.1:8888/stream/3", "WDCB Jazz", "https://wdcb.example/logo.png"),
	}
	for name, loc := range own {
		slot, ok := StationLocationOwnSlot(loc)
		if !ok || slot != 3 {
			t.Errorf("%s: got slot=%d ok=%v, want slot 3 (loc %q)", name, slot, ok, loc)
		}
	}
	foreign := map[string]string{
		"external stream":   OrionStationLocation("https://stream.example.com/live.mp3", "Foreign", ""),
		"slot out of range": OrionStationLocation("http://127.0.0.1:8888/stream/7", "Seven", ""),
		"ad-hoc raw proxy":  OrionStationLocation("http://127.0.0.1:8888/stream/raw?u=abc", "Ad hoc", ""),
		"lan host":          OrionStationLocation("http://192.0.2.5:8888/stream/3", "Not loopback", ""),
		"https loopback":    OrionStationLocation("https://127.0.0.1:8888/stream/3", "Wrong scheme", ""),
		"upnp form":         "http://127.0.0.1:8888/stream/3",
		"garbage":           "/station?data=!!!not-base64!!!",
		"empty payload":     "/station?data=",
		"no data":           "/station",
		"deezer":            "https://api.deezer.com/user/me/flow",
		"empty":             "",
	}
	for name, loc := range foreign {
		if slot, ok := StationLocationOwnSlot(loc); ok {
			t.Errorf("%s: must not be an own slot, got slot %d (loc %q)", name, slot, loc)
		}
	}
}
