package webui

import (
	"context"
	"testing"
	"time"
)

// A speaker in its own setup AP is not associated, because its single radio is
// in AP mode. The guard used to read that as a broken chip and power-cycle the
// SDIO device, pulling the radio out from under the owner mid-onboarding. It
// runs above the hasTarget gate, so it reached speakers whose owner had never
// touched STR's Wi-Fi feature.
func TestOwnerIsProvisioningFromTheEpisodeCounter(t *testing.T) {
	s := &Server{}
	if s.ownerIsProvisioning(context.Background()) {
		t.Error("a speaker with no setup episode and no host reads as provisioning")
	}
	s.NoteBoxSetupEpisode()
	if !s.ownerIsProvisioning(context.Background()) {
		t.Error("a live setup episode was not noticed, so the radio would be reset under it")
	}
}

// The episode has to age out, or a speaker that was set up once is protected
// from a genuine radio recovery for ever after.
func TestTheSetupEpisodeAgesOut(t *testing.T) {
	s := &Server{}
	s.NoteBoxSetupEpisode()
	s.boxSetup.mu.Lock()
	s.boxSetup.lastAt = time.Now().Add(-24 * time.Hour)
	s.boxSetup.mu.Unlock()
	if s.ownerIsProvisioning(context.Background()) {
		t.Error("a day-old setup still blocks the radio recovery")
	}
}

// The live source is the second signal, for a setup that started after the
// episode counter was last written.
func TestOwnerIsProvisioningFromTheLiveSource(t *testing.T) {
	old := quietWakeNowPlaying
	t.Cleanup(func() { quietWakeNowPlaying = old })

	s := &Server{boxHost: "192.0.2.1"}
	for src, want := range map[string]bool{
		"SETUP":                true,
		"setup":                true,
		"SETUP_INACTIVE":       true,
		"LOCAL_INTERNET_RADIO": false,
		"STANDBY":              false,
		"":                     false,
	} {
		quietWakeNowPlaying = func(context.Context, string) nowPlayingSnapshot {
			return nowPlayingSnapshot{Source: src}
		}
		if got := s.ownerIsProvisioning(context.Background()); got != want {
			t.Errorf("source %q: got %v, want %v", src, got, want)
		}
	}
}

// A speaker that cannot be asked must not be assumed to be in setup, or a real
// radio failure is never recovered.
func TestNoHostIsNotTakenAsSetup(t *testing.T) {
	s := &Server{}
	if s.ownerIsProvisioning(context.Background()) {
		t.Error("a speaker with no host read as being in setup")
	}
}
