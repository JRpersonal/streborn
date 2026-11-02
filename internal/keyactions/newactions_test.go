package keyactions

import (
	"strings"
	"testing"
)

// The fixed sleep lengths (#1242) and shuffle and repeat (#1679) go on the
// thumbs keys only, like the stepping sleep timer: AUX would also switch the
// input away from what they act on.
func TestValidateNewActionsOnThumbsOnly(t *testing.T) {
	for _, a := range []string{ActionSleep15, ActionSleep30, ActionSleep60, ActionShuffle, ActionRepeat} {
		d := Document{Keys: map[string]string{"thumbsDown": strings.ToUpper(a)}}
		if err := d.Validate(nil); err != nil {
			t.Fatalf("%s on a thumbs key refused: %v", a, err)
		}
		if d.Keys["thumbsDown"] != a {
			t.Errorf("%s not normalised: %q", a, d.Keys["thumbsDown"])
		}
		aux := Document{Keys: map[string]string{"aux": a}}
		if err := aux.Validate(nil); err == nil {
			t.Errorf("%s on AUX accepted, want refused", a)
		}
	}
}

func TestSleepMinutesAndPlayMode(t *testing.T) {
	for a, want := range map[string]int{ActionSleep15: 15, ActionSleep30: 30, ActionSleep60: 60, ActionSleep: 0, ActionShuffle: 0} {
		if got := SleepMinutes(a); got != want {
			t.Errorf("SleepMinutes(%q) = %d, want %d", a, got, want)
		}
	}
	if !IsPlayMode(ActionShuffle) || !IsPlayMode(ActionRepeat) || IsPlayMode(ActionSleep) {
		t.Error("IsPlayMode misreads an action")
	}
}
