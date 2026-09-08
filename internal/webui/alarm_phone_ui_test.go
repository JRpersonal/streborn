package webui

import (
	"regexp"
	"strings"
	"testing"
)

// The alarm card is markup plus vanilla JS inside one embedded file, so the
// only cheap guard against losing a piece of it is to assert on the text.
func TestPhoneRemoteHasTheAlarmCard(t *testing.T) {
	for _, want := range []string{
		`id="lblAlarm"`,
		`id="alarmSum"`,
		`id="alarmList"`,
		`id="btnAlarmAdd"`,
		`id="alarmZone"`,
		`id="alarmWarn"`,
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("the alarm card is missing %s", want)
		}
	}
}

// An agent without /api/alarms falls through to handleIndex and answers 200
// with the whole web UI as a STRING. Without this guard the card renders empty
// and a speaker that simply needs an update looks like one with no alarms
// (the #487 experience).
func TestPhoneRemoteAlarmGuardsAnOlderAgent(t *testing.T) {
	if !strings.Contains(indexHTML, "Array.isArray(d.alarms)") {
		t.Error("loadAlarms has to check the answer is really an alarm document")
	}
}

// A save that failed must never look like a save that worked.
func TestPhoneRemoteAlarmReportsAFailedSave(t *testing.T) {
	if !strings.Contains(indexHTML, "T.alarmFail") {
		t.Error("a failed alarm save has to be visible on the card")
	}
}

// The clock warning is the most valuable string on the card: an alarm that
// cannot go off must not look armed.
func TestPhoneRemoteAlarmShowsTheClockWarning(t *testing.T) {
	if !strings.Contains(indexHTML, "T.alarmClockBad") {
		t.Error("the card has to say when the speaker does not know the time")
	}
	if !strings.Contains(indexHTML, "clockTrusted === false") {
		t.Error("the warning has to be driven by the status block")
	}
}

// An empty zone silently means UTC, which is the one failure that looks like it
// worked until an hour too late, so the zone is always on screen.
func TestPhoneRemoteAlarmAlwaysShowsTheZone(t *testing.T) {
	if !strings.Contains(indexHTML, "T.alarmZone") {
		t.Error("the card has to show which zone the times are in")
	}
}

func TestPhoneRemoteAlarmIsLoadedOnOpen(t *testing.T) {
	if !strings.Contains(indexHTML, "loadAlarms()") {
		t.Error("loadAlarms has to run when the page opens")
	}
}

func TestPhoneRemoteAlarmLabelsAreLocalised(t *testing.T) {
	if !strings.Contains(indexHTML, "set('lblAlarm', T.alarm)") {
		t.Error("applyStaticI18n has to set the alarm labels, or the English fallback markup stays on screen")
	}
}

// Every alarm string exists in every locale bundle.
//
// The bundle count is taken from a key in I18N while the alarm keys live in
// I18N2. That works because both dictionaries carry the same 12 locales, which
// is the same assumption TestPhoneRemoteLocalesCarryNoPresets already makes.
func TestPhoneRemoteLocalesCarryTheAlarm(t *testing.T) {
	bundles := strings.Count(indexHTML, "now:\"")
	if bundles == 0 {
		t.Fatal("could not find any locale bundle in indexHTML")
	}
	for _, key := range []string{
		"alarm", "alarmNone", "alarmAdd", "alarmSave", "alarmDelete", "alarmCancel",
		"alarmPreset", "alarmVol", "alarmVolKeep", "alarmZone", "alarmClockBad",
		"alarmFail", "alarmNext", "alarmDow",
	} {
		got := len(regexp.MustCompile(key+`:"`).FindAllString(indexHTML, -1))
		if got != bundles {
			t.Errorf("%s: %d locale bundles but %d keys", key, bundles, got)
		}
	}
}

// alarmDow is one key holding seven names, split on whitespace at render time.
// A bundle that lost one would silently paint "undefined" on a day chip.
func TestPhoneRemoteAlarmDayNamesAreSeven(t *testing.T) {
	for _, m := range regexp.MustCompile(`alarmDow:"([^"]*)"`).FindAllStringSubmatch(indexHTML, -1) {
		if got := len(strings.Fields(m[1])); got != 7 {
			t.Errorf("alarmDow %q has %d names, want 7", m[1], got)
		}
	}
}

// The time and volume live only in the DOM until save. Picking a preset chip
// re-renders the editor from alarmDraft, so they have to be captured first or
// a typed-but-unsaved time snaps back to the default (field report 2026-09-08:
// "the time jumps back to 7:00 whenever you select another preset").
func TestPhoneRemoteAlarmKeepsTheTimeWhenPickingAPreset(t *testing.T) {
	if !strings.Contains(indexHTML, "function captureAlarmEditor()") {
		t.Fatal("the editor needs a capture step for its DOM-only fields")
	}
	// The capture has to run BEFORE the slot is changed and the editor rebuilt.
	handler := "captureAlarmEditor();\n      alarmDraft.slot = Number(b.getAttribute('data-alarm-slot'));\n      renderAlarms();"
	if !strings.Contains(indexHTML, handler) {
		t.Error("the preset chip handler has to capture the time and volume before re-rendering")
	}
	// And save goes through the same capture, so the two paths cannot drift.
	if !strings.Contains(indexHTML, "press(save);\n    captureAlarmEditor();") {
		t.Error("save has to reuse the same capture step")
	}
}

// The time uses the platform's own picker, so a phone gets its native wheel and
// whichever of 12h/24h its locale uses. The element's VALUE is always 24h
// "HH:MM" whatever the display, which is what captureAlarmEditor parses.
func TestPhoneRemoteAlarmUsesTheNativeTimePicker(t *testing.T) {
	if !strings.Contains(indexHTML, `<input type="time" id="alarmTime"`) {
		t.Error("the alarm editor needs the platform time input")
	}
	if !strings.Contains(indexHTML, "String(t.value).split(':')") {
		t.Error("the capture step has to parse the input's 24h HH:MM value")
	}
}

// The week is SHOWN Monday first, the way a European week reads, while the
// stored values stay 0=Sunday so they still line up with Go's time.Weekday.
func TestPhoneRemoteAlarmWeekStartsMonday(t *testing.T) {
	if !strings.Contains(indexHTML, "function alarmDayOrder() { return [1, 2, 3, 4, 5, 6, 0]; }") {
		t.Fatal("the weekday chips have to render Monday first")
	}
	// Both the chips and the summary label follow that order.
	if strings.Count(indexHTML, "alarmDayOrder()") < 3 {
		t.Error("the chips and the summary label both have to use the display order")
	}
}
