// The cloud host the firmware itself names is the only proof that a config
// repair took effect, and the only signal that a box carrying a rival mod's
// leftover address can never reach STR (#986). It is read off the /info body
// this package already fetches every few minutes, so the speaker is asked
// nothing extra.

package autopair

import "testing"

func TestLiveMargeURLIsTakenFromTheInfoBodyAlreadyFetched(t *testing.T) {
	t.Cleanup(func() { noteLiveMargeURL([]byte("<info><margeURL></margeURL></info>")) })

	// The stock answer, measured on all five of the maintainer's speakers
	// (ST30 scm, three ST10 sm2, Portable taigan) on 2026-09-25.
	noteLiveMargeURL([]byte(`<info deviceID="x"><margeURL>https://streaming.bose.com</margeURL></info>`))
	if got := LiveMargeURL(); got != "https://streaming.bose.com" {
		t.Errorf("LiveMargeURL = %q", got)
	}

	// #986's ST30, verbatim.
	noteLiveMargeURL([]byte(`<info><margeAccountUUID>u</margeAccountUUID><margeURL>http://content.api.bose.io:7777</margeURL></info>`))
	if got := LiveMargeURL(); got != "http://content.api.bose.io:7777" {
		t.Errorf("LiveMargeURL = %q", got)
	}

	// A body without the field must not wipe what was already learnt: some
	// firmwares omit it, and "unknown" has to stay distinguishable from
	// "stock" (the caller falls back to the config files on an empty value).
	noteLiveMargeURL([]byte(`<info><name>x</name></info>`))
	if got := LiveMargeURL(); got != "http://content.api.bose.io:7777" {
		t.Errorf("a body without margeURL cleared the snapshot: %q", got)
	}
}

func TestIsPairedStillOnlyJudgesTheAccountUUID(t *testing.T) {
	// The margeURL snapshot rides along on the same read; it must not change
	// what IsPaired concludes.
	if !hasMargeUUID([]byte(`<info><margeAccountUUID>abc</margeAccountUUID><margeURL>http://x</margeURL></info>`)) {
		t.Error("a paired box with an odd cloud host must still read as paired")
	}
	if hasMargeUUID([]byte(`<info><margeAccountUUID></margeAccountUUID><margeURL>https://streaming.bose.com</margeURL></info>`)) {
		t.Error("an unpaired box with the stock host must still read as unpaired")
	}
}
