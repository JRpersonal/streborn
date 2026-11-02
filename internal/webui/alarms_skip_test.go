package webui

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Days without an alarm (discussion #1491).

// skipMondayHarness runs weekdayDoc with Monday 2026-09-07 on the list.
func skipMondayHarness(t *testing.T) *alarmHarness {
	t.Helper()
	d := weekdayDoc()
	d.SkipDates = []string{"2026-09-07"}
	return newAlarmHarness(t, d)
}

func TestAlarmStaysSilentOnASkippedDay(t *testing.T) {
	t.Parallel()
	h := skipMondayHarness(t)
	h.settled(berlinTime(t, 2026, time.September, 7, 6, 30))
	h.settled(berlinTime(t, 2026, time.September, 7, 6, 32))
	if got := h.firedCount(); got != 0 {
		t.Fatalf("the alarm rang %d times on a day on the list", got)
	}
	tuesday := berlinTime(t, 2026, time.September, 8, 6, 30)
	h.at(tuesday)
	h.s.evaluateAlarms(tuesday)
	if !waitFor(func() bool { return h.firedCount() == 1 }) {
		t.Errorf("the day after the skipped one did not ring: %d recalls", h.firedCount())
	}
}

// Taking today off the list at 06:31 is an edit like any other: the save must
// not ring the 06:30 alarm at once, whose time and days did not change.
func TestAlarmUnskippedInsideItsOwnWindowWaitsForTheNextOne(t *testing.T) {
	t.Parallel()
	h := skipMondayHarness(t)
	h.settled(berlinTime(t, 2026, time.September, 7, 6, 0))
	h.save(t, weekdayDoc())
	h.settled(berlinTime(t, 2026, time.September, 7, 6, 31))
	if got := h.firedCount(); got != 0 {
		t.Fatalf("removing today from the list rang the alarm %d times", got)
	}
}

// And adding a different day to the list at 06:31 must not swallow a 06:30
// alarm that has not rung yet (a clock repaired a minute late).
func TestAlarmSkipEditForAnotherDayDoesNotSwallowToday(t *testing.T) {
	t.Parallel()
	h := newAlarmHarness(t, weekdayDoc())
	h.s.alarmSeen, h.s.alarmSeeded = weekdayDoc(), true
	d := weekdayDoc()
	d.SkipDates = []string{"2026-12-24"}
	h.save(t, d)
	now := berlinTime(t, 2026, time.September, 7, 6, 31)
	h.at(now)
	h.s.evaluateAlarms(now)
	if !waitFor(func() bool { return h.firedCount() == 1 }) {
		t.Errorf("a skip date in December swallowed today's alarm: %d recalls", h.firedCount())
	}
}

func TestAlarmsEndpointSkipDates(t *testing.T) {
	s := newAlarmServer(t)
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	s.alarmNow = func() time.Time { return now }

	body := strings.Replace(goodAlarmDoc, `"alarms"`, `"skipDates":["2026-09-08","2026-09-01","2026-09-07"],"alarms"`, 1)
	w := alarmRequest(t, s, http.MethodPut, body, "192.168.1.20:5000")
	if w.Code != http.StatusOK {
		t.Fatalf("PUT = %d, body %q", w.Code, w.Body.String())
	}
	var got alarmView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// The past day is dropped on save, today and tomorrow stay, sorted.
	if want := []string{"2026-09-07", "2026-09-08"}; !reflect.DeepEqual(got.SkipDates, want) {
		t.Errorf("skipDates = %v, want %v", got.SkipDates, want)
	}
	if stored := s.alarms.Get().SkipDates; !reflect.DeepEqual(stored, got.SkipDates) {
		t.Errorf("stored %v, answered %v", stored, got.SkipDates)
	}
	// The next ring the editor shows skips Tuesday the 8th.
	if want := "2026-09-09T06:30:00+02:00"; got.Status.NextFire != want {
		t.Errorf("nextFire = %q, want %q", got.Status.NextFire, want)
	}
	if got.Status.MaxSkipDates != 60 {
		t.Errorf("maxSkipDates = %d", got.Status.MaxSkipDates)
	}

	// An editor that knows nothing of skip dates sends no field: the list stays.
	w = alarmRequest(t, s, http.MethodPut, goodAlarmDoc, "192.168.1.20:5000")
	if w.Code != http.StatusOK {
		t.Fatalf("PUT without the field = %d", w.Code)
	}
	if got := s.alarms.Get().SkipDates; len(got) != 2 {
		t.Errorf("a PUT without skipDates wiped them: %v", got)
	}
	// An empty list clears them.
	body = strings.Replace(goodAlarmDoc, `"alarms"`, `"skipDates":[],"alarms"`, 1)
	if w = alarmRequest(t, s, http.MethodPut, body, "192.168.1.20:5000"); w.Code != http.StatusOK {
		t.Fatalf("PUT with [] = %d", w.Code)
	}
	if got := s.alarms.Get().SkipDates; len(got) != 0 {
		t.Errorf("[] did not clear the list: %v", got)
	}

	// A malformed date reaches the user verbatim.
	body = strings.Replace(goodAlarmDoc, `"alarms"`, `"skipDates":["24.12.2026"],"alarms"`, 1)
	w = alarmRequest(t, s, http.MethodPut, body, "192.168.1.20:5000")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "not a date") {
		t.Errorf("bad date: %d %q", w.Code, w.Body.String())
	}
}

// A day that has passed is not shown, and reading never writes.
func TestAlarmsEndpointHidesPastSkipDates(t *testing.T) {
	s := newAlarmServer(t)
	d := weekdayDoc()
	d.SkipDates = []string{"2026-09-01", "2026-12-24"}
	if err := s.alarms.Set(d); err != nil {
		t.Fatal(err)
	}
	s.alarmNow = func() time.Time { return time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC) }
	w := alarmRequest(t, s, http.MethodGet, "", "192.168.1.20:5000")
	var got alarmView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if want := []string{"2026-12-24"}; !reflect.DeepEqual(got.SkipDates, want) {
		t.Errorf("GET skipDates = %v, want %v", got.SkipDates, want)
	}
	if stored := s.alarms.Get().SkipDates; len(stored) != 2 {
		t.Errorf("a GET changed the stored list: %v", stored)
	}
}
