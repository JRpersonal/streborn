package boxapi

import (
	"context"
	"testing"
)

// The verdict comes from the firmware's own /capabilities answer. The two
// fixtures are the shapes measured on FW 27.0.6: a Portable (display) and an
// ST10 (LEDs only).
func TestHasDisplayReadsClockDisplayCapability(t *testing.T) {
	cases := []struct {
		host, caps string
		want, ok   bool
	}{
		{"display-portable", `<capabilities deviceID="x"><lightswitch>true</lightswitch><clockDisplay>true</clockDisplay></capabilities>`, true, true},
		{"display-st10", `<capabilities deviceID="x"><lightswitch>false</lightswitch><clockDisplay>false</clockDisplay></capabilities>`, false, true},
		{"display-unknown", `<capabilities deviceID="x"><lightswitch>false</lightswitch></capabilities>`, false, false},
	}
	for _, tc := range cases {
		c, rec := newRecordingBox(t, tc.host, map[string]string{"/capabilities": tc.caps})
		host := tc.host
		t.Cleanup(func() { displayHosts.Delete(host) })
		has, ok := c.HasDisplay(context.Background())
		if has != tc.want || ok != tc.ok {
			t.Errorf("%s: HasDisplay = (%v, %v), want (%v, %v)", tc.host, has, ok, tc.want, tc.ok)
		}
		// A definite verdict is cached; asking again must not re-probe.
		c.HasDisplay(context.Background())
		wantProbes := 1
		if !tc.ok {
			wantProbes = 2
		}
		if n := rec.requested("/capabilities"); n != wantProbes {
			t.Errorf("%s: /capabilities probed %d times, want %d", tc.host, n, wantProbes)
		}
	}
}

func TestHasDisplayFailedReadIsNotAVerdict(t *testing.T) {
	c, _ := newRecordingBox(t, "display-404", map[string]string{})
	t.Cleanup(func() { displayHosts.Delete("display-404") })
	if has, ok := c.HasDisplay(context.Background()); has || ok {
		t.Fatalf("a 404 must not produce a verdict, got (%v, %v)", has, ok)
	}
}
