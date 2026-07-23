// Package autopair makes sure the Bose box is always paired with the
// stick. Runs at agent start and can also be actively triggered after
// box reboots.
//
// Pair flow:
//  1. GET http://<box>:8090/info to read margeAccountUUID
//  2. If empty: POST http://<box>:8090/setMargeAccount with PairDeviceWithAccount XML
//  3. Box calls the stick's marge stub /streaming/account/.../device/
//  4. Stub answers with adddeviceresponse (wrap201 format)
//  5. Box state machine transitions to MargeStateAssociated
package autopair

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultAccountID = "stick@local"
	defaultToken     = "stick-local-auth"
	defaultEmail     = "stick@local"
)

// Config describes the pair identity.
type Config struct {
	BoxHost   string // e.g. "127.0.0.1" or box LAN IP
	AccountID string // e.g. "stick@local"
	AuthToken string // sent as userAuthToken
	Email     string // optional
}

// Manager can check and trigger the pair status.
type Manager struct {
	logger *slog.Logger
	cfg    Config
	client *http.Client
	// base is the box's BoseApp REST origin ("http://<host>:8090").
	// A field (not recomputed per request) so tests can point the
	// Manager at an httptest server.
	base string

	// inFlight makes the pair cycle single-flight: the RunBackground
	// ticker, hardware preset presses and app recalls all trigger
	// pairing concurrently, and /setMargeAccount can hang for many
	// seconds on a slow/booting box. Without this the triggers stack
	// into a POST storm against :8090 (seen live: repeated 8s-timeout
	// pair POSTs every tick, #375). A trigger that finds a cycle
	// already running simply coalesces into it.
	inFlight atomic.Bool

	// lastPaired is the result of the most recent EnsurePaired call.
	// nil = unknown (no successful status read yet), &true / &false
	// for the last known state. Used to emit phase-marker logs only
	// on transitions so a diagnostic bundle has a clean timeline
	// without the 5-min heartbeat drowning everything else.
	// Guarded by pairedMu: ensure cycles run on whichever goroutine
	// triggered them (RunBackground tick, press-time TriggerNow, boxws
	// reconnect), while RunBackground reads the state for its heartbeat.
	pairedMu   sync.Mutex
	lastPaired *bool
	// tickCount is only touched by RunBackground's own goroutine.
	tickCount int

	// onPaired fires when the box transitions from unpaired to paired (a
	// completed (re-)onboarding). The firmware wipes its hardware-key preset
	// registrations during exactly that onboarding, so the agent hooks this to
	// schedule an immediate key re-sync instead of waiting for the reconcile
	// cadence. Set once at wiring time, before RunBackground starts.
	onPaired func()

	// ForcePair coalescing state. A reactive forced re-login (after the box
	// refused a source 1036 NOT_LOGGED_IN on a real press) may race the regular
	// pair cycle; lastSuspectAssert stamps the most recent account POST so a
	// second one right behind it is skipped instead of churning the MargeHSM.
	// Guarded by suspectMu.
	//
	// STR used to keep the box "login-suspect" for an hour after any 1036 and
	// re-POST setMargeAccount on every heartbeat to proactively MAINTAIN the fake
	// login. That was removed: re-onboarding a *playing* box bounces its active
	// source and the firmware powers it off mid-song (the 0.9.17 self-off, all
	// models), and it never actually prevented the box's own post-standby 1036
	// anyway (proven live 2026-07-23). v0.9.0 had no proactive maintenance at all
	// and was stable for exactly this reason. Only the reactive ForcePair (which
	// un-wedges an ST300 after a real failed press, #342) remains.
	suspectMu         sync.Mutex
	lastSuspectAssert time.Time
}

// New creates a Manager with sensible defaults.
func New(logger *slog.Logger, cfg Config) *Manager {
	if cfg.BoxHost == "" {
		cfg.BoxHost = "127.0.0.1"
	}
	if cfg.AccountID == "" {
		cfg.AccountID = defaultAccountID
	}
	if cfg.AuthToken == "" {
		cfg.AuthToken = defaultToken
	}
	if cfg.Email == "" {
		cfg.Email = defaultEmail
	}
	return &Manager{
		logger: logger,
		cfg:    cfg,
		client: &http.Client{Timeout: 8 * time.Second},
		base:   "http://" + cfg.BoxHost + ":8090",
	}
}

// IsPaired reads /info and checks whether margeAccountUUID is set.
func (m *Manager) IsPaired(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.base+"/info", nil)
	if err != nil {
		return false, err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return false, err
	}
	return hasMargeUUID(body), nil
}

var uuidRe = regexp.MustCompile(`<margeAccountUUID>([^<]+)</margeAccountUUID>`)

func hasMargeUUID(body []byte) bool {
	m := uuidRe.FindSubmatch(body)
	return len(m) == 2 && len(strings.TrimSpace(string(m[1]))) > 0
}

// Pair triggers the pair flow via POST to /setMargeAccount.
// Success = box answers 200 OK (margeAccountUUID is set afterwards).
func (m *Manager) Pair(ctx context.Context) error {
	body := buildPairXML(m.cfg.AccountID, m.cfg.AuthToken, m.cfg.Email)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.base+"/setMargeAccount", strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/xml")
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("setMargeAccount status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// EnsurePaired checks the status and triggers Pair if needed.
// Idempotent: if the box is already paired, it does nothing.
//
// Before the first Pair attempt this also checks that the box's own
// clock is past 2024. The Bose firmware's RTC reads 2015 right after
// power-on and only catches up once the box reaches NTP. Calling Pair
// while the clock is in 2015 fails with `tls: expired certificate`
// (the box sees STR's 2026-issued cert as not-yet-valid even though
// it's already backdated, depending on how far back NotBefore goes).
// Gating here lets the periodic ticker simply retry until the clock
// is sane.
func (m *Manager) EnsurePaired(ctx context.Context) error {
	return m.ensure(ctx, false)
}

// ensure is EnsurePaired's implementation. force additionally re-asserts the
// account when the box already reports paired: used on the first cycle after
// agent start, where a stale "paired" can hide two real problems - (a) the
// box's boot-time cloud check ran against the real, dead streaming.bose.com
// before STR's marge stub was up (/etc/hosts is tmpfs on several firmwares,
// so the interception is lost on every reboot) and left the amber cloud icon
// on; the setMargeAccount -> marge round trip is the documented clearer
// (#270, v0.5.26 finding). (b) Some firmwares keep the UUID but drop the
// login state (SoundTouch 300).
func (m *Manager) ensure(ctx context.Context, force bool) error {
	if !m.inFlight.CompareAndSwap(false, true) {
		// Another trigger's cycle is already running; it answers for this
		// one too. Never stack setMargeAccount POSTs on a slow box (#375).
		m.logger.Debug("autopair: pair cycle already in flight, coalescing")
		return nil
	}
	defer m.inFlight.Store(false)
	paired, err := m.IsPaired(ctx)
	if err != nil {
		// Status read failure is a phase marker on its own: it tells the
		// diagnostic bundle whether the box was reachable at all during
		// e.g. the standby window. Logged at WARN even though the loop
		// will retry, because "box silent for N ticks" is exactly the
		// signal we need for #60.
		m.logger.Warn("autopair phase: /info read failed", "err", err)
		return fmt.Errorf("check status: %w", err)
	}
	m.recordPairedState(paired)
	if paired && !force {
		// A paired box (margeAccountUUID present) is a no-op on the regular
		// cycle - the v0.9.0 behaviour. The proactive per-heartbeat re-assert
		// that used to run here was removed (see the suspectMu comment): it
		// re-onboarded playing boxes and powered them off, and never actually
		// prevented the box's own post-standby 1036. The reactive ForcePair
		// after a real failed press still re-logs in to un-wedge the box.
		m.logger.Debug("box already paired, no re-pair needed")
		return nil
	}
	if ok, when := m.boxClockSane(ctx); !ok {
		m.logger.Info("auto pair deferred, box clock not yet synced (will retry next tick)",
			"boxDate", when)
		return nil
	}
	switch {
	case paired:
		m.logger.Warn("autopair phase: first cycle after start, re-asserting the account (clears a stale paired state and the dead-cloud amber icon)", "accountID", m.cfg.AccountID)
	default:
		m.logger.Warn("autopair phase: box not paired, starting auto pair", "accountID", m.cfg.AccountID)
	}
	if err := m.Pair(ctx); err != nil {
		return fmt.Errorf("pair: %w", err)
	}
	m.logger.Warn("autopair phase: box paired successfully", "accountID", m.cfg.AccountID)
	return nil
}

// recordPairedState emits a phase marker on every transition (paired
// <-> not paired). The first observation also counts as a transition,
// so the diagnostic bundle always carries an explicit "initial state"
// line right after agent start.
func (m *Manager) recordPairedState(paired bool) {
	m.pairedMu.Lock()
	if m.lastPaired == nil {
		v := paired
		m.lastPaired = &v
		m.pairedMu.Unlock()
		m.logger.Warn("autopair phase: initial state observed", "paired", paired)
		return
	}
	if *m.lastPaired == paired {
		m.pairedMu.Unlock()
		return
	}
	was := *m.lastPaired
	v := paired
	m.lastPaired = &v
	m.pairedMu.Unlock()
	m.logger.Warn("autopair phase: paired state changed", "from", was, "to", paired)
	// unpaired -> paired = a (re-)onboarding just completed; the firmware
	// wipes its key-layer preset registrations in that transition, so let
	// the agent re-register them right away. The initial observation is
	// deliberately NOT a transition: an already-paired box at agent start
	// went through no onboarding.
	if paired && !was && m.onPaired != nil {
		m.onPaired()
	}
}

// pairedState reports the last observed pair state: known=false means no
// successful status read has happened yet. Safe for concurrent use.
func (m *Manager) pairedState() (known, paired bool) {
	m.pairedMu.Lock()
	defer m.pairedMu.Unlock()
	if m.lastPaired == nil {
		return false, false
	}
	return true, *m.lastPaired
}

// SetOnPaired registers the unpaired->paired transition hook. Call before
// RunBackground starts; the callback must be fast (it runs inside the pair
// cycle).
func (m *Manager) SetOnPaired(fn func()) {
	m.onPaired = fn
}

// boxClockSane returns true if the box's own clock — as reported by
// the Date header on /info — is past 2024. Returns false on any error
// reading the header so callers default to "not sane" and retry
// later. The second return value is the parsed (or raw) date string
// for logging.
func (m *Manager) boxClockSane(ctx context.Context) (bool, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.base+"/info", nil)
	if err != nil {
		return false, "request-build-failed"
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return false, "request-failed"
	}
	defer resp.Body.Close()
	dh := resp.Header.Get("Date")
	if dh == "" {
		// Older Bose firmware variants do not always emit a Date
		// header on /info. Be lenient: if the box did answer, fall
		// through and let Pair proceed — the worst case is a single
		// failed handshake that the ticker retries.
		return true, "no-date-header"
	}
	t, err := http.ParseTime(dh)
	if err != nil {
		return true, "unparseable: " + dh
	}
	if t.Year() < 2024 {
		return false, t.UTC().Format(time.RFC3339)
	}
	return true, t.UTC().Format(time.RFC3339)
}

// RunBackground runs in the background, pairs once at start after delay,
// and re-pairs when the box loses the status (every "interval"). Stop via
// ctx cancel.
//
// The delay at start gives the box time to bring up the BoseApp web
// server after a box reboot.
func (m *Manager) RunBackground(ctx context.Context, startDelay, interval time.Duration) {
	if startDelay > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(startDelay):
		}
	}

	// Every 6th tick (~30 min at the default 5-min interval) emit a
	// phase-marker heartbeat at WARN even when nothing changed, so a
	// diagnostic bundle proves the autopair loop is still alive across
	// the standby window. Without this, a healthy paired box looks
	// indistinguishable from a stalled goroutine in the log.
	const heartbeatEvery = 6
	// A box loses its margeAccountUUID across a reboot/standby cycle and only
	// re-reports it briefly during its own re-onboarding, so the very first
	// post-boot check can see a stale "paired" and then the box sits UNPAIRED
	// for a whole interval (live: scm/mojo ST30 dropped to an empty account for
	// ~5 min after every reboot). Poll fast for the first couple of minutes so a
	// cleared account is re-paired within seconds, then settle to the steady
	// interval.
	const fastInterval = 20 * time.Second
	const fastFor = 2 * time.Minute
	// fastForUnpairedMax extends the fast window while the box has NOT yet
	// been observed paired: on scm/mojo the post-boot clock sync and account
	// clear can take ~5-8 minutes, and the old fixed 2-minute window left the
	// box unpaired on the 5-minute steady cadence for most of that time (the
	// field bundle showed an ~8-minute unpaired hole after a reboot, with the
	// hardware keys dead throughout). Bounded so a permanently unreachable box
	// still settles to the cheap steady interval eventually.
	const fastForUnpairedMax = 10 * time.Minute
	start := time.Now()
	cur := fastInterval
	if interval < fastInterval {
		cur = interval
	}
	tick := time.NewTicker(cur)
	defer tick.Stop()
	for {
		m.tickCount++
		// The first cycle after start forces a re-assert even when the box
		// reports paired: see ensure() for the two live failure modes a
		// stale "paired" hides (amber cloud icon after reboot, ST300 silent
		// login loss).
		if err := m.ensure(ctx, m.tickCount == 1); err != nil {
			m.logger.Warn("auto pair failed, will retry next tick", "err", err)
		} else if m.tickCount%heartbeatEvery == 0 {
			state := "unknown"
			if known, paired := m.pairedState(); known {
				if paired {
					state = "paired"
				} else {
					state = "not paired"
				}
			}
			m.logger.Warn("autopair phase: heartbeat",
				"tick", m.tickCount, "state", state, "interval", cur.String())
		}
		// Settle to the steady interval once the fast post-boot window elapses;
		// a box not yet observed paired holds the fast cadence longer (capped).
		_, pairedObserved := m.pairedState()
		if cur != interval && shouldSettleToSteady(time.Since(start), pairedObserved, fastFor, fastForUnpairedMax) {
			cur = interval
			tick.Reset(interval)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// shouldSettleToSteady decides when the post-boot fast poll drops to the
// steady interval: after fastFor once the box has been observed paired, or
// after the (longer) unpaired cap otherwise.
func shouldSettleToSteady(elapsed time.Duration, pairedObserved bool, fastFor, fastForUnpairedMax time.Duration) bool {
	if pairedObserved {
		return elapsed >= fastFor
	}
	return elapsed >= fastForUnpairedMax
}

// TriggerNow forces a pair-check cycle, independent of the RunBackground
// ticker. Useful e.g. when boxws signals a reconnect.
func (m *Manager) TriggerNow(ctx context.Context) {
	if err := m.EnsurePaired(ctx); err != nil {
		m.logger.Warn("auto pair trigger failed", "err", err)
	}
}

// ForcePair re-asserts the account UNCONDITIONALLY, even when the box already
// reports a margeAccountUUID. EnsurePaired skips a box that carries a UUID, but
// some firmwares (the SoundTouch 300) keep the UUID yet tell STR they are
// NOT_LOGGED_IN when handed a UPnP source; re-POSTing setMargeAccount can
// restore the login state that the UUID-present check would otherwise skip.
// Best-effort: a failure is logged, not fatal.
//
// Runs inside the same single-flight guard as ensure(): the press-time suspect
// re-assert and the same press's 1036-driven ForcePair used to POST
// setMargeAccount concurrently to a box whose POST handler can hang for
// seconds, re-introducing exactly the stacked-POST pattern the guard exists
// for (#375). ForcePair now waits (bounded by ctx) for a running cycle to
// finish instead of overlapping it, and skips its own POST when that cycle
// just re-asserted the account anyway. Deliberately does NOT read /info first:
// the whole point is to POST even when the paired state looks fine.
func (m *Manager) ForcePair(ctx context.Context) {
	for !m.inFlight.CompareAndSwap(false, true) {
		select {
		case <-ctx.Done():
			m.logger.Info("autopair: forced re-login skipped, another pair cycle ran for the whole window (it re-asserts too: the box is now login-suspect)")
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
	defer m.inFlight.Store(false)
	// If a suspect-driven re-assert completed while we waited, the account was
	// just POSTed; a second POST right behind it only churns the MargeHSM.
	m.suspectMu.Lock()
	justAsserted := !m.lastSuspectAssert.IsZero() && time.Since(m.lastSuspectAssert) < 5*time.Second
	m.suspectMu.Unlock()
	if justAsserted {
		m.logger.Info("autopair: forced re-login coalesced into a just-completed suspect re-assert")
		return
	}
	m.logger.Warn("autopair: forcing a re-login (box reported not-logged-in)")
	if err := m.Pair(ctx); err != nil {
		m.logger.Warn("autopair: forced re-login failed", "err", err)
		return
	}
	// Stamp the suspect min-gap so the next ensure trigger (press, wake,
	// reconnect, tick) does not immediately re-POST within seconds of this one.
	m.suspectMu.Lock()
	m.lastSuspectAssert = time.Now()
	m.suspectMu.Unlock()
	m.logger.Warn("autopair: forced re-login sent")
}

func buildPairXML(accountID, token, email string) string {
	return `<?xml version="1.0" encoding="UTF-8" ?>` +
		`<PairDeviceWithAccount>` +
		`<accountId>` + xmlEscape(accountID) + `</accountId>` +
		`<userAuthToken>` + xmlEscape(token) + `</userAuthToken>` +
		`<accountEmail>` + xmlEscape(email) + `</accountEmail>` +
		`</PairDeviceWithAccount>`
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
