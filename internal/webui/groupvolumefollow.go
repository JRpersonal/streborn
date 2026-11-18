package webui

// groupvolumefollow.go: in a permanent group, the volume keys of the main
// speaker change the volume of the whole group (Ideas discussion #1429).
//
// Until now a volume key on the main speaker of a group moved that speaker
// alone, and the group could only be turned up or down as one from the app or
// the phone page. This file makes the agent on the main speaker pass the step
// on to every member that is playing along.
//
// What counts, and what does not:
//
//   - ONLY a change a person made on the main speaker itself counts: its own
//     volume keys, or the IR remote pointed at it. Every level STR writes (the
//     app's and the phone page's sliders for this speaker, the group slider,
//     the Spotify app's volume, a muted wake and its restore, the start
//     volume, the volume ceiling's turn-down, an alarm, an announcement) is
//     never passed on. The group slider already wrote every member itself, so
//     passing its echo on would apply it twice.
//   - ONLY a permanent group (the saved, opt-in kind). An ad-hoc zone is left
//     as it was, and so is a stereo pair, which the firmware levels itself.
//   - ONLY the members that are in the live zone right now. A stored member
//     that is asleep, was taken out or plays on its own keeps its level.
//   - A member's own volume keys stay local. They change the balance, exactly
//     as the per-speaker sliders do.
//
// The rule is the group slider's rule (#401, desktop-app/frontend/src/views/
// music/group.js setGroupVolume, and zoneVolumeStep for the phone): RELATIVE.
// Every member moves by the same number of steps as the main speaker, from the
// level it had when the gesture began, clamped to 0..100. The offset is always
// measured against that captured baseline and never against the levels the
// writes already produced, so a member that hits 0 or 100 gets its spacing
// back when the key goes the other way. followTarget is that rule; it exists
// once in Go here because the desktop's copy lives in the frontend.
//
// How the agent knows a person pressed a key. The gabbo volumeUpdated frame
// carries a level and nothing about who set it, so the frame alone cannot
// tell. Two layers, in this order:
//
//  1. Exclusion, on every chassis. Each level STR writes into this speaker is
//     marked before the write (noteOwnVolumeWrite), and the frame that reports
//     it back is recognised by its value and dropped. Frames that arrive while
//     STR holds the level for a wake of its own are dropped too.
//  2. Key identity, where the speaker offers it. The firmware's own key trace
//     (internal/boxlog) names VOLUME_UP and VOLUME_DOWN with their producer
//     (console or IR remote) on the chassis whose trace reports them. Once the
//     trace has shown one such key on this speaker, a frame is passed on only
//     when a physical volume key goes with it. A third-party program writing
//     to the Bose port (:8090) is then left alone.
//
// On a chassis whose trace never names the volume key (the sm2 boards: the
// SoundTouch 10, and the sm2 builds of the 20 and 30) only layer 1 exists: a
// frame is passed on when no STR writer explains it. There a third-party
// writer on :8090 is indistinguishable from the key and moves the group too.
// The firmware's own level changes around a power-on are kept out by a settle
// window after standby.
//
// Rules inherited from zonejoinvolume.go and wakelevel.go, all kept:
//
//   - A member that runs STR is written through its own agent, which holds the
//     write behind that speaker's command lock and applies that speaker's own
//     volume ceiling (setJoinerVolume). Never a raw :8090 write first.
//   - Nothing is re-asserted on a timer (#548). A member is written when the
//     main speaker's level changed, once, and never again until it changes
//     again.
//   - A 0 that STR's own mute wrote is never a level to pass on: it is an own
//     write, and it arrives while STR holds the level.
//
// Mute stays local. Neither group slider touches mute, and a mute frame
// carries the unchanged target level, so it is no step. A member that is
// muted is left out of the gesture: a level written to it could lift a mute
// somebody set on purpose.

import (
	"context"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/zones"
)

const (
	// ownVolumeEchoWindow is how long a level STR wrote into this speaker is
	// remembered, so the frame that reports it back is recognised. Matched by
	// value and used up by the first frame that carries it, so a key press to
	// a different level inside the window still counts. Same length as the
	// Spotify bridge's echo window (internal/spotify/volumesync.go).
	ownVolumeEchoWindow = 3 * time.Second
	// groupFollowGestureTTL is how long after the last step the captured
	// member levels stay the baseline. A held key or a run of presses adds up
	// on top of them; after a pause the members are read afresh, so a balance
	// somebody changed in between is the new starting point. The phone's
	// group slider trusts its own baseline for the same time.
	groupFollowGestureTTL = groupStepBaseTTL
	// groupFollowKeyHeldMax bounds how long a volume key the trace reported as
	// pressed counts as held when no release line follows. Going from 0 to 100
	// with the key held takes well under this.
	groupFollowKeyHeldMax = 20 * time.Second
	// groupFollowStandbySettle is how long after this speaker left standby its
	// volume frames are not passed on where only exclusion decides. The
	// firmware sets its own level as it powers on (about 30 on a speaker that
	// forgot its level), and that frame is nobody's key press. With key
	// identity the window is not needed and not applied.
	groupFollowStandbySettle = 10 * time.Second
	// groupFollowSkipLogEvery rate-limits the INFO line for a change that is
	// not passed on, per reason: a slider drag or a held key produces a frame
	// per step, and the NAND log must not carry one line for each.
	groupFollowSkipLogEvery = 3 * time.Second
)

// groupFollowTiming holds the waits of this file. On the Server so a test can
// install shorter ones without touching package state.
type groupFollowTiming struct {
	// debounce is the pause between the first step of a gesture and the first
	// write to the members. The frames of a held key arrive a few tens of
	// milliseconds apart; the pause lets the first write carry the newest
	// level instead of the first one.
	debounce time.Duration
	// keyBefore is how recently the key trace must have named a physical
	// volume key for a frame to count as that key's.
	keyBefore time.Duration
	// keyWait is how long a frame waits for its key where key identity
	// decides. The trace line was measured to trail the gabbo frame by up to
	// about a second on the Portable (cmd/agent/keytrace.go keyTraceWait).
	keyWait time.Duration
	// memberBudget bounds one member read or write.
	memberBudget time.Duration
}

var productionGroupFollowTiming = groupFollowTiming{
	debounce:     150 * time.Millisecond,
	keyBefore:    2 * time.Second,
	keyWait:      1500 * time.Millisecond,
	memberBudget: 10 * time.Second,
}

func (s *Server) groupFollowTimes() groupFollowTiming {
	if s.groupFollowT != nil {
		return *s.groupFollowT
	}
	return productionGroupFollowTiming
}

// followTarget is the group slider's rule for one member: the level it had
// when the gesture began plus the number of steps the main speaker moved since
// then, clamped to 0..100. The clamp is applied to the result only, never to
// the baseline, which is what gives a member its spacing back after it ran
// into 0 or 100.
func followTarget(memberBase, masterAnchor, masterNow int) int {
	v := memberBase + (masterNow - masterAnchor)
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

type ownVolumeMark struct {
	level int
	at    time.Time
}

// groupFollowMember is one member of a running gesture.
type groupFollowMember struct {
	name string
	addr netip.Addr
	// base is the level the member had when the gesture began.
	base int
	// want is the level the member should have now, sent the level the last
	// write carried. busy while a write is in flight; a newer want replaces
	// the one waiting, so a member never has more than one request open and
	// never receives a superseded level after a newer one.
	want, sent int
	busy       bool
}

// groupFollowGesture is one run of steps on the main speaker.
type groupFollowGesture struct {
	// anchor is the main speaker's level the offset is measured from: its
	// level before the first step, moved along by every change STR made
	// itself since, so those never reach the members.
	anchor int
	// level is the main speaker's newest level.
	level  int
	from   int
	how    string
	lastAt time.Time
	// loading while the member list is being read; ready once it is there.
	loading, ready bool
	members        []*groupFollowMember
	inFlight       int
}

// groupFollowPending is a step that waits for its key (key identity only).
type groupFollowPending struct {
	prev, level int
	at          time.Time
}

// groupFollowState is everything this file keeps. Own mutex, never taken
// while newPlayLevel's or quietWakeState's is held.
type groupFollowState struct {
	mu sync.Mutex
	// ownWrites are the levels STR wrote into this speaker inside
	// ownVolumeEchoWindow.
	ownWrites []ownVolumeMark
	// last is the main speaker's level from the newest frame.
	last      int
	lastKnown bool
	// inStandby and leftStandbyAt follow the gabbo source edges.
	inStandby     bool
	leftStandbyAt time.Time
	// The key trace: when it last named a physical volume key, which of the
	// two keys it reported as held, and whether it has named one at all since
	// the agent started. Only then does "no key in the trace" mean nobody
	// pressed one, and not a speaker whose trace never reports the key.
	keyAt      time.Time
	keyDown    map[string]time.Time
	keysTraced bool

	gesture *groupFollowGesture
	pending *groupFollowPending

	skipLogged map[string]time.Time
	// Counters for the debug section.
	passed, skipped int
	lastSkip        string
}

// noteOwnVolumeWrite marks a level STR is about to write into THIS speaker.
// Call it before the write: the speaker's frame can arrive before the HTTP
// call returns. The frame that reports the level back is then not taken for a
// key press and not passed on to a group.
func (s *Server) noteOwnVolumeWrite(level int) {
	now := time.Now()
	f := &s.groupFollow
	f.mu.Lock()
	f.ownWrites = append(pruneOwnVolumeMarks(f.ownWrites, now), ownVolumeMark{level: level, at: now})
	f.mu.Unlock()
}

// NoteOwnVolumeWrite is noteOwnVolumeWrite for the agent's other writers of
// this speaker's volume (the Spotify engine mirroring the Spotify app's
// slider onto the speaker).
func (s *Server) NoteOwnVolumeWrite(level int) { s.noteOwnVolumeWrite(level) }

func pruneOwnVolumeMarks(list []ownVolumeMark, now time.Time) []ownVolumeMark {
	keep := list[:0]
	for _, mk := range list {
		if now.Sub(mk.at) < ownVolumeEchoWindow {
			keep = append(keep, mk)
		}
	}
	return keep
}

// takeOwnMarkLocked reports whether level is the echo of one of STR's own
// writes and uses that mark up. Caller holds f.mu.
func (f *groupFollowState) takeOwnMarkLocked(level int, now time.Time) bool {
	f.ownWrites = pruneOwnVolumeMarks(f.ownWrites, now)
	for i, mk := range f.ownWrites {
		if mk.level == level {
			f.ownWrites = append(f.ownWrites[:i], f.ownWrites[i+1:]...)
			return true
		}
	}
	return false
}

// SetVolumeKeyTraceHealthyFn wires the key trace's liveness
// (boxlog.Reader.Healthy). Key identity decides only while the trace runs;
// with a trace that stopped, a silent trace says nothing about the keys.
func (s *Server) SetVolumeKeyTraceHealthyFn(fn func() bool) { s.volumeKeyTraceHealthyFn = fn }

// NoteBoxVolumeKey is the key trace's report of a volume key a PERSON pressed
// on this speaker or its IR remote (cmd/agent OnKeyEvent hands over only
// physical producers). name is VOLUME_UP or VOLUME_DOWN. released is the
// key going up; hasRelease says whether this kind of trace line reports a
// release at all (the DEBUG trace does, the analytics line does not), because
// only then can a key count as held between two lines. Never blocks.
func (s *Server) NoteBoxVolumeKey(name string, released, hasRelease bool, at time.Time) {
	if at.IsZero() {
		at = time.Now()
	}
	f := &s.groupFollow
	f.mu.Lock()
	first := !f.keysTraced
	f.keysTraced = true
	f.keyAt = at
	if f.keyDown == nil {
		f.keyDown = map[string]time.Time{}
	}
	switch {
	case released:
		delete(f.keyDown, name)
	case hasRelease:
		// Stamped by every line of the key, so a key counts as held for
		// groupFollowKeyHeldMax after the last thing the trace said about it.
		f.keyDown[name] = at
	}
	p := f.pending
	f.pending = nil
	var start *groupFollowGesture
	if p != nil {
		// The step that was waiting for its key: it is that key's.
		start = s.acceptStepLocked(p.prev, p.level, "the key trace named the volume key", time.Now())
	}
	f.mu.Unlock()
	if first {
		s.logger.Info("group volume: the key trace names this speaker's volume keys, from now on only a key press on it or its remote moves a permanent group",
			"key", name)
	}
	if start != nil {
		go s.groupFollowLoad(start)
	}
}

// keyEvidenceLocked reports whether the key trace says a person is pressing,
// or just pressed, a volume key. Caller holds f.mu.
func (f *groupFollowState) keyEvidenceLocked(now time.Time, before time.Duration) bool {
	if !f.keyAt.IsZero() && now.Sub(f.keyAt) < before {
		return true
	}
	for _, downAt := range f.keyDown {
		if now.Sub(downAt) < groupFollowKeyHeldMax {
			return true
		}
	}
	return false
}

// noteGroupFollowSource follows the gabbo source edge for the settle window
// after standby. Called from NoteBoxSourceChanged, on the gabbo read loop.
func (s *Server) noteGroupFollowSource(from, to string) {
	f := &s.groupFollow
	f.mu.Lock()
	switch {
	case to == "STANDBY":
		f.inStandby = true
	case from == "STANDBY":
		f.inStandby = false
		f.leftStandbyAt = time.Now()
	default:
		f.inStandby = false
	}
	f.mu.Unlock()
}

// followDoc returns the stored group when it is one whose main speaker passes
// its volume keys on, and otherwise why not. quiet is true when there is
// simply no stored group, which is every standalone speaker on every volume
// step and not worth a log line.
func (s *Server) followDoc() (z zones.Zone, why string, quiet bool) {
	if s.zones == nil {
		return z, "no group is stored", true
	}
	z, ok := s.zones.Get()
	switch {
	case !ok || len(z.Slaves) == 0:
		return z, "no group is stored", true
	case z.Stereo:
		return z, "a stereo pair levels itself", false
	case !z.Permanent:
		return z, "the group is not a permanent one", false
	}
	return z, "", false
}

// noteGroupFollowLevel sees every volume frame of this speaker (level >= 0),
// after the volume ceiling and the muted-wake bookkeeping. held says STR is
// holding the level for a wake of its own. It decides whether the change is a
// person's key press on the main speaker of a permanent group and, if so,
// hands it to the gesture. Called on the gabbo read loop: it only does
// arithmetic under its own mutex, everything that talks to a speaker runs in
// a goroutine that ends with the gesture.
func (s *Server) noteGroupFollowLevel(level int, held bool) {
	raw := level
	// The members follow the level the main speaker really plays at. A key
	// held past this speaker's own ceiling reports levels above it for the
	// moment the turn-down takes (maxvolume.go); those are no steps.
	if limit, ok := s.maxVolume(); ok && level > limit {
		level = limit
	}
	now := time.Now()
	t := s.groupFollowTimes()
	f := &s.groupFollow
	f.mu.Lock()
	prev, known := f.last, f.lastKnown
	f.last, f.lastKnown = level, true
	own := f.takeOwnMarkLocked(raw, now)
	if !known || level == prev {
		// The first frame since the agent started has nothing to be a step
		// from; a frame at the same level is a mute, a repeat, or a key held
		// at the ceiling.
		f.mu.Unlock()
		return
	}
	skip := func(reason string, quiet bool) {
		// A change that is not passed on still moved the main speaker, so the
		// offset of a running gesture is measured from the new level: the
		// members must not make that move at the next key press.
		if g := f.gesture; g != nil {
			g.anchor += level - prev
		}
		if p := f.pending; p != nil {
			p.prev += level - prev
		}
		info := s.skipLogDueLocked(reason, now) && !quiet
		f.skipped++
		f.lastSkip = reason
		f.mu.Unlock()
		if info {
			s.logger.Info("group volume: not passing this speaker's volume change on to a group",
				"reason", reason, "from", prev, "to", level, "userActivityAgoMs", s.userActivityAgoMs(now))
		} else {
			s.logger.Debug("group volume: not passing this speaker's volume change on to a group",
				"reason", reason, "from", prev, "to", level)
		}
	}
	switch {
	case own:
		skip("STR wrote this level itself (a slider, the Spotify app, a wake, the start volume, the ceiling, an alarm)", false)
		return
	case held:
		skip("STR holds the level for a wake of its own", false)
		return
	}
	if _, why, quiet := s.followDoc(); why != "" {
		skip(why, quiet)
		return
	}
	keyIdentity := f.keysTraced && s.volumeKeyTraceHealthyFn != nil && s.volumeKeyTraceHealthyFn()
	var start *groupFollowGesture
	switch {
	case f.keyEvidenceLocked(now, t.keyBefore):
		start = s.acceptStepLocked(prev, level, "the key trace named the volume key", now)
	case keyIdentity:
		// The trace reports this speaker's volume keys and has not named one
		// for this frame yet. Its line can trail the frame, so the step waits
		// for it, bounded, and is dropped when no key comes: then somebody
		// else wrote the level (a third-party program on the Bose port).
		if p := f.pending; p != nil {
			p.level = level
		} else {
			p = &groupFollowPending{prev: prev, level: level, at: now}
			f.pending = p
			time.AfterFunc(t.keyWait, func() { s.dropPendingStep(p) })
		}
	case f.inStandby:
		skip("the speaker is in standby", false)
		return
	case !f.leftStandbyAt.IsZero() && now.Sub(f.leftStandbyAt) < groupFollowStandbySettle:
		skip("the speaker just left standby and sets its own level", false)
		return
	case s.groupWake.episodeActive():
		skip("STR woke this speaker for a group operation a moment ago", false)
		return
	default:
		start = s.acceptStepLocked(prev, level, "no STR writer explains it (this speaker's key trace does not name the volume key)", now)
	}
	f.mu.Unlock()
	if start != nil {
		go s.groupFollowLoad(start)
	}
}

// dropPendingStep ends the wait of a step whose key never came.
func (s *Server) dropPendingStep(p *groupFollowPending) {
	f := &s.groupFollow
	f.mu.Lock()
	if f.pending != p {
		f.mu.Unlock()
		return // its key came, or a newer wait replaced it
	}
	f.pending = nil
	if g := f.gesture; g != nil {
		g.anchor += p.level - p.prev
	}
	f.skipped++
	f.lastSkip = "no volume key press on this speaker or its remote goes with it"
	f.mu.Unlock()
	s.logger.Info("group volume: not passing this speaker's volume change on to a group",
		"reason", "no volume key press on this speaker or its remote goes with it (somebody else wrote the level, such as a program on the Bose port)",
		"from", p.prev, "to", p.level, "waitedMs", time.Since(p.at).Milliseconds(),
		"userActivityAgoMs", s.userActivityAgoMs(time.Now()))
}

// skipLogDueLocked reports whether the INFO line for reason is due, and
// stamps it. Caller holds f.mu.
func (s *Server) skipLogDueLocked(reason string, now time.Time) bool {
	f := &s.groupFollow
	if f.skipLogged == nil {
		f.skipLogged = map[string]time.Time{}
	}
	if last, ok := f.skipLogged[reason]; ok && now.Sub(last) < groupFollowSkipLogEvery {
		return false
	}
	f.skipLogged[reason] = now
	return true
}

// userActivityAgoMs is how long ago the speaker last reported a key press of
// any kind on its gabbo bus or in its key trace, -1 when never or not wired.
// Logged with every decision so a bundle shows whether that signal goes with
// a volume key on a chassis whose trace does not name it.
func (s *Server) userActivityAgoMs(now time.Time) int64 {
	if s.userActivityFn == nil {
		return -1
	}
	at := s.userActivityFn()
	if at.IsZero() {
		return -1
	}
	return now.Sub(at).Milliseconds()
}

// acceptStepLocked takes a person's step from prev to level into the running
// gesture, or begins one. It returns the gesture when its member list still
// has to be read, for the caller to start groupFollowLoad once f.mu is
// released. Caller holds f.mu.
func (s *Server) acceptStepLocked(prev, level int, how string, now time.Time) *groupFollowGesture {
	f := &s.groupFollow
	g := f.gesture
	// A gesture whose writes are still on their way is kept even past the
	// pause: a second set of writers for the same members could deliver an
	// older level after a newer one.
	if g == nil || (now.Sub(g.lastAt) > groupFollowGestureTTL && g.inFlight == 0 && !g.loading) {
		g = &groupFollowGesture{anchor: prev, from: prev}
		f.gesture = g
	}
	g.level, g.lastAt, g.how = level, now, how
	if g.ready {
		s.applyGestureLocked(g)
		return nil
	}
	if g.loading {
		return nil
	}
	g.loading = true
	return g
}

// applyGestureLocked sets every member's wanted level from the main speaker's
// newest one and starts a writer for each member that has none running.
// Caller holds f.mu.
func (s *Server) applyGestureLocked(g *groupFollowGesture) {
	for _, m := range g.members {
		want := followTarget(m.base, g.anchor, g.level)
		if want == m.want {
			continue
		}
		m.want = want
		if !m.busy {
			m.busy = true
			g.inFlight++
			go s.groupFollowWrite(g, m)
		}
	}
}

// groupFollowLoad begins a gesture: after the debounce it finds the members
// that are in the live group and reads the level each one has, which is the
// baseline the whole gesture offsets from. Runs once per gesture and ends.
func (s *Server) groupFollowLoad(g *groupFollowGesture) {
	t := s.groupFollowTimes()
	time.Sleep(t.debounce)
	f := &s.groupFollow
	abandon := func(reason string, warn bool) {
		f.mu.Lock()
		if f.gesture == g {
			f.gesture = nil
		}
		from, to := g.from, g.level
		f.skipped++
		f.lastSkip = reason
		f.mu.Unlock()
		if warn {
			s.logger.Warn("group volume: could not pass this speaker's volume change on to the group", "reason", reason, "from", from, "to", to)
			return
		}
		s.logger.Info("group volume: not passing this speaker's volume change on to a group", "reason", reason, "from", from, "to", to)
	}
	z, why, _ := s.followDoc()
	if why != "" {
		abandon(why, false)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), t.memberBudget)
	defer cancel()
	live, why := s.followLiveMembers(ctx, z)
	if why != "" {
		abandon(why, false)
		return
	}
	// The baseline: what each member plays at now. Read at once, a silent
	// member costs one timeout and not one per member.
	members := make([]*groupFollowMember, len(live))
	var wg sync.WaitGroup
	for i, lm := range live {
		wg.Add(1)
		go func(i int, lm followLiveMember) {
			defer wg.Done()
			v, err := s.readMemberVolume(ctx, lm.addr.String())
			switch {
			case err != nil:
				s.logger.Warn("group volume: a member did not answer, leaving it out of this volume change",
					"member", lm.name, "ip", lm.addr.String(), "err", err)
			case v.Muted:
				s.logger.Info("group volume: a member is muted, leaving it out of this volume change",
					"member", lm.name, "ip", lm.addr.String(), "level", v.Actual)
			default:
				members[i] = &groupFollowMember{name: lm.name, addr: lm.addr, base: v.Actual, want: v.Actual, sent: v.Actual}
			}
		}(i, lm)
	}
	wg.Wait()
	kept := make([]*groupFollowMember, 0, len(members))
	for _, m := range members {
		if m != nil {
			kept = append(kept, m)
		}
	}
	if len(kept) == 0 {
		abandon("no member of the group could take it (none answered, or all are muted)", true)
		return
	}
	f.mu.Lock()
	g.members = kept
	g.loading, g.ready = false, true
	from, to, how := g.from, g.level, g.how
	s.applyGestureLocked(g)
	f.mu.Unlock()
	s.logger.Info("group volume: the main speaker's volume keys move the permanent group",
		"group", z.Name, "from", from, "to", to, "members", len(kept), "storedMembers", len(z.Slaves),
		"why", how, "userActivityAgoMs", s.userActivityAgoMs(time.Now()))
}

// groupFollowWrite is one member's writer. It runs while that member's wanted
// level differs from the one last sent, one request at a time, always with
// the newest level, and ends when the two agree. So a held key costs a member
// as many requests as it can answer, and never a queue of them.
func (s *Server) groupFollowWrite(g *groupFollowGesture, m *groupFollowMember) {
	t := s.groupFollowTimes()
	f := &s.groupFollow
	for {
		f.mu.Lock()
		want, was := m.want, m.sent
		if want == was {
			m.busy = false
			g.inFlight--
			f.mu.Unlock()
			return
		}
		masterFrom, masterTo := g.from, g.level
		f.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), t.memberBudget)
		err := s.setMemberVolume(ctx, m.addr, want)
		cancel()

		f.mu.Lock()
		// Sent either way: a member that did not take the level is not
		// hammered with it. The next step tries again with the level that is
		// right then.
		m.sent = want
		if err == nil {
			f.passed++
		}
		f.mu.Unlock()
		if err != nil {
			s.logger.Warn("group volume: a member could not be reached, it keeps its level",
				"member", m.name, "ip", m.addr.String(), "wanted", want, "was", was, "err", err)
			continue
		}
		s.logger.Info("group volume: passed the main speaker's volume change on to a member",
			"member", m.name, "ip", m.addr.String(), "memberFrom", was, "memberTo", want,
			"memberBase", m.base, "masterFrom", masterFrom, "masterTo", masterTo)
	}
}

// followLiveMember is a member of the stored group that plays along now.
type followLiveMember struct {
	name string
	addr netip.Addr
}

// followLiveMembers returns the stored members that are in this speaker's
// live zone right now, or why nobody is to be written.
//
// This speaker has to LEAD that zone: a member can hold a copy of the group
// document, and its firmware names the same main speaker, so the deviceIDs
// decide (liveGroupView explains why senderIsMaster cannot). A stored member
// the firmware does not list is asleep, was taken out or plays on its own
// (groupView's rule for the phone's slider, Gaestebad 2026-09-06), and keeps
// its level. When this speaker's own zone cannot be read, each member is
// asked whose zone it is in instead of trusting the document blindly.
func (s *Server) followLiveMembers(ctx context.Context, z zones.Zone) ([]followLiveMember, string) {
	if s.boxHost == "" && s.rejoinLiveZoneFn == nil {
		return nil, "the speaker's address is not known"
	}
	self := strings.TrimSpace(s.rejoinSelfDeviceID(ctx))
	if self == "" {
		return nil, "this speaker did not say who it is, so it is not known whether it leads the group"
	}
	if !strings.EqualFold(self, strings.TrimSpace(z.Master)) {
		return nil, "this speaker is a member of the stored group, its own volume stays local"
	}
	live, err := s.rejoinLiveZone(ctx, s.boxHost)
	if err == nil && strings.TrimSpace(live.Master) == "" {
		return nil, "the group is not playing together right now (the members are asleep or out)"
	}
	if err == nil && !strings.EqualFold(strings.TrimSpace(live.Master), self) {
		return nil, "this speaker follows another speaker right now"
	}
	inLiveIP := map[string]string{}
	inLiveID := map[string]string{}
	for _, m := range live.Members {
		ip := strings.TrimSpace(m.IP)
		if ip != "" {
			inLiveIP[ip] = ip
		}
		if id := strings.ToUpper(strings.TrimSpace(m.DeviceID)); id != "" {
			inLiveID[id] = ip
		}
	}
	var out []followLiveMember
	for _, m := range z.Slaves {
		ip := strings.TrimSpace(m.IP)
		id := strings.ToUpper(strings.TrimSpace(m.DeviceID))
		if err == nil {
			// IP first, deviceID second: a two-chip chassis is listed under
			// another deviceID than discovery stored (newlyJoinedMembers).
			// The firmware's address is the fresher one of the two.
			liveIP, ok := inLiveIP[ip]
			if !ok && id != "" {
				liveIP, ok = inLiveID[id]
			}
			if !ok {
				s.logger.Info("group volume: a stored member is not in the live group (asleep, taken out, or playing on its own), it keeps its level",
					"member", s.memberLogName(m), "ip", ip)
				continue
			}
			if liveIP != "" {
				ip = liveIP
			}
		} else if ip == "" || !strings.EqualFold(strings.TrimSpace(s.rejoinReadZoneMaster(ctx, ip)), self) {
			s.logger.Info("group volume: this speaker's zone could not be read and a stored member does not report following it, it keeps its level",
				"member", s.memberLogName(m), "ip", ip, "err", err)
			continue
		}
		addr, ok := lanPeerAddr(ip)
		if !ok {
			continue // nothing to call without a LAN address
		}
		out = append(out, followLiveMember{name: s.memberLogName(m), addr: addr})
	}
	if len(out) == 0 {
		return nil, "no stored member is in the live group right now (asleep, taken out, or playing on its own)"
	}
	return out, ""
}

// memberLogName is a member's name for the log: the one the peer list knows,
// else its address.
func (s *Server) memberLogName(m zones.Member) string {
	if m.IP == "" {
		return m.DeviceID
	}
	return s.peerName(m)
}

// GroupVolumeFollowSnapshot is the group_volume_follow debug section: which of
// the two ways decides on this speaker, and what happened last.
func (s *Server) GroupVolumeFollowSnapshot() map[string]any {
	_, why, _ := s.followDoc()
	traceHealthy := s.volumeKeyTraceHealthyFn != nil && s.volumeKeyTraceHealthyFn()
	f := &s.groupFollow
	f.mu.Lock()
	defer f.mu.Unlock()
	decides := "exclusion: a volume change no STR writer explains"
	if f.keysTraced && traceHealthy {
		decides = "key identity: a volume key in the speaker's key trace"
	}
	out := map[string]any{
		"decidesBy":           decides,
		"volumeKeysTraced":    f.keysTraced,
		"keyTraceHealthy":     traceHealthy,
		"leadsPermanentGroup": why == "",
		"notFollowingBecause": why,
		"lastLevel":           f.last,
		"lastLevelKnown":      f.lastKnown,
		"memberWrites":        f.passed,
		"notPassedOn":         f.skipped,
		"lastNotPassedOn":     f.lastSkip,
	}
	if !f.keyAt.IsZero() {
		out["lastVolumeKeyAgoSec"] = int(time.Since(f.keyAt).Seconds())
	}
	if g := f.gesture; g != nil {
		out["lastGesture"] = map[string]any{
			"from": g.from, "to": g.level, "members": len(g.members), "why": g.how,
			"agoSec": int(time.Since(g.lastAt).Seconds()),
		}
	}
	return out
}
