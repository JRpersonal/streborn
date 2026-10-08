package marge

import (
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// fakeProbe answers the partner probe from a script: each call pops the next
// result, and the last one repeats.
type fakeProbe struct {
	mu      sync.Mutex
	results []error
	calls   int
}

func (f *fakeProbe) probe(string, time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.results) == 0 {
		return nil
	}
	r := f.results[0]
	if len(f.results) > 1 {
		f.results = f.results[1:]
	}
	return r
}

func (f *fakeProbe) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

var errUnreachable = errors.New("dial tcp 192.0.2.25:8090: connect: network is unreachable")

func pairedTestServer(p *fakeProbe) (*Server, *groupRecord) {
	g := &groupRecord{
		ID: "str-grp-DEV#MASTER", MasterDeviceID: "DEV#MASTER",
		Roles: []groupRole{
			{DeviceID: "DEV#SELF", Role: "LEFT", IP: "192.0.2.26"},
			{DeviceID: "DEV#MASTER", Role: "RIGHT", IP: "192.0.2.25"},
		},
	}
	s := &Server{
		logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		deviceID:        "DEV#SELF",
		group:           g,
		partnerProbe:    p.probe,
		partnerInterval: time.Millisecond,
	}
	return s, g
}

// #1208: right after an update reboot the agent started before the network
// was up, the one startup probe failed with "network is unreachable" and both
// stereo pairs refused every play from then on. A probe that fails at first and
// then answers must leave nothing recorded.
func TestStartupProbeRetriesUntilThePartnerAnswers(t *testing.T) {
	p := &fakeProbe{results: []error{errUnreachable, errUnreachable, nil}}
	s, g := pairedTestServer(p)
	s.notePartnerReachability(g)
	if ip, _ := s.PartnerUnreachable(); ip != "" {
		t.Fatalf("partner recorded as gone (%q) although it answered on the third attempt", ip)
	}
	if p.count() != 3 {
		t.Fatalf("probe ran %d times, want 3 (stop at the first answer)", p.count())
	}
}

func TestStartupProbeRecordsAPartnerThatNeverAnswers(t *testing.T) {
	p := &fakeProbe{results: []error{errUnreachable}}
	s, g := pairedTestServer(p)
	s.notePartnerReachability(g)
	ip, id := s.PartnerUnreachable()
	if ip != "192.0.2.25" || id != "DEV#MASTER" {
		t.Fatalf("PartnerUnreachable = %q, %q; want the partner after the final failure", ip, id)
	}
	if p.count() != partnerProbeAttempts {
		t.Fatalf("probe ran %d times, want the bounded %d", p.count(), partnerProbeAttempts)
	}
}

func TestStartupProbeStopsWhenThePairIsDissolvedMeanwhile(t *testing.T) {
	p := &fakeProbe{results: []error{errUnreachable}}
	s, g := pairedTestServer(p)
	s.partnerProbe = func(ip string, d time.Duration) error {
		s.ClearGroup("test")
		return p.probe(ip, d)
	}
	s.notePartnerReachability(g)
	if ip, _ := s.PartnerUnreachable(); ip != "" {
		t.Fatalf("a dissolved pair still reports its partner as gone: %q", ip)
	}
	if p.count() != 1 {
		t.Fatalf("probe ran %d times after the pair was dissolved, want 1", p.count())
	}
}

func TestRecheckClearsOnceThePartnerAnswers(t *testing.T) {
	p := &fakeProbe{results: []error{errUnreachable}}
	s, g := pairedTestServer(p)
	s.partnerAttempts = 1
	s.notePartnerReachability(g)
	if ip, _ := s.PartnerUnreachable(); ip == "" {
		t.Fatal("setup: partner not recorded as gone")
	}
	p.mu.Lock()
	p.results = []error{nil}
	p.mu.Unlock()
	s.partnerCheckedAt = time.Time{} // outside the rate-limit window
	if ip, _ := s.RecheckPartner(); ip != "" {
		t.Fatalf("RecheckPartner = %q after the partner answered, want empty", ip)
	}
	if ip, _ := s.PartnerUnreachable(); ip != "" {
		t.Fatalf("PartnerUnreachable = %q after a successful recheck", ip)
	}
}

func TestRecheckIsRateLimited(t *testing.T) {
	p := &fakeProbe{results: []error{errUnreachable}}
	s, g := pairedTestServer(p)
	s.partnerAttempts = 1
	s.notePartnerReachability(g)
	before := p.count()
	// The startup probe has just run: inside the window nothing is probed and
	// the cached verdict stands.
	for range 5 {
		if ip, _ := s.RecheckPartner(); ip != "192.0.2.25" {
			t.Fatalf("RecheckPartner = %q inside the window, want the cached verdict", ip)
		}
	}
	if p.count() != before {
		t.Fatalf("probe ran %d extra times inside the rate-limit window", p.count()-before)
	}
	s.partnerCheckedAt = time.Now().Add(-partnerRecheckEvery - time.Second)
	if ip, _ := s.RecheckPartner(); ip != "192.0.2.25" {
		t.Fatalf("RecheckPartner = %q for a partner that stays down, want it still recorded", ip)
	}
	s.RecheckPartner()
	if p.count() != before+1 {
		t.Fatalf("probe ran %d times after the window, want exactly one", p.count()-before)
	}
}

func TestRecheckCostsNothingWithoutARecord(t *testing.T) {
	p := &fakeProbe{}
	s, _ := pairedTestServer(p)
	if ip, _ := s.RecheckPartner(); ip != "" {
		t.Fatalf("RecheckPartner = %q with nothing recorded", ip)
	}
	if p.count() != 0 {
		t.Fatalf("RecheckPartner probed %d times with nothing recorded, want none", p.count())
	}
}
