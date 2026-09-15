package main

import "testing"

// Christoph's SoundTouch 20, 2026-09-14. Two evenings with a dark speaker, and
// the app's own failure report told him ST Reborn had reached the box and run
// the install. It never connected: every attempt that evening ended in
// "install_str: ssh handshake failed after retries". He read the report and
// wrote "also ich glaube jetzt hab ich sie gekillt".
//
// The markers were prefixes, so the failure lines matched them.
func TestAFailedHandshakeIsNotProofTheAppReachedTheSpeaker(t *testing.T) {
	failures := []string{
		"install_str: ssh handshake failed after retries",
		"install_str: ssh probe failed after retries",
		"repair_ssh: ssh handshake failed",
		"repair_ssh: staging failed on all targets",
	}
	for _, line := range failures {
		r := failureReport{History: "install: started\n" + line + "\ninstall: FAILED"}
		if reachedThisSession(r) {
			t.Errorf("%q was read as proof the app got into the speaker", line)
		}
		// Same line arriving through the other input must not pass either.
		r2 := failureReport{}
		r2.Facts.LogTail = line
		if reachedThisSession(r2) {
			t.Errorf("%q in the log tail was read as a successful reach", line)
		}
	}
}

// The genuine markers must keep working, or a speaker the app really did get
// into goes back to being blamed on the user's firewall.
func TestARealReachStillSwitchesTheAdvice(t *testing.T) {
	reaches := []string{
		"install_str: ssh ok",
		"install_str: SSH-staged install ran but the agent did not come up in time",
		"repair_ssh: staged install files",
		"repair_ssh: install ran",
	}
	for _, line := range reaches {
		r := failureReport{History: line}
		if !reachedThisSession(r) {
			t.Errorf("%q should count as having reached the speaker", line)
		}
	}
	if reachedThisSession(failureReport{}) {
		t.Error("an empty report claims the app reached the speaker")
	}
}
