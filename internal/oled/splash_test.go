package oled

import (
	"testing"
	"time"
)

// The flip thread alone is not enough: on the ST30 it exists seconds before
// the display controller has finished booting, and a hold in that window
// left the firmware unable to paint the panel until the next reboot.
func TestSplashWaitsForTheDisplayControllerOrAnOldBoseApp(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		flip, ctrl bool
		age        time.Duration
		want       bool
	}{
		{"nothing up", false, false, 0, false},
		{"flip thread only, fresh BoseApp", true, false, 4 * time.Second, false},
		{"controller seen but no flip thread", false, true, 0, false},
		{"flip thread and controller past boot", true, true, 4 * time.Second, true},
		{"flip thread and BoseApp up for minutes", true, false, boseAppSettledAge, true},
		{"flip thread, BoseApp just short of settled", true, false, boseAppSettledAge - time.Second, false},
	}
	for _, c := range cases {
		if got := splashMayStart(c.flip, c.ctrl, c.age); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
