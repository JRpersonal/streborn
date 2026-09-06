package webui

import (
	"encoding/base64"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxurl"
)

func TestDecodeNativeStationOwnSlotProxy(t *testing.T) {
	loc := OrionStationLocation(boxurl.StreamSlot(2), "1LIVE", "https://example.com/1live.png")
	st, ok := DecodeNativeStation(loc)
	if !ok {
		t.Fatal("not decoded")
	}
	if st.ProxySlot != 2 || st.OriginStreamURL != "" || st.Name != "1LIVE" || st.StreamURL != boxurl.StreamSlot(2) {
		t.Fatalf("got %+v", st)
	}
	// The artwork travels through the art proxy for the speaker; the origin
	// comes back out.
	if st.Art != "https://example.com/1live.png" {
		t.Fatalf("art = %q", st.Art)
	}
	if !NativeLocationIsOwnSlotProxy(loc, 2) || NativeLocationIsOwnSlotProxy(loc, 3) {
		t.Fatal("own-slot verdict wrong")
	}
	// The firmware reports the slot behind the adapter path too.
	full := "/core02/svc-bmx-adapter-orion/prod/orion" + loc
	if st2, ok := DecodeNativeStation(full); !ok || st2.ProxySlot != 2 {
		t.Fatalf("adapter-path form: ok=%v %+v", ok, st2)
	}
}

func TestDecodeNativeStationRawProxyUnwindsToTheOrigin(t *testing.T) {
	loc := OrionStationLocation(boxurl.RawStream("https://stream.example.com/live.aac"), "Example", "")
	st, ok := DecodeNativeStation(loc)
	if !ok {
		t.Fatal("not decoded")
	}
	if st.ProxySlot != 0 || st.OriginStreamURL != "https://stream.example.com/live.aac" {
		t.Fatalf("got %+v", st)
	}
	// No art: the stand-in logo the agent puts in is not the station's.
	if st.Art != "" {
		t.Fatalf("stand-in logo leaked as art: %q", st.Art)
	}
	if NativeLocationIsOwnSlotProxy(loc, 3) {
		t.Fatal("a raw-proxy slot is not its own slot proxy")
	}
}

func TestDecodeNativeStationPlainOriginAndUnreadable(t *testing.T) {
	payload := `{"streamUrl":"http://stream.example.com/live.mp3","name":"Plain","imageUrl":"http://img.example.com/logo.png"}`
	loc := "/station?data=" + base64.StdEncoding.EncodeToString([]byte(payload))
	st, ok := DecodeNativeStation(loc)
	if !ok || st.OriginStreamURL != "http://stream.example.com/live.mp3" || st.Art != "http://img.example.com/logo.png" {
		t.Fatalf("ok=%v %+v", ok, st)
	}
	for _, bad := range []string{"", "http://example.com/live.mp3", "/station?data=@@@",
		"/station?data=" + base64.RawURLEncoding.EncodeToString([]byte(`{"name":"no stream"}`))} {
		if _, ok := DecodeNativeStation(bad); ok {
			t.Errorf("%q decoded", bad)
		}
		// Unreadable means "leave the slot alone".
		if !NativeLocationIsOwnSlotProxy(bad, 1) {
			t.Errorf("%q: unreadable location must count as own", bad)
		}
	}
}
