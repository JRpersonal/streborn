package webui

import (
	"strings"
	"testing"
)

// Found in a PUBLIC GitHub attachment on 2026-09-13: the reporter's household
// network name sat in the exported setup_log_prev as
//
//	wlan.conf parsed: SSID='TGM21stCenturyNet' password_length=37
//
// while the structured wlan_configured list in the same file was properly
// redacted. The password was never in there; the name was.
func TestTheNetworkNameNeverLeavesTheSpeakerInALog(t *testing.T) {
	const secret = "TGM21stCenturyNet"
	cases := []string{
		"wlan.conf parsed: SSID='" + secret + "' password_length=37",
		`goform_wlan_push @ 192.0.2.1 ssid='` + secret + `' rc=0 resp='ok'`,
		`level=INFO msg="joined" ssid=` + secret + ` signal=GOOD`,
		`ssid="` + secret + `"`,
		`SSID = '` + secret + `'`,
	}
	for _, in := range cases {
		got := redactNetworkNamesInLog(in)
		if strings.Contains(got, secret) {
			t.Errorf("the name survived: %q -> %q", in, got)
		}
		if !strings.Contains(got, "REDACTED") {
			t.Errorf("nothing was redacted in %q -> %q", in, got)
		}
	}
}

// The length stays, because "did the box get an SSID at all, and was it the
// long one or the short one" is a real question a bundle has to answer.
func TestTheLengthSurvivesSoTheLogStaysUseful(t *testing.T) {
	got := redactNetworkNamesInLog("wlan.conf parsed: SSID='abcde' password_length=37")
	if !strings.Contains(got, "name_length=5") {
		t.Errorf("length missing: %q", got)
	}
	if !strings.Contains(got, "password_length=37") {
		t.Errorf("the password length is not a secret and must survive: %q", got)
	}
}

// Nothing else in a log line may be eaten: these tails are the only record of
// what a speaker did, and an over-eager mask costs every future diagnosis.
func TestNothingElseIsTouched(t *testing.T) {
	keep := []string{
		"",
		"preset reconcile healed slot=5",
		`level=WARN msg="box could not be woken" host=192.0.2.1 elapsed_ms=6000`,
		"wifi failover seed: box online with NO stored Wi-Fi profile (src=stick) - seeding (name_length=12)",
		"ssid=<REDACTED>",
	}
	for _, in := range keep {
		if got := redactNetworkNamesInLog(in); got != in {
			t.Errorf("changed a line it should not touch:\n  in  %q\n  out %q", in, got)
		}
	}
}

// Several names in one tail, which is the real case: a box that was moved
// between networks logs each one.
func TestEveryOccurrenceInATailIsMasked(t *testing.T) {
	in := "a ssid='HomeNet' x\nb SSID=\"Guest\" y\nc ssid=Cafe z"
	got := redactNetworkNamesInLog(in)
	for _, name := range []string{"HomeNet", "Guest", "Cafe"} {
		if strings.Contains(got, name) {
			t.Errorf("%s survived: %q", name, got)
		}
	}
	if n := strings.Count(got, "REDACTED"); n != 3 {
		t.Errorf("masked %d of 3: %q", n, got)
	}
}
