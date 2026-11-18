package webui

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

// The member addresses are from a private range on purpose: a member is only
// ever written at a LAN address (lanPeerAddr), which the documentation range
// 192.0.2.0/24 is not.
const (
	followMasterIP = "192.168.10.10"
	followKitchen  = "192.168.10.11"
	followBath     = "192.168.10.12"
)

// followFleet is a permanent group as the main speaker's agent sees it: the
// live zone its firmware reports, and the members with the level each one has.
type followFleet struct {
	mu     sync.Mutex
	levels map[string]int
	muted  map[string]bool
	writes map[string][]int
	// dead members answer neither a read nor a write; deaf ones answer the
	// read and refuse the write.
	dead, deaf map[string]bool
	live       boxapi.Zone
	liveErr    error
	self       string
	// hold, when set, blocks every member write until it is closed.
	hold chan struct{}
}

func (fl *followFleet) wrote(ip string) []int {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	return append([]int(nil), fl.writes[ip]...)
}

func (fl *followFleet) totalWrites() int {
	fl.mu.Lock()
	defer fl.mu.Unlock()
	n := 0
	for _, w := range fl.writes {
		n += len(w)
	}
	return n
}

func followGroup(permanent, stereo bool) zones.Zone {
	return zones.Zone{
		Master: "MASTER", MasterIP: followMasterIP, Name: "Downstairs",
		Permanent: permanent, Stereo: stereo,
		Slaves: []zones.Member{
			{DeviceID: "KITCHEN", IP: followKitchen},
			{DeviceID: "BATH", IP: followBath},
		},
	}
}

// followServer is the main speaker of a permanent group with a kitchen member
// at 10 and a bathroom member at 30, both in the live zone. The waits are
// shortened; nothing here reaches a network.
func followServer(t *testing.T, z zones.Zone) (*Server, *followFleet) {
	t.Helper()
	fl := &followFleet{
		levels: map[string]int{followKitchen: 10, followBath: 30},
		muted:  map[string]bool{},
		writes: map[string][]int{},
		dead:   map[string]bool{},
		deaf:   map[string]bool{},
		self:   "MASTER",
		live: boxapi.Zone{Master: "MASTER", Members: []boxapi.ZoneMember{
			{DeviceID: "KITCHEN", IP: followKitchen}, {DeviceID: "BATH", IP: followBath},
		}},
	}
	store, err := zones.Load(filepath.Join(t.TempDir(), "zones.json"))
	if err != nil {
		t.Fatalf("open the group store: %v", err)
	}
	if err := store.Set(z); err != nil {
		t.Fatalf("store the group: %v", err)
	}
	s := &Server{
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		boxHost:       followMasterIP,
		zones:         store,
		maxVolumePath: filepath.Join(t.TempDir(), "max-volume"),
		groupFollowT: &groupFollowTiming{
			debounce:     5 * time.Millisecond,
			keyBefore:    150 * time.Millisecond,
			keyWait:      80 * time.Millisecond,
			memberBudget: 2 * time.Second,
		},
	}
	s.rejoinSelfDeviceIDFn = func(context.Context) string {
		fl.mu.Lock()
		defer fl.mu.Unlock()
		return fl.self
	}
	s.rejoinLiveZoneFn = func(context.Context, string) (boxapi.Zone, error) {
		fl.mu.Lock()
		defer fl.mu.Unlock()
		return fl.live, fl.liveErr
	}
	s.rejoinReadZoneMasterFn = func(_ context.Context, ip string) string {
		fl.mu.Lock()
		defer fl.mu.Unlock()
		if fl.dead[ip] {
			return ""
		}
		return "MASTER"
	}
	s.readMemberVolumeFn = func(_ context.Context, host string) (boxapi.Volume, error) {
		fl.mu.Lock()
		defer fl.mu.Unlock()
		if fl.dead[host] {
			return boxapi.Volume{}, errors.New("no answer")
		}
		lv := fl.levels[host]
		return boxapi.Volume{Target: lv, Actual: lv, Muted: fl.muted[host]}, nil
	}
	s.setMemberVolumeFn = func(_ context.Context, addr netip.Addr, pct int) error {
		fl.mu.Lock()
		hold := fl.hold
		fl.mu.Unlock()
		if hold != nil {
			<-hold
		}
		ip := addr.String()
		fl.mu.Lock()
		defer fl.mu.Unlock()
		fl.writes[ip] = append(fl.writes[ip], pct)
		if fl.dead[ip] || fl.deaf[ip] {
			return errors.New("no answer")
		}
		fl.levels[ip] = pct
		return nil
	}
	return s, fl
}

func followWait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// followQuiet gives everything that could still write a member time to do so:
// several debounces and the whole wait for a key.
func followQuiet() { time.Sleep(250 * time.Millisecond) }

func lastOf(v []int) int {
	if len(v) == 0 {
		return -1
	}
	return v[len(v)-1]
}

// The rule is the group slider's (setGroupVolume in the desktop frontend,
// zoneVolumeStep on the phone): every member moves by the main speaker's
// offset from a captured baseline, clamped, and gets its spacing back.
func TestFollowTargetIsTheGroupSlidersRule(t *testing.T) {
	t.Parallel()
	// The desktop slider, written out: v = clamp(round(base + (pct - at))).
	slider := func(base, at, pct int) int {
		v := base + (pct - at)
		return max(0, min(100, v))
	}
	for _, c := range []struct{ base, anchor, now int }{
		{10, 20, 22}, {30, 20, 22}, {10, 20, 5}, {95, 20, 40}, {0, 50, 49}, {100, 50, 51}, {40, 40, 40},
	} {
		if got, want := followTarget(c.base, c.anchor, c.now), slider(c.base, c.anchor, c.now); got != want {
			t.Errorf("followTarget(%d, %d, %d) = %d, the slider gives %d", c.base, c.anchor, c.now, got, want)
		}
	}
	// A member that ran into 0 comes back to where it was: the offset is
	// measured from the baseline, not from the clamped level.
	if got := followTarget(5, 20, 5); got != 0 {
		t.Fatalf("a member at 5 with the main speaker 15 down sits at 0, got %d", got)
	}
	if got := followTarget(5, 20, 20); got != 5 {
		t.Fatalf("back at the starting level the member has its 5 again, got %d", got)
	}
	// And the phone's step: base + delta, clamped (zoneVolumeStep).
	if got := followTarget(98, 30, 35); got != 100 {
		t.Fatalf("98 plus 5 stops at 100, got %d", got)
	}
}

// A volume key on the main speaker moves every member by the same number of
// steps and keeps the balance between them.
func TestMasterKeyMovesTheMembersOfAPermanentGroup(t *testing.T) {
	t.Parallel()
	s, fl := followServer(t, followGroup(true, false))
	s.NoteBoxVolumeLevel(20) // where the main speaker is; nothing to step from yet
	followQuiet()
	if n := fl.totalWrites(); n != 0 {
		t.Fatalf("the first level seen is no step, got %d member writes", n)
	}
	s.NoteBoxVolumeLevel(22)
	followWait(t, "kitchen at 12 and bathroom at 32", func() bool {
		return lastOf(fl.wrote(followKitchen)) == 12 && lastOf(fl.wrote(followBath)) == 32
	})
	// A second press inside the gesture adds up on the same baseline.
	s.NoteBoxVolumeLevel(19)
	followWait(t, "kitchen at 9 and bathroom at 29", func() bool {
		return lastOf(fl.wrote(followKitchen)) == 9 && lastOf(fl.wrote(followBath)) == 29
	})
}

// Every level STR wrote itself comes back as a frame, and none of them moves
// a member: the sliders, the Spotify app, a muted wake, the ceiling.
func TestOwnWritesAreNotPassedOn(t *testing.T) {
	t.Parallel()
	s, fl := followServer(t, followGroup(true, false))
	s.ownLevelSetFn = func(context.Context, int) error { return nil }
	s.NoteBoxVolumeLevel(20)

	// The app's or the phone's slider for this speaker, and the group slider's
	// write to it (handleBoxVolume, zoneVolumeStep).
	s.noteOwnVolumeWrite(35)
	s.NoteBoxVolumeLevel(35)
	// The Spotify app's volume, mirrored onto the speaker by the engine.
	s.NoteOwnVolumeWrite(28)
	s.NoteBoxVolumeLevel(28)
	// A muted wake and its restore, the start volume, the ceiling's turn-down.
	_ = s.setOwnLevel(context.Background(), 0)
	s.NoteBoxVolumeLevel(0)
	_ = s.setOwnLevel(context.Background(), 24)
	s.NoteBoxVolumeLevel(24)
	// The phone's group slider writing this speaker as one of the members.
	s.noteGroupSliderWrite(zoneMemberVolume{IP: followMasterIP, IsSelf: true}, 26)
	s.NoteBoxVolumeLevel(26)
	followQuiet()
	if n := fl.totalWrites(); n != 0 {
		t.Fatalf("STR's own writes must not reach a member, got %d writes: kitchen %v bathroom %v",
			n, fl.wrote(followKitchen), fl.wrote(followBath))
	}

	// A key press after all that moves the members by ITS step only, not by
	// what STR wrote before it.
	s.NoteBoxVolumeLevel(27)
	followWait(t, "kitchen at 11 and bathroom at 31", func() bool {
		return lastOf(fl.wrote(followKitchen)) == 11 && lastOf(fl.wrote(followBath)) == 31
	})
}

// The group slider's write to another member is not this speaker's write.
func TestGroupSliderWriteToAnotherMemberIsNotMarked(t *testing.T) {
	t.Parallel()
	s, _ := followServer(t, followGroup(true, false))
	s.noteGroupSliderWrite(zoneMemberVolume{IP: followKitchen}, 40)
	s.groupFollow.mu.Lock()
	n := len(s.groupFollow.ownWrites)
	s.groupFollow.mu.Unlock()
	if n != 0 {
		t.Fatalf("a write to another speaker marked %d own writes", n)
	}
}

// An own write in the middle of a key gesture does not reach the members
// either: the offset is measured from the level after it.
func TestOwnWriteDuringAGestureIsLeftOut(t *testing.T) {
	t.Parallel()
	s, fl := followServer(t, followGroup(true, false))
	s.NoteBoxVolumeLevel(20)
	s.NoteBoxVolumeLevel(21)
	followWait(t, "the first step", func() bool { return lastOf(fl.wrote(followKitchen)) == 11 })
	s.noteOwnVolumeWrite(40) // the app's slider for the main speaker
	s.NoteBoxVolumeLevel(40)
	s.NoteBoxVolumeLevel(41) // and one more key press
	followWait(t, "kitchen at 12 and bathroom at 32", func() bool {
		return lastOf(fl.wrote(followKitchen)) == 12 && lastOf(fl.wrote(followBath)) == 32
	})
}

// A mark is used up by the frame that reports it, so a key press back to the
// same level a moment later counts.
func TestOwnWriteMarkIsUsedOnce(t *testing.T) {
	t.Parallel()
	s, fl := followServer(t, followGroup(true, false))
	s.NoteBoxVolumeLevel(20)
	s.noteOwnVolumeWrite(25)
	s.NoteBoxVolumeLevel(25) // the echo
	s.NoteBoxVolumeLevel(26) // key up
	followWait(t, "kitchen at 11", func() bool { return lastOf(fl.wrote(followKitchen)) == 11 })
	s.NoteBoxVolumeLevel(25) // key down, to the very level STR wrote before
	followWait(t, "kitchen back at 10 and bathroom at 30", func() bool {
		return lastOf(fl.wrote(followKitchen)) == 10 && lastOf(fl.wrote(followBath)) == 30
	})
}

// A held key is a burst of frames. A member gets the level it can take, one
// request at a time, and always ends on the last one.
func TestHeldKeyIsCoalescedPerMember(t *testing.T) {
	t.Parallel()
	s, fl := followServer(t, followGroup(true, false))
	hold := make(chan struct{})
	fl.hold = hold
	s.NoteBoxVolumeLevel(20)
	start := time.Now()
	for v := 21; v <= 40; v++ {
		s.NoteBoxVolumeLevel(v)
		time.Sleep(2 * time.Millisecond)
	}
	if e := time.Since(start); e > time.Second {
		t.Fatalf("a frame must never wait for a member, 20 frames took %v", e)
	}
	// Both members have a write in flight and the burst has ended behind it.
	time.Sleep(50 * time.Millisecond)
	fl.mu.Lock()
	fl.hold = nil
	fl.mu.Unlock()
	close(hold)
	followWait(t, "kitchen at 30 and bathroom at 50", func() bool {
		return lastOf(fl.wrote(followKitchen)) == 30 && lastOf(fl.wrote(followBath)) == 50
	})
	followQuiet()
	for _, ip := range []string{followKitchen, followBath} {
		w := fl.wrote(ip)
		if len(w) > 2 {
			t.Fatalf("20 frames must cost a slow member at most the write in flight and the last level, %s got %v", ip, w)
		}
	}
	s.groupFollow.mu.Lock()
	inFlight := s.groupFollow.gesture.inFlight
	s.groupFollow.mu.Unlock()
	if inFlight != 0 {
		t.Fatalf("no member writer may outlive its work, %d still counted", inFlight)
	}
}

// A member that cannot be reached keeps its level and is not hammered; the
// others follow.
func TestUnreachableMemberDoesNotStopTheOthers(t *testing.T) {
	t.Parallel()
	t.Run("does not answer the read", func(t *testing.T) {
		s, fl := followServer(t, followGroup(true, false))
		fl.dead[followKitchen] = true
		s.NoteBoxVolumeLevel(20)
		s.NoteBoxVolumeLevel(23)
		followWait(t, "bathroom at 33", func() bool { return lastOf(fl.wrote(followBath)) == 33 })
		followQuiet()
		if w := fl.wrote(followKitchen); len(w) != 0 {
			t.Fatalf("a member that did not answer is left out of the gesture, got %v", w)
		}
	})
	t.Run("refuses the write", func(t *testing.T) {
		s, fl := followServer(t, followGroup(true, false))
		fl.deaf[followKitchen] = true
		s.NoteBoxVolumeLevel(20)
		s.NoteBoxVolumeLevel(23)
		followWait(t, "bathroom at 33", func() bool { return lastOf(fl.wrote(followBath)) == 33 })
		followQuiet()
		if w := fl.wrote(followKitchen); len(w) != 1 || w[0] != 13 {
			t.Fatalf("a refused level is tried once and not repeated, got %v", w)
		}
		// The next step tries again, with the level that is right then.
		s.NoteBoxVolumeLevel(24)
		followWait(t, "a second try at 14", func() bool { return lastOf(fl.wrote(followKitchen)) == 14 })
	})
	t.Run("nobody answers", func(t *testing.T) {
		s, fl := followServer(t, followGroup(true, false))
		fl.dead[followKitchen], fl.dead[followBath] = true, true
		s.NoteBoxVolumeLevel(20)
		s.NoteBoxVolumeLevel(23)
		followQuiet()
		if n := fl.totalWrites(); n != 0 {
			t.Fatalf("nobody to write, got %d writes", n)
		}
		s.groupFollow.mu.Lock()
		g := s.groupFollow.gesture
		s.groupFollow.mu.Unlock()
		if g != nil {
			t.Fatal("a gesture nobody could take must not stay")
		}
	})
}

// Only the members in the live zone follow: a stored member that is asleep or
// out keeps its level, and a group that is not playing together moves nobody.
func TestOnlyLiveMembersFollow(t *testing.T) {
	t.Parallel()
	t.Run("one member asleep", func(t *testing.T) {
		s, fl := followServer(t, followGroup(true, false))
		fl.live.Members = []boxapi.ZoneMember{{DeviceID: "BATH", IP: followBath}}
		s.NoteBoxVolumeLevel(20)
		s.NoteBoxVolumeLevel(22)
		followWait(t, "bathroom at 32", func() bool { return lastOf(fl.wrote(followBath)) == 32 })
		followQuiet()
		if w := fl.wrote(followKitchen); len(w) != 0 {
			t.Fatalf("a member outside the live zone keeps its level, got %v", w)
		}
	})
	t.Run("the group rests", func(t *testing.T) {
		s, fl := followServer(t, followGroup(true, false))
		fl.live = boxapi.Zone{}
		s.NoteBoxVolumeLevel(20)
		s.NoteBoxVolumeLevel(22)
		followQuiet()
		if n := fl.totalWrites(); n != 0 {
			t.Fatalf("no live zone, nobody follows, got %d writes", n)
		}
	})
	t.Run("the zone cannot be read", func(t *testing.T) {
		// Then each member is asked whose zone it is in. The bathroom says
		// ours, the kitchen does not answer.
		s, fl := followServer(t, followGroup(true, false))
		fl.live, fl.liveErr = boxapi.Zone{}, errors.New("no answer")
		fl.dead[followKitchen] = true
		s.NoteBoxVolumeLevel(20)
		s.NoteBoxVolumeLevel(22)
		followWait(t, "bathroom at 32", func() bool { return lastOf(fl.wrote(followBath)) == 32 })
		followQuiet()
		if w := fl.wrote(followKitchen); len(w) != 0 {
			t.Fatalf("a member that does not say it follows keeps its level, got %v", w)
		}
	})
	t.Run("a muted member", func(t *testing.T) {
		s, fl := followServer(t, followGroup(true, false))
		fl.muted[followKitchen] = true
		s.NoteBoxVolumeLevel(20)
		s.NoteBoxVolumeLevel(22)
		followWait(t, "bathroom at 32", func() bool { return lastOf(fl.wrote(followBath)) == 32 })
		followQuiet()
		if w := fl.wrote(followKitchen); len(w) != 0 {
			t.Fatalf("a muted member is left alone, got %v", w)
		}
	})
}

// A group that is not permanent, a stereo pair, a standalone speaker and a
// member of somebody else's group are left exactly as they were.
func TestOtherGroupsAreUntouched(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(s *Server, fl *followFleet)
		z     zones.Zone
	}{
		{name: "a group that is not permanent", z: followGroup(false, false)},
		{name: "a stereo pair", z: followGroup(false, true)},
		{name: "a permanent stereo pair", z: followGroup(true, true)},
		{name: "a standalone speaker", z: followGroup(true, false), setup: func(s *Server, _ *followFleet) {
			if err := s.zones.Forget(); err != nil {
				t.Fatalf("forget: %v", err)
			}
		}},
		{name: "a member holding a copy of the group", z: followGroup(true, false), setup: func(_ *Server, fl *followFleet) {
			fl.self = "KITCHEN" // its own volume keys stay local
		}},
		{name: "a main speaker that follows somebody else right now", z: followGroup(true, false), setup: func(_ *Server, fl *followFleet) {
			fl.live.Master = "SOMEBODY"
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, fl := followServer(t, c.z)
			if c.setup != nil {
				c.setup(s, fl)
			}
			s.NoteBoxVolumeLevel(20)
			for v := 21; v <= 25; v++ {
				s.NoteBoxVolumeLevel(v)
			}
			followQuiet()
			if n := fl.totalWrites(); n != 0 {
				t.Fatalf("nobody may be written, got kitchen %v bathroom %v", fl.wrote(followKitchen), fl.wrote(followBath))
			}
		})
	}
}

// A temporary group on top of a permanent one is the current group, and it is
// not permanent: the keys stay local until the permanent group is back.
func TestTemporaryGroupOnTopOfAPermanentOneIsUntouched(t *testing.T) {
	t.Parallel()
	s, fl := followServer(t, followGroup(true, false))
	if err := s.zones.Set(followGroup(false, false)); err != nil {
		t.Fatal(err)
	}
	s.NoteBoxVolumeLevel(20)
	s.NoteBoxVolumeLevel(22)
	followQuiet()
	if n := fl.totalWrites(); n != 0 {
		t.Fatalf("a temporary group is not followed, got %d writes", n)
	}
}

// While STR holds the level for a wake of its own, no frame is a key press:
// neither its mute to 0, nor the firmware's power-on level, nor the restore.
func TestMutedWakeIsNotPassedOn(t *testing.T) {
	t.Parallel()
	s, fl := followServer(t, followGroup(true, false))
	s.NoteBoxVolumeLevel(20)
	s.groupWake.begin() // a quiet or silent wake is muting the speaker
	s.NoteBoxVolumeLevel(30)
	s.NoteBoxVolumeLevel(0)
	s.NoteBoxVolumeLevel(20)
	s.groupWake.end()
	s.newPlayLevel.mu.Lock() // a muted wake for a new play, restore not done
	s.newPlayLevel.episodes = 1
	s.newPlayLevel.mu.Unlock()
	s.NoteBoxVolumeLevel(0)
	s.NoteBoxVolumeLevel(20)
	s.newPlayLevel.episodeDone()
	followQuiet()
	if n := fl.totalWrites(); n != 0 {
		t.Fatalf("a muted wake must not reach a member, got kitchen %v bathroom %v", fl.wrote(followKitchen), fl.wrote(followBath))
	}
	// Afterwards a key press counts again, from the level the wake left.
	s.NoteBoxVolumeLevel(21)
	followWait(t, "kitchen at 11", func() bool { return lastOf(fl.wrote(followKitchen)) == 11 })
}

// Where only exclusion decides, the level the firmware sets as the speaker
// powers on is kept out by the settle window after standby.
func TestPowerOnLevelIsNotAKeyPressWithoutKeyIdentity(t *testing.T) {
	t.Parallel()
	s, fl := followServer(t, followGroup(true, false))
	s.NoteBoxVolumeLevel(20)
	s.NoteBoxSourceChanged("UPNP", "STANDBY")
	s.NoteBoxVolumeLevel(18)
	s.NoteBoxSourceChanged("STANDBY", "UPNP")
	s.NoteBoxVolumeLevel(30) // the firmware's own power-on level
	followQuiet()
	if n := fl.totalWrites(); n != 0 {
		t.Fatalf("the power-on level is nobody's key press, got %d writes", n)
	}
	// Past the window the key counts.
	s.groupFollow.mu.Lock()
	s.groupFollow.leftStandbyAt = time.Now().Add(-2 * groupFollowStandbySettle)
	s.groupFollow.mu.Unlock()
	s.NoteBoxVolumeLevel(31)
	followWait(t, "kitchen at 11", func() bool { return lastOf(fl.wrote(followKitchen)) == 11 })
}

// Once the key trace has named a volume key on this speaker, a frame needs a
// key: a program writing to the Bose port does not move the group.
func TestKeyIdentityDecidesWhereTheTraceNamesTheVolumeKey(t *testing.T) {
	t.Parallel()
	newTraced := func(t *testing.T) (*Server, *followFleet) {
		s, fl := followServer(t, followGroup(true, false))
		s.SetVolumeKeyTraceHealthyFn(func() bool { return true })
		s.NoteBoxVolumeLevel(20)
		// An earlier key press proved that this speaker's trace names the key.
		at := time.Now().Add(-time.Minute)
		s.NoteBoxVolumeKey("VOLUME_UP", false, true, at)
		s.NoteBoxVolumeKey("VOLUME_UP", true, true, at)
		return s, fl
	}
	t.Run("a frame without a key is somebody else's", func(t *testing.T) {
		s, fl := newTraced(t)
		s.NoteBoxVolumeLevel(40) // a third-party program on :8090
		followQuiet()
		if n := fl.totalWrites(); n != 0 {
			t.Fatalf("no key, no group move, got %d writes", n)
		}
		// The key press after it moves the members by its own step only.
		s.NoteBoxVolumeKey("VOLUME_UP", false, true, time.Now())
		s.NoteBoxVolumeLevel(41)
		followWait(t, "kitchen at 11", func() bool { return lastOf(fl.wrote(followKitchen)) == 11 })
	})
	t.Run("the key's line trails the frame", func(t *testing.T) {
		s, fl := newTraced(t)
		s.NoteBoxVolumeLevel(22)
		time.Sleep(20 * time.Millisecond)
		if n := fl.totalWrites(); n != 0 {
			t.Fatalf("the frame waits for its key, got %d writes", n)
		}
		s.NoteBoxVolumeKey("VOLUME_UP", false, true, time.Now())
		followWait(t, "kitchen at 12 and bathroom at 32", func() bool {
			return lastOf(fl.wrote(followKitchen)) == 12 && lastOf(fl.wrote(followBath)) == 32
		})
	})
	t.Run("a held key covers the whole ramp", func(t *testing.T) {
		s, fl := newTraced(t)
		s.NoteBoxVolumeKey("VOLUME_DOWN", false, true, time.Now())
		time.Sleep(200 * time.Millisecond) // longer than keyBefore: only the held key explains the frames
		s.NoteBoxVolumeLevel(15)
		followWait(t, "kitchen at 5 and bathroom at 25", func() bool {
			return lastOf(fl.wrote(followKitchen)) == 5 && lastOf(fl.wrote(followBath)) == 25
		})
		s.NoteBoxVolumeKey("VOLUME_DOWN", true, true, time.Now())
	})
	t.Run("an own write is left out even while a key is down", func(t *testing.T) {
		s, fl := newTraced(t)
		s.NoteBoxVolumeKey("VOLUME_UP", false, true, time.Now())
		s.noteOwnVolumeWrite(50)
		s.NoteBoxVolumeLevel(50)
		followQuiet()
		if n := fl.totalWrites(); n != 0 {
			t.Fatalf("STR's own write is never a key press, got %d writes", n)
		}
	})
	t.Run("a trace that stopped proves nothing", func(t *testing.T) {
		s, fl := newTraced(t)
		s.SetVolumeKeyTraceHealthyFn(func() bool { return false })
		s.NoteBoxVolumeLevel(22)
		followWait(t, "kitchen at 12", func() bool { return lastOf(fl.wrote(followKitchen)) == 12 })
	})
}

// The members follow the level the main speaker really plays at: a key held
// past its ceiling moves nobody further, and the turn-down is STR's own write.
func TestMasterCeilingIsNotExceededByTheGroup(t *testing.T) {
	t.Parallel()
	s, fl := followServer(t, followGroup(true, false))
	if err := os.WriteFile(s.maxVolumePath, []byte("50"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.ownLevelSetFn = func(context.Context, int) error { return nil }
	s.maxVolumeRecheck = 5 * time.Millisecond
	s.NoteBoxVolumeLevel(48)
	s.NoteBoxVolumeLevel(50)
	followWait(t, "kitchen at 12 and bathroom at 32", func() bool {
		return lastOf(fl.wrote(followKitchen)) == 12 && lastOf(fl.wrote(followBath)) == 32
	})
	s.NoteBoxVolumeLevel(52) // the key is still down; the ceiling turns it back
	s.NoteBoxVolumeLevel(54)
	s.NoteBoxVolumeLevel(50) // the frame of the turn-down
	followQuiet()
	if k, b := lastOf(fl.wrote(followKitchen)), lastOf(fl.wrote(followBath)); k != 12 || b != 32 {
		t.Fatalf("levels above the main speaker's ceiling are no steps, got kitchen %v bathroom %v",
			fl.wrote(followKitchen), fl.wrote(followBath))
	}
}

// A mute frame carries the unchanged level: it is no step, and mute stays
// with the speaker it was pressed on.
func TestMuteOnTheMasterStaysLocal(t *testing.T) {
	t.Parallel()
	s, fl := followServer(t, followGroup(true, false))
	s.NoteBoxVolumeLevel(20)
	s.NoteBoxMute(true)
	s.NoteBoxVolumeLevel(20)
	s.NoteBoxMute(false)
	s.NoteBoxVolumeLevel(20)
	followQuiet()
	if n := fl.totalWrites(); n != 0 {
		t.Fatalf("mute moves nobody, got %d writes", n)
	}
}

// After a pause the members are read afresh, so a balance somebody changed in
// between is the new starting point.
func TestNewGestureReadsTheMembersAgain(t *testing.T) {
	t.Parallel()
	s, fl := followServer(t, followGroup(true, false))
	s.NoteBoxVolumeLevel(20)
	s.NoteBoxVolumeLevel(21)
	followWait(t, "kitchen at 11", func() bool { return lastOf(fl.wrote(followKitchen)) == 11 })
	// Somebody turns the kitchen up at the kitchen speaker, and time passes.
	fl.mu.Lock()
	fl.levels[followKitchen] = 40
	fl.mu.Unlock()
	s.groupFollow.mu.Lock()
	s.groupFollow.gesture.lastAt = time.Now().Add(-2 * groupFollowGestureTTL)
	s.groupFollow.mu.Unlock()
	s.NoteBoxVolumeLevel(22)
	followWait(t, "kitchen at 41 and bathroom at 32", func() bool {
		return lastOf(fl.wrote(followKitchen)) == 41 && lastOf(fl.wrote(followBath)) == 32
	})
}

func TestGroupVolumeFollowSnapshotNamesWhatDecides(t *testing.T) {
	t.Parallel()
	s, _ := followServer(t, followGroup(true, false))
	if got := s.GroupVolumeFollowSnapshot()["decidesBy"]; got != "exclusion: a volume change no STR writer explains" {
		t.Fatalf("without a traced volume key exclusion decides, got %v", got)
	}
	s.SetVolumeKeyTraceHealthyFn(func() bool { return true })
	s.NoteBoxVolumeKey("VOLUME_UP", false, true, time.Now())
	snap := s.GroupVolumeFollowSnapshot()
	if got := snap["decidesBy"]; got != "key identity: a volume key in the speaker's key trace" {
		t.Fatalf("with a traced volume key the key decides, got %v", got)
	}
	if snap["leadsPermanentGroup"] != true {
		t.Fatalf("the stored group is a permanent one, got %v", snap)
	}
}
