package webui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/zones"
)

// fakeZoneStore gives the server a persisted group with the given member IPs.
// The sleep timer reads membership from the PERSISTED zone, like the group
// volume does, because the firmware's own zone endpoint answers on some chassis
// and not on others.
func fakeZoneStore(t *testing.T, memberIPs ...string) *zones.Store {
	t.Helper()
	st, err := zones.Load(filepath.Join(t.TempDir(), "zones.json"))
	if err != nil {
		t.Fatalf("could not open the test zone store: %v", err)
	}
	z := zones.Zone{Master: "DEV-SELF", MasterIP: "192.0.2.10"}
	for _, ip := range memberIPs {
		z.Slaves = append(z.Slaves, zones.Member{DeviceID: "DEV-" + ip, IP: ip, Role: "member"})
	}
	if err := st.Set(z); err != nil {
		t.Fatalf("could not build the test zone: %v", err)
	}
	return st
}

func newSleepServer() *Server {
	return &Server{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		boxHost: "192.0.2.10",
	}
}

// fakeBox installs the test seams and records what the timer did. Without these
// the box calls go to a fixed firmware port that no test server can occupy, so
// every case would take the "cannot read it, stand down" path and prove nothing.
type fakeBox struct {
	mu       sync.Mutex
	source   string
	standbys []string
	fail     map[string]bool
}

func (f *fakeBox) install(t *testing.T) {
	t.Helper()
	prevRead, prevOff := sleepReadSource, sleepStandby
	sleepReadSource = func(_ context.Context, _ string) string {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.source
	}
	sleepStandby = func(_ context.Context, host string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.fail[host] {
			return errors.New("unreachable")
		}
		f.standbys = append(f.standbys, host)
		return nil
	}
	t.Cleanup(func() { sleepReadSource, sleepStandby = prevRead, prevOff })
}

func (f *fakeBox) offList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.standbys...)
}

func sleepGet(t *testing.T, s *Server) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleSleep(w, httptest.NewRequest(http.MethodGet, "/api/box/sleep", nil))
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("status is not JSON: %v (%s)", err, w.Body.String())
	}
	return out
}

func sleepPost(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleSleep(w, httptest.NewRequest(http.MethodPost, "/api/box/sleep", strings.NewReader(body)))
	return w
}

// waitFor gives the timer callback a moment without pinning a fixed sleep.
func waitFor(cond func() bool) bool {
	for i := 0; i < 100; i++ {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func TestSleepArmsAndReports(t *testing.T) {
	s := newSleepServer()
	if got := sleepGet(t, s)["active"]; got != false {
		t.Errorf("a fresh server reports active=%v, want false", got)
	}
	sleepPost(t, s, `{"minutes":30}`)
	st := sleepGet(t, s)
	if st["active"] != true {
		t.Fatalf("after arming: active=%v", st["active"])
	}
	rem, _ := st["remainingSec"].(float64)
	if rem < 1700 || rem > 1800 {
		t.Errorf("remainingSec = %v, want about 1800", rem)
	}
	if st["group"] != false {
		t.Errorf("group = %v, want false", st["group"])
	}
}

// DELETE and "0 minutes" are the same intent: the phone sends one, a script may
// send the other.
func TestSleepCancels(t *testing.T) {
	s := newSleepServer()
	sleepPost(t, s, `{"minutes":30}`)
	w := httptest.NewRecorder()
	s.handleSleep(w, httptest.NewRequest(http.MethodDelete, "/api/box/sleep", nil))
	if sleepGet(t, s)["active"] != false {
		t.Error("DELETE did not cancel the timer")
	}

	sleepPost(t, s, `{"minutes":30}`)
	sleepPost(t, s, `{"minutes":0}`)
	if sleepGet(t, s)["active"] != false {
		t.Error("minutes:0 did not cancel the timer")
	}
}

func TestSleepRejectsAnAbsurdDuration(t *testing.T) {
	s := newSleepServer()
	if w := sleepPost(t, s, `{"minutes":100000}`); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if sleepGet(t, s)["active"] != false {
		t.Error("a rejected value armed the timer anyway")
	}
}

// The rule the whole design turns on: a speaker that fell asleep by itself is
// left alone. On some chassis the request itself is what wakes it, so acting
// here would make the sleep timer the reason a speaker is awake.
func TestSleepStandsDownWhenTheSpeakerIsAlreadyInStandby(t *testing.T) {
	f := &fakeBox{source: "STANDBY"}
	f.install(t)
	s := newSleepServer()

	s.armSleep(10*time.Millisecond, false)
	time.Sleep(150 * time.Millisecond)
	if got := f.offList(); len(got) != 0 {
		t.Errorf("a sleeping speaker was sent a power-off (%v); it must be left alone", got)
	}
	if sleepGet(t, s)["active"] != false {
		t.Error("the timer stayed armed after standing down")
	}
}

// Unreadable is the same case: if we cannot tell what the speaker is doing, we
// do not act.
func TestSleepStandsDownWhenTheSpeakerCannotBeRead(t *testing.T) {
	f := &fakeBox{source: ""}
	f.install(t)
	s := newSleepServer()

	s.armSleep(10*time.Millisecond, false)
	time.Sleep(150 * time.Millisecond)
	if got := f.offList(); len(got) != 0 {
		t.Errorf("an unreadable speaker was switched off anyway: %v", got)
	}
}

func TestSleepSwitchesAPlayingSpeakerOff(t *testing.T) {
	f := &fakeBox{source: "UPNP"}
	f.install(t)
	s := newSleepServer()

	s.armSleep(10*time.Millisecond, false)
	if !waitFor(func() bool { return len(f.offList()) == 1 }) {
		t.Fatalf("standby calls = %v, want exactly the speaker itself", f.offList())
	}
	if got := f.offList()[0]; got != "192.0.2.10" {
		t.Errorf("switched off %q, want the speaker this page belongs to", got)
	}
	if sleepGet(t, s)["active"] != false {
		t.Error("the timer stayed armed after firing")
	}
}

// Re-arming replaces the old timer. If the old one still fired, the speaker
// would switch off at a time the user had already changed.
func TestSleepReArmingCancelsTheOldTimer(t *testing.T) {
	f := &fakeBox{source: "UPNP"}
	f.install(t)
	s := newSleepServer()

	s.armSleep(15*time.Millisecond, false)
	s.armSleep(10*time.Second, false)
	time.Sleep(150 * time.Millisecond)
	if got := f.offList(); len(got) != 0 {
		t.Errorf("the replaced timer still switched the speaker off: %v", got)
	}
}

// Cancelling while the timer is running out must hold even if the callback is
// already on its way.
func TestSleepCancelBeatsAFiringTimer(t *testing.T) {
	f := &fakeBox{source: "UPNP"}
	f.install(t)
	s := newSleepServer()

	s.armSleep(30*time.Millisecond, false)
	s.cancelSleep("test")
	time.Sleep(150 * time.Millisecond)
	if got := f.offList(); len(got) != 0 {
		t.Errorf("a cancelled timer switched the speaker off: %v", got)
	}
}

// A group timer switches every member off, not only the speaker the timer was
// set on: that is the whole point of the group option.
func TestSleepGroupSwitchesEveryMemberOff(t *testing.T) {
	f := &fakeBox{source: "UPNP"}
	f.install(t)
	s := newSleepServer()
	s.zones = fakeZoneStore(t, "192.0.2.20", "192.0.2.30")

	s.armSleep(10*time.Millisecond, true)
	if !waitFor(func() bool { return len(f.offList()) == 3 }) {
		t.Fatalf("switched off %v, want the speaker plus both members", f.offList())
	}
	got := strings.Join(f.offList(), ",")
	for _, want := range []string{"192.0.2.10", "192.0.2.20", "192.0.2.30"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s stayed on (got %s)", want, got)
		}
	}
}

// One unreachable member must not stop the others from being switched off. A
// half-executed sleep timer that gives up on the first error would leave a room
// playing all night.
func TestSleepGroupCarriesOnPastAnUnreachableMember(t *testing.T) {
	f := &fakeBox{source: "UPNP", fail: map[string]bool{"192.0.2.20": true}}
	f.install(t)
	s := newSleepServer()
	s.zones = fakeZoneStore(t, "192.0.2.20", "192.0.2.30")

	s.armSleep(10*time.Millisecond, true)
	if !waitFor(func() bool { return len(f.offList()) == 2 }) {
		t.Fatalf("switched off %v, want the speaker and the reachable member", f.offList())
	}
	if got := strings.Join(f.offList(), ","); !strings.Contains(got, "192.0.2.30") {
		t.Errorf("the reachable member stayed on (got %s)", got)
	}
}

// A speaker with no group ignores the group flag rather than failing.
func TestSleepGroupOnAStandaloneSpeaker(t *testing.T) {
	f := &fakeBox{source: "UPNP"}
	f.install(t)
	s := newSleepServer()

	s.armSleep(10*time.Millisecond, true)
	if !waitFor(func() bool { return len(f.offList()) == 1 }) {
		t.Fatalf("switched off %v, want just the speaker", f.offList())
	}
}
