// The speaker's answer to a request STR sent, and the shape bucket it used to
// steal.
//
// Both of these came out of the same hardware run on 2026-09-10, the first time
// STR ever wrote anything on this socket. The write worked; the log showed two
// things that did not. The firmware's acknowledgement was recorded as an
// unrecognized frame, and it was recorded under the shape "?xml", which is the
// bucket every frame carrying an XML declaration shares. That capture exists to
// log the first full body of each NEW shape the firmware sends, so STR's own
// acknowledgements would have sat in that slot for good.

package boxws

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// Captured verbatim from a SoundTouch 10 answering a balance write (deviceID
// replaced with a placeholder).
const balanceResponseFrame = `<?xml version="1.0" encoding="UTF-8" ?>` +
	`<msg><header deviceID="AABBCCDDEEFF" url="balance" method="POST">` +
	`<request requestID="1" msgType="RESPONSE"><info mainNode="balanceSet" type="new" /></request>` +
	`</header><body><balance deviceID="AABBCCDDEEFF"><balanceAvailable>true</balanceAvailable>` +
	`<balanceMin>-7</balanceMin><balanceMax>7</balanceMax><balanceDefault>0</balanceDefault>` +
	`<targetBalance>-3</targetBalance><actualBalance>-3</actualBalance></balance></body></msg>`

func TestTheSpeakersAnswerIsRecognisedAsOurOwn(t *testing.T) {
	r, ok := parseGabboResponse([]byte(balanceResponseFrame))
	if !ok {
		t.Fatal("the speaker's answer to our own write was not recognised")
	}
	if r.Header.URL != "balance" || r.Header.Method != "POST" {
		t.Fatalf("header read wrong: url=%q method=%q", r.Header.URL, r.Header.Method)
	}
	// The requestID comes back. Nothing correlates on it yet, and this is the
	// evidence that a request/response helper could.
	if r.Header.Request.ID != "1" {
		t.Fatalf("requestID = %q, want the number we sent", r.Header.Request.ID)
	}
}

// A notification is not an answer. Every frame the speaker sends unprompted must
// keep going through the normal dispatch.
func TestANotificationIsNotMistakenForAnAnswer(t *testing.T) {
	for _, frame := range []string{
		`<updates deviceID="AABBCCDDEEFF"><balanceUpdated /></updates>`,
		`<?xml version="1.0" encoding="UTF-8" ?><updates deviceID="AABBCCDDEEFF"><volumeUpdated /></updates>`,
		`<userActivityUpdate deviceID="AABBCCDDEEFF" />`,
		// A <msg> that is a REQUEST, not a response. The firmware does send
		// these; they must not be swallowed as our own echo.
		`<msg><header deviceID="AABBCCDDEEFF" url="balance" method="GET">` +
			`<request requestID="4"><info type="new"/></request></header></msg>`,
	} {
		if _, ok := parseGabboResponse([]byte(frame)); ok {
			t.Fatalf("treated as our own answer: %s", frame)
		}
	}
}

// The answer must not reach the unrecognized-frame capture, and it must not be
// logged at Info: it arrives once per write, and the write is already logged.
func TestTheAnswerDoesNotLandInTheUnrecognizedCapture(t *testing.T) {
	var buf bytes.Buffer
	c := New(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})),
		"ws://127.0.0.1:8080/", &recHandler{})
	c.handleMessage(t.Context(), []byte(balanceResponseFrame))

	out := buf.String()
	if strings.Contains(out, "unrecognized frame") {
		t.Fatalf("the speaker's own answer was logged as an unknown shape:\n%s", out)
	}
	if out != "" {
		t.Fatalf("the answer logged at Info; once per write is NAND churn:\n%s", out)
	}
}

// The shape bucket. Two different frames that both carry a declaration must not
// collapse into one, or the first of them silences the full-body log for the
// other forever.
func TestFrameShapeLooksPastTheXMLDeclaration(t *testing.T) {
	cases := []struct{ frame, want string }{
		{`<?xml version="1.0" encoding="UTF-8" ?><msg><header/></msg>`, "msg"},
		{`<?xml version="1.0" encoding="UTF-8" ?><userInactivityUpdate />`, "userInactivityUpdate"},
		{`<?xml version="1.0" ?><updates><volumeUpdated /></updates>`, "updates/volumeUpdated"},
		// No declaration: unchanged behaviour.
		{`<userActivityUpdate />`, "userActivityUpdate"},
		{`<updates><balanceUpdated /></updates>`, "updates/balanceUpdated"},
	}
	for _, c := range cases {
		if got := frameShape([]byte(c.frame)); got != c.want {
			t.Fatalf("frameShape(%s) = %q, want %q", c.frame, got, c.want)
		}
	}
	// The point of the fix, stated as the property it protects.
	a := frameShape([]byte(`<?xml version="1.0" ?><msg><header/></msg>`))
	b := frameShape([]byte(`<?xml version="1.0" ?><somethingNew />`))
	if a == b {
		t.Fatalf("two different declaration-prefixed frames share the bucket %q", a)
	}
}
