package alarm

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// offAt is a daily alarm at hour:minute that switches off at the time of day
// off (discussion #1364).
func offAt(hour, minute int, off string) Alarm {
	a := daily(hour, minute)
	a.OffAt = off
	return a
}

func TestSwitchOffAtLaterTheSameDay(t *testing.T) {
	loc := berlin(t)
	due := time.Date(2026, 9, 7, 7, 0, 0, 0, loc)
	got, ok := offAt(7, 0, "20:00").SwitchOff(due, due, loc)
	if !ok {
		t.Fatal("an alarm with a switch-off time has to switch off")
	}
	if want := time.Date(2026, 9, 7, 20, 0, 0, 0, loc); !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A switch-off time earlier than the alarm is the next day: an alarm at 07:00
// that should play until one in the morning.
func TestSwitchOffAtEarlierThanTheAlarmIsTheNextDay(t *testing.T) {
	loc := berlin(t)
	due := time.Date(2026, 9, 7, 7, 0, 0, 0, loc)
	got, ok := offAt(7, 0, "01:00").SwitchOff(due, due, loc)
	if !ok {
		t.Fatal("an alarm with a switch-off time has to switch off")
	}
	if want := time.Date(2026, 9, 8, 1, 0, 0, 0, loc); !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if d := got.Sub(due); d != 18*time.Hour {
		t.Errorf("played for %v, want 18h", d)
	}
}

// A snooze moves a switch-off after some minutes on, but not a switch-off at a
// time of day: "off at 20:00" means 20:00, whenever the latest ring started.
func TestSwitchOffAtIgnoresTheSnooze(t *testing.T) {
	loc := berlin(t)
	due := time.Date(2026, 9, 7, 7, 0, 0, 0, loc)
	rang := due.Add(18 * time.Minute)
	got, _ := offAt(7, 0, "08:00").SwitchOff(due, rang, loc)
	if want := time.Date(2026, 9, 7, 8, 0, 0, 0, loc); !got.Equal(want) {
		t.Errorf("time of day: got %v, want %v", got, want)
	}
	mins := daily(7, 0)
	mins.AutoOff = 60
	got, _ = mins.SwitchOff(due, rang, loc)
	if want := rang.Add(time.Hour); !got.Equal(want) {
		t.Errorf("minutes: got %v, want %v (counted from the latest ring)", got, want)
	}
}

func TestSwitchOffNeverWithoutEither(t *testing.T) {
	loc := berlin(t)
	due := time.Date(2026, 9, 7, 7, 0, 0, 0, loc)
	if _, ok := daily(7, 0).SwitchOff(due, due, loc); ok {
		t.Error("an alarm without a switch-off must never switch off")
	}
}

// Fall back: 2026-10-25 Europe/Berlin repeats 02:00-03:00. An alarm on the
// Saturday evening at 20:00 that switches off at 06:00 reads ten hours on the
// wall clock but runs eleven real ones, and the switch-off still lands on the
// 06:00 the user set.
func TestSwitchOffAtAcrossTheFallBackNight(t *testing.T) {
	loc := berlin(t)
	due := time.Date(2026, 10, 24, 20, 0, 0, 0, loc)
	got, ok := offAt(20, 0, "06:00").SwitchOff(due, due, loc)
	if !ok {
		t.Fatal("expected a switch-off")
	}
	if got.Day() != 25 || got.Hour() != 6 || got.Minute() != 0 {
		t.Errorf("got %v, want 06:00 on the 25th", got)
	}
	if d := got.Sub(due); d != 11*time.Hour {
		t.Errorf("played for %v, want 11h of real time", d)
	}
}

// Spring forward: 2026-03-29 Europe/Berlin jumps 02:00 -> 03:00. A switch-off
// at 06:00 after a 22:00 alarm is eight hours on the wall clock but seven real
// ones, and one inside the missing hour lands after it rather than vanishing.
func TestSwitchOffAtAcrossTheSpringForwardNight(t *testing.T) {
	loc := berlin(t)
	due := time.Date(2026, 3, 28, 22, 0, 0, 0, loc)
	got, _ := offAt(22, 0, "06:00").SwitchOff(due, due, loc)
	if d := got.Sub(due); d != 7*time.Hour {
		t.Errorf("played for %v, want 7h of real time", d)
	}
	gap, ok := offAt(22, 0, "02:30").SwitchOff(due, due, loc)
	if !ok {
		t.Fatal("a switch-off inside the missing hour must not disappear")
	}
	if gap.Day() != 29 || !gap.After(due) {
		t.Errorf("got %v, want a moment on the 29th after the alarm", gap)
	}
	if want := time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC); !gap.Equal(want) {
		t.Errorf("got %v (%v UTC), want %v UTC", gap, gap.UTC(), want)
	}
}

// The time of day wins over the minutes, and the minutes are cleared on save
// so the stored alarm cannot carry two switch-offs.
func TestValidateOffAtClearsTheMinutes(t *testing.T) {
	d := docWith(func(a *Alarm) { a.AutoOff = 60; a.OffAt = " 20:00 " })
	if err := d.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	a := d.Alarms[0]
	if a.OffAt != "20:00" || a.AutoOff != 0 {
		t.Errorf("got offAt %q autoOff %d, want 20:00 and 0", a.OffAt, a.AutoOff)
	}
}

// The setting is part of the document on NAND, so it survives a reboot.
func TestOffAtSurvivesAReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alarms.json")
	s, err := Load(path, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set(docWith(func(a *Alarm) { a.OffAt = "20:00" })); err != nil {
		t.Fatalf("set: %v", err)
	}
	again, err := Load(path, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Get().Alarms[0].OffAt; got != "20:00" {
		t.Errorf("after a reload offAt = %q, want 20:00", got)
	}
	// And an alarm without one does not grow the file by an empty field.
	b, _ := json.Marshal(weekdayAlarm())
	if strings.Contains(string(b), "offAt") {
		t.Errorf("an alarm without a switch-off time writes %s", b)
	}
}
