package webui

import (
	"strings"
	"testing"
)

// A soundbar's inputs cannot be hardcoded: a CineMate calls its analogue input
// LOCAL where a SoundTouch 10 says AUX, an SA-5 answers AUX with sourceAccount
// AUX1..AUX3, and a soundbar's sockets arrive as PRODUCT with the socket name
// in sourceAccount. The phone therefore builds its buttons from the box's own
// list, and this guards the shape of that.
func TestPhoneRemoteBuildsInputsFromTheBoxsOwnList(t *testing.T) {
	if strings.Contains(indexHTML, `if (bt) bt.style.display = have.BLUETOOTH`) {
		t.Error("the phone still only shows the two hardcoded inputs; a soundbar's TV/HDMI sockets can never appear")
	}
	for _, want := range []string{
		"function renderInputs(",
		"function isPhysicalInput(",
		"UserName$/.test",
		"function inputLabel(",
		"NOT_AN_INPUT",
		"x.isLocal",
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("phone input rendering is missing %q", want)
		}
	}
	// The label must come from the speaker, not from a table of ours.
	if !strings.Contains(indexHTML, "x.displayName") {
		t.Error("the input button ignores the name the speaker gives the socket")
	}
	// STR's own sources must never be offered as physical inputs.
	for _, ours := range []string{"UPNP", "LOCAL_INTERNET_RADIO", "STORED_MUSIC"} {
		if !strings.Contains(indexHTML, ours+":1") {
			t.Errorf("%s is not excluded from the input buttons", ours)
		}
	}
	// The account has to travel with the request or a soundbar's sockets are
	// indistinguishable from one another.
	if !strings.Contains(indexHTML, "sourceAccount: account") {
		t.Error("setSource does not send the sourceAccount, so PRODUCT sockets cannot be told apart")
	}
}

// contentItemForSource takes a source name from the client and puts it inside
// an XML attribute. Anything not reported by the speaker itself must be
// refused, both because it cannot work and because echoing an arbitrary string
// there would let a caller write the ContentItem.
func TestSourceSelectionIsBoundedByWhatTheBoxReports(t *testing.T) {
	if strings.Contains(xmlAttr(`x" evil="1`), `evil="1`) {
		t.Error("xmlAttr does not neutralise a quote, so a source name can break out of the attribute")
	}
	for _, in := range []string{`a"b`, "a<b", "a&b", "a'b"} {
		got := xmlAttr(in)
		if strings.ContainsAny(got, `"<>`) {
			t.Errorf("xmlAttr(%q) = %q still carries a character that changes the markup", in, got)
		}
	}
}

// The fallback matters: a speaker slow to answer /sources must keep the inputs
// it has always had rather than losing them to a timeout. contentItemForSource
// is exercised here through the fallback branch only (no box to reach), which
// is exactly the case that must not regress.
func TestSourceFallbackKeepsTheClassicInputs(t *testing.T) {
	s := &Server{boxHost: "127.0.0.1:1"} // nothing listening: forces the fallback
	for _, tc := range []struct {
		src  string
		want string
	}{
		{"AUX", `source="AUX" sourceAccount="AUX"`},
		{"BLUETOOTH", `source="BLUETOOTH"`},
		{"LOCAL", `<ContentItem source="LOCAL"></ContentItem>`},
	} {
		body, ok := s.contentItemForSource(t.Context(), tc.src, "")
		if !ok {
			t.Errorf("%s was refused when the box could not be asked", tc.src)
			continue
		}
		if !strings.Contains(body, tc.want) {
			t.Errorf("%s built %q, want it to contain %q", tc.src, body, tc.want)
		}
	}
	if _, ok := s.contentItemForSource(t.Context(), "SOMETHING_INVENTED", ""); ok {
		t.Error("an unknown source was accepted even though the box could not confirm it")
	}
}

// The filter that decides what becomes a button, checked against source lists
// captured from three real speakers on 2026-08-09. Both rules here exist
// because the obvious ones were wrong on hardware.
func TestOnlyPhysicalSocketsBecomeInputButtons(t *testing.T) {
	// Verbatim from the boxes: a Portable, a SoundTouch 10 and a SoundTouch 30.
	cases := []struct {
		source, account, status string
		local, want             bool
		why                     string
	}{
		{"AUX", "AUX", "READY", true, true, "the analogue socket, on all three boxes"},
		{"BLUETOOTH", "", "UNAVAILABLE", true, true,
			"a real input on the ST10 that reports UNAVAILABLE because nothing is paired: status must not filter"},
		{"QPLAY", "QPlay1UserName", "UNAVAILABLE", true, false,
			"reports isLocal=true on every box but is a network protocol, not a socket"},
		{"QPLAY", "QPlay2UserName", "UNAVAILABLE", true, false, "the second QPlay slot"},
		{"UPNP", "UPnPUserName", "UNAVAILABLE", false, false, "STR's own playback source"},
		{"SPOTIFY", "SpotifyConnectUserName", "UNAVAILABLE", false, false, "a service"},
		{"STORED_MUSIC_MEDIA_RENDERER", "StoredMusicUserName", "UNAVAILABLE", false, false, "a service"},
		{"LOCAL_INTERNET_RADIO", "", "READY", false, false, "STR's own radio source"},
		{"AIRPLAY", "", "READY", false, false, "a service"},
		{"ALEXA", "", "READY", false, false, "a service"},
		{"NOTIFICATION", "", "UNAVAILABLE", false, false, "firmware bookkeeping"},

		// What a soundbar is expected to report. Not captured from hardware, so
		// these encode the intent rather than a measurement.
		{"PRODUCT", "TV", "UNAVAILABLE", true, true, "a soundbar socket with the television off"},
		{"PRODUCT", "BD-DVD", "READY", true, true, "a soundbar socket"},
		{"LOCAL", "", "READY", true, true, "what a CineMate calls its analogue input"},
	}
	for _, c := range cases {
		got := phoneKeepsAsInput(c.source, c.account, c.local)
		if got != c.want {
			t.Errorf("%s/%s: kept=%v, want %v (%s)", c.source, c.account, got, c.want, c.why)
		}
	}
}

// phoneKeepsAsInput mirrors isPhysicalInput in the phone page. Kept in step by
// TestPhoneRemoteBuildsInputsFromTheBoxsOwnList, which fails if the page stops
// using the same two rules.
func phoneKeepsAsInput(source, account string, isLocal bool) bool {
	notAnInput := map[string]bool{
		"UPNP": true, "LOCAL_INTERNET_RADIO": true, "STORED_MUSIC": true,
		"STORED_MUSIC_MEDIA_SERVER": true, "STORED_MUSIC_MEDIA_RENDERER": true,
		"STANDBY": true, "INVALID_SOURCE": true, "NOTIFICATION": true,
		"UPDATE": true, "SETUP": true,
	}
	up := strings.ToUpper(source)
	if up == "" || !isLocal || notAnInput[up] {
		return false
	}
	return !strings.HasSuffix(account, "UserName")
}
