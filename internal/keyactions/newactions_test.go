package keyactions

import (
	"strings"
	"testing"
)

// Shuffle and repeat (#1679) go on the thumbs keys only, like the sleep
// timer: AUX would also switch the input away from what they act on.
func TestValidateNewActionsOnThumbsOnly(t *testing.T) {
	for _, a := range []string{ActionShuffle, ActionRepeat} {
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

func TestIsPlayMode(t *testing.T) {
	if !IsPlayMode(ActionShuffle) || !IsPlayMode(ActionRepeat) || IsPlayMode(ActionSleep) {
		t.Error("IsPlayMode misreads an action")
	}
}
