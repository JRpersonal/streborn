package spotify

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// How a stereo pair reaches the Spotify app.
//
// Reported on #976 with a screenshot of the device list: the pair "Grace pair
// (L+R)" appears under its own name when the entry comes from the Bose firmware,
// and as two separate halves, "Bose (Grace left) (STR)" and "Bose (Grace right)
// (STR)", when the entries come from STR. The owner then has to guess which half
// to pick, and both answers are wrong, because half a stereo pair is not
// something anybody wants to play to.
//
// So the master carries the pair's name and the follower carries nothing.

func stereoTestManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "go-librespot")
	if err := os.WriteFile(bin, []byte("not a real engine"), 0o755); err != nil {
		t.Fatal(err)
	}
	return New(bin, filepath.Join(dir, "cfg"), "ST Reborn", nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestAStereoMasterAdvertisesThePairAndTheFollowerAdvertisesNothing(t *testing.T) {
	m := stereoTestManager(t)

	// No pair: the speaker's own name, exactly as before.
	if got := m.connectName("Bose (Grace left)"); got != "Bose (Grace left)" {
		t.Errorf("standalone name = %q, want the speaker's own name", got)
	}
	if m.suspendedForStereo() {
		t.Error("a standalone speaker must run its engine")
	}

	// Master: the pair's name, which is the name the Bose app and the STR phone
	// remote already show for it.
	m.SetStereoFn(func() (StereoRole, string) { return StereoMaster, "Grace pair (L+R)" })
	if got := m.connectName("Bose (Grace left)"); got != "Grace pair (L+R)" {
		t.Errorf("master name = %q, want the pair name", got)
	}
	if m.suspendedForStereo() {
		t.Error("the master of a pair must run its engine, it is the half that plays")
	}

	// Follower: no engine, so no entry at all.
	m.SetStereoFn(func() (StereoRole, string) { return StereoFollower, "Grace pair (L+R)" })
	if !m.suspendedForStereo() {
		t.Error("the following half must not advertise itself to Spotify")
	}
}

// The full device_name the engine registers, since that string is what the
// Spotify app shows and the (STR) marker has to survive the change.
func TestThePairNameKeepsTheSTRMarker(t *testing.T) {
	m := stereoTestManager(t)
	m.SetStereoFn(func() (StereoRole, string) { return StereoMaster, "Grace pair (L+R)" })

	yaml := m.configYAML("Bose (Grace left)", 20)
	want := `device_name: "Grace pair (L+R) (STR)"`
	if !strings.Contains(yaml, want) {
		t.Fatalf("config does not carry %s:\n%s", want, firstLines(yaml, 3))
	}
	// And the speaker's own name must be gone from it, or the app shows both.
	if strings.Contains(yaml, "Grace left") {
		t.Errorf("the config still names one half of the pair:\n%s", firstLines(yaml, 3))
	}
}

// A pair with no name cannot be advertised as one. Inventing a label would put a
// name into the Spotify app that appears nowhere else in the user's setup, so the
// speaker keeps its own name instead.
func TestAPairWithNoNameFallsBackToTheSpeakerName(t *testing.T) {
	m := stereoTestManager(t)
	m.SetStereoFn(func() (StereoRole, string) { return StereoMaster, "" })
	if got := m.connectName("Bose (Grace left)"); got != "Bose (Grace left)" {
		t.Errorf("name = %q, want the speaker's own name when the pair has none", got)
	}
}

// A nil reporter is the state on a box whose agent never wired one, and on every
// unit test that does not care. It must read as "no pair" rather than panic or
// suspend an engine.
func TestNoReporterMeansNoPair(t *testing.T) {
	m := stereoTestManager(t)
	if role, name := m.stereoIdentity(); role != StereoNone || name != "" {
		t.Fatalf("stereoIdentity = %v/%q, want none", role, name)
	}
	if m.suspendedForStereo() {
		t.Error("a speaker with no reporter must run its engine")
	}
	if got := m.connectName("Kueche"); got != "Kueche" {
		t.Errorf("name = %q, want the speaker's own", got)
	}
}

// The follower's engine comes back the moment the pair is gone, and the wait
// itself ends when the agent shuts down rather than hanging on to a goroutine.
func TestTheFollowerStartsItsEngineAgainWhenThePairIsGone(t *testing.T) {
	m := stereoTestManager(t)
	m.SetStereoFn(func() (StereoRole, string) { return StereoNone, "" })
	if !m.waitWhileStereoFollower(context.Background()) {
		t.Error("a speaker that is not a follower must not wait at all")
	}

	m.SetStereoFn(func() (StereoRole, string) { return StereoFollower, "Grace pair (L+R)" })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if m.waitWhileStereoFollower(ctx) {
		t.Error("a cancelled context must report that the supervisor should stop, not carry on")
	}
}

func firstLines(s string, n int) string {
	parts := strings.SplitN(s, "\n", n+1)
	if len(parts) > n {
		parts = parts[:n]
	}
	return strings.Join(parts, "\n")
}
