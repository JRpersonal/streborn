package webui

// Update every STR speaker at once from the phone.
//
// The self-update (selfupdate.go) made one speaker update itself. A household
// with six speakers then had to open six phone pages and tap six times, so the
// phone card now offers "Update all speakers" by default: the speaker the phone
// is talking to drives the run and asks each of the others, one after the
// other, to update ITSELF through the very same /api/agent/self-update
// endpoint. Nothing new is installed by anybody but the speaker it is for, and
// every peer still checks the signature, the version and the hash on its own.
//
// Rules the run keeps:
//
//   - One speaker at a time, never in parallel. Each update downloads 16 MB
//     over Wi-Fi, rewrites the NAND and reboots the speaker; two at once is
//     two speakers fighting over the same air and nobody to blame afterwards.
//   - This speaker goes last. Its own reboot cuts the phone off, so the run
//     state for that step travels in the existing pending marker and the
//     summary is back on the card once the speaker is.
//   - Only speakers from the fleet list (the peer roster the desktop app and
//     mDNS fill), only plain private LAN addresses, only the two agent ports,
//     and a dialer that refuses loopback and link-local. The peer enforces its
//     own LAN gate on the request, exactly as for a phone.
//   - A peer whose agent predates the self-update endpoint is reported as
//     "needs one update from the desktop app" and never touched another way.
//   - The waits are bounded and belong to a run the owner started. Nothing
//     here runs on a timer (standing rule, 2026-07-29).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/netutil"
	"github.com/JRpersonal/streborn/internal/updatesig"
)

// Speaker states on the card.
const (
	fleetWaiting  = "waiting"
	fleetUpdating = "updating"
	fleetDone     = "done"
	fleetFailed   = "failed"
	fleetSkipped  = "skipped"
)

// Reasons for a skipped speaker. The phone turns them into words.
const (
	fleetSkipCurrent     = "current"
	fleetSkipOldAgent    = "old-agent"
	fleetSkipUnreachable = "unreachable"
)

// fleetSpeaker is one row of the card.
type fleetSpeaker struct {
	Name    string `json:"name"`
	IP      string `json:"ip,omitempty"`
	Self    bool   `json:"self,omitempty"`
	Version string `json:"version,omitempty"`
	Target  string `json:"target,omitempty"`
	Needs   bool   `json:"needs"`
	State   string `json:"state"`
	// Phase details an updating speaker: checking, downloading, rebooting, engine.
	Phase string `json:"phase,omitempty"`
	Pct   int    `json:"pct,omitempty"`
	// Reason is a skip code (fleetSkip*) or, for a failed speaker, the error.
	Reason string `json:"reason,omitempty"`
	// Port is the agent port this speaker answered on, or the one the peer
	// list knows it by. The phone tries it first when it reaches the peer
	// itself (selfupdate_phone.go).
	Port int `json:"port,omitempty"`
}

// fleetRun is the plan the card shows, or the run that executes it.
type fleetRun struct {
	Mode     string         `json:"mode"` // plan, running, finished
	Target   string         `json:"target,omitempty"`
	Speakers []fleetSpeaker `json:"speakers"`
	At       time.Time      `json:"at"`
	// Only is set when a phone handed this speaker some peers it could not
	// reach itself: the run updates just those, and not this speaker.
	Only []string `json:"only,omitempty"`
	// token is the phone run this run belongs to, "" for one started here.
	token string
}

type fleetState struct {
	mu  sync.Mutex
	run *fleetRun
}

// runningFor reports who a running run belongs to: "" when none runs, the
// phone's token for a hand-over, "speaker" for a run started on its own.
func (f *fleetState) runningFor() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.run == nil || f.run.Mode != "running" {
		return ""
	}
	if f.run.token != "" {
		return f.run.token
	}
	return "speaker"
}

var (
	// fleetPeerBase is where a peer's agent answers. A seam for the tests,
	// which put fake peers behind loopback listeners.
	fleetPeerBase = func(ip string, port int) string {
		return "http://" + net.JoinHostPort(ip, strconv.Itoa(port))
	}
	// fleetClient talks to peers: refuses loopback, link-local and metadata at
	// dial time and follows no redirect, so a peer cannot bounce the driving
	// speaker somewhere else.
	fleetClient = func(timeout time.Duration) *http.Client {
		c := netutil.GuardedClient(timeout)
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return c
	}
	// fleetPollEvery is how often a peer is asked how its update is going.
	fleetPollEvery = 5 * time.Second
	// fleetPeerBudget bounds one peer's update, download through reboot. The
	// desktop OTA allows a slow chassis about five minutes to come back; the
	// download over the speaker's own Wi-Fi comes on top.
	fleetPeerBudget = 12 * time.Minute
	// fleetEngineBudget is how long a peer that is back on the new version
	// may spend fetching its Spotify engine before the run moves on. Waiting
	// keeps the next speaker's download off the air while this one's runs.
	fleetEngineBudget = 4 * time.Minute
	// fleetAgentPorts are the only ports a peer agent is reached on: 8888
	// directly (sm2), 17008 through the PREROUTING redirect (BCO chassis).
	fleetAgentPorts = []int{8888, 17008}
)

// fleetMaxPeers caps the roster a run walks, the same cap the seed list has.
const fleetMaxPeers = 32

func (f *fleetState) snapshot() *fleetRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.run == nil {
		return nil
	}
	cp := *f.run
	cp.Speakers = append([]fleetSpeaker(nil), f.run.Speakers...)
	return &cp
}

func (f *fleetState) running() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.run != nil && f.run.Mode == "running"
}

// edit changes speaker i of the current run under the lock.
func (f *fleetState) edit(i int, fn func(*fleetSpeaker)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.run != nil && i >= 0 && i < len(f.run.Speakers) {
		fn(&f.run.Speakers[i])
	}
}

// fleetPeerAddrs returns the speakers a run may touch: the peer roster,
// narrowed to bare private LAN addresses, one entry per address, and in a
// stable order. The test override's "peers" list narrows it further, so a
// developer can run the whole flow against two speakers on a LAN with more.
func (s *Server) fleetPeerAddrs(ctx context.Context) []fleetSpeaker {
	if s.peersFn == nil {
		return nil
	}
	allow := selfUpdatePeerAllowlist()
	seen := map[string]bool{}
	var out []fleetSpeaker
	for _, p := range s.peersFn(ctx) {
		ip, port := fleetPeerAddr(p)
		if ip == "" || seen[ip] || !isLANPeer(ip) {
			continue
		}
		if allow != nil && !allow[ip] {
			continue
		}
		seen[ip] = true
		name := strings.TrimSpace(p.Name)
		if name == "" {
			name = ip
		}
		out = append(out, fleetSpeaker{Name: name, IP: ip, Port: port})
		if len(out) == fleetMaxPeers {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].IP < out[j].IP
	})
	return out
}

// fleetPeerAddr takes the address and the port a roster entry was last
// reached on. Only the two agent ports count as a hint; anything else in the
// URL is ignored and the default order applies.
func fleetPeerAddr(p PeerLink) (string, int) {
	ip := strings.TrimSpace(p.IP)
	port := 0
	if u, err := url.Parse(p.URL); err == nil {
		if ip == "" {
			ip = u.Hostname()
		}
		if n, _ := strconv.Atoi(u.Port()); n == 8888 || n == 17008 {
			port = n
		}
	}
	if net.ParseIP(ip) == nil {
		return "", 0
	}
	return ip, port
}

// fleetPorts is the order a peer's agent is tried in: the port it was last
// reached on first, then the other one.
func fleetPorts(hint int) []int {
	out := []int{}
	if hint != 0 {
		out = append(out, hint)
	}
	for _, p := range fleetAgentPorts {
		if p != hint {
			out = append(out, p)
		}
	}
	return out
}

// errOldAgent marks a peer whose agent has no self-update endpoint yet.
var errOldAgent = errors.New("this speaker's STR is too old to update from the phone")

// fleetGetJSON GETs a peer endpoint and decodes a JSON answer.
func fleetGetJSON(ctx context.Context, u string, timeout time.Duration, out any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := fleetClient(timeout).Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "json") {
		return resp.StatusCode, errors.New("not a JSON answer")
	}
	return resp.StatusCode, json.Unmarshal(b, out)
}

// fleetPostAction POSTs {"action": action} to a peer's self-update endpoint.
func fleetPostAction(ctx context.Context, base, action string, timeout time.Duration) (int, selfUpdateStatus, string, error) {
	var st selfUpdateStatus
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"action": action})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/agent/self-update", bytes.NewReader(body))
	if err != nil {
		return 0, st, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := fleetClient(timeout).Do(req)
	if err != nil {
		return 0, st, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(b, &st) != nil {
		st = selfUpdateStatus{}
	}
	return resp.StatusCode, st, strings.TrimSpace(string(b)), nil
}

// probeFleetPeer finds a peer's agent and asks what it runs. The agent is
// recognised by a JSON /api/agent/version, so the Bose service that also
// listens on :17008 of an sm2 speaker is never mistaken for it. It then
// reports whether the agent has the self-update endpoint: an older agent
// answers that path with its page (HTML) or a 404.
func probeFleetPeer(ctx context.Context, ip string, hint int) (port int, version string, err error) {
	if !isLANPeer(ip) {
		return 0, "", fmt.Errorf("%s is not a speaker address on this network", ip)
	}
	for _, p := range fleetPorts(hint) {
		var v struct {
			Version string `json:"version"`
		}
		if _, gerr := fleetGetJSON(ctx, fleetPeerBase(ip, p)+"/api/agent/version", 5*time.Second, &v); gerr != nil || v.Version == "" {
			continue
		}
		var st selfUpdateStatus
		code, serr := fleetGetJSON(ctx, fleetPeerBase(ip, p)+"/api/agent/self-update", 5*time.Second, &st)
		switch {
		case serr == nil && st.State != "":
			return p, v.Version, nil
		case code == http.StatusNotFound || code == http.StatusOK:
			// 404, or a 200 that is the page rather than the status.
			return p, v.Version, errOldAgent
		case code != 0:
			return p, v.Version, fmt.Errorf("the speaker refused (HTTP %d)", code)
		default:
			return p, v.Version, fmt.Errorf("no answer: %w", serr)
		}
	}
	return 0, "", errors.New("not reachable")
}

// planFleetPeer decides one peer's row for the plan: waiting when it needs the
// update and can take it from the phone, skipped with the reason otherwise.
func planFleetPeer(ctx context.Context, sp fleetSpeaker, target string) fleetSpeaker {
	sp.Target = target
	port, ver, err := probeFleetPeer(ctx, sp.IP, sp.Port)
	if port != 0 {
		sp.Port = port
	}
	sp.Version = ver
	sp.Needs = ver != "" && updatesig.VersionLess(ver, target)
	switch {
	case port == 0:
		sp.State, sp.Reason = fleetSkipped, fleetSkipUnreachable
	case !sp.Needs:
		sp.State, sp.Reason = fleetSkipped, fleetSkipCurrent
	case errors.Is(err, errOldAgent):
		sp.State, sp.Reason = fleetSkipped, fleetSkipOldAgent
	case err != nil:
		sp.State, sp.Reason = fleetSkipped, fleetSkipUnreachable
	default:
		sp.State = fleetWaiting
	}
	return sp
}

// planFleet builds the card's list: every peer, then this speaker, last.
// The probes only read, so they run a few at a time; the updates never do.
// With only set (a phone's hand-over) the list is just those peers, and this
// speaker is not part of it: the phone updates it itself, last.
func (s *Server) planFleet(ctx context.Context, target string, only map[string]bool) []fleetSpeaker {
	var peers []fleetSpeaker
	for _, p := range s.fleetPeerAddrs(ctx) {
		if only == nil || only[p.IP] {
			peers = append(peers, p)
		}
	}
	out := make([]fleetSpeaker, len(peers))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for i, p := range peers {
		wg.Add(1)
		go func(i int, p fleetSpeaker) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = planFleetPeer(ctx, p, target)
		}(i, p)
	}
	wg.Wait()
	if only != nil {
		return out
	}
	return append(out, s.fleetSelfRow(target))
}

// fleetKnownPeers keeps the addresses of ips that are on this speaker's own
// peer list (and its test allowlist), in list order. A hand-over can never
// aim this speaker at an address it would not have walked itself.
func (s *Server) fleetKnownPeers(ctx context.Context, ips []string) []string {
	want := map[string]bool{}
	for _, ip := range ips {
		want[strings.TrimSpace(ip)] = true
	}
	var out []string
	for _, p := range s.fleetPeerAddrs(ctx) {
		if want[p.IP] {
			out = append(out, p.IP)
		}
	}
	return out
}

// fleetSelfRow is this speaker's own row.
func (s *Server) fleetSelfRow(target string) fleetSpeaker {
	name := ""
	if s.boxNameFn != nil {
		name, _ = s.boxNameFn()
	}
	cur := agentVersion()
	sp := fleetSpeaker{Name: name, Self: true, Version: cur, Target: target}
	sp.Needs = updatesig.VersionLess(cur, target)
	if sp.Needs {
		sp.State = fleetWaiting
	} else {
		sp.State, sp.Reason = fleetSkipped, fleetSkipCurrent
	}
	return sp
}

// fleetLatest returns the latest release, checking again when the last check
// is missing or older than the install would accept.
func (s *Server) fleetLatest(ctx context.Context) (string, error) {
	s.selfUpd.mu.Lock()
	ix, at := s.selfUpd.index, s.selfUpd.checkedAt
	s.selfUpd.mu.Unlock()
	if ix != nil && time.Since(at) <= selfUpdateCheckTTL {
		return ix.Version, nil
	}
	fresh, err := s.selfUpdateCheck(ctx)
	if err != nil {
		return "", err
	}
	return fresh.Version, nil
}

// fleetPlan answers the "plan" action: the list the card shows before the
// owner taps. A running or just finished run is left alone.
func (s *Server) fleetPlan(ctx context.Context) error {
	target, err := s.fleetLatest(ctx)
	if err != nil {
		return err
	}
	speakers := s.planFleet(ctx, target, nil)
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	if s.fleet.run != nil && s.fleet.run.Mode == "running" {
		return nil
	}
	s.fleet.run = &fleetRun{Mode: "plan", Target: target, Speakers: speakers, At: time.Now().UTC()}
	return nil
}

// startFleet claims the run and starts it in the background. False when a
// run is already going. only narrows it to those peers (a phone's hand-over,
// this speaker not included); token ties it to that phone's run.
func (s *Server) startFleet(only []string, token string) bool {
	s.fleet.mu.Lock()
	if s.fleet.run != nil && s.fleet.run.Mode == "running" {
		s.fleet.mu.Unlock()
		return false
	}
	prev := s.fleet.run
	run := &fleetRun{Mode: "running", At: time.Now().UTC(), Only: only, token: token}
	if prev != nil {
		run.Target = prev.Target
		for _, sp := range prev.Speakers {
			if only == nil || (!sp.Self && slices.Contains(only, sp.IP)) {
				run.Speakers = append(run.Speakers, sp)
			}
		}
	}
	s.fleet.run = run
	s.fleet.mu.Unlock()
	go s.runFleet()
	return true
}

// runFleet updates the peers one at a time and this speaker last.
func (s *Server) runFleet() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	target, err := s.fleetLatest(ctx)
	var only map[string]bool
	s.fleet.mu.Lock()
	if s.fleet.run != nil && s.fleet.run.Only != nil {
		only = map[string]bool{}
		for _, ip := range s.fleet.run.Only {
			only[ip] = true
		}
	}
	s.fleet.mu.Unlock()
	var speakers []fleetSpeaker
	if err == nil {
		// Planned again: the card's list may be minutes old, and a speaker
		// somebody updated from the desktop meanwhile is simply skipped.
		speakers = s.planFleet(ctx, target, only)
	}
	cancel()
	s.fleet.mu.Lock()
	if err != nil {
		s.fleet.run.Mode = "finished"
		for i := range s.fleet.run.Speakers {
			if s.fleet.run.Speakers[i].State == fleetWaiting {
				s.fleet.run.Speakers[i].State, s.fleet.run.Speakers[i].Reason = fleetFailed, "could not check for updates: "+err.Error()
			}
		}
		s.fleet.mu.Unlock()
		s.logger.Warn("self-update fleet: could not start", "err", err)
		return
	}
	s.fleet.run.Target, s.fleet.run.Speakers = target, speakers
	s.fleet.mu.Unlock()

	queued := 0
	for _, sp := range speakers {
		if sp.State == fleetWaiting {
			queued++
		}
	}
	s.logger.Info("self-update fleet: run started", "target", target, "speakers", len(speakers), "toUpdate", queued)

	for i, sp := range speakers {
		if sp.Self {
			continue
		}
		if sp.State != fleetWaiting {
			s.logger.Info("self-update fleet: peer skipped", "peer", sp.Name, "ip", sp.IP, "version", sp.Version, "reason", sp.Reason)
			continue
		}
		pctx, pcancel := context.WithTimeout(context.Background(), fleetPeerBudget+fleetEngineBudget+2*time.Minute)
		s.updateFleetPeer(pctx, sp, func(fn func(*fleetSpeaker)) { s.fleet.edit(i, fn) })
		pcancel()
	}

	selfIdx := len(speakers) - 1
	if selfIdx >= 0 && speakers[selfIdx].Self && speakers[selfIdx].State == fleetWaiting {
		if s.fleetUpdateSelf(selfIdx) {
			return // this speaker reboots now; resumeSelfUpdate finishes the run
		}
	}
	s.finishFleet()
}

// finishFleet marks the run finished and logs the tally.
func (s *Server) finishFleet() {
	s.fleet.mu.Lock()
	if s.fleet.run == nil {
		s.fleet.mu.Unlock()
		return
	}
	s.fleet.run.Mode = "finished"
	counts := map[string]int{}
	for _, sp := range s.fleet.run.Speakers {
		counts[sp.State]++
	}
	s.fleet.mu.Unlock()
	s.logger.Info("self-update fleet: run finished", "done", counts[fleetDone], "failed", counts[fleetFailed], "skipped", counts[fleetSkipped])
}

// fleetUpdateSelf runs this speaker's own update as the run's last step. True
// when the new agent is written and the reboot is under way.
func (s *Server) fleetUpdateSelf(i int) bool {
	if updateInFlight.Load() || !s.selfUpd.claim() {
		s.fleet.edit(i, func(sp *fleetSpeaker) {
			sp.State, sp.Reason = fleetFailed, "an update is already running on this speaker"
		})
		return false
	}
	s.fleet.edit(i, func(sp *fleetSpeaker) { sp.State, sp.Phase = fleetUpdating, "rebooting" })
	s.logger.Info("self-update fleet: updating this speaker last", "from", agentVersion())
	s.runSelfUpdate() // releases the claim; the pending marker carries the run
	s.selfUpd.mu.Lock()
	state, msg := s.selfUpd.state, s.selfUpd.err
	s.selfUpd.mu.Unlock()
	if state == "rebooting" {
		return true
	}
	if msg == "" {
		msg = "the update did not start"
	}
	s.fleet.edit(i, func(sp *fleetSpeaker) { sp.State, sp.Phase, sp.Reason = fleetFailed, "", msg })
	s.logger.Warn("self-update fleet: this speaker's own update failed", "err", msg)
	return false
}

// updateFleetPeer takes one peer through its update and reports every step
// through edit. The peer does all the real work itself; this only starts it
// and watches, bounded by ctx and fleetPeerBudget.
func (s *Server) updateFleetPeer(ctx context.Context, sp fleetSpeaker, edit func(func(*fleetSpeaker))) {
	base := fleetPeerBase(sp.IP, sp.Port)
	fail := func(step, msg string) {
		edit(func(r *fleetSpeaker) { r.State, r.Phase, r.Pct, r.Reason = fleetFailed, "", 0, msg })
		s.logger.Info("self-update fleet: peer failed", "peer", sp.Name, "ip", sp.IP, "step", step, "err", msg)
	}
	edit(func(r *fleetSpeaker) { r.State, r.Phase = fleetUpdating, "checking" })
	s.logger.Info("self-update fleet: peer check", "peer", sp.Name, "ip", sp.IP, "port", sp.Port, "running", sp.Version)

	// The peer checks the release itself: its own keys, its own source.
	code, st, raw, err := fleetPostAction(ctx, base, "check", 75*time.Second)
	switch {
	case err != nil:
		fail("check", "no answer: "+err.Error())
		return
	case code == http.StatusNotFound || (code == http.StatusOK && st.State == ""):
		edit(func(r *fleetSpeaker) { r.State, r.Phase, r.Reason = fleetSkipped, "", fleetSkipOldAgent })
		s.logger.Info("self-update fleet: peer skipped", "peer", sp.Name, "ip", sp.IP, "reason", fleetSkipOldAgent)
		return
	case code == http.StatusConflict:
		fail("check", "the speaker is busy with another update")
		return
	case code != http.StatusOK:
		fail("check", fmt.Sprintf("the speaker refused (HTTP %d): %s", code, shortBody(raw)))
		return
	case st.State == "failed":
		fail("check", st.Error)
		return
	case !st.Available || st.Latest == "":
		edit(func(r *fleetSpeaker) {
			r.State, r.Phase, r.Reason, r.Version = fleetSkipped, "", fleetSkipCurrent, st.Current
		})
		s.logger.Info("self-update fleet: peer skipped", "peer", sp.Name, "ip", sp.IP, "reason", fleetSkipCurrent, "running", st.Current, "latest", st.Latest)
		return
	}
	target := st.Latest
	edit(func(r *fleetSpeaker) { r.Target, r.Phase = target, "downloading" })

	code, st, raw, err = fleetPostAction(ctx, base, "install", 30*time.Second)
	switch {
	case err != nil:
		fail("install", "no answer: "+err.Error())
		return
	case code == http.StatusConflict:
		fail("install", "the speaker is busy with another update")
		return
	case code != http.StatusAccepted && code != http.StatusOK:
		fail("install", fmt.Sprintf("the speaker refused (HTTP %d): %s", code, shortBody(raw)))
		return
	}
	s.logger.Info("self-update fleet: peer installing", "peer", sp.Name, "ip", sp.IP, "from", sp.Version, "to", target)

	deadline := time.Now().Add(fleetPeerBudget)
	var backAt time.Time
	gone := false
	for {
		if ctx.Err() != nil {
			fail("wait", fmt.Sprintf("gave up waiting for %s", target))
			return
		}
		if backAt.IsZero() && time.Now().After(deadline) {
			fail("wait", fmt.Sprintf("not back on %s within %d minutes", target, int(fleetPeerBudget/time.Minute)))
			return
		}
		select {
		case <-ctx.Done():
			continue
		case <-time.After(fleetPollEvery):
		}
		var now selfUpdateStatus
		if _, err := fleetGetJSON(ctx, base+"/api/agent/self-update", 6*time.Second, &now); err != nil {
			// Rebooting: the peer is off the network for a minute or two.
			if !gone {
				gone = true
				s.logger.Info("self-update fleet: peer restarting", "peer", sp.Name, "ip", sp.IP)
			}
			edit(func(r *fleetSpeaker) { r.Phase, r.Pct = "rebooting", 0 })
			continue
		}
		onTarget := now.Current != "" && !updatesig.VersionLess(now.Current, target)
		switch {
		case now.State == "failed" && onTarget:
			// The agent is the new one; what failed is the engine fetch after it.
			edit(func(r *fleetSpeaker) {
				r.State, r.Phase, r.Pct, r.Version, r.Reason = fleetDone, "", 0, now.Current, now.Error
			})
			s.logger.Info("self-update fleet: peer updated, engine fetch failed", "peer", sp.Name, "ip", sp.IP, "version", now.Current, "err", now.Error)
			return
		case now.State == "failed":
			fail("install", now.Error)
			return
		case onTarget && now.State == "engine":
			if backAt.IsZero() {
				backAt = time.Now()
				s.logger.Info("self-update fleet: peer back on the new version, fetching its Spotify engine", "peer", sp.Name, "ip", sp.IP, "version", now.Current)
			}
			if time.Since(backAt) < fleetEngineBudget {
				edit(func(r *fleetSpeaker) { r.Phase, r.Pct, r.Version = "engine", 0, now.Current })
				continue
			}
			fallthrough
		case onTarget:
			edit(func(r *fleetSpeaker) { r.State, r.Phase, r.Pct, r.Version = fleetDone, "", 0, now.Current })
			s.logger.Info("self-update fleet: peer updated", "peer", sp.Name, "ip", sp.IP, "version", now.Current)
			return
		case now.State == "downloading" || now.State == "installing":
			pct := 0
			if now.Total > 0 {
				pct = int(100 * now.Done / now.Total)
			}
			edit(func(r *fleetSpeaker) { r.Phase, r.Pct = "downloading", pct })
		case now.State == "rebooting":
			edit(func(r *fleetSpeaker) { r.Phase, r.Pct = "rebooting", 0 })
		}
	}
}

func shortBody(s string) string {
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// selfUpdatePeerAllowlist reads the test override's "peers": when set, a run
// touches only those addresses. nil means the whole roster.
func selfUpdatePeerAllowlist() map[string]bool {
	src, ok := readSelfUpdateSource()
	if !ok || len(src.Peers) == 0 {
		return nil
	}
	m := map[string]bool{}
	for _, p := range src.Peers {
		m[strings.TrimSpace(p)] = true
	}
	return m
}

// fleetFromPending restores the run after this speaker's own reboot, with
// its own row settled: done when it came back on the new version, failed
// with the reason otherwise.
func (s *Server) fleetFromPending(p selfUpdatePending, errMsg string) {
	if p.Fleet == nil {
		return
	}
	run := *p.Fleet
	run.Speakers = append([]fleetSpeaker(nil), p.Fleet.Speakers...)
	run.Mode = "finished"
	for i := range run.Speakers {
		sp := &run.Speakers[i]
		if !sp.Self {
			continue
		}
		sp.Phase, sp.Pct = "", 0
		if errMsg != "" {
			sp.State, sp.Reason = fleetFailed, errMsg
		} else {
			sp.State, sp.Version, sp.Reason = fleetDone, agentVersion(), ""
		}
	}
	s.fleet.mu.Lock()
	s.fleet.run = &run
	s.fleet.mu.Unlock()
	s.logger.Info("self-update fleet: run finished after this speaker's restart", "version", agentVersion(), "ok", errMsg == "")
}
