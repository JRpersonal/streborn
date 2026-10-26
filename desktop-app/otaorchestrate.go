package main

// The speaker update, end to end, in Go.
//
// Until now the sequence lived in the frontend: runBoxUpdate pushed the agent,
// polled the version, held the stability window, classified a miss, retried the
// one class of failure a retry fixes and then delivered the Spotify engine, and
// the whole-house batch wrapped that in a gate and a per-row renderer. The
// backend only ever saw one call at a time and never knew it was inside an
// update. That had two costs: the live harness (ota_live_contract_test.go)
// could not drive the real sequence and re-implemented it, and every rule the
// field taught was pinned by tests that read main.js as text.
//
// Now one Go method runs a speaker's update from the first byte to the engine
// confirmation, and the fleet update is a Go loop over it. The frontend keeps
// what only a window can do: the pre-checks and their dialogs, the mapping of
// a phase to a sentence, and the rendering. Every number in otaPolicy is a
// field measurement; the comments beside the steps say which one.

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	wailsrt "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Phase ids, exactly the ones the frontend already maps to text.
const (
	phaseUploading       = "uploading"
	phaseRebooting       = "rebooting"
	phaseVerifying       = "verifying"
	phaseSettling        = "settling"
	phaseRetrying        = "retrying"
	phaseConfirmed       = "confirmed"
	phaseSpotify         = "spotify"
	phaseEngineQueued    = "engineQueued"
	phaseEngineUploading = "engineUploading"
)

// Outcome ids, one per row of the whole-house panel and one per toast of the
// single-speaker button.
const (
	outcomeDone       = "done"       // agent updated, engine present or freshly delivered
	outcomePartial    = "partial"    // agent updated, the engine could not be delivered in-window
	outcomeTimeout    = "timeout"    // upload accepted, the box never confirmed the new build
	outcomeAgentGone  = "agentGone"  // the speaker answers its own Bose API, STR does not run on it
	outcomeBoxInSetup = "boxInSetup" // the speaker is in Bose's own out-of-box setup
	outcomeFailed     = "failed"     // the push was cleanly rejected, or an engine-only repair failed
)

// unmetSpotifyEngine names the half a partial outcome is missing.
const unmetSpotifyEngine = "spotify-engine"

// unmetNoEmbeddedEngine is the engine-only row's reason when this build
// carries no engine to deliver: the row is deferred, not failed.
const unmetNoEmbeddedEngine = "no-embedded-engine"

// updatePhaseEvent is the Wails event every phase change goes out on.
const updatePhaseEvent = "box:update:phase"

// updatePhase is one step of a speaker's update, as the frontend sees it. The
// phase ids are the ones main.js has always rendered; the other fields carry
// what that phase's sentence needs (a countdown, the live state of the box) and,
// on a terminal phase, the outcome.
type updatePhase struct {
	Host            string `json:"host"`
	Phase           string `json:"phase"`
	Attempt         int    `json:"attempt"`
	RemainingMs     int64  `json:"remainingMs"`
	Reachable       bool   `json:"reachable"`
	Version         string `json:"version"`
	Engine          string `json:"engine"`
	Outcome         string `json:"outcome"`
	EngineDelivered bool   `json:"engineDelivered"`
	EngineTooFull   bool   `json:"engineTooFull"`
	Quiet           bool   `json:"quiet"`
	Unmet           string `json:"unmet"`
	Err             string `json:"err"`
}

// UpdateOutcome is what one speaker's update resolved to. Bound: it is the
// result of RunSpeakerUpdate and one row of RunFleetUpdate.
type UpdateOutcome struct {
	Host string `json:"host"`
	Name string `json:"name"`
	// EngineOnly marks a fleet row that only had the engine put back.
	EngineOnly bool `json:"engineOnly"`
	// Outcome is one of done, partial, timeout, agentGone, boxInSetup, failed.
	Outcome string `json:"outcome"`
	// Version is the speaker's version document at the end: the confirmed
	// reading for a done update, the last one for a partial. Nil when the
	// box never confirmed.
	Version map[string]string `json:"version"`
	// Confirmed is true when THIS run confirmed the agent on the new build.
	// False on the engine-only skip, where nothing was pushed; the frontend's
	// loop breaker is cleared only on a confirmation.
	Confirmed bool `json:"confirmed"`
	// Verdict is ClassifyOTAResult's word when the verify window expired
	// (not-landed, landed-not-running, swap-failed, unreachable, agent-gone,
	// box-in-setup), fed to the frontend's loop breaker. Empty otherwise.
	Verdict         string `json:"verdict"`
	EngineDelivered bool   `json:"engineDelivered"`
	EngineTooFull   bool   `json:"engineTooFull"`
	// Quiet asks the frontend not to announce the result: STR is being removed
	// from this speaker and the engine step was skipped on purpose.
	Quiet bool `json:"quiet"`
	// Unmet names what a partial outcome is missing.
	Unmet string `json:"unmet"`
	// Attempts counts the full push cycles (1, or 2 after the one retry).
	Attempts int    `json:"attempts"`
	Err      string `json:"err"`
}

// FleetTarget is one speaker of a whole-house run. EngineOnly rows get the
// engine put back and nothing else written to them.
type FleetTarget struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	DeviceID   string `json:"deviceID"`
	Name       string `json:"name"`
	EngineOnly bool   `json:"engineOnly"`
}

// FleetOutcome is the per-speaker result of RunFleetUpdate, in target order.
type FleetOutcome struct {
	Rows []UpdateOutcome `json:"rows"`
}

// otaPolicy holds every timing of the sequence. The production values are
// field-learned and are NOT to be tuned without a new field reason; tests run
// the same sequence at a fast clock through this struct.
type otaPolicy struct {
	// verifyWindow is how long the box has to come back on the new build after
	// the push; verifyPoll is how often it is asked.
	verifyWindow time.Duration
	verifyPoll   time.Duration
	// stability is the bootstrap-reboot stability window: how long the box has
	// to stay reachable on the new version before it is believed.
	stability time.Duration
	// engineWindow bounds the whole Spotify engine step after the confirmation.
	engineWindow time.Duration
	// settleProbe, settleStable and settleMinUptime are waitForStableAgent's
	// gates: probe cadence, how long the agent must keep answering, and the
	// box uptime below which it is still in the reboot-prone post-OTA window.
	settleProbe     time.Duration
	settleStable    time.Duration
	settleMinUptime int
	// backoffBase and backoffCap shape the wait between engine passes:
	// min(cap, base * 2^(attempt-1)).
	backoffBase time.Duration
	backoffCap  time.Duration
	// maxAttempts bounds the full push cycles: the one retry on not-landed.
	maxAttempts int
	// phaseTick is how often a countdown phase is re-emitted while the
	// sequence sleeps, so the frontend's remaining-time text moves every
	// second without a timer of its own.
	phaseTick time.Duration
	// followEvery is how often a speaker that does not answer, or whose
	// address another speaker answers on, is looked for at another address
	// (otafollow.go). Zero looks on every pass.
	followEvery time.Duration
}

// otaProductionPolicy is the sequence as it runs against real speakers.
var otaProductionPolicy = otaPolicy{
	verifyWindow:    360 * time.Second,
	verifyPoll:      2 * time.Second,
	stability:       50 * time.Second,
	engineWindow:    600 * time.Second,
	settleProbe:     3 * time.Second,
	settleStable:    30 * time.Second,
	settleMinUptime: 150,
	backoffBase:     2 * time.Second,
	backoffCap:      30 * time.Second,
	maxAttempts:     2,
	phaseTick:       time.Second,
	followEvery:     15 * time.Second,
}

// otaSeams is everything the sequence reaches out to, so it can run against a
// fake speaker at a fast clock. Production wiring is in App.otaSeams.
type otaSeams struct {
	push          func(host string, port int) error
	ensureEngine  func(host string, port int) (string, error)
	readVersion   func(host string, port int) (map[string]string, error)
	classify      func(host string, port int) string
	recordOutcome func(host, verdict string)
	restoreGroup  func(host string, port int)
	// locate finds the address a speaker is on now (otafollow.go); nil
	// disables following. noteUnconfirmed hands an address-change verdict to
	// the late-confirmation memo, which ClassifyOTAResult does for the others.
	locate          func(id speakerIdentity, exclude string, sweep bool) (host string, port int, ok bool)
	noteUnconfirmed func(host, verdict string)
	recordIntent    func(host string, port int, deviceID, name string)
	clearIntent     func(host string, port int)
	// spawn runs fn without waiting for it (the group restore is fire and
	// forget). Tests run it inline.
	spawn func(fn func())
	sleep func(d time.Duration)
	now   func() time.Time
	// emit delivers a phase to the frontend; nil when there is no window.
	emit func(p updatePhase)
	// taps is where the byte-progress of a push is observed, so the fleet
	// gate can release the moment a push is on the wire.
	taps *progressTaps
	// What this build carries, for the "is the speaker on it" questions.
	appVersion, appBuild, appSha string
	logger                       *slog.Logger
}

// progressTaps lets the fleet gate watch one speaker's upload progress. The
// transfer emitter calls notify for every report it makes, before the Wails
// guard, so a headless run sees the same signal a window does.
type progressTaps struct {
	mu   sync.Mutex
	next int
	taps map[string]map[int]func(pct int)
}

// add registers fn for host and returns its removal.
func (t *progressTaps) add(host string, fn func(pct int)) (remove func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.taps == nil {
		t.taps = map[string]map[int]func(pct int){}
	}
	if t.taps[host] == nil {
		t.taps[host] = map[int]func(pct int){}
	}
	id := t.next
	t.next++
	t.taps[host][id] = fn
	return func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if m := t.taps[host]; m != nil {
			delete(m, id)
			if len(m) == 0 {
				delete(t.taps, host)
			}
		}
	}
}

// notify hands one progress reading to every tap on host.
func (t *progressTaps) notify(host string, pct int) {
	if t == nil || host == "" {
		return
	}
	t.mu.Lock()
	fns := make([]func(int), 0, len(t.taps[host]))
	for _, fn := range t.taps[host] {
		fns = append(fns, fn)
	}
	t.mu.Unlock()
	for _, fn := range fns {
		fn(pct)
	}
}

// uploadGate serialises the network-heavy part of an update while leaving
// everything else free to overlap.
//
// Updating a house used to run two speakers at a time from start to finish, so
// a speaker that had already been written to held its slot for the whole of its
// restart, four to six minutes in which it needs nothing from the network and
// nobody else may start. Six speakers took the best part of an hour, almost all
// of it spent watching speakers reboot one pair at a time (#390).
//
// Only the pushes actually compete: two concurrent ~16 MB streams saturate a
// weak shared access point and trip the 60 s upload-stall watchdog, which is
// what the old limit was protecting against. Restarting and waiting cost no
// bandwidth at all. So the pushes queue here and everything else runs at once,
// which turns a house of six into roughly one upload after another with all the
// restarts happening in parallel behind them.
type uploadGate struct {
	slots chan struct{}
	// pending counts the batch's agent pushes that have not happened yet. An
	// engine restore after a failed update waits for it to reach zero before
	// it queues its ~16 MB: the other speakers' agent updates go first, and
	// the restore does not hold them up over the link that just failed.
	mu      sync.Mutex
	pending int
}

// expectPushes announces n agent pushes the batch is still going to make.
func (g *uploadGate) expectPushes(n int) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.pending += n
	g.mu.Unlock()
}

// pushSettled marks one announced agent push as done (sent, failed, or never
// needed).
func (g *uploadGate) pushSettled() {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.pending > 0 {
		g.pending--
	}
	g.mu.Unlock()
}

// pendingPushes is how many announced agent pushes are still to come.
func (g *uploadGate) pendingPushes() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pending
}

func newUploadGate(limit int) *uploadGate {
	if limit < 1 {
		limit = 1
	}
	return &uploadGate{slots: make(chan struct{}, limit)}
}

func (g *uploadGate) acquire() { g.slots <- struct{}{} }
func (g *uploadGate) release() { <-g.slots }

// run holds the gate for the duration of fn and always gives it back, so one
// speaker failing mid-push cannot strand the queue behind it.
func (g *uploadGate) run(fn func()) {
	if g == nil {
		fn()
		return
	}
	g.acquire()
	defer g.release()
	fn()
}

// Error classifiers, the same expressions the frontend used.

// lostReplyErr matches a push rejection that is not a failure: the reply was
// lost after the upload. A timeout-class rejection ("deadline exceeded ...
// while reading body", common on a slow link or with an HTTP-inspecting suite
// like Norton) does NOT mean the OTA failed: the box may still be applying it,
// so the sequence falls through to the poll. A clean reject (binary definitely
// refused) is a real failure.
var lostReplyErr = regexp.MustCompile(`(?i)deadline exceeded|client\.timeout|while reading body`)

// spaceRefusal is the engine push refused for lack of room. The agent says
// "insufficient nand", the transport says "no space" or carries the 507 status.
var spaceRefusal = regexp.MustCompile(`(?i)insufficient nand|no space|507`)

// engineStreamDrop matches errors that mean the ~16 MB engine push was cut
// mid-stream (box rebooting or its network stack folding under the write):
// re-streaming immediately races the next reboot with the very payload that
// cut the last one (deqw's ST20 died exactly this way, 2026-07-12).
var engineStreamDrop = regexp.MustCompile(`(?i)broken pipe|connection reset|deadline exceeded|forcibly closed|host is down|unexpected eof|wsarecv`)

// noEmbeddedEngine is EnsureSpotifyEngine's answer on a build that carries
// only the engine stub.
var noEmbeddedEngine = regexp.MustCompile(`(?i)no embedded engine`)

// engineSkipped is EnsureSpotifyEngine's answer while STR is being removed.
var engineSkipped = regexp.MustCompile(`(?i)^skipped:`)

// agentCurrentEngineMissing reports whether a speaker already runs exactly the
// agent this app would push and only lacks the Spotify engine. Such a speaker
// needs the engine, not another agent upload and the reboot that comes with it.
//
// The running agent's checksum decides when both sides know it. It is the
// running one on purpose: the one on disk differs exactly when a push landed
// and did not boot, and that speaker does need the agent again. Without a
// checksum, version and build must both match.
func agentCurrentEngineMissing(v map[string]string, appVersion, appBuild, appSha string) bool {
	if v == nil || v["goLibrespot"] != "missing" {
		return false
	}
	if appSha != "" && v["agentRunningSha256"] != "" {
		return v["agentRunningSha256"] == appSha
	}
	return appBuild != "" && appVersion != "" && v["build"] == appBuild && v["version"] == appVersion
}

// speakerReachedTarget answers the only question that matters at the end of an
// update: is this speaker actually where it was meant to be. It returns which
// half is outstanding ("agent", "engine", "unreachable") so the caller can say
// so rather than showing a spinner with no explanation.
//
// It exists because judging on one half is how a run lies. A speaker whose
// Spotify engine survived the reboot reports the engine present within seconds,
// while its agent is still being replaced, and anything that stops looking at
// that moment declares success on the old software. The mirror case is just as
// real: the agent lands and the engine is still missing. Both halves, always.
//
// Learned from a fleet run on 2026-07-29 where a Portable passed on the old
// build and only finished minutes later. Users hit the same on slow speakers.
//
// The binary the speaker is RUNNING first, then the version string. "the
// version string moved" cannot be satisfied by a push of the SAME version, and
// such a push is not harmless: a speaker that has the Spotify engine is by
// definition NAND-tight, so the agent drops the engine to make the update fit.
// Judging by the string then returns before the engine is put back, and a
// working Spotify install is destroyed by a re-push that was meant to change
// nothing. Four of six speakers in one household ended up that way after the
// v0.9.88 stamp skew invited re-push after re-push.
func speakerReachedTarget(live map[string]string, preVersion string, wantEngine bool, appSha string) (done bool, missing string) {
	if live == nil {
		return false, "unreachable"
	}
	agentDone := (appSha != "" && live["agentRunningSha256"] == appSha) ||
		(live["version"] != "" && (preVersion == "" || live["version"] != preVersion))
	engineDone := !wantEngine || live["goLibrespot"] == "present"
	if !agentDone {
		return false, "agent"
	}
	if !engineDone {
		return false, "engine"
	}
	return true, ""
}

// updateConfirms is the predicate the post-push poll runs every two seconds for
// up to six minutes: does this version reading prove the update took effect.
//
// The binary the speaker is RUNNING, first, because that is the only answer a
// clock cannot spoil. Without it the v0.9.88 stamp skew made a successful update
// wait out the whole 360 s window and then report "the update did not take
// effect", which was false and told the user to stop trying.
//
// Not agentBinarySha256: that is what lies on the box's DISK, and the one case
// where the two differ is a push that landed and then did not boot (#381).
// Reading the disk here would report that rollback as a success.
//
// After the hash: the app's own build stamp, then "no longer the pre-OTA build",
// then "no longer the pre-OTA version", which survive app/agent stamp drift
// and an agent too old to report a hash.
func updateConfirms(v map[string]string, appSha, appBuild, preBuild, preVersion string) bool {
	if v == nil {
		return false
	}
	if appSha != "" && v["agentRunningSha256"] == appSha {
		return true
	}
	if appBuild != "" && v["build"] == appBuild {
		return true
	}
	if preBuild != "" && v["build"] != "" && v["build"] != preBuild {
		return true
	}
	if preVersion != "" && v["version"] != "" && v["version"] != preVersion {
		return true
	}
	return false
}

// engineBackoff is the wait between engine passes: exponential (2s -> 30s
// cap). Each failed attempt costs the box a probe, and during a slow agent
// start hammering it every 2s just prolongs the settling (#270).
func (p otaPolicy) engineBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := p.backoffBase
	for i := 1; i < attempt && d < p.backoffCap; i++ {
		d *= 2
	}
	if d > p.backoffCap {
		d = p.backoffCap
	}
	return d
}

// speakerUpdater runs one speaker's sequence. One per speaker per run; the
// fleet shares the gate and the taps between them.
type speakerUpdater struct {
	host     string
	port     int
	deviceID string
	name     string
	seams    *otaSeams
	policy   otaPolicy
	gate     *uploadGate
	onPhase  func(updatePhase)
	attempts int
	// countdown is the phase a sleeping step keeps re-emitting with a fresh
	// remaining time, or nil when no countdown is running.
	countdown *updatePhase
	deadline  time.Time
	// settlePush, when set (a batch row), tells the gate this row's agent
	// push is behind it. Idempotent.
	settlePush func()

	// live and livePort are the address the speaker is on NOW. host stays the
	// address the update started on: the window's row, the journal thread and
	// the update record are keyed by it. They differ only after the speaker
	// got a new address mid-update and was followed there (otafollow.go).
	live     string
	livePort int
	// ident is who the speaker said it was before the push.
	ident speakerIdentity
	// foreign is true while the last answer at live came from another
	// speaker; sawForeign stays true until the speaker is followed.
	foreign, sawForeign bool
	foreignNoted        map[string]bool
	lastLocate          time.Time
	// intentHost/intentPort are where the update record is kept once it was
	// moved with the speaker (empty: at host). intentCleared is set once the
	// record was cleared, so a later move does not write it again.
	intentHost    string
	intentPort    int
	intentCleared bool
}

// addr is the address the speaker is on now.
func (u *speakerUpdater) addr() (string, int) {
	if u.live == "" {
		u.live, u.livePort = u.host, u.port
	}
	return u.live, u.livePort
}

// agentPushSettled reports this row's agent push as done to the batch gate.
func (u *speakerUpdater) agentPushSettled() {
	if u.settlePush != nil {
		u.settlePush()
	}
}

func (u *speakerUpdater) log() *slog.Logger {
	if u.seams != nil && u.seams.logger != nil {
		return u.seams.logger
	}
	return slog.Default()
}

// phase reports one step. A phase with a RemainingMs becomes the running
// countdown and is re-emitted on every tick of a sleep; any other phase ends
// the countdown.
func (u *speakerUpdater) phase(p updatePhase) {
	p.Host = u.host
	if p.RemainingMs > 0 {
		cp := p
		u.countdown = &cp
		u.deadline = u.seams.now().Add(time.Duration(p.RemainingMs) * time.Millisecond)
	} else {
		u.countdown = nil
	}
	u.deliver(p)
}

func (u *speakerUpdater) deliver(p updatePhase) {
	if u.seams.emit != nil {
		u.seams.emit(p)
	}
	if u.onPhase != nil {
		u.onPhase(p)
	}
}

// sleep waits d, re-emitting the running countdown at least every phaseTick so
// the remaining time counts down smoothly instead of jumping in steps. The
// frontend formats the text; it keeps no timer of its own.
func (u *speakerUpdater) sleep(d time.Duration) {
	tick := u.policy.phaseTick
	if u.countdown == nil || tick <= 0 || d <= tick {
		u.seams.sleep(d)
		if u.countdown != nil {
			u.tickCountdown()
		}
		return
	}
	end := u.seams.now().Add(d)
	for {
		left := end.Sub(u.seams.now())
		if left <= 0 {
			return
		}
		step := tick
		if left < step {
			step = left
		}
		u.seams.sleep(step)
		if u.countdown == nil {
			return
		}
		u.tickCountdown()
	}
}

func (u *speakerUpdater) tickCountdown() {
	p := *u.countdown
	left := u.deadline.Sub(u.seams.now()).Milliseconds()
	if left < 0 {
		left = 0
	}
	p.RemainingMs = left
	u.deliver(p)
}

func (u *speakerUpdater) record(verdict string) {
	if u.seams.recordOutcome != nil {
		u.seams.recordOutcome(u.host, verdict)
	}
}

func (u *speakerUpdater) clearIntent() {
	u.intentCleared = true
	if u.seams.clearIntent != nil {
		h, p := u.intentAt()
		u.seams.clearIntent(h, p)
	}
}

// intentAt is the address the update record is kept under: where the update
// started, or where the speaker was followed to.
func (u *speakerUpdater) intentAt() (string, int) {
	if u.intentHost == "" {
		return u.host, u.port
	}
	return u.intentHost, u.intentPort
}

// moveIntent keeps the update record with the speaker when it was followed to
// a new address. Left at the old one, the record would be checked against the
// speaker that now holds that address: either cleared because THAT speaker is
// current (and this one's missing engine never flagged), or flagged under this
// speaker's name for the other one.
func (u *speakerUpdater) moveIntent(host string, port int) {
	if u.intentCleared || u.seams.recordIntent == nil || u.seams.clearIntent == nil {
		return
	}
	oh, op := u.intentAt()
	if oh == host {
		return
	}
	id := u.deviceID
	if id == "" && len(u.ident.ids) > 0 {
		id = u.ident.ids[0]
	}
	u.seams.clearIntent(oh, op)
	u.seams.recordIntent(host, port, id, u.name)
	u.intentHost, u.intentPort = host, port
}

// gatedPush serialises only the BYTE push, not the box's whole reboot.
//
// UpdateBoxAgent does not return when the bytes land: the box replies, then
// reboots ~1.5 s later, and that reboot usually eats the reply, so the call then
// sits on a dead connection for up to ~107 s before its reply window times out
// (#466). Holding the upload gate for all of that keeps the next box from
// starting its push long after this box's bytes were delivered and it is
// already rebooting on its own, the serial-restart stall the gate exists to
// kill. So release the gate the moment the upload bytes are on the wire
// (progress pct >= 100, the same signal the overlay uses to flip a row to
// "restarting") and await the real UpdateBoxAgent result OUTSIDE the gate. A
// box that never streams pct >= 100 (a fast failure, or a chassis that does not
// emit progress) falls back to releasing when the call settles, exactly the old
// behavior, never worse. The single-speaker path (no gate) has nothing to queue
// behind, so it runs the call directly.
func (u *speakerUpdater) gatedPush() error {
	host, port := u.addr()
	if u.gate == nil {
		return u.seams.push(host, port)
	}
	u.gate.acquire()
	// A retry can wait here behind the other speakers' pushes while this one
	// is already back from its restart; its address may have gone to another
	// speaker meanwhile. Ask again right before sending.
	if u.attempts > 1 {
		if _, err := u.read(); errors.Is(err, errOtherSpeaker) || u.foreign {
			u.gate.release()
			return errOtherSpeaker
		}
		host, port = u.addr()
	}
	released := make(chan struct{})
	var once sync.Once
	free := func() {
		once.Do(func() {
			u.gate.release()
			close(released)
		})
	}
	untap := func() {}
	if u.seams.taps != nil {
		untap = u.seams.taps.add(host, func(pct int) {
			if pct >= 100 {
				free()
			}
		})
	}
	errc := make(chan error, 1)
	go func() { errc <- u.seams.push(host, port) }()
	select {
	case err := <-errc:
		untap()
		free()
		return err
	case <-released:
	}
	untap()
	return <-errc
}

// run is the whole sequence for one speaker; attempt restarts it once on the
// one verdict a retry fixes.
func (u *speakerUpdater) run(attempt int) UpdateOutcome {
	u.attempts = attempt
	base := UpdateOutcome{Host: u.host, Name: u.name, Attempts: attempt}
	appSha, appBuild, appVersion := u.seams.appSha, u.seams.appBuild, u.seams.appVersion
	// Record what the box runs RIGHT NOW; the post-OTA success signal is
	// "reachable AND no longer this pre-OTA build", which survives app/agent
	// build-stamp drift. A failed read leaves both empty and the hash and the
	// app build decide.
	preBuild, preVersion := "", ""
	// The first reading is also who the speaker is: every later answer is held
	// against it, so a speaker that took this one's address while both
	// restarted is never taken for it (otafollow.go). A retry reads through
	// the same guard, at the address the speaker was followed to.
	var pv map[string]string
	var perr error
	if attempt == 1 && !u.ident.reported() {
		host, port := u.addr()
		pv, perr = u.seams.readVersion(host, port)
		if perr == nil && pv != nil {
			hints := u.ident.hints
			u.ident = identityFrom(pv)
			u.ident.hints = hints
		}
	} else {
		pv, perr = u.read()
		// The retry runs after the speaker restarted, so another speaker may
		// hold its address by now. Pushing there would put this update on
		// that speaker and restart it.
		if errors.Is(perr, errOtherSpeaker) || u.foreign {
			return u.addressChanged(base, attempt, "agent upload of the retry not sent")
		}
	}
	if perr == nil && pv != nil {
		preBuild, preVersion = pv["build"], pv["version"]
	} else {
		pv = nil
	}
	// A speaker that already runs exactly this agent and only lacks the Spotify
	// engine gets the engine and nothing else. Pushing the agent again bought
	// nothing and cost a reboot: on a slow chassis that reboot outlasts the
	// verify window, the engine step after it never ran, and the speaker came
	// back still without an engine, so every click on "Update" started the same
	// round again (a CineMate, three rounds of about eight minutes each,
	// 2026-10-10).
	if agentCurrentEngineMissing(pv, appVersion, appBuild, appSha) {
		b := pv["build"]
		if b == "" {
			b = "?"
		}
		u.record("agent upload skipped: the speaker already runs build " + b + ", only the Spotify engine is missing")
		return u.deliverEngine(base, pv, preVersion)
	}
	u.phase(updatePhase{Phase: phaseUploading, Attempt: attempt})
	err := u.gatedPush()
	u.agentPushSettled()
	if errors.Is(err, errOtherSpeaker) {
		return u.addressChanged(base, attempt, "agent upload of the retry not sent")
	}
	if err != nil {
		if !lostReplyErr.MatchString(err.Error()) {
			base.Outcome = outcomeFailed
			base.Err = err.Error()
			base = u.restoreEngineAfterIncompleteUpdate(base)
			u.phase(updatePhase{Phase: outcomeFailed, Attempt: attempt, Outcome: outcomeFailed, Err: base.Err})
			return base
		}
		u.log().Info("speaker update: push reply lost, the version poll decides", "host", u.host, "err", err)
	}
	u.phase(updatePhase{Phase: phaseRebooting, Attempt: attempt})
	deadline := u.seams.now().Add(u.policy.verifyWindow)
	var confirmed map[string]string
	// Bootstrap-reboot stability window: the first boot after an OTA can
	// deliberately reboot ONCE more (~35 s after the agent API is back) when the
	// new agent refreshed run-override.sh/rc.local on the NAND. Reporting
	// success on the first version match made the app say "done" while the
	// speaker went down again and blinked through another boot with no
	// explanation (ST300, 2026-07-09). Confirm only after the box stays
	// reachable on the new version for a full window; a drop inside the window
	// returns to waiting and the next match confirms immediately-ish (the
	// bootstrap reboot is one-time and loop-guarded on the agent side).
	var stableSince time.Time
	sawSecondDrop := false
	for u.seams.now().Before(deadline) {
		// Only while we are still waiting for the box to come back. Once it has
		// and we are merely holding it for the stability window, 'settling'
		// below owns the line: emitting both every pass made the text flap
		// between "restarting" and "back up, confirming" twice every two seconds.
		if stableSince.IsZero() {
			u.phase(updatePhase{Phase: phaseVerifying, Attempt: attempt, RemainingMs: deadline.Sub(u.seams.now()).Milliseconds()})
		}
		u.sleep(u.policy.verifyPoll)
		v, err := u.read()
		switch {
		case err == nil && updateConfirms(v, appSha, appBuild, preBuild, preVersion):
			now := u.seams.now()
			if stableSince.IsZero() {
				stableSince = now
			}
			if sawSecondDrop || now.Sub(stableSince) >= u.policy.stability {
				confirmed = v
			} else {
				// The speaker IS back on the new version; we are only holding it
				// for the stability window before believing it. Saying
				// "restarting" here reads as if the app had not noticed, which is
				// exactly how it looked to Jens watching two speakers that had
				// visibly already come back.
				u.phase(updatePhase{Phase: phaseSettling, Attempt: attempt,
					RemainingMs: (u.policy.stability - now.Sub(stableSince)).Milliseconds()})
			}
		case err == nil:
			stableSince = time.Time{}
		default:
			// Unreachable: either still mid-first-reboot, or the bootstrap reboot
			// just took the box down again after we already saw the new version.
			if !stableSince.IsZero() {
				sawSecondDrop = true
			}
			stableSince = time.Time{}
		}
		if confirmed != nil {
			break
		}
	}
	if confirmed == nil {
		// Another speaker answers at this one's address and this one was not
		// found at a new one in time. Classifying the address would judge the
		// OTHER speaker (it may well be on the new build, which reads as
		// "confirmed"), and the unreachable advice blames a firewall for what
		// is a router handing out new addresses. Say what happened; a later
		// discovery sighting of this speaker corrects the journal.
		if u.sawForeign {
			return u.addressChanged(base, attempt, "")
		}
		// Journal the real verdict and classify why (unreachable / not landed /
		// landed-but-not-running), feeding the loop breaker so an update that
		// cannot stick is not re-offered as a fresh one-click push forever.
		live, livePort := u.addr()
		cls := u.seams.classify(live, livePort)
		// "confirmed" = the box IS on the new build, it just crossed the verify
		// window late (slow boot / late reachability). Treating that as a timeout
		// made update-all report a perfectly updated ST10 as stuck (live
		// 2026-07-25, "confirmed late" at +6 min). Count it as updated and fall
		// through to the engine step like any confirmed box.
		if cls == "confirmed" {
			if v, err := u.read(); err == nil {
				confirmed = v
			}
		}
		// "not-landed" means the speaker took the whole file and the new software
		// never reached its disk. That is the write squeezing itself into the last
		// megabyte of a nearly full speaker: it stalls instead of failing, so the
		// speaker reboots onto its old version. It is marginal rather than
		// permanent, proven on two near-identical speakers where one stalled, and
		// the very same push then succeeded on a retry. So retry once here rather
		// than hand back a failure whose fix is "press the button again".
		//
		// Only this verdict. "landed-not-running" already knows an identical
		// re-push cannot help, and an unreachable speaker is switched off or gone
		// from the network, where retrying just hammers it.
		if confirmed == nil && cls == "not-landed" && attempt < u.policy.maxAttempts {
			u.record("retrying once: the software never reached the speaker disk (marginal write)")
			u.phase(updatePhase{Phase: phaseRetrying, Attempt: attempt + 1})
			return u.run(attempt + 1)
		}
		if confirmed == nil {
			base.Verdict = cls
			switch cls {
			// "agent-gone" is the speaker answering its OWN Bose web API while
			// STR's agent is not running on it. That is not a timeout and not a
			// network problem: the box is up, on the LAN, and reachable from this
			// PC. It is also the one outcome with an exact, reliable fix, so it
			// gets its own result instead of being folded into the generic "took
			// longer than expected" line that sent a user hunting through his
			// antivirus settings while three speakers sat there answering Bose's
			// ports (2026-08-22).
			case "agent-gone":
				base.Outcome = outcomeAgentGone
			// "box-in-setup" is the speaker running Bose's OWN out-of-box setup:
			// it leaves the network for minutes at a time while it does, so the
			// update may well have landed and confirmLateOTA corrects the journal
			// on its own. Its own outcome, because the advice is the opposite of
			// agentGone's: do NOT pull the plug on a speaker mid-setup (#873).
			case "box-in-setup":
				base.Outcome = outcomeBoxInSetup
			default:
				base.Outcome = outcomeTimeout
			}
			base = u.restoreEngineAfterIncompleteUpdate(base)
			u.phase(updatePhase{Phase: base.Outcome, Attempt: attempt, Outcome: base.Outcome})
			return base
		}
	}
	base.Confirmed = true
	b := confirmed["build"]
	if b == "" {
		b = "?"
	}
	u.record("confirmed: box is on build " + b + " (stability window passed)")
	// Put back the multiroom group the update reboot took apart. This is the one
	// point EVERY confirmed update reaches, in-window or late, which is why it
	// lives here and not next to the late-only ClassifyOTAResult call above.
	// Fire and forget: a speaker whose group cannot be rebuilt must not hold up
	// the rest of the update.
	//
	// Not for a speaker that changed its address: the remembered group names
	// the members by the addresses they had BEFORE the update, and one of them
	// now belongs to another speaker, so rebuilding it could pull the wrong
	// speaker into the group. The owner regroups by hand.
	if live, _ := u.addr(); live != u.host {
		u.record("group not rebuilt automatically: the speaker changed its address during the update, and the remembered group names it by the old one")
	} else if u.seams.restoreGroup != nil {
		host, port := u.host, u.port
		u.seams.spawn(func() { u.seams.restoreGroup(host, port) })
	}
	return u.deliverEngine(base, confirmed, preVersion)
}

// engineRestoreAttempts bounds the engine re-delivery after an update that did
// not complete. Two: one retry covers a stream the poor link that just killed
// the agent upload also cuts once, more would only keep a weak speaker busy.
const engineRestoreAttempts = 2

// restoreEngineAfterIncompleteUpdate puts the Spotify engine back on a speaker
// whose agent update did not complete but whose engine was already dropped to
// make room for it.
//
// The agent drops the engine on a NAND-tight speaker so the new binary fits,
// and the re-delivery used to live only behind a confirmed update
// (deliverEngine). An upload that died halfway therefore left the speaker on
// its old agent with no Spotify, and nothing ever put it back: a Portable on a
// poor Wi-Fi link timed out twice at about 2.5 of 18.3 MB in the 2026-10-25
// fleet roll and ended exactly so. The old agent is running and accepts the
// engine again once it clears its update-in-flight latch (every give-up path
// does, but only after its read of the dead upload fails: see the latch wait),
// so it goes back now, while the update run is still the one writing to this
// speaker.
//
// It acts only on the speaker's own word: goLibrespot "missing" together with
// goLibrespotDroppedForUpdate "true". An engine that was never installed, a
// speaker that does not answer, and the two outcomes where the speaker cannot
// take a delivery (STR's agent is not running, or Bose's setup is) are left
// as they are. The outcome of the update itself is not changed: it did not
// complete, and the frontend is told so as before.
func (u *speakerUpdater) restoreEngineAfterIncompleteUpdate(base UpdateOutcome) UpdateOutcome {
	if base.Outcome == outcomeAgentGone || base.Outcome == outcomeBoxInSetup {
		return base
	}
	// Through the identity guard: the engine goes to the speaker whose update
	// this is, never to another one that answers at its old address.
	live, err := u.read()
	if err != nil || live == nil || live["goLibrespot"] != "missing" || live["goLibrespotDroppedForUpdate"] != "true" {
		return base
	}
	u.record("update did not complete and the speaker reports its Spotify engine dropped for it: delivering the engine again to the running agent " + live["version"])
	u.log().Info("speaker update: update incomplete, the engine was dropped for it; putting it back",
		"host", u.host, "outcome", base.Outcome, "agent", live["version"])
	// In a batch, the other speakers' agent pushes go first. The restore is
	// ~16 MB over the link that just failed; queued in the shared gate ahead
	// of them it held every remaining speaker's update behind it. Bounded, so
	// a batch that never drains (it always does: every push ends) cannot park
	// the restore for good.
	if n := u.gate.pendingPushes(); n > 0 {
		u.record(fmt.Sprintf("engine re-delivery waits for the other speakers' updates to be sent first (%d still to go)", n))
		drainBy := u.seams.now().Add(u.policy.engineWindow)
		for u.gate.pendingPushes() > 0 && u.seams.now().Before(drainBy) {
			u.sleep(u.policy.settleProbe)
		}
	}
	// The speaker may still hold its update latch: on a link that went dead
	// rather than closed, the old agent sits in its read until TCP gives up
	// (minutes), and refuses the engine with 409 update-in-flight the whole
	// time. Those refusals send nothing, so they do not use up an attempt;
	// they are retried with backoff until the latch clears or latchBy passes.
	var latchBy time.Time
	latchWaits := 0
	for attempt := 1; attempt <= engineRestoreAttempts; {
		if attempt > 1 && latchWaits == 0 {
			u.sleep(u.policy.engineBackoff(attempt - 1))
		}
		if u.foreign {
			// The last answer at the address came from another speaker.
			u.record("engine re-delivery after the incomplete update stopped: another speaker answers at this speaker's address; the next update run delivers it")
			return base
		}
		u.phase(updatePhase{Phase: phaseEngineQueued, Attempt: attempt})
		var res string
		var engErr error
		sent := false
		u.gate.run(func() {
			eh, ep, ok := u.engineTarget()
			if !ok {
				return
			}
			sent = true
			u.phase(updatePhase{Phase: phaseEngineUploading, Attempt: attempt})
			res, engErr = u.seams.ensureEngine(eh, ep)
		})
		if !sent {
			if u.foreign {
				u.record("engine re-delivery after the incomplete update stopped: another speaker answers at this speaker's address; the next update run delivers it")
				return base
			}
			// Not answering at the moment the push was due: nothing was sent,
			// counted as a failed attempt.
			latchWaits = 0
			attempt++
			continue
		}
		if engErr == nil && res == EngineDeferredUpdateInFlight {
			if latchBy.IsZero() {
				latchBy = u.seams.now().Add(u.policy.engineWindow)
				u.record("engine re-delivery: the speaker still reports its update in flight; waiting for it to give up")
				u.log().Info("speaker update: engine restore deferred, the speaker still holds its update latch", "host", u.host)
			}
			if !u.seams.now().Before(latchBy) {
				u.record("engine re-delivery after the incomplete update did not succeed: the speaker kept reporting its update in flight; the next update run delivers it")
				return base
			}
			latchWaits++
			u.sleep(u.policy.engineBackoff(latchWaits))
			continue
		}
		latchWaits = 0
		attempt++
		if engErr != nil {
			m := engErr.Error()
			u.log().Warn("speaker update: engine re-delivery after the incomplete update failed",
				"host", u.host, "attempt", attempt-1, "err", engErr)
			if spaceRefusal.MatchString(m) {
				base.EngineTooFull = true
				u.record("engine re-delivery after the incomplete update refused for lack of room: " + m)
				return base
			}
			continue
		}
		if noEmbeddedEngine.MatchString(res) || engineSkipped.MatchString(res) {
			u.record("engine re-delivery after the incomplete update skipped: " + res)
			return base
		}
		check, cerr := u.read()
		if cerr == nil && check["goLibrespot"] == "present" {
			base.EngineDelivered = engineSent(res)
			u.record("engine back on the speaker after the incomplete update (" + res + ")")
			return base
		}
	}
	u.record("engine re-delivery after the incomplete update did not succeed; the next update run delivers it")
	return base
}

// deliverEngine is the second half of an update: the agent is on the target
// build (just confirmed, or already there) and the Spotify engine is put back
// if it is missing or outdated.
func (u *speakerUpdater) deliverEngine(base UpdateOutcome, confirmedVer map[string]string, preVersion string) UpdateOutcome {
	// The agent half is done and proven. Say so now rather than at the very
	// end: the engine step below can run for another ten minutes, and a user
	// watching a single speaker has earned the news that the update itself
	// landed.
	u.phase(updatePhase{Phase: phaseConfirmed, Attempt: base.Attempts, Reachable: true,
		Version: confirmedVer["version"], Engine: confirmedVer["goLibrespot"]})
	done := func(out UpdateOutcome) UpdateOutcome {
		u.phase(updatePhase{Phase: out.Outcome, Attempt: base.Attempts, Outcome: out.Outcome,
			Reachable: out.Version != nil, Version: out.Version["version"], Engine: out.Version["goLibrespot"],
			EngineDelivered: out.EngineDelivered, EngineTooFull: out.EngineTooFull, Quiet: out.Quiet, Unmet: out.Unmet})
		return out
	}
	// Post-reboot Spotify engine reconcile: covers the one-time pre-v0.8.22
	// upgrade case (#240, engine missing) AND a present-but-outdated engine on a
	// tight box whose pre-reboot staging was deferred (the old engine's space is
	// reclaimable only after the reboot). EnsureSpotifyEngine is cheap when the
	// engine is already current (one version GET, returns "current").
	if confirmedVer["goLibrespot"] == "" {
		u.clearIntent()
		base.Outcome = outcomeDone
		base.Version = confirmedVer
		return done(base)
	}
	// 10 min window, and every push attempt is gated on a settled box. The old
	// 240 s window with an ungated first push burned itself on exactly two
	// doomed ~107 s streams per OTA (#466): the box reliably reboots or resets
	// large uploads in its first post-OTA minutes, while a push at a genuinely
	// settled box completes in ~15 s. Waiting out the settling window first
	// costs a minute in the good case and wins the bad ones.
	engDeadline := u.seams.now().Add(u.policy.engineWindow)
	attempt := 0
	// What actually happened to the engine, so the caller can say it.
	// "Delivered" and "was already current" both end as a done update but only
	// one of them is worth telling the user about, and "no room left" is the one
	// case where the user has something to do about it.
	engineDelivered, engineTooFull := false, false
	for u.seams.now().Before(engDeadline) {
		attempt++
		// Report the live state on every pass, not just "attempt 3": the user is
		// being asked to wait, so they get to see WHAT is being waited for,
		// whether the speaker is answering at all, which build it runs and
		// whether the engine is there yet.
		live, lerr := u.read()
		if lerr != nil {
			live = nil
		}
		engine := "unknown"
		if live != nil && live["goLibrespot"] != "" {
			engine = live["goLibrespot"]
		}
		u.phase(updatePhase{Phase: phaseSpotify, Attempt: attempt,
			RemainingMs: engDeadline.Sub(u.seams.now()).Milliseconds(),
			Reachable:   live != nil, Version: live["version"], Engine: engine})
		// Already in the target state (another pass landed it, or the agent
		// hot-swapped it in): nothing left to do.
		if reached, _ := speakerReachedTarget(live, preVersion, true, u.seams.appSha); reached {
			u.clearIntent()
			base.Outcome = outcomeDone
			base.Version = live
			base.EngineDelivered = engineDelivered
			return done(base)
		}
		u.waitForStableAgent(engDeadline)
		// Never push at an address another speaker answers on: the engine
		// would land on that speaker, and this one would stay without it
		// (field case 2026-10-26). read() has already looked for this speaker
		// elsewhere; until it is found, wait.
		if u.foreign {
			u.sleep(u.policy.engineBackoff(attempt))
			continue
		}
		// "Spotify engine: missing, waiting for the target state" described the
		// speaker, not what the app was doing, so the ~16 MB delivery that
		// follows looked like nothing was happening (Jens, watching a live run).
		// Say that it is queued, then that it is being sent; the row's bar is
		// already fed by the transfer's own progress events.
		u.phase(updatePhase{Phase: phaseEngineQueued, Attempt: attempt})
		var engRes string
		var engErr error
		sent := false
		u.gate.run(func() {
			eh, ep, ok := u.engineTarget()
			if !ok {
				return
			}
			sent = true
			u.phase(updatePhase{Phase: phaseEngineUploading, Attempt: attempt})
			engRes, engErr = u.seams.ensureEngine(eh, ep)
		})
		if !sent {
			// Not answering, or another speaker answers at the address, at
			// the moment the push was due: nothing was sent. Wait and look
			// again.
			u.sleep(u.policy.engineBackoff(attempt))
			continue
		}
		if engErr == nil {
			fallback := live
			if fallback == nil {
				fallback = confirmedVer
			}
			// A build that carries no engine cannot deliver one, so waiting the
			// full window would only burn ten minutes to reach the same answer.
			if noEmbeddedEngine.MatchString(engRes) {
				u.clearIntent()
				base.Outcome = outcomeDone
				base.Version = fallback
				base.EngineDelivered = engineDelivered
				return done(base)
			}
			// STR is being removed from this speaker: the delivery was skipped on
			// purpose, and a "Spotify engine installed" toast in the middle of an
			// uninstall reads as a bug (report of 2026-09-06). Finish quietly.
			if engineSkipped.MatchString(engRes) {
				u.clearIntent()
				base.Outcome = outcomeDone
				base.Version = fallback
				base.EngineDelivered = false
				base.Quiet = true
				return done(base)
			}
			// "current" means nothing was sent: the engine was already the right
			// one.
			if engineSent(engRes) {
				engineDelivered = true
			}
			// Do not take the delivery's word for it. The update may only be
			// called finished when the speaker itself reports the state it was
			// supposed to end up in.
			if check, cerr := u.read(); cerr == nil && check["goLibrespot"] == "present" {
				u.clearIntent()
				base.Outcome = outcomeDone
				base.Version = check
				base.EngineDelivered = engineDelivered
				return done(base)
			}
			continue // delivered but not visible yet: stay in the loop
		}
		m := engErr.Error()
		// Too full even counting the reclaimable old engine: retrying cannot
		// help, only freeing space can. The agent update itself succeeded, so the
		// intent is CLEARED: keeping it "so the app finishes the job next time"
		// made the supervisor toast a permanent per-session nag on a speaker
		// whose state cannot change by itself (Walter's CineMate, green
		// "unvollständig" window on every update, 2026-08-25). This run's own
		// result already names what to free, and once space exists the next
		// update run delivers the engine anyway.
		if spaceRefusal.MatchString(m) {
			engineTooFull = true
			u.clearIntent()
			break
		}
		u.log().Warn("spotify engine delivery attempt failed (will retry)", "host", u.host, "attempt", attempt, "err", engErr)
		// Mid-stream drop: the box rebooted or reset the stream. Loop straight
		// back: the top-of-loop settle gate does the waiting (reachable, uptime
		// past the settling window, stable for 30 s).
		if engineStreamDrop.MatchString(m) {
			continue
		}
		u.sleep(u.policy.engineBackoff(attempt))
	}
	// The agent is on the target build but the engine is not there. The record
	// stays, so the next time this speaker is seen the app finishes the job
	// instead of leaving it half-done.
	base.Outcome = outcomePartial
	base.Version = confirmedVer
	base.Unmet = unmetSpotifyEngine
	base.EngineTooFull = engineTooFull
	return done(base)
}

// waitForStableAgent waits (bounded by deadline) until the box's agent answers
// again and KEEPS answering for settleStable, so the next engine attempt
// streams at a settled box instead of one mid-reboot.
//
// Agents that report their box uptime (uptimeSec, v0.9.20+) get two extra
// gates, learned from the #466 bundles where the FIRST post-confirm 16 MB push
// reliably died with a connection reset ~107 s in while a retry minutes later
// sailed through in ~15 s: the box must be past the reboot-prone post-OTA
// settling window (uptime >= settleMinUptime), and an uptime DROP between two
// probes is a reboot that plain reachability polling misses entirely (the box
// can be back up before the next probe), so it resets the stability clock.
// Older agents without uptimeSec keep the reachability-only behavior.
func (u *speakerUpdater) waitForStableAgent(deadline time.Time) bool {
	var up time.Time
	lastUptime := -1
	for u.seams.now().Before(deadline) {
		u.sleep(u.policy.settleProbe)
		v, err := u.read()
		if err != nil {
			up = time.Time{}
			lastUptime = -1
			continue
		}
		if uptime, ok := parseUptime(v["uptimeSec"]); ok {
			if uptime < lastUptime {
				up = time.Time{} // rebooted between probes
			}
			lastUptime = uptime
			if uptime < u.policy.settleMinUptime {
				continue // still in the settling window
			}
		}
		now := u.seams.now()
		if up.IsZero() {
			up = now
		}
		if now.Sub(up) >= u.policy.settleStable {
			return true
		}
	}
	return false
}

// parseUptime reads the agent's uptimeSec the way the frontend's parseInt did:
// an empty or non-numeric value means "not reported".
func parseUptime(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// otaOrchestration is the one-run-at-a-time latch for RunSpeakerUpdate and
// RunFleetUpdate. The frontend has its own (boxUpdateBusy / updateAllBusy),
// but a latch that lives behind the buttons is the one that also covers the
// headless harness and any future caller.
type otaOrchestration struct {
	mu      sync.Mutex
	running string
}

var otaOrch otaOrchestration

// claim marks a run as started, or refuses with the STR_BUSY token the window
// already knows how to read as "your second start was turned away" rather than
// "the update failed".
func (o *otaOrchestration) claim(what string) (release func(), err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.running != "" {
		return func() {}, fmt.Errorf("%s an update is already running (%s); wait for it to finish", errBoxBusyToken, o.running)
	}
	o.running = what
	return func() {
		o.mu.Lock()
		o.running = ""
		o.mu.Unlock()
	}, nil
}

// otaSeams wires the sequence to the real speaker: the bound methods the
// buttons used to call one by one.
func (a *App) otaSeams() *otaSeams {
	return &otaSeams{
		push:          a.UpdateBoxAgent,
		ensureEngine:  a.EnsureSpotifyEngine,
		readVersion:   a.BoxAgentVersion,
		classify:      a.ClassifyOTAResult,
		recordOutcome: a.RecordOTAOutcome,
		restoreGroup:  a.RestoreGroupAfterUpdate,
		locate: func(id speakerIdentity, exclude string, sweep bool) (string, int, bool) {
			return a.locateSpeaker(id, exclude, sweep, a.BoxAgentVersion)
		},
		noteUnconfirmed: a.noteOTAUnconfirmed,
		recordIntent: func(host string, port int, deviceID, name string) {
			a.RecordUpdateIntent(host, port, appVersion, deviceID, name, true)
		},
		clearIntent: a.ClearUpdateIntent,
		spawn:       func(fn func()) { go fn() },
		sleep:       time.Sleep,
		now:         time.Now,
		emit:        a.emitUpdatePhase,
		taps:        &a.uploadTaps,
		appVersion:  appVersion,
		appBuild:    appBuild,
		appSha:      embeddedAgentSha256(),
		logger:      a.logger,
	}
}

// emitUpdatePhase sends one phase to the window. Only once the Wails runtime
// is live: EventsEmit REQUIRES the real lifecycle context and Fatals on any
// other, and the sequence is exercised headless by the live harness, so a nil
// ctx must skip the UI event, not kill the process. Same guard as the progress
// emitter.
func (a *App) emitUpdatePhase(p updatePhase) {
	if a == nil || a.ctx == nil {
		return
	}
	wailsrt.EventsEmit(a.appCtx(), updatePhaseEvent, p)
}

// RunSpeakerUpdate updates one speaker end to end: records the intent, pushes
// the agent, waits for the box to come back on the new build and stay there,
// classifies and retries a miss, then puts the Spotify engine back. Phases go
// out on box:update:phase; the frontend only renders them. The app reports
// itself busy for the whole run, so the window asks before closing.
//
// A clean push rejection returns the error, so the window's existing failure
// path (busy token, still-working hint, failure report) stays what it was.
func (a *App) RunSpeakerUpdate(host string, port int, deviceID, name string) (UpdateOutcome, error) {
	return a.runSpeakerUpdate(host, port, deviceID, name, nil)
}

// runSpeakerUpdate is RunSpeakerUpdate with a phase callback for headless
// callers (the live harness watches the phases it would otherwise only see as
// window events).
func (a *App) runSpeakerUpdate(host string, port int, deviceID, name string, onPhase func(updatePhase)) (UpdateOutcome, error) {
	return orchestrateSpeaker(a.otaSeams(), otaProductionPolicy, host, port, deviceID, name, onPhase)
}

// orchestrateSpeaker is RunSpeakerUpdate with its seams and policy exposed.
func orchestrateSpeaker(seams *otaSeams, policy otaPolicy, host string, port int, deviceID, name string, onPhase func(updatePhase)) (UpdateOutcome, error) {
	label := name
	if label == "" {
		label = host
	}
	release, err := otaOrch.claim("an update for " + label)
	if err != nil {
		return UpdateOutcome{Host: host, Name: name, Outcome: outcomeFailed, Err: err.Error()}, err
	}
	defer release()
	otaRunning.Store(true)
	defer otaRunning.Store(false)
	// Write down what this speaker is supposed to end up running BEFORE any of
	// it happens. From here on the target survives this app process: if the
	// window is closed during the reboot, or the machine goes to sleep, the next
	// run compares the speaker against this record and finishes the job.
	if seams.recordIntent != nil {
		seams.recordIntent(host, port, deviceID, name)
	}
	u := &speakerUpdater{host: host, port: port, deviceID: deviceID, name: name, seams: seams, policy: policy, onPhase: onPhase,
		ident: speakerIdentity{hints: appendIDs(nil, deviceID)}}
	out := u.run(1)
	if out.Outcome == outcomeFailed {
		return out, errors.New(out.Err)
	}
	return out, nil
}

// RunFleetUpdate updates every target in one run. All speakers start together
// and queue only for the push itself, so the restarts, which are the long part
// and need no network, all happen together instead of two at a time (see
// uploadGate). Engine-only targets get the engine put back and nothing else.
// Nothing stops the batch early; every row ends with an outcome.
func (a *App) RunFleetUpdate(targets []FleetTarget) (FleetOutcome, error) {
	return a.runFleetUpdate(targets, nil)
}

func (a *App) runFleetUpdate(targets []FleetTarget, onPhase func(updatePhase)) (FleetOutcome, error) {
	return orchestrateFleet(a.otaSeams(), otaProductionPolicy, targets, onPhase)
}

// orchestrateFleet is RunFleetUpdate with its seams and policy exposed.
func orchestrateFleet(seams *otaSeams, policy otaPolicy, targets []FleetTarget, onPhase func(updatePhase)) (FleetOutcome, error) {
	release, err := otaOrch.claim("an update for all speakers")
	if err != nil {
		return FleetOutcome{}, err
	}
	defer release()
	// The batch writes software to speakers exactly like the single update, so
	// it carries the same guarantees: the window asks before closing for the
	// whole run, and every speaker's target is written down before its turn.
	otaRunning.Store(true)
	defer otaRunning.Store(false)
	gate := newUploadGate(1)
	agentRows := 0
	for _, t := range targets {
		if !t.EngineOnly {
			agentRows++
		}
	}
	gate.expectPushes(agentRows)
	rows := make([]UpdateOutcome, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t FleetTarget) {
			defer wg.Done()
			u := &speakerUpdater{host: t.Host, port: t.Port, deviceID: t.DeviceID, name: t.Name,
				seams: seams, policy: policy, gate: gate, onPhase: onPhase,
				ident: speakerIdentity{hints: appendIDs(nil, t.DeviceID)}}
			if t.EngineOnly {
				rows[i] = u.runEngineOnly()
			} else {
				// A row that ends before its push (already current, only the
				// engine missing, unreachable) still counts down, here.
				var once sync.Once
				u.settlePush = func() { once.Do(gate.pushSettled) }
				defer u.agentPushSettled()
				rows[i] = u.runFleetRow()
			}
		}(i, t)
	}
	wg.Wait()
	return FleetOutcome{Rows: rows}, nil
}

// runFleetRow is one update target of the batch: the full sequence, then the
// honesty re-check.
func (u *speakerUpdater) runFleetRow() UpdateOutcome {
	if u.seams.recordIntent != nil {
		u.seams.recordIntent(u.host, u.port, u.deviceID, u.name)
	}
	out := u.run(1)
	// Honesty check: a box can LOSE its just-delivered engine to an extra reboot
	// AFTER the engine step returned (tight-NAND ST30, the Portable's
	// battery/fd-leak reboot). So before calling it "done", confirm the engine
	// is actually present; if it is gone, report "Spotify pending" (deferred)
	// instead of a green "done". Nothing re-delivers it later on its own any
	// more, so the record of what this speaker should be running stays, and the
	// user is told once the next time they open it.
	if out.Outcome == outcomeDone {
		if fv, err := u.read(); err == nil && fv["goLibrespot"] == "missing" {
			out.Outcome = outcomePartial
			out.Unmet = unmetSpotifyEngine
			u.phase(updatePhase{Phase: outcomePartial, Attempt: out.Attempts, Outcome: outcomePartial,
				Reachable: true, Version: fv["version"], Engine: fv["goLibrespot"], Unmet: out.Unmet})
		}
	}
	return out
}

// runEngineOnly puts the Spotify engine back on a speaker whose agent is
// already current. Nothing else is written to it, and a current agent
// hot-swaps the engine the moment it lands, so this normally costs no restart
// at all. It still records the target first, like every other speaker in the
// run, so a window closed halfway is noticed afterwards instead of looking
// like nothing ever happened.
func (u *speakerUpdater) runEngineOnly() UpdateOutcome {
	out := UpdateOutcome{Host: u.host, Name: u.name, EngineOnly: true, Attempts: 1}
	if u.seams.recordIntent != nil {
		u.seams.recordIntent(u.host, u.port, u.deviceID, u.name)
	}
	finish := func() UpdateOutcome {
		u.phase(updatePhase{Phase: out.Outcome, Attempt: 1, Outcome: out.Outcome,
			Reachable: out.Version != nil, Version: out.Version["version"], Engine: out.Version["goLibrespot"],
			EngineDelivered: out.EngineDelivered, EngineTooFull: out.EngineTooFull, Unmet: out.Unmet, Err: out.Err})
		return out
	}
	u.phase(updatePhase{Phase: phaseEngineQueued, Attempt: 1})
	var res string
	var err error
	u.gate.run(func() {
		u.phase(updatePhase{Phase: phaseEngineUploading, Attempt: 1})
		res, err = u.seams.ensureEngine(u.host, u.port)
	})
	if err != nil {
		out.Outcome = outcomeFailed
		out.Err = err.Error()
		// A full NAND is not something to retry blindly, so it gets its own row
		// text instead of a bare "failed"; the speaker's own screen names the
		// other SoundTouch software eating the space.
		if spaceRefusal.MatchString(out.Err) {
			out.EngineTooFull = true
		}
		u.log().Warn("update all: engine repair failed", "host", u.host, "err", err)
		return finish()
	}
	if noEmbeddedEngine.MatchString(res) {
		// This app build carries no engine, so there is nothing to deliver.
		out.Outcome = outcomePartial
		out.Unmet = unmetNoEmbeddedEngine
		return finish()
	}
	if engineSent(res) {
		out.EngineDelivered = true
	}
	// The same honesty check the update path does: a speaker can lose the
	// engine again to a reboot right after it was delivered, so confirm it is
	// really there before the row goes green. A read that fails means the box
	// is still settling: trust the delivery.
	stillMissing := false
	if fv, ferr := u.seams.readVersion(u.host, u.port); ferr == nil {
		out.Version = fv
		stillMissing = fv["goLibrespot"] == "missing"
	}
	if stillMissing {
		out.Outcome = outcomePartial
		out.Unmet = unmetSpotifyEngine
		return finish()
	}
	out.Outcome = outcomeDone
	u.clearIntent()
	return finish()
}
