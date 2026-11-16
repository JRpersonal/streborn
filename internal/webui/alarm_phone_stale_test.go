package webui

import (
	"regexp"
	"strings"
	"testing"
)

// Discussion #1918: an alarm switched off from another app still showed as on,
// because the page read the alarms only when it opened. Showing the Play tab
// and focusing the window have to re-read them.
func TestPhoneRemoteAlarmRereadsWhenTheUserComesBack(t *testing.T) {
	if !regexp.MustCompile(`if \(name === 'Play'\) alarmReread\(\);`).MatchString(indexHTML) {
		t.Error("showTab has to re-read the alarms when the Play tab is shown")
	}
	if !strings.Contains(indexHTML, `window.addEventListener('focus', function(){ alarmReread(); });`) {
		t.Error("a focused window has to re-read the alarms")
	}
}

// The worse half of #1918: a stale page saved its whole copy and undid the
// other app's change. A save names the revision it read and re-applies its one
// change on a 409; no path may PUT the page's copy without going through that.
func TestPhoneRemoteAlarmSavesWithTheRevision(t *testing.T) {
	for _, want := range []string{
		"body.rev = alarmDoc.rev",
		"r.status === 409 && alarmApply(r.data)",
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("the alarm save lacks %q", want)
		}
	}
	if strings.Contains(indexHTML, "saveAlarms(") {
		t.Error("saveAlarms PUT the whole stale copy; every alarm change has to go through alarmCommit")
	}
	if n := strings.Count(indexHTML, "apiDetail('/api/alarms', 'PUT'"); n != 1 {
		t.Errorf("%d places PUT the alarms, want exactly one (alarmPut)", n)
	}
}
