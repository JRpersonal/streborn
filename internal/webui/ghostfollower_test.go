package webui

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

// "Kueche_re follows EssZi like a ghost, out of sync, although I have no group
// active any more. It also switches off with EssZi, and the app does not show
// it as active." Eleven speakers, 2026-09-13.
//
// The master's firmware had reported the zone gone while STR still held the
// document, and the sweep for that moment declined because the master was
// playing nothing to compare a follower's audio against. Right for the audio,
// wrong for the membership: the follower's firmware still named the master, so
// it kept powering off with it.

type fakeFollower struct {
	zone boxapi.Zone
	err  error
	left int
}

func withFollowers(t *testing.T, f map[string]*fakeFollower) {
	t.Helper()
	prevZone, prevLeave := firmwareZoneFn, leaveZoneAtFollowerFn
	firmwareZoneFn = func(_ context.Context, host string) (boxapi.Zone, error) {
		if fb, ok := f[host]; ok {
			return fb.zone, fb.err
		}
		return boxapi.Zone{}, errors.New("no such speaker")
	}
	leaveZoneAtFollowerFn = func(_ context.Context, host string, _, _ boxapi.ZoneMember) error {
		if fb, ok := f[host]; ok {
			fb.left++
			return nil
		}
		return errors.New("no such speaker")
	}
	t.Cleanup(func() { firmwareZoneFn, leaveZoneAtFollowerFn = prevZone, prevLeave })
}

func sweepServer(host string) *Server {
	return &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: host}
}

func TestAFollowerStillNamingTheMasterIsFreed(t *testing.T) {
	const master = "DEV#76eceba9"
	f := map[string]*fakeFollower{
		"192.0.2.11": {zone: boxapi.Zone{Master: master}},
		"192.0.2.19": {zone: boxapi.Zone{Master: strings.ToLower(master)}}, // case must not matter
	}
	withFollowers(t, f)

	sweepServer("192.0.2.10").clearStaleZoneMembership(master, []zones.Member{
		{DeviceID: "DEV#ffffe004", IP: "192.0.2.11"},
		{DeviceID: "DEV#030f2c92", IP: "192.0.2.19"},
	})

	for ip, fb := range f {
		if fb.left != 1 {
			t.Errorf("%s: told to leave %d times, want 1", ip, fb.left)
		}
	}
}

// Three ways a follower must be left alone, and none of them is a guess about
// audio: it already knows, it belongs to somebody else now, or it did not
// answer at all. Unreachable is not the same as wrong, and between two rhino
// ST10s it is the normal case.
func TestAFollowerIsOnlyFreedOnPositiveEvidence(t *testing.T) {
	const master = "DEV#76eceba9"
	f := map[string]*fakeFollower{
		"192.0.2.11": {zone: boxapi.Zone{Master: ""}},             // already free
		"192.0.2.19": {zone: boxapi.Zone{Master: "DEV#somebody"}}, // another group
		"192.0.2.21": {err: errors.New("connection refused")},     // did not answer
	}
	withFollowers(t, f)

	sweepServer("192.0.2.10").clearStaleZoneMembership(master, []zones.Member{
		{IP: "192.0.2.11"}, {IP: "192.0.2.19"}, {IP: "192.0.2.21"},
	})

	for ip, fb := range f {
		if fb.left != 0 {
			t.Errorf("%s: was told to leave, want left alone", ip)
		}
	}
}

// The master must never be sent its own teardown, and a document with no
// master is not evidence of anything.
func TestTheMasterAndAnEmptyDocumentAreSkipped(t *testing.T) {
	f := map[string]*fakeFollower{"192.0.2.10": {zone: boxapi.Zone{Master: "DEV#76eceba9"}}}
	withFollowers(t, f)
	s := sweepServer("192.0.2.10")

	s.clearStaleZoneMembership("DEV#76eceba9", []zones.Member{{IP: "192.0.2.10"}})
	s.clearStaleZoneMembership("", []zones.Member{{IP: "192.0.2.11"}})

	if f["192.0.2.10"].left != 0 {
		t.Error("the master was sent its own teardown")
	}
}
