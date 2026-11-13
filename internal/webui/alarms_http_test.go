package webui

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/alarm"
	"github.com/JRpersonal/streborn/internal/presets"
)

func newAlarmServer(t *testing.T) *Server {
	t.Helper()
	return &Server{
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		alarms:      alarm.New(),
		alarmState:  alarm.LoadState("", slog.New(slog.NewTextHandler(io.Discard, nil))),
		alarmReload: make(chan struct{}, 1),
	}
}

func alarmRequest(t *testing.T, s *Server, method, body, remote string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, "/api/alarms", nil)
	} else {
		r = httptest.NewRequest(method, "/api/alarms", strings.NewReader(body))
	}
	r.RemoteAddr = remote
	w := httptest.NewRecorder()
	s.handleAlarms(w, r)
	return w
}

const goodAlarmDoc = `{"zone":"Europe/Berlin","alarms":[{"id":"wake","enabled":true,"hour":6,"minute":30,"days":[1,2,3,4,5],"slot":3,"volume":25}]}`

func TestAlarmsEndpointRoundTrip(t *testing.T) {
	s := newAlarmServer(t)

	w := alarmRequest(t, s, http.MethodPut, goodAlarmDoc, "192.168.1.20:5000")
	if w.Code != http.StatusOK {
		t.Fatalf("PUT = %d, body %q", w.Code, w.Body.String())
	}
	var got alarmView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode the PUT answer: %v", err)
	}
	if got.Zone != "Europe/Berlin" || len(got.Alarms) != 1 || got.Alarms[0].Slot != 3 {
		t.Fatalf("PUT did not answer with the saved document: %+v", got)
	}
	if !got.Status.ZoneResolved {
		t.Error("Europe/Berlin has to resolve")
	}
	// The status has to prove the SPEAKER agrees about when this goes off.
	if got.Status.NextFire == "" || got.Status.NextAlarmID != "wake" {
		t.Errorf("no next fire in the status: %+v", got.Status)
	}

	w = alarmRequest(t, s, http.MethodGet, "", "192.168.1.20:5000")
	if w.Code != http.StatusOK {
		t.Fatalf("GET = %d", w.Code)
	}
	var back alarmView
	if err := json.Unmarshal(w.Body.Bytes(), &back); err != nil {
		t.Fatalf("decode the GET answer: %v", err)
	}
	if len(back.Alarms) != 1 || back.Alarms[0].ID != "wake" {
		t.Errorf("GET lost the document: %+v", back)
	}
}

// The validation message is what the editor shows the user, so it has to come
// back verbatim rather than as a generic 400.
func TestAlarmsEndpointRejectsWithTheMessage(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"no days", `{"alarms":[{"id":"a","enabled":true,"hour":6,"minute":30,"days":[],"slot":3}]}`, "at least one day"},
		{"bad slot", `{"alarms":[{"id":"a","enabled":true,"hour":6,"minute":30,"days":[1],"slot":13}]}`, "preset 1 to 12"},
		{"bad zone", `{"zone":"Mars/Olympus","alarms":[]}`, "unknown time zone"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newAlarmServer(t)
			w := alarmRequest(t, s, http.MethodPut, tc.body, "192.168.1.20:5000")
			if w.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400", w.Code)
			}
			if !strings.Contains(w.Body.String(), tc.want) {
				t.Errorf("body %q does not mention %q", strings.TrimSpace(w.Body.String()), tc.want)
			}
		})
	}
}

func TestAlarmsEndpointRejectsBadJSON(t *testing.T) {
	s := newAlarmServer(t)
	w := alarmRequest(t, s, http.MethodPut, "{not json", "192.168.1.20:5000")
	if w.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", w.Code)
	}
}

// The LAN gate is the only auth the agent has.
func TestAlarmsEndpointIsLANOnly(t *testing.T) {
	s := newAlarmServer(t)
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		w := alarmRequest(t, s, method, goodAlarmDoc, "203.0.113.9:5000")
		if w.Code != http.StatusForbidden {
			t.Errorf("%s from the internet = %d, want 403", method, w.Code)
		}
	}
}

func TestAlarmsEndpointMethodNotAllowed(t *testing.T) {
	s := newAlarmServer(t)
	w := alarmRequest(t, s, http.MethodDelete, "", "192.168.1.20:5000")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d, want 405", w.Code)
	}
	if allow := w.Header().Get("Allow"); allow != "GET, PUT" {
		t.Errorf("Allow = %q, want %q", allow, "GET, PUT")
	}
}

func TestAlarmsEndpointUnconfigured(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	w := alarmRequest(t, s, http.MethodGet, "", "192.168.1.20:5000")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503 when no store is wired", w.Code)
	}
}

// An empty document answers with an empty array, not null: the phone card does
// Array.isArray on it.
func TestAlarmsEndpointEmptyIsAnArray(t *testing.T) {
	s := newAlarmServer(t)
	w := alarmRequest(t, s, http.MethodGet, "", "192.168.1.20:5000")
	if !strings.Contains(w.Body.String(), `"alarms":[]`) {
		t.Errorf("empty document answered %q, want an empty array", w.Body.String())
	}
}

func TestAlarmsSnapshotIsSafeWithoutAStore(t *testing.T) {
	if got := (&Server{}).AlarmsSnapshot(); got != nil {
		t.Errorf("an unwired server has no snapshot, got %v", got)
	}
}

// alarmPresetStore is a preset store holding exactly the given slots.
func alarmPresetStore(t *testing.T, slots ...int) *presets.Store {
	t.Helper()
	ps, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatalf("preset store: %v", err)
	}
	for _, n := range slots {
		if err := ps.SetSlot(presets.Preset{Slot: n, Name: "Radio", StreamURL: "http://example.invalid/s"}); err != nil {
			t.Fatalf("seed slot %d: %v", n, err)
		}
	}
	return ps
}

// An enabled alarm on a key with nothing on it is skipped when it comes due, so
// the save is refused and the message names the key (discussion #1897).
func TestAlarmsEndpointRefusesAnEmptyKey(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"new alarm with an id", `{"zone":"Europe/Berlin","alarms":[{"id":"wake","enabled":true,"hour":6,"minute":30,"days":[1],"slot":2}]}`, "key 2 is empty"},
		{"new alarm without an id", `{"zone":"Europe/Berlin","alarms":[{"enabled":true,"hour":6,"minute":30,"days":[1],"slot":2}]}`, "key 2 is empty"},
		{"second row", `{"zone":"Europe/Berlin","alarms":[{"id":"wake","enabled":true,"hour":6,"minute":30,"days":[1],"slot":8}]}`, "key 2 pressed twice is empty"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newAlarmServer(t)
			s.presets = alarmPresetStore(t, 1, 3)
			w := alarmRequest(t, s, http.MethodPut, tc.body, "192.168.1.20:5000")
			if w.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400", w.Code)
			}
			if !strings.Contains(w.Body.String(), tc.want) {
				t.Errorf("body %q does not mention %q", strings.TrimSpace(w.Body.String()), tc.want)
			}
			if n := len(s.alarms.Get().Alarms); n != 0 {
				t.Errorf("a refused save stored %d alarms", n)
			}
		})
	}
}

// A key that is filled saves as before.
func TestAlarmsEndpointAcceptsAFilledKey(t *testing.T) {
	s := newAlarmServer(t)
	s.presets = alarmPresetStore(t, 3)
	if w := alarmRequest(t, s, http.MethodPut, goodAlarmDoc, "192.168.1.20:5000"); w.Code != http.StatusOK {
		t.Fatalf("PUT = %d, body %q", w.Code, w.Body.String())
	}
}

// A switched-off alarm never rings, so its key may be empty.
func TestAlarmsEndpointAcceptsADisabledAlarmOnAnEmptyKey(t *testing.T) {
	s := newAlarmServer(t)
	s.presets = alarmPresetStore(t)
	body := `{"zone":"Europe/Berlin","alarms":[{"id":"wake","enabled":false,"hour":6,"minute":30,"days":[1],"slot":2}]}`
	if w := alarmRequest(t, s, http.MethodPut, body, "192.168.1.20:5000"); w.Code != http.StatusOK {
		t.Fatalf("PUT = %d, body %q", w.Code, w.Body.String())
	}
}

// An alarm stored while its key was filled must not lock the whole document
// once that key is cleared: the other alarms stay editable. Switching it back
// on or moving it to another empty key is a change, and is checked.
func TestAlarmsEndpointKeepsAStoredAlarmWhoseKeyWasCleared(t *testing.T) {
	s := newAlarmServer(t)
	s.presets = alarmPresetStore(t, 2, 3)
	stored := `{"zone":"Europe/Berlin","alarms":[` +
		`{"id":"old","enabled":true,"hour":6,"minute":30,"days":[1],"slot":2},` +
		`{"id":"off","enabled":false,"hour":7,"minute":0,"days":[1],"slot":2}]}`
	if w := alarmRequest(t, s, http.MethodPut, stored, "192.168.1.20:5000"); w.Code != http.StatusOK {
		t.Fatalf("seed PUT = %d, body %q", w.Code, w.Body.String())
	}
	if err := s.presets.RemoveSlot(2); err != nil {
		t.Fatalf("clear key 2: %v", err)
	}

	// The stored alarm changed only in its time, plus a new one on a filled
	// key: accepted.
	edit := `{"zone":"Europe/Berlin","alarms":[` +
		`{"id":"old","enabled":true,"hour":6,"minute":45,"days":[1],"slot":2},` +
		`{"id":"off","enabled":false,"hour":7,"minute":0,"days":[1],"slot":2},` +
		`{"id":"new","enabled":true,"hour":8,"minute":0,"days":[6],"slot":3}]}`
	if w := alarmRequest(t, s, http.MethodPut, edit, "192.168.1.20:5000"); w.Code != http.StatusOK {
		t.Fatalf("PUT with an unchanged alarm on a cleared key = %d, body %q", w.Code, w.Body.String())
	}

	// Switching the disabled one on is a change: refused.
	on := strings.Replace(edit, `"id":"off","enabled":false`, `"id":"off","enabled":true`, 1)
	if w := alarmRequest(t, s, http.MethodPut, on, "192.168.1.20:5000"); w.Code != http.StatusBadRequest {
		t.Errorf("switching an alarm on onto an empty key = %d, want 400", w.Code)
	}

	// Moving the stored one to another empty key is a change: refused.
	moved := strings.Replace(edit, `"minute":45,"days":[1],"slot":2`, `"minute":45,"days":[1],"slot":5`, 1)
	if w := alarmRequest(t, s, http.MethodPut, moved, "192.168.1.20:5000"); w.Code != http.StatusBadRequest {
		t.Errorf("moving an alarm onto another empty key = %d, want 400", w.Code)
	}
}
