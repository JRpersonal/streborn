package webui

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// #954: forming a group while a MEMBER is playing left everything silent.
//
// Three SoundTouch 10s on v0.9.81. The master formed the zone cleanly, with no
// fetch and no error, while the speaker that had been playing logged "a native
// station ended right after OUR preset write" and then "re-push: not resuming,
// the speaker's group changed a moment ago". The master is the group's source
// after a form, and nobody had given it anything to play.

func withMemberNowPlaying(t *testing.T, by map[string]nowPlayingSnapshot) {
	t.Helper()
	prev := memberNowPlaying
	memberNowPlaying = func(_ context.Context, host string) nowPlayingSnapshot { return by[host] }
	t.Cleanup(func() { memberNowPlaying = prev })
}

func resumeServer() *Server {
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: "192.0.2.239"}
}

func TestTheMasterTakesOverAPlayingMembersStation(t *testing.T) {
	// The shape the field bundle carries: a native station, whose location
	// wraps the stream URL that the playing speaker serves on loopback.
	loc := OrionStationLocation("http://127.0.0.1:8888/stream/6", "Exclusively Rush", "")
	withMemberNowPlaying(t, map[string]nowPlayingSnapshot{
		"192.0.2.60": {PlayStatus: "PLAY_STATE", ItemName: "Exclusively Rush", Location: loc},
	})

	got := resumeServer().memberResumeForZone(context.Background(), []boxapi.ZoneMember{
		{IP: "192.0.2.4"}, {IP: "192.0.2.60"},
	})
	if got == nil {
		t.Fatal("the member's station was not picked up, so the group forms into silence")
	}
	if got.title != "Exclusively Rush" {
		t.Errorf("title = %q, want the station name", got.title)
	}
	// The loopback address has to become the member's own, or the master
	// fetches its own empty slot. Verified on hardware that a speaker really
	// does fetch through another speaker's agent, rhino to rhino included.
	// :17008 rather than :8888 on purpose: that is the agent entry reachable
	// from another speaker on every chassis, via the PREROUTING REDIRECT.
	if want := "http://192.0.2.60:17008/stream/6"; got.boxURL != want {
		t.Errorf("boxURL = %q, want %q", got.boxURL, want)
	}
}

// A member that is not audibly playing must contribute nothing, or forming a
// group would start music nobody asked for.
func TestASilentMemberIsNotTakenOver(t *testing.T) {
	loc := OrionStationLocation("http://127.0.0.1:8888/stream/6", "Exclusively Rush", "")
	withMemberNowPlaying(t, map[string]nowPlayingSnapshot{
		"192.0.2.60": {PlayStatus: "STOP_STATE", ItemName: "Exclusively Rush", Location: loc},
		"192.0.2.4":  {PlayStatus: "PLAY_STATE"}, // playing, but no URL to take
	})
	if got := resumeServer().memberResumeForZone(context.Background(), []boxapi.ZoneMember{
		{IP: "192.0.2.60"}, {IP: "192.0.2.4"},
	}); got != nil {
		t.Errorf("took over %v; a stopped member and a member with no URL both offer nothing", got)
	}
}

// The master must never take over from itself: its own capture already covers
// that, and reading its loopback URL back would point it at itself.
func TestTheMasterIsSkipped(t *testing.T) {
	loc := OrionStationLocation("http://127.0.0.1:8888/stream/6", "Own", "")
	withMemberNowPlaying(t, map[string]nowPlayingSnapshot{
		"192.0.2.239": {PlayStatus: "PLAY_STATE", ItemName: "Own", Location: loc},
	})
	if got := resumeServer().memberResumeForZone(context.Background(), []boxapi.ZoneMember{
		{IP: "192.0.2.239"}, {IP: ""},
	}); got != nil {
		t.Errorf("took over the master's own stream: %v", got)
	}
}

// A UPnP push carries its URL in the location directly, which is the other
// shape a member can be playing.
func TestAPushedStreamIsAlsoTakenOver(t *testing.T) {
	withMemberNowPlaying(t, map[string]nowPlayingSnapshot{
		"192.0.2.60": {PlayStatus: "PLAY_STATE", ItemName: "Some Station",
			Location: "http://127.0.0.1:8888/stream/3"},
	})
	got := resumeServer().memberResumeForZone(context.Background(), []boxapi.ZoneMember{{IP: "192.0.2.60"}})
	if got == nil || got.boxURL != "http://192.0.2.60:17008/stream/3" {
		t.Errorf("got %v, want the member's own address", got)
	}
}

// #965: the takeover above was captured correctly and then thrown away.
//
// handleZoneForm passed the stream it was about to push as the staleness
// reference too. For a member takeover that stream is the MEMBER's URL, so the
// gate compared it against the MASTER's own live entry and every master that
// had ever played anything looked superseded. Three ST10s, v0.9.82: "taking
// over the stream a member was playing, member=192.0.2.60" and 2 s later "not
// restarting playback after forming, a newer play superseded it,
// captured=http://192.0.2.60:17008/stream/6 current=http://127.0.0.1:8888/stream/5".
// The group formed with nothing playing, which is the solid amber light.
func TestAMemberTakeoverSurvivesTheMastersOwnOlderPlay(t *testing.T) {
	s, rec := newPlayTestServer(t)
	s.playStateFn = func() (bool, bool) { return false, false } // master idle
	// What the master itself last played, minutes ago. This is masterRef, and
	// it is also what s.lastPlay still holds, so nothing was superseded.
	masterRef := lastPlayInfo{boxURL: "http://127.0.0.1:8888/stream/5", title: "181.FM"}
	s.lastPlayMu.Lock()
	cp := masterRef
	s.lastPlay = &cp
	s.lastPlayMu.Unlock()
	// What a member is playing and the master must take over.
	member := lastPlayInfo{boxURL: "http://192.0.2.60:17008/stream/6", title: "Exclusively Rush"}

	s.resumeAfterZoneForm(zoneResume{push: member, ref: &cp, survivorReachesMembers: false})

	if !rec.has("SetAVTransportURI") {
		t.Fatalf("the member's station was not pushed, so the group forms silent and amber: %v", rec.list())
	}
}

// The other half of the same argument: the gate still has to fire. A real play
// on the master between capture and push moves s.lastPlay away from masterRef,
// and then the stale takeover must not overwrite what the user just started.
func TestARealPlayOnTheMasterStillCancelsTheTakeover(t *testing.T) {
	s, rec := newPlayTestServer(t)
	s.playStateFn = func() (bool, bool) { return false, false }
	masterRef := lastPlayInfo{boxURL: "http://127.0.0.1:8888/stream/5", title: "181.FM"}
	s.lastPlayMu.Lock()
	newer := lastPlayInfo{boxURL: "http://127.0.0.1:8888/stream/2", title: "something the user just pressed"}
	s.lastPlay = &newer
	s.lastPlayMu.Unlock()
	member := lastPlayInfo{boxURL: "http://192.0.2.60:17008/stream/6", title: "Exclusively Rush"}

	s.resumeAfterZoneForm(zoneResume{push: member, ref: &masterRef, survivorReachesMembers: false})

	if rec.has("SetAVTransportURI") {
		t.Fatalf("a newer play on the master was overwritten by the takeover: %v", rec.list())
	}
}
