package main

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxlog"
	"github.com/JRpersonal/streborn/internal/webui"
)

// The measured case, from an ST30 on 2026-09-27: the owner pressed key 2 on a
// sleeping speaker, the firmware woke into the wrong state, threw the first
// fetch away, logged "Could not obtain first-frame" and then
// "PlaybackFailure ERROR_NO_DECODED_DATA" two seconds later, and only the
// owner's second press started the music.
func TestTheSecondPressIsTakenOverAfterAFailedFirstOne(t *testing.T) {
	var f firstPressRescue
	press := time.Now()
	f.noteNativeActivation(2, press)

	// The failure arrives 2.1 s later, as it did on the speaker.
	if got := f.claim(press.Add(2100 * time.Millisecond)); got != 2 {
		t.Fatalf("claim = %d, want the pressed key 2", got)
	}
	// ONE rescue per press. The firmware logs both a no-first-frame line and a
	// PlaybackFailure line for the same silence; two presses would start the
	// station twice.
	if got := f.claim(press.Add(2200 * time.Millisecond)); got != 0 {
		t.Fatalf("a second failure from the same press claimed another rescue (%d)", got)
	}
}

func TestAFailureThatIsNotAboutThePressIsLeftAlone(t *testing.T) {
	var f firstPressRescue

	// Nothing was pressed: a failure on its own is not ours to answer.
	if got := f.claim(time.Now()); got != 0 {
		t.Errorf("claim without a press = %d, want 0", got)
	}

	// A failure long after the press is a different story. Pressing the key
	// then would restart music somebody is listening to.
	press := time.Now()
	f.noteNativeActivation(3, press)
	if got := f.claim(press.Add(firstPressWindow + time.Second)); got != 0 {
		t.Errorf("a failure %v after the press claimed a rescue (%d)", firstPressWindow+time.Second, got)
	}

	// A failure logged BEFORE the press belongs to whatever came before it.
	press2 := time.Now()
	f.noteNativeActivation(4, press2)
	if got := f.claim(press2.Add(-2 * time.Second)); got != 0 {
		t.Errorf("a failure before the press claimed a rescue (%d)", got)
	}
}

// A new press re-arms: two bad wakes in a row each get their one retry.
func TestEachPressGetsItsOwnRescue(t *testing.T) {
	var f firstPressRescue
	first := time.Now()
	f.noteNativeActivation(1, first)
	if got := f.claim(first.Add(time.Second)); got != 1 {
		t.Fatalf("first claim = %d, want 1", got)
	}
	second := first.Add(time.Minute)
	f.noteNativeActivation(6, second)
	if got := f.claim(second.Add(time.Second)); got != 6 {
		t.Fatalf("second claim = %d, want 6", got)
	}
}

// The rescue is spent and the box failed again: that is the point where the
// station counts as down (the display message). Exactly once, and never for
// the firmware's own second log line about the FIRST failure.
func TestAFailureAfterTheRescueIsReportedOnce(t *testing.T) {
	var f firstPressRescue
	press := time.Now()
	f.noteNativeActivation(2, press)
	fail1 := press.Add(2100 * time.Millisecond)
	if got := f.claim(fail1); got != 2 {
		t.Fatalf("claim = %d", got)
	}
	// The companion line of the first failure, 100 ms later.
	if _, ok := f.failedAfterRescue(fail1.Add(100 * time.Millisecond)); ok {
		t.Fatal("the first failure's second log line was read as a second failure")
	}
	// The rescue press activates the same slot again; it must not re-arm.
	f.noteNativeActivation(2, fail1.Add(firstPressSettle))
	if got := f.claim(fail1.Add(firstPressSettle + 2*time.Second)); got != 0 {
		t.Fatalf("the rescue's own press bought another rescue (%d)", got)
	}
	slot, ok := f.failedAfterRescue(fail1.Add(firstPressSettle + 2*time.Second))
	if !ok || slot != 2 {
		t.Fatalf("second failure not reported: (%d, %v)", slot, ok)
	}
	if _, ok := f.failedAfterRescue(fail1.Add(firstPressSettle + 3*time.Second)); ok {
		t.Fatal("reported twice")
	}
}

// Without a rescue there is nothing to report: a single failure is the
// rescue's job, not the display's.
func TestNoReportWithoutARescue(t *testing.T) {
	var f firstPressRescue
	press := time.Now()
	f.noteNativeActivation(5, press)
	if _, ok := f.failedAfterRescue(press.Add(3 * time.Second)); ok {
		t.Fatal("reported a failure that the rescue had not handled yet")
	}
}

// The handler turns the second failure into the "station not answering"
// display message (the webui then decides between that and "no internet").
func TestSecondNativeFailureAsksForTheDisplayMessage(t *testing.T) {
	var asked []webui.DisplayMsgKind
	h := &presetWsHandler{
		logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		showDisplayMessage: func(k webui.DisplayMsgKind) { asked = append(asked, k) },
	}
	press := time.Now()
	h.firstPress.noteNativeActivation(3, press)
	fail1 := press.Add(2 * time.Second)
	h.firstPress.claim(fail1) // the rescue took this one
	h.OnPlayFailure(boxlog.PlayFailure{At: fail1.Add(firstPressSettle + time.Second)})
	if len(asked) != 1 || asked[0] != webui.DisplayMsgStationDown {
		t.Fatalf("asked = %v, want one station-unreachable message", asked)
	}
	// Without the hook wired nothing breaks.
	h.showDisplayMessage = nil
	h.displayMessage(webui.DisplayMsgNoInternet)
}

// A press that left Bluetooth or AUX and whose firmware then gave up without
// ever asking for the station (#1170, ST10): the box switched to the radio,
// fetched nothing, fell to INVALID_SOURCE five seconds later and went back
// to Bluetooth. No PlayFailure exists for that, so the source fall-back is
// the trigger.
func TestAPressThatFellBackFromBluetoothIsRescued(t *testing.T) {
	never := time.Time{}
	cases := []struct {
		name string
		// origin is the source the press left; the box switches 45 ms before
		// the agent notes the activation, as measured.
		origin string
		// seenAfter is when, relative to the press, the station was resolved
		// or fetched; negative means not since the press.
		seenAfter time.Duration
		// fallAfter is when the source fell, and fallTo where to.
		fallAfter time.Duration
		fallTo    string
		want      int
	}{
		{"bluetooth falls to INVALID_SOURCE", srcBluetooth, -1, 5140 * time.Millisecond, srcInvalid, 5},
		{"bluetooth falls straight back to bluetooth", srcBluetooth, -1, 5 * time.Second, srcBluetooth, 5},
		{"aux falls back to aux", srcAux, -1, 4 * time.Second, srcAux, 5},
		{"station resolved before the fall-back", srcBluetooth, 200 * time.Millisecond, 5 * time.Second, srcInvalid, 0},
		{"fall-back after the window", srcBluetooth, -1, firstPressWindow + time.Second, srcInvalid, 0},
		{"press from standby", "STANDBY", -1, 5 * time.Second, srcInvalid, 0},
		{"press from another radio source", "UPNP", -1, 5 * time.Second, srcInvalid, 0},
		{"bluetooth press moves on to a normal source", srcBluetooth, -1, 2 * time.Second, "UPNP", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var f firstPressRescue
			press := time.Now()
			f.fallBack(tc.origin, srcRadio, press.Add(-45*time.Millisecond), never)
			f.noteNativeActivation(5, press)
			seen := never
			if tc.seenAfter >= 0 {
				seen = press.Add(tc.seenAfter)
			}
			if got := f.fallBack(srcRadio, tc.fallTo, press.Add(tc.fallAfter), seen); got != tc.want {
				t.Fatalf("fallBack = %d, want %d", got, tc.want)
			}
			if tc.want == 0 {
				return
			}
			if got := f.originOf(5); got != tc.origin {
				t.Errorf("originOf = %q, want %q", got, tc.origin)
			}
			// INVALID_SOURCE -> BLUETOOTH right after is the same failure:
			// once per press, and a PlayFailure cannot buy a second retry.
			if got := f.fallBack(srcInvalid, srcBluetooth, press.Add(tc.fallAfter+200*time.Millisecond), never); got != 0 {
				t.Errorf("the second hop of the same fall-back claimed another rescue (%d)", got)
			}
			if got := f.claim(press.Add(tc.fallAfter + time.Second)); got != 0 {
				t.Errorf("a PlayFailure after the fall-back rescue claimed another (%d)", got)
			}
		})
	}
}

// The switch to the radio may also arrive just AFTER the activation note; the
// origin must be picked up either way.
func TestTheSourceSwitchAfterTheActivationStillCounts(t *testing.T) {
	var f firstPressRescue
	press := time.Now()
	f.noteNativeActivation(2, press)
	f.fallBack(srcBluetooth, srcRadio, press.Add(100*time.Millisecond), time.Time{})
	if got := f.fallBack(srcRadio, srcInvalid, press.Add(5*time.Second), time.Time{}); got != 2 {
		t.Fatalf("fallBack = %d, want 2", got)
	}
}

// An old switch into the radio (minutes before) is not this press's origin:
// a press made while already on the radio has no origin and never triggers.
func TestAStaleSourceSwitchIsNotThePressOrigin(t *testing.T) {
	var f firstPressRescue
	press := time.Now()
	f.fallBack(srcBluetooth, srcRadio, press.Add(-time.Minute), time.Time{})
	f.noteNativeActivation(3, press)
	if got := f.fallBack(srcRadio, srcInvalid, press.Add(5*time.Second), time.Time{}); got != 0 {
		t.Fatalf("a press made on the radio claimed a rescue (%d)", got)
	}
}
