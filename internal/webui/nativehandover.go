package webui

// Handing the speaker over from a native station to a UPnP stream.
//
// A station started natively (the speaker fetches it itself, source
// LOCAL_INTERNET_RADIO, see nativeselect.go) is owned by the speaker's own
// player, not by its UPnP transport. Pushing a UPnP stream on top of it does
// not always take over: on an ST10 (#1065, v1.0.10) the firmware logged
// "HandleUpnpSetAvTransportUri: SourceSelect already sent" and then
// "HandleUpnpPlay() >> InactiveSelected", while both SOAP calls answered
// success. The station kept playing, now_playing still read
// LOCAL_INTERNET_RADIO, and the app reported every one of twelve Spotify key
// presses as accepted.
//
// So a push that finds a native station in charge stops that station first,
// with the speaker's own STOP key (a UPnP Stop does not reach it, see
// transportsource.go), and then checks that the speaker really changed over.
// One more stop and push if it did not, and an error after that, so the caller
// answers a failure instead of a success that never played.

import (
	"context"
	"errors"
	"strings"
	"time"
)

// nativeRadioSource is the source a natively started station plays under.
const nativeRadioSource = "LOCAL_INTERNET_RADIO"

// errNativeStationKept is returned when the speaker ignored the UPnP push twice
// and kept playing its own station. The text is what the app shows.
var errNativeStationKept = errors.New("the speaker kept playing its own radio station and did not switch over. Press Stop on the speaker or in the app, then try again")

// Handover timing. Variables so the tests do not wait for real seconds.
var (
	// nativeHandoverWindow is how long the speaker gets to report the UPnP
	// source after a push.
	nativeHandoverWindow = 3 * time.Second
	// nativeHandoverPoll is the gap between two source reads in that window.
	nativeHandoverPoll = 500 * time.Millisecond
)

// stopNativeStation sends the speaker's own STOP key to end a station it plays
// itself. Returns whether the key was delivered.
func (s *Server) stopNativeStation(ctx context.Context) bool {
	if s.nativeStopFn != nil {
		return s.nativeStopFn(ctx)
	}
	// The firmware answers the key with a STOP_STATE frame that would otherwise
	// read as the user pressing stop and stand down the play this belongs to.
	if s.renderer != nil && s.renderer.OnTransportCommand != nil {
		s.renderer.OnTransportCommand()
	}
	return s.transportKeyFallback(ctx, "STOP")
}

// awaitUPnPSource polls the speaker's source after a push until it reads UPNP
// or the handover window runs out. It returns the last source it read and
// whether the handover counts as done.
//
// Only a source the speaker plays itself counts as a failed handover. A failed
// read ("") or a transitional INVALID_SOURCE gives the push the benefit of the
// doubt: the check exists to catch the native station that stays in charge,
// not to turn a slow probe into an error.
func (s *Server) awaitUPnPSource(ctx context.Context) (string, bool) {
	deadline := time.Now().Add(nativeHandoverWindow)
	last := ""
	for {
		if src := strings.TrimSpace(s.boxSourceNow()); src != "" {
			last = src
		}
		if strings.EqualFold(last, "UPNP") {
			return last, true
		}
		if !time.Now().Before(deadline) {
			return last, !boxOwnedSource(last)
		}
		select {
		case <-ctx.Done():
			return last, !boxOwnedSource(last)
		case <-time.After(nativeHandoverPoll):
		}
	}
}

// pushOverNativeStation runs push, the one UPnP push of a play, and makes sure
// it takes over from a native station the speaker is playing.
//
// When the speaker is on any other source, push runs exactly as before and its
// result is returned unchanged: the extra cost is one now_playing read.
func (s *Server) pushOverNativeStation(ctx context.Context, title string, push func() error) error {
	before := strings.TrimSpace(s.boxSourceNow())
	if !strings.EqualFold(before, nativeRadioSource) {
		return push()
	}
	s.logger.Info("play: the speaker is playing a native station, stopping it before the UPnP push",
		"title", title, "source", before)
	stopped := s.stopNativeStation(ctx)
	if err := push(); err != nil {
		return err
	}
	src, ok := s.awaitUPnPSource(ctx)
	if ok {
		s.logger.Info("play: the speaker switched from its native station to the UPnP stream",
			"title", title, "source", src, "stopKeySent", stopped)
		return nil
	}
	s.logger.Warn("play: the speaker kept its native station after the UPnP push, stopping it and pushing once more",
		"title", title, "source", src, "stopKeySent", stopped)
	stopped = s.stopNativeStation(ctx)
	if err := push(); err != nil {
		return err
	}
	src, ok = s.awaitUPnPSource(ctx)
	if ok {
		s.logger.Info("play: the second push took over from the native station",
			"title", title, "source", src, "stopKeySent", stopped)
		return nil
	}
	s.logger.Warn("play: the speaker ignored the UPnP push twice and kept playing its native station, reporting the play as failed",
		"title", title, "source", src, "stopKeySent", stopped)
	return errNativeStationKept
}

// PushOverNativeStation is pushOverNativeStation for the agent, whose Spotify
// auto-attach (the user picks this speaker in the Spotify app) pushes the
// stream itself and ran into the same swallowed push.
func (s *Server) PushOverNativeStation(ctx context.Context, title string, push func() error) error {
	return s.pushOverNativeStation(ctx, title, push)
}
