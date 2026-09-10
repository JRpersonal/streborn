package alarm

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func weekdayAlarm() Alarm {
	return Alarm{ID: "a", Enabled: true, Hour: 6, Minute: 30, Days: []int{1, 2, 3, 4, 5}, Slot: 3}
}

func TestValidateAccepts(t *testing.T) {
	d := Document{Zone: "Europe/Berlin", Alarms: []Alarm{weekdayAlarm()}}
	if err := d.Validate(); err != nil {
		t.Fatalf("a plain weekday alarm must validate: %v", err)
	}
	if d.Zone != "Europe/Berlin" {
		t.Errorf("zone changed: %q", d.Zone)
	}
}

func TestValidateEmptyZoneIsUTC(t *testing.T) {
	d := Document{Zone: "  ", Alarms: []Alarm{weekdayAlarm()}}
	if err := d.Validate(); err != nil {
		t.Fatalf("an empty zone is allowed: %v", err)
	}
	loc, err := d.Location()
	if err != nil {
		t.Fatalf("location: %v", err)
	}
	if loc != time.UTC {
		t.Errorf("an empty zone has to mean UTC, got %v", loc)
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name string
		doc  Document
		want string
	}{
		{"hour too big", docWith(func(a *Alarm) { a.Hour = 24 }), "hour outside"},
		{"negative hour", docWith(func(a *Alarm) { a.Hour = -1 }), "hour outside"},
		{"minute too big", docWith(func(a *Alarm) { a.Minute = 60 }), "minute outside"},
		{"slot zero", docWith(func(a *Alarm) { a.Slot = 0 }), "preset 1 to 6"},
		{"slot seven", docWith(func(a *Alarm) { a.Slot = 7 }), "preset 1 to 6"},
		{"volume over 100", docWith(func(a *Alarm) { a.Volume = 101 }), "between 0 and 100"},
		{"negative switch-off", docWith(func(a *Alarm) { a.AutoOff = -1 }), "switch-off time"},
		{"switch-off over the cap", docWith(func(a *Alarm) { a.AutoOff = MaxAutoOffMinutes + 1 }), "switch-off time"},
		{"no days", docWith(func(a *Alarm) { a.Days = nil }), "at least one day"},
		{"empty day slice", docWith(func(a *Alarm) { a.Days = []int{} }), "at least one day"},
		{"day out of range", docWith(func(a *Alarm) { a.Days = []int{7} }), "outside Sunday"},
		{"duplicate day", docWith(func(a *Alarm) { a.Days = []int{1, 1} }), "same day twice"},
		{"name too long", docWith(func(a *Alarm) { a.Name = strings.Repeat("x", MaxNameLen+1) }), "too long"},
		{"unknown zone", Document{Zone: "Mars/Olympus", Alarms: []Alarm{weekdayAlarm()}}, "unknown time zone"},
		{"zone too long", Document{Zone: strings.Repeat("x", MaxZoneLen+1)}, "too long"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := tc.doc
			err := doc.Validate()
			if err == nil {
				t.Fatalf("expected a rejection")
			}
			// The message reaches the user verbatim, so it is part of the
			// contract and worth asserting on.
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message %q does not mention %q", err.Error(), tc.want)
			}
		})
	}
}

func TestValidateTooManyAlarms(t *testing.T) {
	d := Document{}
	for i := 0; i < MaxAlarms+1; i++ {
		a := weekdayAlarm()
		a.ID = ""
		a.Minute = i
		d.Alarms = append(d.Alarms, a)
	}
	if err := d.Validate(); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("expected an at-most rejection, got %v", err)
	}
}

func TestValidateAssignsIDsAndSortsDays(t *testing.T) {
	d := Document{Alarms: []Alarm{{Enabled: true, Hour: 7, Minute: 5, Days: []int{5, 1, 3}, Slot: 1}}}
	if err := d.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if d.Alarms[0].ID == "" {
		t.Error("an alarm saved without an id has to be given one")
	}
	got := d.Alarms[0].Days
	if len(got) != 3 || got[0] != 1 || got[1] != 3 || got[2] != 5 {
		t.Errorf("days were not sorted: %v", got)
	}
}

func TestValidateRejectsDuplicateIDs(t *testing.T) {
	a, b := weekdayAlarm(), weekdayAlarm()
	d := Document{Alarms: []Alarm{a, b}}
	if err := d.Validate(); err == nil || !strings.Contains(err.Error(), "share the id") {
		t.Fatalf("expected a duplicate-id rejection, got %v", err)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alarms.json")
	s, err := Load(path, quietLogger())
	if err != nil {
		t.Fatalf("load a missing file: %v", err)
	}
	if len(s.Get().Alarms) != 0 {
		t.Fatal("a missing file has to load as an empty document")
	}
	want := Document{Zone: "Europe/Berlin", Alarms: []Alarm{weekdayAlarm()}}
	if err := s.Set(want); err != nil {
		t.Fatalf("set: %v", err)
	}
	again, err := Load(path, quietLogger())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := again.Get()
	if got.Zone != want.Zone || len(got.Alarms) != 1 || got.Alarms[0].Slot != 3 {
		t.Fatalf("round trip lost the document: %+v", got)
	}
}

func TestStoreSetRejectsInvalid(t *testing.T) {
	s := New()
	err := s.Set(Document{Alarms: []Alarm{{Enabled: true, Hour: 6, Slot: 9, Days: []int{1}}}})
	if err == nil {
		t.Fatal("Set has to validate before it writes")
	}
	if len(s.Get().Alarms) != 0 {
		t.Error("a rejected document must not reach the store")
	}
}

func TestGetIsACopy(t *testing.T) {
	s := New()
	if err := s.Set(Document{Alarms: []Alarm{weekdayAlarm()}}); err != nil {
		t.Fatalf("set: %v", err)
	}
	got := s.Get()
	got.Alarms[0].Slot = 6
	got.Alarms[0].Days[0] = 0
	if s.Get().Alarms[0].Slot != 3 || s.Get().Alarms[0].Days[0] != 1 {
		t.Error("Get has to hand out a copy, not the stored document")
	}
}

// A 0-byte primary is the overnight-standby power-cut case: the backup is what
// stands between that and a speaker that silently stops waking anyone up.
func TestLoadRecoversFromBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alarms.json")
	s, _ := Load(path, quietLogger())
	if err := s.Set(Document{Zone: "Europe/Berlin", Alarms: []Alarm{weekdayAlarm()}}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	again, err := Load(path, quietLogger())
	if err != nil {
		t.Fatalf("load after truncation: %v", err)
	}
	if len(again.Get().Alarms) != 1 {
		t.Fatal("a 0-byte primary has to be recovered from the backup")
	}
	// The primary is rewritten so the box is whole again.
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		t.Errorf("the primary was not restored: %d bytes, err %v", len(b), err)
	}
}

func TestLoadCorruptWithoutBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alarms.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	s, err := Load(path, quietLogger())
	if err == nil {
		t.Fatal("a corrupt file with no backup has to report the error")
	}
	if s == nil || len(s.Get().Alarms) != 0 {
		t.Error("a corrupt file still has to yield a usable empty store")
	}
}

// A stored document that no longer validates is kept, not dropped: one bad
// entry must not take the working alarms down with it.
func TestLoadKeepsDefectiveDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alarms.json")
	raw, err := json.Marshal(Document{Alarms: []Alarm{{ID: "x", Enabled: true, Hour: 6, Slot: 99, Days: []int{1}}}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	s, err := Load(path, quietLogger())
	if err != nil {
		t.Fatalf("a defective but parseable document is not a load error: %v", err)
	}
	if len(s.Get().Alarms) != 1 {
		t.Error("the defective document has to be kept for the next save to fix")
	}
}

func docWith(mut func(*Alarm)) Document {
	a := weekdayAlarm()
	mut(&a)
	return Document{Alarms: []Alarm{a}}
}

// Zero means never and the cap is the sleep timer's own bound, which is what an
// alarm's switch-off arms.
func TestValidateAcceptsTheSwitchOffRange(t *testing.T) {
	for _, m := range []int{0, 1, 60, MaxAutoOffMinutes} {
		d := docWith(func(a *Alarm) { a.AutoOff = m })
		if err := d.Validate(); err != nil {
			t.Errorf("a switch-off of %d minutes was rejected: %v", m, err)
		}
	}
}

// The JSON tag has to survive a save and reload, or a switch-off set on the
// phone would quietly not be there in the morning.
func TestStoreRoundTripsTheSwitchOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alarms.json")
	s, err := Load(path, quietLogger())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := weekdayAlarm()
	want.AutoOff = 90
	if err := s.Set(Document{Zone: "Europe/Berlin", Alarms: []Alarm{want}}); err != nil {
		t.Fatalf("set: %v", err)
	}
	again, err := Load(path, quietLogger())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := again.Get().Alarms[0].AutoOff; got != 90 {
		t.Errorf("switch-off came back as %d, want 90", got)
	}
}
