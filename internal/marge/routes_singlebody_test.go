package marge

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every marge route must write exactly ONE document.
//
// The routing is a switch whose cases each call a responder; a case that
// forgets its `return` writes its response and then falls out of the switch
// into the legacy fallback below, which matches on loose substrings ("source",
// "account", "preset") and appends a SECOND body to the same response. The box
// then receives two XML declarations and two root elements in one HTTP reply -
// not well-formed XML - and on the account sources route the appended document
// was an EMPTY source list, which is the worst possible thing to send a
// firmware that is deciding whether to keep a registered source.
//
// Measured live on an ST10 (2026-08-02) on /streaming/account/<acct>/sources.
// This test covers every route rather than that one path, because the defect is
// a missing keyword that any future case can repeat.
func TestEveryMargeRouteWritesOneDocument(t *testing.T) {
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)), WithDeviceID("94E36DF9CE40"))
	s.SetAccount(&AccountInfo{AccountEmail: "stick@local"})

	routes := []struct {
		method, path string
	}{
		{http.MethodGet, "/streaming/account/stick@local/sources"},
		{http.MethodGet, "/streaming/account/stick@local/sources/"},
		{http.MethodPost, "/streaming/account/stick@local/source"},
		{http.MethodGet, "/streaming/account/stick@local/full"},
		{http.MethodPost, "/streaming/account/stick@local/device/"},
		{http.MethodGet, "/streaming/account/stick@local/device/94E36DF9CE40/group/"},
		{http.MethodGet, "/streaming/sourceproviders"},
		{http.MethodGet, "/streaming/account/stick@local/provider_settings"},
		{http.MethodPost, "/streaming/support/power_on"},
		{http.MethodGet, "/bmx/registry/v1/services"},
		{http.MethodGet, "/bmx/registry/v1/servicesAvailability"},
		{http.MethodPost, "/streaming/account/stick@local/device/94E36DF9CE40/recent"},
	}

	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest(rt.method, rt.path, strings.NewReader("")))

			body := w.Body.String()
			if n := strings.Count(body, "<?xml"); n > 1 {
				t.Fatalf("response carries %d XML declarations, want at most 1 "+
					"(a route case is missing its return and fell into the legacy fallback):\n%s",
					n, body)
			}
			// A JSON route must not have XML appended to it either.
			if strings.HasPrefix(strings.TrimSpace(body), "{") && strings.Contains(body, "<?xml") {
				t.Fatalf("JSON response has XML appended:\n%s", body)
			}
		})
	}
}

// The per-station recents report must be answered with recents.
//
// Its path carries "/device" and it is a POST, so it matched the AddDevice case
// and was answered with an adddeviceresponse and a marge token: the pairing
// state machine's answer, handed to the box every time a user started a
// station. Harmless in practice on the firmware measured (ST30, 2026-08-12),
// but the ordering is a trap for the next person adding a /device route, so it
// is pinned here.
func TestRecentsPostIsAnsweredWithRecents(t *testing.T) {
	s := New(slog.New(slog.NewTextHandler(io.Discard, nil)), WithDeviceID("000C8A96488D"))
	s.SetAccount(&AccountInfo{AccountEmail: "stick@local"})

	const posted = `<?xml version="1.0" encoding="UTF-8" ?><recent>` +
		`<lastplayedat>2026-08-12T04:49:18+00:00</lastplayedat><sourceid>3</sourceid>` +
		`<name>WDR2</name><location>/station?data=abc</location>` +
		`<contentItemType>stationurl</contentItemType></recent>`

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost,
		"/streaming/account/stick@local/device/000C8A96488D/recent", strings.NewReader(posted)))

	body := w.Body.String()
	if strings.Contains(body, "adddeviceresponse") || strings.Contains(body, "margetoken") {
		t.Fatalf("recents POST answered with the AddDevice document:\n%s", body)
	}
	if !strings.Contains(body, "<recents") {
		t.Fatalf("recents POST must be answered with a recents document, got:\n%s", body)
	}
}

func TestInnerText(t *testing.T) {
	const doc = `<recent><name>WDR2</name><contentItemType>stationurl</contentItemType></recent>`
	if got := innerText([]byte(doc), "name"); got != "WDR2" {
		t.Fatalf("name = %q, want WDR2", got)
	}
	if got := innerText([]byte(doc), "contentItemType"); got != "stationurl" {
		t.Fatalf("contentItemType = %q, want stationurl", got)
	}
	// A field the document does not carry (artwork, for one) must come back
	// empty rather than trip the parser.
	if got := innerText([]byte(doc), "containerArt"); got != "" {
		t.Fatalf("missing field = %q, want empty", got)
	}
	if got := innerText([]byte(`<name>unterminated`), "name"); got != "" {
		t.Fatalf("unterminated tag = %q, want empty", got)
	}
}
