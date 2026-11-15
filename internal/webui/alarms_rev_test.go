package webui

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// alarmViewOf decodes an answer of GET or PUT /api/alarms.
func alarmViewOf(t *testing.T, w interface{ Bytes() []byte }) alarmView {
	t.Helper()
	var v alarmView
	if err := json.Unmarshal(w.Bytes(), &v); err != nil {
		t.Fatalf("decode the alarm answer: %v (%q)", err, w.Bytes())
	}
	return v
}

// GET carries the revision, always, 0 included: a third-party app has to be
// able to rely on the field being there.
func TestAlarmsEndpointAnswersWithARevision(t *testing.T) {
	s := newAlarmServer(t)
	w := alarmRequest(t, s, http.MethodGet, "", "192.168.1.20:5000")
	if !strings.Contains(w.Body.String(), `"rev":0`) {
		t.Fatalf("GET of an empty document = %q, want \"rev\":0", w.Body.String())
	}
	w = alarmRequest(t, s, http.MethodPut, goodAlarmDoc, "192.168.1.20:5000")
	if got := alarmViewOf(t, w.Body).Rev; got != 1 {
		t.Errorf("PUT answered rev %d, want 1", got)
	}
	w = alarmRequest(t, s, http.MethodGet, "", "192.168.1.20:5000")
	if got := alarmViewOf(t, w.Body).Rev; got != 1 {
		t.Errorf("GET after one save answered rev %d, want 1", got)
	}
}

// Discussion #1918: the STR Remote app switched an alarm off, then a phone
// page still showing it on saved its stale copy and switched it back on. With
// the revision it read, the page's save is refused with 409 and the current
// document instead.
func TestAlarmsEndpointRefusesAStaleRevision(t *testing.T) {
	s := newAlarmServer(t)
	alarmRequest(t, s, http.MethodPut, goodAlarmDoc, "192.168.1.20:5000") // rev 1, on

	// The other app switches it off; it sends no rev at all.
	off := strings.Replace(goodAlarmDoc, `"enabled":true`, `"enabled":false`, 1)
	if w := alarmRequest(t, s, http.MethodPut, off, "192.168.1.30:5000"); w.Code != http.StatusOK {
		t.Fatalf("a PUT without rev = %d, want 200 (old clients keep working)", w.Code)
	}

	// The stale page: still rev 1, still "on", saving a holiday.
	stale := `{"rev":1,"zone":"Europe/Berlin","alarms":[{"id":"wake","enabled":true,"hour":6,"minute":30,"days":[1,2,3,4,5],"slot":3,"volume":25}],"skipDates":["2099-12-24"]}`
	w := alarmRequest(t, s, http.MethodPut, stale, "192.168.1.20:5000")
	if w.Code != http.StatusConflict {
		t.Fatalf("a PUT at a stale rev = %d, want 409", w.Code)
	}
	cur := alarmViewOf(t, w.Body)
	if cur.Rev != 2 || len(cur.Alarms) != 1 || cur.Alarms[0].Enabled {
		t.Fatalf("the 409 has to carry the current document, got %+v", cur)
	}
	if got := s.alarms.Get(); got.Alarms[0].Enabled || len(got.SkipDates) != 0 {
		t.Fatalf("the stale save reached the store: %+v", got)
	}

	// The page re-applies its one change to what the 409 handed it.
	merged := `{"rev":2,"zone":"Europe/Berlin","alarms":[{"id":"wake","enabled":false,"hour":6,"minute":30,"days":[1,2,3,4,5],"slot":3,"volume":25}],"skipDates":["2099-12-24"]}`
	w = alarmRequest(t, s, http.MethodPut, merged, "192.168.1.20:5000")
	if w.Code != http.StatusOK {
		t.Fatalf("the retry at the current rev = %d (%q), want 200", w.Code, w.Body.String())
	}
	got := alarmViewOf(t, w.Body)
	if got.Rev != 3 || got.Alarms[0].Enabled || len(got.SkipDates) != 1 {
		t.Errorf("the retry stored %+v, want the alarm still off, the holiday kept, rev 3", got)
	}
}

// rev 0 is a real revision (a document never saved since revisions existed),
// not "no revision": a PUT with rev 0 on a saved document is stale.
func TestAlarmsEndpointRevZoneIsChecked(t *testing.T) {
	s := newAlarmServer(t)
	body := `{"rev":0,` + strings.TrimPrefix(goodAlarmDoc, "{")
	if w := alarmRequest(t, s, http.MethodPut, body, "192.168.1.20:5000"); w.Code != http.StatusOK {
		t.Fatalf("rev 0 on a fresh store = %d, want 200", w.Code)
	}
	if w := alarmRequest(t, s, http.MethodPut, body, "192.168.1.20:5000"); w.Code != http.StatusConflict {
		t.Errorf("rev 0 after a save = %d, want 409", w.Code)
	}
}

// The save line names who saved, so a report like #1918 ("something switched
// my alarm back on") can be traced to the client that did it.
func TestAlarmsEndpointLogsWhoSaved(t *testing.T) {
	s := newAlarmServer(t)
	var buf bytes.Buffer
	s.logger = slog.New(slog.NewTextHandler(&buf, nil))
	req := httptest.NewRequest(http.MethodPut, "/api/alarms", strings.NewReader(goodAlarmDoc))
	req.RemoteAddr = "192.168.1.30:5000"
	req.Header.Set("User-Agent", "STRRemote/1.0")
	s.handleAlarms(httptest.NewRecorder(), req)
	line := buf.String()
	for _, want := range []string{"alarms: saved from an editor", "remote=192.168.1.30:5000", "ua=STRRemote/1.0", "sent_rev=none", "rev=1"} {
		if !strings.Contains(line, want) {
			t.Errorf("the save log %q lacks %q", line, want)
		}
	}
}
