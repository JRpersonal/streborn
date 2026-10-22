package alarm

import (
	"encoding/json"
	"strings"
	"testing"
)

// An alarm saved before snooze existed has no field, and snoozes for the
// default nine minutes (discussion #1415).
func TestOldAlarmSnoozesForNineMinutes(t *testing.T) {
	var d Document
	raw := `{"zone":"Europe/Berlin","alarms":[{"id":"a","enabled":true,"hour":6,"minute":30,"days":[1],"slot":3}]}`
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("an old alarm without the field must still validate: %v", err)
	}
	if got := d.Alarms[0].SnoozeMinutes(); got != 9 {
		t.Errorf("SnoozeMinutes = %d, want 9", got)
	}
	// And it is written back without the field, so the file stays as it was.
	b, _ := json.Marshal(d.Alarms[0])
	if strings.Contains(string(b), "snooze") {
		t.Errorf("an alarm without a snooze length wrote one: %s", b)
	}
}

func TestValidateSnoozeChoices(t *testing.T) {
	for _, n := range []int{0, 5, 9, 10, 15} {
		a := weekdayAlarm()
		a.Snooze = n
		d := Document{Alarms: []Alarm{a}}
		if err := d.Validate(); err != nil {
			t.Errorf("snooze %d refused: %v", n, err)
		}
	}
	for _, n := range []int{-1, 1, 7, 30, 60} {
		a := weekdayAlarm()
		a.Snooze = n
		d := Document{Alarms: []Alarm{a}}
		if err := d.Validate(); err == nil {
			t.Errorf("snooze %d accepted", n)
		}
	}
	a := weekdayAlarm()
	a.Snooze = 15
	if a.SnoozeMinutes() != 15 {
		t.Error("a stored length has to win over the default")
	}
}

// Snooze length does not move an alarm, so a save that only changes it must
// not be read as a reschedule.
func TestSnoozeIsNotPartOfTheSchedule(t *testing.T) {
	a := weekdayAlarm()
	b := a
	b.Snooze = 15
	if !a.SameSchedule(b) {
		t.Error("a new snooze length counted as a moved alarm")
	}
}
