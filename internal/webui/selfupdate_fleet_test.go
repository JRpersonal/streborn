package webui

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/updatesig"
)

// fakePeer is another speaker's agent as the driving speaker sees it.
type fakePeer struct {
	mu        sync.Mutex
	version   string
	target    string
	behaviour string // success, timeout, fail, old, current, refuse, netfail, outage
	installed bool
	installs  int
	finished  bool
	polls     int
	inFlight  *int32 // shared across peers: installs running right now
	maxSeen   *int32
	order     *[]string
	orderMu   *sync.Mutex
	name      string
}

func (p *fakePeer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/agent/version", func(w http.ResponseWriter, _ *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"version": p.version})
	})
	mux.HandleFunc("/api/agent/self-update", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.behaviour == "old" {
			// An agent from before the endpoint answers with its page.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, "<!doctype html><title>STR</title>")
			return
		}
		if p.behaviour == "refuse" {
			http.Error(w, "update only allowed from LAN", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodGet {
			p.polls++
			st := selfUpdateStatus{Current: p.version, State: "idle", Engine: "present"}
			behaviour := p.behaviour
			if behaviour == "netfail" && p.installs > 1 {
				behaviour = "success" // the network is back for the second try
			}
			if p.installed {
				switch behaviour {
				case "netfail":
					// The DNS of a router that is only just back (#1339).
					st.State, st.Error = "failed", `download: Get "https://github.com/JRpersonal/streborn/releases/download/v1.0.13/streborn-armv7l": dial tcp: lookup github.com on 10.0.0.1:53: server misbehaving`
					p.done()
				case "outage":
					switch p.polls {
					case 1:
						st.State, st.Done, st.Total = "downloading", 30, 100
					case 2, 3:
						// The router is off: no answer, in the MIDDLE of a download.
						http.Error(w, "gone", http.StatusServiceUnavailable)
						return
					default:
						p.version = p.target
						st.Current = p.target
						p.done()
					}
				case "success":
					switch p.polls {
					case 1:
						st.State, st.Done, st.Total = "downloading", 50, 100
					case 2:
						// Rebooting: the speaker is not there.
						http.Error(w, "gone", http.StatusServiceUnavailable)
						return
					case 3:
						p.version = p.target
						st.Current, st.State = p.target, "engine"
					default:
						st.Current = p.target
						p.done()
					}
				case "timeout":
					st.State = "rebooting"
				case "fail":
					st.State, st.Error = "failed", "download does not match the signed release"
					p.done()
				}
			}
			writeJSON(w, http.StatusOK, st)
			return
		}
		var req struct{ Action string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Action {
		case "check":
			st := selfUpdateStatus{Current: p.version, State: "idle", Latest: p.target, Engine: "present"}
			st.Available = updatesig.VersionLess(p.version, p.target)
			writeJSON(w, http.StatusOK, st)
		case "install":
			p.installed = true
			p.installs++
			p.finished = false
			p.polls = 0
			n := atomic.AddInt32(p.inFlight, 1)
			for {
				m := atomic.LoadInt32(p.maxSeen)
				if n <= m || atomic.CompareAndSwapInt32(p.maxSeen, m, n) {
					break
				}
			}
			p.orderMu.Lock()
			*p.order = append(*p.order, p.name)
			p.orderMu.Unlock()
			writeJSON(w, http.StatusAccepted, selfUpdateStatus{Current: p.version, State: "downloading"})
		}
	})
	return mux
}

// done marks this peer's install finished for the in-flight counter. Caller
// holds p.mu.
func (p *fakePeer) done() {
	if !p.finished {
		p.finished = true
		atomic.AddInt32(p.inFlight, -1)
	}
}

type fleetHarness struct {
	s        *Server
	peers    map[string]*fakePeer
	inFlight int32
	maxSeen  int32
	order    []string
	orderMu  sync.Mutex
	// states is what the last runPeer showed on the row, in order.
	states []string
}

// newFleetHarness wires fake peers behind loopback listeners and points the
// fleet seams at them. Addresses are private LAN ones, as on a real network;
// "10.0.0.250" has no listener and plays a speaker that is switched off.
func newFleetHarness(t *testing.T, self string, peers map[string]*fakePeer) *fleetHarness {
	t.Helper()
	h := &fleetHarness{peers: peers}
	urls := map[string]string{}
	var links []PeerLink
	for ip, p := range peers {
		p.inFlight, p.maxSeen, p.order, p.orderMu = &h.inFlight, &h.maxSeen, &h.order, &h.orderMu
		if p.target == "" {
			p.target = "v1.0.13"
		}
		srv := httptest.NewServer(p.handler())
		t.Cleanup(srv.Close)
		urls[ip] = srv.URL
		links = append(links, PeerLink{Name: p.name, IP: ip, URL: "http://" + ip + ":8888/", Reachable: true})
	}
	links = append(links,
		PeerLink{Name: "Switched off", IP: "10.0.0.250", URL: "http://10.0.0.250:8888/"},
		// Never part of a run: not a LAN address, and loopback.
		PeerLink{Name: "Public", IP: "8.8.8.8", URL: "http://8.8.8.8:8888/"},
		PeerLink{Name: "Loopback", IP: "127.0.0.1", URL: "http://127.0.0.1:8888/"},
	)
	origBase, origClient := fleetPeerBase, fleetClient
	origPoll, origBudget, origEngine, origNetPause := fleetPollEvery, fleetPeerBudget, fleetEngineBudget, fleetNetPause
	origVer := agentVersion
	origWait, origProbe, origReach := fleetNetWaitFor, fleetNetProbeEvery, fleetReleaseReachable
	t.Cleanup(func() {
		fleetPeerBase, fleetClient = origBase, origClient
		fleetPollEvery, fleetPeerBudget, fleetEngineBudget, fleetNetPause = origPoll, origBudget, origEngine, origNetPause
		fleetNetWaitFor, fleetNetProbeEvery, fleetReleaseReachable = origWait, origProbe, origReach
		agentVersion = origVer
	})
	fleetPeerBase = func(ip string, _ int) string {
		if u, ok := urls[ip]; ok {
			return u
		}
		return "http://127.0.0.1:1" // nothing listens: unreachable
	}
	fleetClient = func(timeout time.Duration) *http.Client { return &http.Client{Timeout: timeout} }
	fleetPollEvery, fleetPeerBudget, fleetEngineBudget = 5*time.Millisecond, 2*time.Second, 50*time.Millisecond
	fleetNetPause = 10 * time.Millisecond
	fleetNetWaitFor, fleetNetProbeEvery = time.Second, 5*time.Millisecond
	fleetReleaseReachable = func(context.Context) bool { return true }
	SetAgentVersion(self)

	dir := t.TempDir()
	for _, v := range []*string{&selfUpdateSourcePath, &selfUpdatePendingPath, &selfUpdateResultPath} {
		orig := *v
		t.Cleanup(func() { *v = orig })
	}
	selfUpdateSourcePath = filepath.Join(dir, "source.json")
	selfUpdatePendingPath = filepath.Join(dir, "pending.json")
	selfUpdateResultPath = filepath.Join(dir, "last.json")

	h.s = &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.s.peersFn = func(context.Context) []PeerLink { return links }
	h.s.boxNameFn = func() (string, string) { return "Driver", "SoundTouch 10" }
	// The latest release, as a check just found it.
	h.s.selfUpd.index = &updatesig.Index{Version: "v1.0.13"}
	h.s.selfUpd.checkedAt = time.Now()
	return h
}

func rowByName(t *testing.T, rows []fleetSpeaker, name string) fleetSpeaker {
	t.Helper()
	for _, r := range rows {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no row %q in %+v", name, rows)
	return fleetSpeaker{}
}

// The plan lists every peer in a stable order and this speaker LAST, with
// the skip rules applied: up to date, too old for the phone, not reachable.
// Addresses that are not on the LAN never make it into the list.
func TestFleetPlanOrdersThisSpeakerLastAndAppliesSkipRules(t *testing.T) {
	h := newFleetHarness(t, "v1.0.12", map[string]*fakePeer{
		"10.0.0.2": {name: "Kitchen", version: "v1.0.12", behaviour: "success"},
		"10.0.0.3": {name: "Bath", version: "v1.0.13", behaviour: "success"},
		"10.0.0.4": {name: "Attic", version: "v1.0.5", behaviour: "old"},
	})
	rows := h.s.planFleet(context.Background(), "v1.0.13", nil)
	var names []string
	for _, r := range rows {
		names = append(names, r.Name)
		if r.IP == "8.8.8.8" || r.IP == "127.0.0.1" {
			t.Errorf("a non-LAN address made it into the run: %+v", r)
		}
	}
	if got := strings.Join(names, ","); got != "Attic,Bath,Kitchen,Switched off,Driver" {
		t.Fatalf("order %s", got)
	}
	if last := rows[len(rows)-1]; !last.Self || last.State != fleetWaiting || !last.Needs {
		t.Errorf("this speaker must come last and need the update: %+v", last)
	}
	if r := rowByName(t, rows, "Kitchen"); r.State != fleetWaiting || r.Version != "v1.0.12" || !r.Needs {
		t.Errorf("kitchen %+v", r)
	}
	if r := rowByName(t, rows, "Bath"); r.State != fleetSkipped || r.Reason != fleetSkipCurrent {
		t.Errorf("bath is current: %+v", r)
	}
	if r := rowByName(t, rows, "Attic"); r.State != fleetSkipped || r.Reason != fleetSkipOldAgent || r.Version != "v1.0.5" {
		t.Errorf("attic is too old for the phone: %+v", r)
	}
	if r := rowByName(t, rows, "Switched off"); r.State != fleetSkipped || r.Reason != fleetSkipUnreachable {
		t.Errorf("switched off: %+v", r)
	}
	if n := atomic.LoadInt32(&h.inFlight); n != 0 || len(h.order) != 0 {
		t.Fatal("planning installed something")
	}
}

// A speaker that is current itself still drives a run for the others, and
// its own row says so.
func TestFleetPlanSelfCurrentIsSkipped(t *testing.T) {
	h := newFleetHarness(t, "v1.0.13", map[string]*fakePeer{
		"10.0.0.2": {name: "Kitchen", version: "v1.0.12", behaviour: "success"},
	})
	rows := h.s.planFleet(context.Background(), "v1.0.13", nil)
	last := rows[len(rows)-1]
	if !last.Self || last.State != fleetSkipped || last.Reason != fleetSkipCurrent {
		t.Fatalf("self row %+v", last)
	}
}

func runPeer(t *testing.T, h *fleetHarness, ip string) fleetSpeaker {
	t.Helper()
	p := h.peers[ip]
	row := planFleetPeer(context.Background(), fleetSpeaker{Name: p.name, IP: ip, Port: 8888}, "v1.0.13")
	if row.State != fleetWaiting {
		return row
	}
	var mu sync.Mutex
	var states []string
	h.s.updateFleetPeer(context.Background(), row, func(fn func(*fleetSpeaker)) {
		mu.Lock()
		defer mu.Unlock()
		fn(&row)
		if len(states) == 0 || states[len(states)-1] != row.State+"/"+row.Phase {
			states = append(states, row.State+"/"+row.Phase)
		}
	})
	t.Logf("%s: %s", p.name, strings.Join(states, " -> "))
	h.states = states
	return row
}

func TestFleetPeerSuccessWaitsThroughRebootAndEngine(t *testing.T) {
	h := newFleetHarness(t, "v1.0.12", map[string]*fakePeer{
		"10.0.0.2": {name: "Kitchen", version: "v1.0.12", behaviour: "success"},
	})
	row := runPeer(t, h, "10.0.0.2")
	if row.State != fleetDone || row.Version != "v1.0.13" || row.Reason != "" {
		t.Fatalf("row %+v", row)
	}
}

func TestFleetPeerTimeoutIsAFailureWithAReason(t *testing.T) {
	h := newFleetHarness(t, "v1.0.12", map[string]*fakePeer{
		"10.0.0.2": {name: "Kitchen", version: "v1.0.12", behaviour: "timeout"},
	})
	fleetPeerBudget = 100 * time.Millisecond
	row := runPeer(t, h, "10.0.0.2")
	if row.State != fleetFailed || !strings.Contains(row.Reason, "not back on v1.0.13") {
		t.Fatalf("row %+v", row)
	}
}

func TestFleetPeerFailureCarriesThePeersError(t *testing.T) {
	h := newFleetHarness(t, "v1.0.12", map[string]*fakePeer{
		"10.0.0.2": {name: "Kitchen", version: "v1.0.12", behaviour: "fail"},
	})
	row := runPeer(t, h, "10.0.0.2")
	if row.State != fleetFailed || !strings.Contains(row.Reason, "does not match") {
		t.Fatalf("row %+v", row)
	}
}

// An agent from before the endpoint is never attempted another way.
func TestFleetPeerOldAgentIsSkippedNotInstalled(t *testing.T) {
	h := newFleetHarness(t, "v1.0.12", map[string]*fakePeer{
		"10.0.0.2": {name: "Kitchen", version: "v1.0.9", behaviour: "old"},
	})
	row := runPeer(t, h, "10.0.0.2")
	if row.State != fleetSkipped || row.Reason != fleetSkipOldAgent {
		t.Fatalf("row %+v", row)
	}
	if len(h.order) != 0 {
		t.Fatal("an old agent was asked to install")
	}
	// The same at run time: a peer that turned out old between plan and run.
	row = fleetSpeaker{Name: "Kitchen", IP: "10.0.0.2", Port: 8888, State: fleetWaiting, Version: "v1.0.9"}
	h.s.updateFleetPeer(context.Background(), row, func(fn func(*fleetSpeaker)) { fn(&row) })
	if row.State != fleetSkipped || row.Reason != fleetSkipOldAgent {
		t.Fatalf("run-time row %+v", row)
	}
}

func TestFleetPeerThatRefusesIsNotUpdated(t *testing.T) {
	h := newFleetHarness(t, "v1.0.12", map[string]*fakePeer{
		"10.0.0.2": {name: "Kitchen", version: "v1.0.12", behaviour: "refuse"},
	})
	row := fleetSpeaker{Name: "Kitchen", IP: "10.0.0.2", Port: 8888, State: fleetWaiting}
	h.s.updateFleetPeer(context.Background(), row, func(fn func(*fleetSpeaker)) { fn(&row) })
	if row.State != fleetFailed || !strings.Contains(row.Reason, "403") || len(h.order) != 0 {
		t.Fatalf("row %+v", row)
	}
}

// The whole run: peers strictly one at a time, in the plan's order, the
// current and the old one left alone, and the run finished with a tally.
// This speaker is current here, so no reboot ends the run.
func TestFleetRunUpdatesPeersOneAtATime(t *testing.T) {
	h := newFleetHarness(t, "v1.0.13", map[string]*fakePeer{
		"10.0.0.2": {name: "Kitchen", version: "v1.0.12", behaviour: "success"},
		"10.0.0.3": {name: "Bath", version: "v1.0.11", behaviour: "success"},
		"10.0.0.4": {name: "Attic", version: "v1.0.12", behaviour: "fail"},
		"10.0.0.5": {name: "Hall", version: "v1.0.13", behaviour: "success"},
		"10.0.0.6": {name: "Garage", version: "v1.0.2", behaviour: "old"},
	})
	if !h.s.startFleet(nil, "") {
		t.Fatal("run did not start")
	}
	if h.s.startFleet(nil, "") {
		t.Fatal("a second run started while the first one runs")
	}
	deadline := time.Now().Add(10 * time.Second)
	for h.s.fleet.running() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	run := h.s.fleet.snapshot()
	if run == nil || run.Mode != "finished" {
		t.Fatalf("run %+v", run)
	}
	if got := strings.Join(h.order, ","); got != "Attic,Bath,Kitchen" {
		t.Errorf("install order %s", got)
	}
	if m := atomic.LoadInt32(&h.maxSeen); m != 1 {
		t.Errorf("%d peers installed at the same time", m)
	}
	want := map[string]string{
		"Attic": fleetFailed, "Bath": fleetDone, "Kitchen": fleetDone,
		"Hall": fleetSkipped, "Garage": fleetSkipped, "Switched off": fleetSkipped, "Driver": fleetSkipped,
	}
	for name, st := range want {
		if r := rowByName(t, run.Speakers, name); r.State != st {
			t.Errorf("%s: %s (%s), want %s", name, r.State, r.Reason, st)
		}
	}
}

// While this speaker drives a run, a phone (or another driver) asking it to
// update on its own is turned away: that would reboot it in the middle.
func TestFleetRunBlocksASingleInstall(t *testing.T) {
	h := newFleetHarness(t, "v1.0.12", nil)
	h.s.fleet.run = &fleetRun{Mode: "running"}
	req := httptest.NewRequest(http.MethodPost, "/api/agent/self-update", strings.NewReader(`{"action":"install"}`))
	req.RemoteAddr = "192.168.1.20:5000"
	rec := httptest.NewRecorder()
	h.s.handleSelfUpdate(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("install during a run: %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/agent/self-update", strings.NewReader(`{"action":"install-all"}`))
	req.RemoteAddr = "192.168.1.20:5000"
	rec = httptest.NewRecorder()
	h.s.handleSelfUpdate(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second run: %d", rec.Code)
	}
	// And the whole thing stays behind the LAN gate.
	req = httptest.NewRequest(http.MethodPost, "/api/agent/self-update", strings.NewReader(`{"action":"install-all"}`))
	req.RemoteAddr = "203.0.113.9:5000"
	rec = httptest.NewRecorder()
	h.s.handleSelfUpdate(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("install-all from outside the LAN: %d", rec.Code)
	}
}

// This speaker's own update is the last step and reboots it. The run rides
// the pending marker across that reboot and comes back finished, with this
// speaker's row settled by the version it came back on.
func TestFleetRunSurvivesThisSpeakersReboot(t *testing.T) {
	h := newFleetHarness(t, "v1.0.12", nil)
	run := &fleetRun{Mode: "running", Target: "v1.0.13", Speakers: []fleetSpeaker{
		{Name: "Kitchen", IP: "10.0.0.2", State: fleetDone, Version: "v1.0.13"},
		{Name: "Driver", Self: true, State: fleetUpdating, Phase: "rebooting", Version: "v1.0.12"},
	}}
	if err := writeJSONFile(selfUpdatePendingPath, selfUpdatePending{Version: "v1.0.13", From: "v1.0.12", Fleet: run}); err != nil {
		t.Fatal(err)
	}
	// A fresh agent: nothing in memory.
	SetAgentVersion("v1.0.13")
	s := &Server{logger: h.s.logger}
	s.resumeSelfUpdate()
	got := s.selfUpdateStatus().Fleet
	if got == nil || got.Mode != "finished" {
		t.Fatalf("run after the reboot: %+v", got)
	}
	if r := rowByName(t, got.Speakers, "Driver"); r.State != fleetDone || r.Version != "v1.0.13" {
		t.Errorf("self row %+v", r)
	}
	if r := rowByName(t, got.Speakers, "Kitchen"); r.State != fleetDone {
		t.Errorf("peer row lost: %+v", r)
	}
	// The resume finishes in the background and removes the marker; let it,
	// or the next write races that removal (Windows refuses the open).
	for deadline := time.Now().Add(5 * time.Second); s.selfUpd.isBusy() && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}

	// Came back on the old version: the row says so.
	if err := writeJSONFile(selfUpdatePendingPath, selfUpdatePending{Version: "v1.0.13", From: "v1.0.12", Fleet: run}); err != nil {
		t.Fatal(err)
	}
	SetAgentVersion("v1.0.12")
	s = &Server{logger: h.s.logger}
	s.resumeSelfUpdate()
	if r := rowByName(t, s.selfUpdateStatus().Fleet.Speakers, "Driver"); r.State != fleetFailed || !strings.Contains(r.Reason, "instead of v1.0.13") {
		t.Errorf("self row after a failed swap %+v", r)
	}
}

// The developer override's "peers" narrows a run to the listed speakers.
func TestFleetPeerAllowlistNarrowsTheRoster(t *testing.T) {
	h := newFleetHarness(t, "v1.0.12", map[string]*fakePeer{
		"10.0.0.2": {name: "Kitchen", version: "v1.0.12", behaviour: "success"},
		"10.0.0.3": {name: "Bath", version: "v1.0.12", behaviour: "success"},
	})
	if err := os.WriteFile(selfUpdateSourcePath, []byte(`{"base":"http://192.0.2.10:8765/","peers":["10.0.0.3"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rows := h.s.fleetPeerAddrs(context.Background())
	if len(rows) != 1 || rows[0].Name != "Bath" {
		t.Fatalf("rows %+v", rows)
	}
}

func TestFleetPortsTryTheKnownPortFirst(t *testing.T) {
	if got := fleetPorts(17008); len(got) != 2 || got[0] != 17008 || got[1] != 8888 {
		t.Errorf("%v", got)
	}
	if got := fleetPorts(0); len(got) != 2 || got[0] != 8888 {
		t.Errorf("%v", got)
	}
	if ip, port := fleetPeerAddr(PeerLink{URL: "http://192.168.1.5:17008/"}); ip != "192.168.1.5" || port != 17008 {
		t.Errorf("%s %d", ip, port)
	}
	if _, port := fleetPeerAddr(PeerLink{IP: "192.168.1.5", URL: "http://192.168.1.5:22/"}); port != 0 {
		t.Errorf("a port that is not an agent port became a hint: %d", port)
	}
}

// --- the phone card ---

func TestPhoneCardOffersUpdateAllByDefault(t *testing.T) {
	page := readIndexHTML(t)
	for _, id := range []string{`id="suFleet"`, `id="suFleetList"`, `id="btnSuAll"`, `id="btnSuOnly"`} {
		if !strings.Contains(page, id) {
			t.Errorf("the card lost %s", id)
		}
	}
	js := page[strings.Index(page, "(function wireSelfUpdate(){"):strings.Index(page, "(function wireDiag(){")]
	if !strings.Contains(js, "action: 'install-all', peers: [row.ip]") || !strings.Contains(js, "action: 'plan'") {
		t.Error("the card does not drive the fleet actions")
	}
	// The phone drives: it claims the run, talks to each peer directly, hands
	// a peer it cannot reach to this speaker, and updates this speaker last.
	for _, want := range []string{
		"action: 'run-claim', token: run.token",
		// Awaited, or the reload sees this phone's own claim as another phone's.
		"await apiDetail(SU, 'POST', { action: 'run-end', token: run.token });",
		"peerCall(base, 'POST', { action: 'check' }",
		"peerCall(base, 'POST', { action: 'install' }",
		"=== 'unreachable') setRow(row, { via: 'speaker'",
		"action: 'install', token: run.token",
		"mode: 'cors'",
		"credentials: 'omit'",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the phone run lost %q", want)
		}
	}
	if strings.Index(js, "await updateSelf(self)") < strings.Index(js, "await updateViaSpeaker(row)") {
		t.Error("this speaker must be updated after every peer")
	}
	// Backgrounded: the run waits between steps; a reload resumes it.
	for _, want := range []string{"visibilitychange", "function gate()", "localStorage.setItem(RUN_KEY", "run = loadRun();"} {
		if !strings.Contains(js, want) {
			t.Errorf("pause/resume lost %q", want)
		}
	}
	// Storage may throw (private mode): every access sits inside a try.
	for _, loc := range regexp.MustCompile(`localStorage\.`).FindAllStringIndex(js, -1) {
		if !strings.Contains(js[max(0, loc[0]-120):loc[0]], "try {") {
			t.Errorf("unguarded storage access at %q", js[loc[0]:min(len(js), loc[0]+40)])
		}
	}
	// Default all: the single-speaker choice starts off.
	if !regexp.MustCompile(`var onlyThis = false`).MatchString(js) {
		t.Error("the default must be all speakers")
	}
	// A reload during a run follows the run instead of starting a check.
	if !strings.Contains(js, "if (runActive()) { drive(); return; }") || !strings.Contains(js, "if (fleetRunning()) { pollFleet(); return; }") {
		t.Error("opening the tab during a run must pick the run up")
	}
	// Names come from the network: text only, never markup.
	if strings.Contains(js, "innerHTML") {
		t.Error("the fleet list must not build markup from speaker names")
	}
}

func TestPhoneCardFleetStringsInEveryLocale(t *testing.T) {
	bundles := strings.Count(indexHTML, "suHead:\"")
	if bundles != 12 {
		t.Fatalf("%d locale bundles carry the update card, want 12", bundles)
	}
	for _, key := range []string{"sfAll", "sfOnly", "sfAllLink", "sfThis", "sfNeeds", "sfWaiting", "sfCurrent",
		"sfOld", "sfUnreach", "suEngineByUpd", "sfChecking", "sfReboot", "sfConfirm", "sfRunning", "sfSummary", "sfLoading",
		"sfVia", "sfOther", "sfOneBtn", "sfOneNote"} {
		if got := len(regexp.MustCompile(`[{,]`+key+`:"`).FindAllString(indexHTML, -1)); got != bundles {
			t.Errorf("%s: in %d of %d locales", key, got, bundles)
		}
	}
	for _, m := range regexp.MustCompile(`sfAll:"([^"]*)"`).FindAllStringSubmatch(indexHTML, -1) {
		if !strings.Contains(m[1], "{n}") {
			t.Errorf("sfAll must name the count: %q", m[1])
		}
	}
	for _, m := range regexp.MustCompile(`sfSummary:"([^"]*)"`).FindAllStringSubmatch(indexHTML, -1) {
		for _, ph := range []string{"{ok}", "{fail}", "{skip}"} {
			if !strings.Contains(m[1], ph) {
				t.Errorf("sfSummary %q lacks %s", m[1], ph)
			}
		}
	}
	// German: du-form and real umlauts; the phone must stay open now.
	if !strings.Contains(indexHTML, `sfRunning:"Die Lautsprecher werden nacheinander aktualisiert. Lass diese Seite offen, bis alles fertig ist."`) {
		t.Error("German running line changed or lost its du-form")
	}
	if !strings.Contains(indexHTML, `sfUnreach:"Gerade nicht erreichbar. Sobald er wieder im Netzwerk ist, taucht er hier wieder auf."`) {
		t.Error("German unreachable line changed")
	}
	// Every locale tells the owner to keep the page open, and none of the
	// new lines uses a dash as punctuation.
	for _, key := range []string{"sfRunning", "sfConfirm", "sfUnreach", "sfOld", "suEngineByUpd", "sfVia", "sfOther"} {
		for _, m := range regexp.MustCompile(key+`:"([^"]*)"`).FindAllStringSubmatch(indexHTML, -1) {
			if strings.ContainsAny(m[1], "–—") || strings.Contains(m[1], " - ") {
				t.Errorf("%s uses a dash: %q", key, m[1])
			}
		}
	}
}

// #1339: after "Only this speaker" the card still listed every peer as
// "Update available" above a plain "Update to vX" button, so the owner read
// it as an update for all of them. The single-speaker mode now lists only
// this speaker, says so, and names it on the button.
func TestPhoneCardOnlyThisSpeakerIsUnambiguous(t *testing.T) {
	page := readIndexHTML(t)
	js := page[strings.Index(page, "(function wireSelfUpdate(){"):strings.Index(page, "(function wireDiag(){")]
	for _, want := range []string{
		"if (onlyThis && mode === 'plan') rows = rows.filter(function(sp){ return sp.self; });",
		"fill(onlyThis && fleet() ? T.sfOneBtn : T.suInstall, st.latest)",
		"else if (onlyThis && fleet() && st.available) show(T.sfOneNote + (suGroupNoteDue() ? ' ' + T.suGrpNote : ''));",
		// Once this speaker is through, the card offers the rest again.
		// A speaker that went quiet mid-update keeps the choice (#1502).
		"if (onlyThis && st && !lostSince && st.state !== 'failed' &&",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("single-speaker mode lost %q", want)
		}
	}
	for _, m := range regexp.MustCompile(`sfOneBtn:"([^"]*)"`).FindAllStringSubmatch(page, -1) {
		if !strings.Contains(m[1], "{v}") {
			t.Errorf("sfOneBtn must name the version: %q", m[1])
		}
	}
	for _, key := range []string{"sfOneBtn", "sfOneNote"} {
		for _, m := range regexp.MustCompile(key+`:"([^"]*)"`).FindAllStringSubmatch(page, -1) {
			if strings.ContainsAny(m[1], "–—") || strings.Contains(m[1], " - ") {
				t.Errorf("%s uses a dash: %q", key, m[1])
			}
		}
	}
	if !strings.Contains(page, `sfOneBtn:"Nur diesen Lautsprecher auf {v} aktualisieren"`) {
		t.Error("German single-speaker button changed")
	}
}
