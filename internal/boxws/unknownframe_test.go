package boxws

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestFrameShape(t *testing.T) {
	cases := []struct{ in, want string }{
		{`<userInactivityUpdate deviceID="AA" />`, "userInactivityUpdate"},
		{`<updates deviceID="AA"><sourcesUpdated /></updates>`, "updates/sourcesUpdated"},
		{`<updates deviceID='AA'><balanceUpdated></balanceUpdated></updates>`, "updates/balanceUpdated"},
		{`<SoundTouchSdkInfo serverVersion="4" />`, "SoundTouchSdkInfo"},
		{`<swUpdateStatusUpdated/>`, "swUpdateStatusUpdated"},
	}
	for _, tc := range cases {
		if got := frameShape([]byte(tc.in)); got != tc.want {
			t.Errorf("frameShape(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Two frames of the same shape carrying different device ids or values must
// group together, or the dedupe achieves nothing on exactly the frames that
// repeat forever.
func TestFrameShapeIgnoresPayload(t *testing.T) {
	a := frameShape([]byte(`<userInactivityUpdate deviceID="08DF1F0C9870" />`))
	b := frameShape([]byte(`<userInactivityUpdate deviceID="FFEEDDCCBBAA" />`))
	if a != b {
		t.Errorf("same shape with different ids grouped apart: %q vs %q", a, b)
	}
}

// The forensic value is learning a shape EXISTS. The hundredth copy of it costs
// NAND in the only log that survives a reboot, and buys nothing.
func TestUnrecognizedFrameLogsTheFirstOfEachShapeOnly(t *testing.T) {
	var buf bytes.Buffer
	c := &Client{logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}

	inactivity := []byte(`<userInactivityUpdate deviceID="08DF1F0C9870" />`)
	for i := 0; i < 40; i++ {
		c.logUnrecognizedFrame(inactivity)
	}
	c.logUnrecognizedFrame([]byte(`<updates deviceID="AA"><sourcesUpdated /></updates>`))
	for i := 0; i < 10; i++ {
		c.logUnrecognizedFrame([]byte(`<updates deviceID="BB"><sourcesUpdated /></updates>`))
	}

	out := buf.String()
	if n := strings.Count(out, "first of this shape"); n != 2 {
		t.Errorf("logged %d first-of-shape lines for 2 shapes, want 2", n)
	}
	// 51 frames arrived; at INFO only the two introductions may appear.
	if n := strings.Count(out, "box ws unrecognized frame"); n != 2 {
		t.Errorf("%d INFO lines for 51 frames, want 2\n%s", n, out)
	}
	for _, want := range []string{"userInactivityUpdate", "updates/sourcesUpdated"} {
		if !strings.Contains(out, want) {
			t.Errorf("shape %q never reached the log", want)
		}
	}
}

// The first frame keeps its full body: that is what makes it possible to map an
// event STR does not handle yet.
func TestUnrecognizedFrameKeepsTheFirstBody(t *testing.T) {
	var buf bytes.Buffer
	c := &Client{logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
	c.logUnrecognizedFrame([]byte(`<presetStoreGesture slot="3" hold="long" />`))
	out := buf.String()
	if !strings.Contains(out, `slot=`) || !strings.Contains(out, "presetStoreGesture") {
		t.Errorf("the first frame lost its body:\n%s", out)
	}
}
