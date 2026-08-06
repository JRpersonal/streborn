// Silencing the speaker before a large OTA transfer.
//
// An update pushes two ARM binaries over the speaker's Wi-Fi: the agent (~12 MB)
// and the Spotify engine (~16 MB). On a SoundTouch that transfer runs at roughly
// 150 KB/s and occupies the box for a minute or two, and the speaker has to
// serve the audio stream through the same radio and the same modest CPU at the
// same time.
//
// It loses. A field bundle from 2026-08-06 catches it exactly: the engine upload
// starts, and 44 seconds in the box gives up on the stream it was playing with
// AUDIO_ERROR_BAD_URL, flaps UPNP -> INVALID_SOURCE, gets resumed by the re-push
// machinery two seconds later, and drops again. The owner described it as the
// stream "stopping/starting when playing after update", and noted it was gone
// after a cold reboot, which is simply the state where no transfer is running.
//
// Fighting for the bandwidth is the wrong answer, because the update needs it
// and the listener cannot have both. So the speaker is quietened first: the
// transfer gets the box to itself, and instead of a minute of broken audio and
// firmware errors the speaker simply goes quiet for the update, which is what a
// speaker doing an update is expected to do.
//
// Scope is deliberately narrow. Only what STR itself is driving gets stopped.
// A speaker playing Bluetooth, AUX or its own native Spotify is left alone: the
// transfer will disturb that too, but reaching in and silencing a source STR
// does not own is a bigger intrusion than the stutter it prevents.

package webui

import (
	"context"
	"errors"
	"time"
)

// hushBudget bounds the whole thing. It runs in front of an upload the user is
// waiting on, so a speaker that will not answer must not delay the update: the
// worst case of giving up is the stuttering playback we had before.
const hushBudget = 6 * time.Second

// Seams for the tests. Both reach the firmware on a fixed port, so a test server
// on a random port cannot be reached through them; without these every case
// would take the "cannot read the source, leave it alone" path and assert
// nothing. Production always uses the real implementations.
var (
	hushReadNowPlaying = func(ctx context.Context, host string) nowPlayingSnapshot {
		return fetchNowPlaying(ctx, host)
	}
	hushStop = func(ctx context.Context, s *Server) error {
		if s.renderer == nil {
			return errNoRenderer
		}
		return s.renderer.Stop(ctx)
	}
)

// errNoRenderer: nothing of STR's is driving this speaker, so there is nothing
// of ours to stop.
var errNoRenderer = errors.New("no renderer")

// hushForUpload stops STR's own playback so a large OTA transfer does not have
// to compete with it. Best-effort and never fatal: an update must go ahead
// whatever the speaker says about what it is playing.
func (s *Server) hushForUpload(what string) {
	ctx, cancel := context.WithTimeout(context.Background(), hushBudget)
	defer cancel()

	np := hushReadNowPlaying(ctx, s.boxHost)
	switch {
	case np.Source == "":
		// Unreadable. Stopping blind could silence a source STR does not own,
		// which is the one outcome worse than the stutter.
		s.logger.Debug("upload: could not read what the speaker is playing, leaving it alone", "endpoint", what)
		return
	case np.Source == "STANDBY" || np.Source == "INVALID_SOURCE":
		return // nothing playing, nothing to quieten
	case np.PlayStatus != "PLAY_STATE" && np.PlayStatus != "BUFFERING_STATE":
		return // a source is selected but silent; stopping it would change nothing
	case boxOwnedSource(np.Source):
		s.logger.Info("upload: the speaker is playing its own source, leaving it alone for the transfer",
			"endpoint", what, "source", np.Source)
		return
	}

	// Latch the stop as deliberate BEFORE it happens. Without this the
	// UPNP -> STANDBY flip it causes reads as a spontaneous firmware drop and
	// the re-push machinery puts the stream straight back on, which is the
	// exact fight this is meant to end.
	s.NoteUserStop()
	if err := hushStop(ctx, s); err != nil {
		s.logger.Warn("upload: could not stop playback before the transfer", "endpoint", what, "err", err)
		return
	}
	s.logger.Info("upload: stopped playback so the transfer has the speaker to itself",
		"endpoint", what, "source", np.Source)
}
