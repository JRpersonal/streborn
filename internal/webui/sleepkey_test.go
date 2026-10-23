package webui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/groupkeys"
	"github.com/JRpersonal/streborn/internal/keyactions"
)

func TestNextSleepStep(t *testing.T) {
	for _, c := range []struct {
		name   string
		active bool
		armed  time.Duration
		want   time.Duration
	}{
		{"nothing runs", false, 0, 15 * time.Minute},
		{"15 from the key", true, 15 * time.Minute, 30 * time.Minute},
		{"30 from the key", true, 30 * time.Minute, 60 * time.Minute},
		{"60 from the key", true, 60 * time.Minute, 0},
		{"10 from the phone", true, 10 * time.Minute, 15 * time.Minute},
		{"45 from the phone", true, 45 * time.Minute, 60 * time.Minute},
		{"90 from the phone", true, 90 * time.Minute, 0},
		{"an alarm's 2 h auto-off", true, 120 * time.Minute, 0},
		// a stale armed value with nothing running starts over
		{"ran out", false, 60 * time.Minute, 15 * time.Minute},
	} {
		if got := nextSleepStep(c.active, c.armed); got != c.want {
			t.Errorf("%s: next step %v, want %v", c.name, got, c.want)
		}
	}
}

// noGroup makes the speaker answer "not grouped" at once, so the group
// resolver behind every key press does not dial the test address.
func noGroup(s *Server) {
	withSpeakers(s, map[string]boxapi.Zone{"192.0.2.10": {}}, map[string]string{"192.0.2.10": "DEV-SELF"})
}

// pressSleep presses the key as a user tapping through would: past the
// debounce of the previous press.
func pressSleep(s *Server) {
	s.sleep.mu.Lock()
	s.sleep.lastKey = time.Time{}
	s.sleep.mu.Unlock()
	s.SleepKeyPress()
}

func sleepMinutesArmed(s *Server) (bool, int) {
	st := s.sleepStatus()
	if st["active"] != true {
		return false, 0
	}
	return true, st["minutes"].(int)
}

func TestSleepKeyCycles(t *testing.T) {
	f := &fakeBox{source: "UPNP"}
	f.install(t)
	s := newSleepServer()
	// The stub lives on s, so the cancel's group push in the cleanup still
	// finds it rather than the real speaker.
	noGroup(s)
	t.Cleanup(func() { s.cancelSleep("test") })

	for i, want := range []int{15, 30, 60, 0, 15} {
		pressSleep(s)
		active, got := sleepMinutesArmed(s)
		if want == 0 {
			if active {
				t.Fatalf("press %d: still armed with %d, want off", i+1, got)
			}
			continue
		}
		if !active || got != want {
			t.Fatalf("press %d: armed=%v minutes=%d, want %d", i+1, active, got, want)
		}
	}
	// the key arms the whole group
	if s.sleepStatus()["group"] != true {
		t.Error("a key press armed a single-speaker timer, want the group")
	}
	// nothing was switched off along the way
	if len(f.offList()) != 0 {
		t.Errorf("stepping switched speakers off: %v", f.offList())
	}
}

// One press reported twice (trace line and analytics line slipping past the
// reader's own dedup) steps once.
func TestSleepKeyDebouncesOnePress(t *testing.T) {
	f := &fakeBox{source: "UPNP"}
	f.install(t)
	s := newSleepServer()
	noGroup(s)
	t.Cleanup(func() { s.cancelSleep("test") })
	s.SleepKeyPress()
	s.SleepKeyPress()
	if _, m := sleepMinutesArmed(s); m != 15 {
		t.Errorf("a doubled press stepped to %d, want 15", m)
	}
}

// A timer set on the phone steps on from what it was set to.
func TestSleepKeyStepsOnFromThePhone(t *testing.T) {
	f := &fakeBox{source: "UPNP"}
	f.install(t)
	s := newSleepServer()
	t.Cleanup(func() { s.cancelSleep("test") })
	noGroup(s)
	sleepPost(t, s, `{"minutes":45}`)
	pressSleep(s)
	if _, m := sleepMinutesArmed(s); m != 60 {
		t.Errorf("45 from the phone stepped to %d, want 60", m)
	}
	sleepPost(t, s, `{"minutes":90}`)
	pressSleep(s)
	if active, _ := sleepMinutesArmed(s); active {
		t.Error("90 from the phone did not step to off")
	}
}

// In a group the countdown, and the notice for a key press, go to every live
// member; switching it off tells them too.
func TestSleepKeyPushesToTheGroup(t *testing.T) {
	f := &fakeBox{source: "UPNP"}
	f.install(t)
	s := newSleepServer()
	t.Cleanup(func() { s.cancelSleep("test") })
	s.zones = fakeZoneStore(t, "192.0.2.20", "192.0.2.30")
	withSpeakers(s,
		map[string]boxapi.Zone{"192.0.2.10": {Master: "DEV-SELF", Members: []boxapi.ZoneMember{
			{DeviceID: "DEV-192.0.2.20", IP: "192.0.2.20"}, {DeviceID: "DEV-192.0.2.30", IP: "192.0.2.30"}}}},
		map[string]string{"192.0.2.10": "DEV-SELF"})

	pressSleep(s)
	f.mu.Lock()
	got := append([]sentPush(nil), f.pushes...)
	f.pushes = nil
	f.mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("pushed %+v, want both members", got)
	}
	for _, p := range got {
		if p.ip == "192.0.2.10" {
			t.Error("the arming speaker pushed to itself")
		}
		if !p.msg.Notice || p.msg.RemainingSec < 14*60 || p.msg.RemainingSec > 15*60 {
			t.Errorf("push to %s: %+v, want about 900 s with the notice", p.ip, p.msg)
		}
	}
	// 30, 60, then off: the last round tells the members it is over
	pressSleep(s)
	pressSleep(s)
	pressSleep(s)
	f.mu.Lock()
	got = append([]sentPush(nil), f.pushes...)
	f.mu.Unlock()
	if len(got) != 6 {
		t.Fatalf("pushed %d messages for three presses, want 6", len(got))
	}
	for _, p := range got[4:] {
		if p.msg.RemainingSec != 0 || !p.msg.Notice {
			t.Errorf("off push to %s: %+v", p.ip, p.msg)
		}
	}
}

// A single-speaker timer from the phone tells nobody.
func TestSleepSingleTimerPushesNothing(t *testing.T) {
	f := &fakeBox{source: "UPNP"}
	f.install(t)
	s := newSleepServer()
	t.Cleanup(func() { s.cancelSleep("test") })
	noGroup(s)
	s.zones = fakeZoneStore(t, "192.0.2.20")
	sleepPost(t, s, `{"minutes":30}`)
	s.cancelSleep("test")
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pushes) != 0 {
		t.Errorf("a single timer pushed %+v", f.pushes)
	}
}

func displaySleepReq(s *Server, method, remote, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/display/sleep", strings.NewReader(body))
	r.RemoteAddr = remote
	w := httptest.NewRecorder()
	s.handleDisplaySleep(w, r)
	return w
}

// The member's side draws and never arms a timer of its own.
func TestDisplaySleepHandler(t *testing.T) {
	s := newSleepServer()
	const lan = "192.168.1.20:5555"
	if w := displaySleepReq(s, http.MethodPost, lan, `{"remainingSec":900,"notice":true}`); w.Code != http.StatusOK {
		t.Fatalf("valid push: %d %s", w.Code, w.Body.String())
	}
	if s.sleepStatus()["active"] != false {
		t.Error("a pushed countdown armed a timer on the member")
	}
	if w := displaySleepReq(s, http.MethodPost, lan, `{"remainingSec":0}`); w.Code != http.StatusOK {
		t.Errorf("clear: %d", w.Code)
	}
	for _, body := range []string{`{"remainingSec":-5}`, `{"remainingSec":99999}`, `not json`} {
		if w := displaySleepReq(s, http.MethodPost, lan, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, w.Code)
		}
	}
	if w := displaySleepReq(s, http.MethodPost, "203.0.113.9:5555", `{"remainingSec":60}`); w.Code != http.StatusForbidden {
		t.Errorf("off-LAN push: %d, want 403", w.Code)
	}
	if w := displaySleepReq(s, http.MethodGet, lan, ""); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d, want 405", w.Code)
	}
}

func keyActionsServer(t *testing.T) *Server {
	t.Helper()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	gk, err := groupkeys.Load(filepath.Join(t.TempDir(), "group-keys.json"), quiet)
	if err != nil {
		t.Fatal(err)
	}
	ka, err := keyactions.Load(filepath.Join(t.TempDir(), "key-actions.json"), quiet)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{logger: quiet, groupKeys: gk, keyActions: ka}
}

func putTo(h http.HandlerFunc, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	r.RemoteAddr = "192.168.1.20:5555"
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

// A thumbs key carries a group or the sleep timer, never both, and both
// endpoints refuse the second binding with the reason.
func TestKeyActionsEndpointAndGroupConflict(t *testing.T) {
	s := keyActionsServer(t)
	if w := putTo(s.handleKeyActions, "/api/keyactions", `{"keys":{"prev":"sleep"}}`); w.Code != http.StatusBadRequest {
		t.Errorf("sleep on prev: %d, want 400", w.Code)
	}
	if w := putTo(s.handleKeyActions, "/api/keyactions", `{"keys":{"thumbsUp":"sleep"}}`); w.Code != http.StatusOK {
		t.Fatalf("sleep on thumbs up: %d %s", w.Code, w.Body.String())
	}
	if s.keyActions.Action("thumbsUp") != keyactions.ActionSleep {
		t.Fatal("binding not stored")
	}
	group := `{"templates":[{"name":"Evening","master":{"deviceID":"AAAA","ip":"192.0.2.1"},"members":[{"deviceID":"BBBB","ip":"192.0.2.2"}]}],"bindings":{"%s":"Evening"}}`
	w := putTo(s.handleGroupKeys, "/api/groupkeys", strings.Replace(group, "%s", "thumbsUp", 1))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "sleep timer") {
		t.Errorf("group on the sleep key: %d %s, want 409 naming the sleep timer", w.Code, w.Body.String())
	}
	if w := putTo(s.handleGroupKeys, "/api/groupkeys", strings.Replace(group, "%s", "thumbsDown", 1)); w.Code != http.StatusOK {
		t.Fatalf("group on the other key: %d %s", w.Code, w.Body.String())
	}
	w = putTo(s.handleKeyActions, "/api/keyactions", `{"keys":{"thumbsUp":"sleep","thumbsDown":"sleep"}}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "group") {
		t.Errorf("sleep on the group key: %d %s, want 409 naming the group", w.Code, w.Body.String())
	}
	// switching the sleep timer off frees the key for a group
	if w := putTo(s.handleKeyActions, "/api/keyactions", `{"keys":{}}`); w.Code != http.StatusOK {
		t.Fatalf("clear: %d", w.Code)
	}
	if w := putTo(s.handleGroupKeys, "/api/groupkeys", strings.Replace(group, "%s", "thumbsUp", 1)); w.Code != http.StatusOK {
		t.Errorf("group on the freed key: %d %s", w.Code, w.Body.String())
	}
}
