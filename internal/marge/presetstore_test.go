package marge

import (
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The firmware's hold-to-store gesture, reconstructed from the Portable's
// syslog of 2026-09-06 ("MargeClient: AddPreset calling Marge Server with
// https://streaming.bose.com/streaming/account/stick@local/device/<id>/preset/N,
// Content: <?xml ...>") and the ContentItem shape the same firmware reports in
// its own /presets list. The location is the ORION station descriptor STR
// writes, base64 JSON behind "/station?data=".

const heldDevicePath = "/streaming/account/stick@local/device/AABBCCDDEEFF/preset/3"

func heldStationLocation(t *testing.T, streamURL, name string) string {
	t.Helper()
	payload := `{"imageUrl":"http://127.0.0.1:8888/icon.png","isRealtime":true,"name":"` + name +
		`","streamType":"liveRadio","streamUrl":"` + streamURL + `"}`
	return "/station?data=" + base64.RawURLEncoding.EncodeToString([]byte(payload))
}

func heldPresetBody(loc, name string) string {
	return `<?xml version="1.0" encoding="UTF-8" ?>` +
		`<ContentItem source="LOCAL_INTERNET_RADIO" type="stationurl" location="` + loc +
		`" sourceAccount="" isPresetable="true"><itemName>` + name + `</itemName>` +
		`<containerArt>http://127.0.0.1:8888/icon.png</containerArt></ContentItem>`
}

func newKeeperServer(t *testing.T, keeper PresetKeeper) *Server {
	t.Helper()
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithDeviceID("AABBCCDDEEFF"), WithPresetKeeper(keeper))
	s.SetAccount(&AccountInfo{AccountEmail: "stick@local"})
	return s
}

func TestPresetPutIsKeptAndAnsweredWithThePresetElement(t *testing.T) {
	var got HeldItem
	s := newKeeperServer(t, func(item HeldItem) error { got = item; return nil })

	loc := heldStationLocation(t, "http://127.0.0.1:8888/stream/raw?u=aHR0cDovL2V4YW1wbGUuY29tL2xpdmUubXAz", "Radio Example &amp; Co")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPut, heldDevicePath,
		strings.NewReader(heldPresetBody(loc, "Radio Example &amp; Co"))))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if n := strings.Count(body, "<?xml"); n != 1 {
		t.Fatalf("want exactly one XML declaration, got %d:\n%s", n, body)
	}
	if !strings.Contains(body, `<preset id="3" createdOn="`) || !strings.HasSuffix(body, "</preset>") {
		t.Fatalf("answer must be a single <preset id=\"3\"> element, got:\n%s", body)
	}
	if strings.Contains(body, "<account") || strings.Contains(body, "<presets") {
		t.Fatalf("answer must be the preset element, not the account or list document:\n%s", body)
	}
	if !strings.Contains(body, `<ContentItem source="LOCAL_INTERNET_RADIO" type="stationurl" location="`+loc+`" sourceAccount="" isPresetable="true">`) {
		t.Fatalf("ContentItem not echoed as sent:\n%s", body)
	}
	if !strings.Contains(body, "<itemName>Radio Example &amp; Co</itemName>") {
		t.Fatalf("itemName not echoed (escaped) in the answer:\n%s", body)
	}
	if got.Slot != 3 || got.Source != "LOCAL_INTERNET_RADIO" || got.Type != "stationurl" ||
		got.Location != loc || got.ItemName != "Radio Example & Co" ||
		got.ContainerArt != "http://127.0.0.1:8888/icon.png" {
		t.Fatalf("keeper got %+v", got)
	}
}

// A POST on the same path is the same request to the firmware; it used to be
// swallowed by the addDevice case (path contains /device, method POST).
func TestPresetPostIsKeptToo(t *testing.T) {
	kept := 0
	s := newKeeperServer(t, func(HeldItem) error { kept++; return nil })
	loc := heldStationLocation(t, "http://127.0.0.1:8888/stream/3", "WDR 5")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, heldDevicePath,
		strings.NewReader(heldPresetBody(loc, "WDR 5"))))
	if kept != 1 || !strings.Contains(w.Body.String(), `<preset id="3"`) {
		t.Fatalf("kept=%d body=%s", kept, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "adddeviceresponse") {
		t.Fatalf("preset POST answered with the addDevice document:\n%s", w.Body.String())
	}
}

// An item STR cannot keep (the keeper refuses) gets the answer this path had
// before: the account document, whose handling by the firmware is measured
// (UpdatePresetFailureCB, key unchanged).
func TestPresetPutRefusedKeepsTheOldAnswer(t *testing.T) {
	s := newKeeperServer(t, func(HeldItem) error { return io.ErrUnexpectedEOF })
	loc := heldStationLocation(t, "http://127.0.0.1:8888/stream/1", "1LIVE")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPut, heldDevicePath,
		strings.NewReader(heldPresetBody(loc, "1LIVE"))))
	body := w.Body.String()
	if strings.Contains(body, "<preset ") {
		t.Fatalf("a refused item must not be confirmed:\n%s", body)
	}
	if !strings.Contains(body, "<account>") || strings.Count(body, "<?xml") != 1 {
		t.Fatalf("refusal must keep the account answer (one document):\n%s", body)
	}
}

func TestPresetPutWithoutKeeperKeepsTheOldAnswer(t *testing.T) {
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)), WithDeviceID("AABBCCDDEEFF"))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPut, heldDevicePath,
		strings.NewReader(heldPresetBody("/station?data=abc", "x"))))
	if body := w.Body.String(); !strings.Contains(body, "<account>") || strings.Contains(body, "<preset ") {
		t.Fatalf("without a keeper the old answer must stand:\n%s", body)
	}
}

// The firmware DELETEs the slot before it re-stores an occupied key, and that
// DELETE is answered fine today ("DeletePresetCB Preset N deleted
// successfully"). It must neither reach the keeper nor change its answer.
func TestPresetDeleteNeverReachesTheKeeper(t *testing.T) {
	s := newKeeperServer(t, func(HeldItem) error { t.Fatal("DELETE reached the keeper"); return nil })
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodDelete, heldDevicePath, strings.NewReader("")))
	if body := w.Body.String(); !strings.Contains(body, "<account>") {
		t.Fatalf("DELETE answer changed:\n%s", body)
	}
}

// A body without a ContentItem (or a list path) must not be mistaken for a
// store request.
func TestPresetPutWithoutContentItemKeepsTheOldAnswer(t *testing.T) {
	s := newKeeperServer(t, func(HeldItem) error { t.Fatal("keeper called on an empty body"); return nil })
	for _, path := range []string{heldDevicePath, "/streaming/account/stick@local/device/AABBCCDDEEFF/preset/"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPut, path, strings.NewReader("<preset/>")))
		if body := w.Body.String(); !strings.Contains(body, "<account>") {
			t.Fatalf("%s: answer changed:\n%s", path, body)
		}
	}
}

func TestParseHeldItemShapes(t *testing.T) {
	loc := "/station?data=eyJzdHJlYW1VcmwiOiJodHRwOi8vZXhhbXBsZS5jb20vbGl2ZSJ9"
	cases := map[string]string{
		"bare with declaration": `<?xml version="1.0" encoding="UTF-8" ?><ContentItem source="LOCAL_INTERNET_RADIO" type="stationurl" location="` + loc + `" sourceAccount="" isPresetable="true"><itemName>WDR 5</itemName><containerArt>http://a/b.png</containerArt></ContentItem>`,
		"wrapped in preset":     `<preset id="3"><ContentItem source="LOCAL_INTERNET_RADIO" type="stationurl" location="` + loc + `" sourceAccount=""><itemName>WDR 5</itemName><containerArt>http://a/b.png</containerArt></ContentItem></preset>`,
		"no declaration":        `<ContentItem source="LOCAL_INTERNET_RADIO" type="stationurl" location="` + loc + `" sourceAccount="" isPresetable="true"><itemName>WDR 5</itemName><containerArt>http://a/b.png</containerArt></ContentItem>`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			item, ok := parseHeldItem([]byte(body))
			if !ok {
				t.Fatalf("not parsed")
			}
			if item.Source != "LOCAL_INTERNET_RADIO" || item.Type != "stationurl" || item.Location != loc ||
				item.ItemName != "WDR 5" || item.ContainerArt != "http://a/b.png" {
				t.Fatalf("parsed %+v", item)
			}
		})
	}
	// The UPnP form the firmware refuses itself ("AddPreset - failed due to
	// invalid SourceID") still parses; the keeper is what refuses it.
	item, ok := parseHeldItem([]byte(`<ContentItem source="UPNP" type="audio" location="http://127.0.0.1:8888/stream/4" sourceAccount="UPnPUserName"><itemName>x</itemName></ContentItem>`))
	if !ok || item.Source != "UPNP" || item.ContainerArt != "" {
		t.Fatalf("UPNP item: ok=%v %+v", ok, item)
	}
	for _, bad := range []string{"", "<preset/>", "<ContentItem type=\"stationurl\"/>", "not xml at all"} {
		if _, ok := parseHeldItem([]byte(bad)); ok {
			t.Fatalf("%q must not parse as a held item", bad)
		}
	}
}

func TestPresetSlotFromPath(t *testing.T) {
	for path, want := range map[string]int{
		heldDevicePath: 3,
		"/streaming/account/stick@local/device/AABBCCDDEEFF/preset/6/": 6,
		"/streaming/account/stick@local/device/AABBCCDDEEFF/preset/7":  0,
		"/streaming/account/stick@local/device/AABBCCDDEEFF/preset/0":  0,
		"/streaming/account/stick@local/device/AABBCCDDEEFF/preset/":   0,
		"/streaming/account/stick@local/device/AABBCCDDEEFF/presets":   0,
		"/preset/list": 0,
	} {
		got, ok := presetSlotFromPath(path)
		if (want == 0) == ok || got != want {
			t.Errorf("%s: got %d/%v, want %d", path, got, ok, want)
		}
	}
}

func TestPresetElementXMLEscapes(t *testing.T) {
	out := presetElementXML(HeldItem{Slot: 2, Source: "LOCAL_INTERNET_RADIO", Type: "stationurl",
		Location: "/station?data=abc", ItemName: `Pop & "Rock"`, ContainerArt: "http://a/b?c=1&d=2"}, time.Unix(1700000000, 0))
	if !strings.Contains(out, "<itemName>Pop &amp; &quot;Rock&quot;</itemName>") ||
		!strings.Contains(out, "<containerArt>http://a/b?c=1&amp;d=2</containerArt>") ||
		!strings.Contains(out, `createdOn="1700000000" updatedOn="1700000000"`) {
		t.Fatalf("got %s", out)
	}
}

// The per-station recents report is answered with the record it created, not
// with the empty list ("AddRecentCB Failed with status=N" on every source
// change until now).
func TestRecentsPostEchoesTheRecordWithAnID(t *testing.T) {
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)), WithDeviceID("AABBCCDDEEFF"))
	const posted = `<?xml version="1.0" encoding="UTF-8" ?><recent>` +
		`<lastplayedat>2026-09-06T17:19:49+00:00</lastplayedat><sourceid>3</sourceid>` +
		`<name>Best Of Rock.FM Alternative Rock</name><location>/station?data=abc</location>` +
		`<contentItemType>stationurl</contentItemType></recent>`
	const path = "/streaming/account/stick@local/device/AABBCCDDEEFF/recent"

	for i, wantID := range []string{`id="1"`, `id="2"`} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(posted)))
		body := w.Body.String()
		if w.Code != http.StatusOK {
			t.Fatalf("POST %d: status %d", i, w.Code)
		}
		if strings.Count(body, "<?xml") != 1 {
			t.Fatalf("POST %d: want one declaration:\n%s", i, body)
		}
		if strings.Contains(body, "<recents") || strings.Contains(body, "adddeviceresponse") {
			t.Fatalf("POST %d: answered with the list / addDevice document:\n%s", i, body)
		}
		if !strings.Contains(body, `<recent `+wantID+` `) || !strings.HasSuffix(body, "</recent>") {
			t.Fatalf("POST %d: want the record with %s:\n%s", i, wantID, body)
		}
		// The record comes back in the preset-list dialect (ContentItem child),
		// which is what the firmware's parser accepts; the flat echo did not.
		for _, field := range []string{`<ContentItem source="LOCAL_INTERNET_RADIO" type="stationurl" location="/station?data=abc"`,
			"<itemName>Best Of Rock.FM Alternative Rock</itemName>", "</ContentItem></recent>"} {
			if !strings.Contains(body, field) {
				t.Fatalf("POST %d: %s missing:\n%s", i, field, body)
			}
		}
	}
}

// A recents POST without a record falls back to the empty list, never to a
// half-built element.
func TestRecentsPostWithoutRecordKeepsTheListAnswer(t *testing.T) {
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)), WithDeviceID("AABBCCDDEEFF"))
	for _, body := range []string{"", "<recents/>", "<recent>unterminated"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost,
			"/streaming/account/stick@local/device/AABBCCDDEEFF/recent", strings.NewReader(body)))
		if got := w.Body.String(); !strings.Contains(got, "<recents/>") || strings.Contains(got, "<recent ") {
			t.Fatalf("%q: got %s", body, got)
		}
	}
}

func TestFlatPresetBodyFromTheFirmwareIsUnderstood(t *testing.T) {
	// Verbatim shape of the Portable's hold-to-store PUT (2026-09-06), the
	// station location shortened.
	body := `<?xml version="1.0" encoding="UTF-8" ?><preset buttonNumber="6"><sourceid>3</sourceid><name>MANGORADIO</name><username>MANGORADIO</username><location>/station?data=eyJuYW1lIjoiTUFOR09SQURJTyJ9</location><contentItemType>stationurl</contentItemType><containerArt></containerArt></preset>`
	item, ok := parseHeldItem([]byte(body))
	if !ok {
		t.Fatal("flat preset body not understood")
	}
	if item.Source != "LOCAL_INTERNET_RADIO" || item.Type != "stationurl" || item.ItemName != "MANGORADIO" ||
		item.Location != "/station?data=eyJuYW1lIjoiTUFOR09SQURJTyJ9" || item.SourceAccount != "MANGORADIO" {
		t.Fatalf("got %+v", item)
	}
	if _, ok := parseHeldItem([]byte(`<preset buttonNumber="2"><sourceid>3</sourceid><name>x</name></preset>`)); ok {
		t.Fatal("a body without a location must be refused")
	}
}

func TestRecentAnswerUsesTheContentItemDialect(t *testing.T) {
	body := `<?xml version="1.0" encoding="UTF-8" ?><recent><lastplayedat>2026-09-06T20:28:30+00:00</lastplayedat><sourceid>3</sourceid><name>MANGORADIO</name><location>/station?data=eyJuYW1lIjoiTUFOR09SQURJTyJ9</location><contentItemType>stationurl</contentItemType></recent>`
	rec, ok := parseFlatRecord([]byte(body), "recent")
	if !ok || rec.lastPlayedAt != "2026-09-06T20:28:30+00:00" || rec.sourceID != "3" {
		t.Fatalf("parse: ok=%v rec=%+v", ok, rec)
	}
	got := recentElementXML(rec, 7, time.Unix(1700000000, 0))
	for _, want := range []string{`<recent id="7" createdOn="1700000000" updatedOn="1700000000">`, `<ContentItem source="LOCAL_INTERNET_RADIO" type="stationurl" location="/station?data=eyJuYW1lIjoiTUFOR09SQURJTyJ9"`, `<itemName>MANGORADIO</itemName>`, `</ContentItem></recent>`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
	if sourceNameForAccountID("12") != "STORED_MUSIC" || sourceNameForAccountID("x") != "SOURCE#x" {
		t.Fatal("source id mapping")
	}
}
