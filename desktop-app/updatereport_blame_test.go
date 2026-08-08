package main

import (
	"errors"
	"strings"
	"testing"
)

// The journal as it reached us on 2026-08-07: every attempt over two days ends
// in a bare 400, so the speaker demonstrably answered every single time.
const fieldJournal = `time=2026-08-07T10:37:17+02:00 host=192.0.2.14 start: port=8888 bytes=13238434 app=v0.9.35
time=2026-08-07T10:37:29+02:00 host=192.0.2.14 sidecar: delivered but could not verify (version read failed): status 400
time=2026-08-07T10:39:29+02:00 host=192.0.2.14 preflight: speaker did not settle within the window, falling back to SSH: status 400 on 192.0.2.14 - body=""
time=2026-08-07T10:44:41+02:00 host=192.0.2.14 outcome: reported failure: HTTP preflight rejected the listener at :8888`

func TestStripWrongBlame(t *testing.T) {
	// What the closing probe produced in the field report: it timed out, so on
	// its own evidence the firewall advice was the correct choice.
	timedOut := errors.New(`Get "http://192.0.2.14:8888/api/agent/version": context deadline exceeded` + "\n\n" + firewallAdvice)

	t.Run("the journal overrules a probe that merely timed out last", func(t *testing.T) {
		got := stripWrongBlame(timedOut, "HTTP preflight rejected the listener at :8888", fieldJournal)
		if strings.Contains(got.Error(), firewallAdvice) {
			t.Error("still tells the user to hunt through firewall and antivirus settings while the journal shows the speaker answering")
		}
		if !strings.Contains(got.Error(), answeredNotSTRAdvice) {
			t.Error("did not put the right advice in its place")
		}
		if !strings.Contains(got.Error(), "context deadline exceeded") {
			t.Error("dropped the underlying error the maintainer needs")
		}
	})

	t.Run("a speaker that really never answered keeps the firewall advice", func(t *testing.T) {
		quiet := `time=2026-08-07T10:37:17+02:00 host=192.0.2.14 start: port=8888 bytes=13238434
time=2026-08-07T10:39:29+02:00 host=192.0.2.14 outcome: reported failure: dial tcp 192.0.2.14:8888: i/o timeout`
		got := stripWrongBlame(timedOut, "dial tcp: i/o timeout", quiet)
		if !strings.Contains(got.Error(), firewallAdvice) {
			t.Error("removed advice that was correct: nothing on the speaker ever replied")
		}
	})

	t.Run("no journal changes nothing", func(t *testing.T) {
		if got := stripWrongBlame(timedOut, "dial tcp: i/o timeout", ""); !strings.Contains(got.Error(), firewallAdvice) {
			t.Error("rewrote the advice with no evidence to justify it")
		}
	})

	t.Run("a probe that already carries the right advice is left alone", func(t *testing.T) {
		already := errors.New("status 400 on 192.0.2.14\n\n" + answeredNotSTRAdvice)
		got := stripWrongBlame(already, "status 400", fieldJournal)
		if got.Error() != already.Error() {
			t.Errorf("changed an error that was already right:\n%s", got)
		}
	})

	t.Run("nil stays nil", func(t *testing.T) {
		if got := stripWrongBlame(nil, "", fieldJournal); got != nil {
			t.Errorf("stripWrongBlame(nil) = %v, want nil", got)
		}
	})
}
