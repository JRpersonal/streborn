package webui

import "testing"

// A Wave owner reported that switching from an internet station to FM jumped
// straight back to the station, and that pressing FM a second time stuck. His
// diagnostic (2026-08-23) shows STR doing it:
//
//	17:59:13.454  source changed  LOCAL_INTERNET_RADIO -> LOCAL
//	17:59:15.539  re-push: box dropped the stream while idle, resuming
//	17:59:15.674  source changed  LOCAL -> UPNP
//
// The busy check could not catch it, because the Wave's own tuner reports no
// PLAY_STATE: it is not a transport the firmware exposes. So the source has to
// be consulted as well.

func TestResumeSafeSourceLeavesAUserChosenSourceAlone(t *testing.T) {
	// Every one of these is the user having picked something on the speaker
	// itself. Pushing a stream over it is taking the speaker off them.
	for _, src := range []string{
		"LOCAL",     // the Wave's FM tuner, and a CineMate's TV input
		"AUX",       // a cable somebody plugged in
		"BLUETOOTH", // a phone that just connected
		"SPOTIFY",   // Spotify Connect, driven from their phone
		"PRODUCT",   // a soundbar's HDMI source
		"ALEXA",
		"STORED_MUSIC",
		"QPLAY",
		"SOMETHING_A_FUTURE_MODEL_CALLS_ITS_TUNER",
	} {
		if resumeSafeSource(src) {
			t.Errorf("%s is the user's choice, a stream must never be pushed over it", src)
		}
	}
}

func TestResumeSafeSourceStillRecoversSTRsOwnSources(t *testing.T) {
	// These are the states a genuine drop leaves behind, and the recovery
	// exists for exactly them.
	for _, src := range []string{"UPNP", "INVALID_SOURCE", "STANDBY", "LOCAL_INTERNET_RADIO"} {
		if !resumeSafeSource(src) {
			t.Errorf("%s must stay recoverable", src)
		}
	}
}

func TestResumeSafeSourceTreatsAFailedProbeAsRecoverable(t *testing.T) {
	// boxSourceNow returns "" on any error. Reading that as "leave it alone"
	// would turn one unanswered request into a silently disabled recovery,
	// which is worse than the fault being fixed here: the user would simply
	// never get their station back and nothing would say why.
	if !resumeSafeSource("") {
		t.Error("an unreadable source must not suppress the recovery")
	}
}
