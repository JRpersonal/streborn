package main

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

const wpaStatusAssociated = `bssid=aa:bb:cc:dd:ee:ff
freq=2437
ssid=example-network
id=0
mode=station
pairwise_cipher=CCMP
group_cipher=CCMP
key_mgmt=WPA2-PSK
wpa_state=COMPLETED
address=aa:bb:cc:dd:ee:00
`

// fakeWPA records every wpa_cli call and answers from a script.
type fakeWPA struct {
	calls  []string
	status string
	reply  map[string]string // first arg -> reply; default "OK"
	err    error
}

func (f *fakeWPA) run(iface string, args ...string) (string, error) {
	f.calls = append(f.calls, iface+" "+strings.Join(args, " "))
	if f.err != nil {
		return "", f.err
	}
	if args[0] == "status" {
		return f.status, nil
	}
	if r, ok := f.reply[args[0]]; ok {
		return r, nil
	}
	return "OK\n", nil
}

func (f *fakeWPA) count(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, "wlan0 "+prefix) {
			n++
		}
	}
	return n
}

type leaseHarness struct {
	r     *leaseRecovery
	wpa   *fakeWPA
	clock time.Time
	setup bool
	busy  bool
}

func newLeaseHarness() *leaseHarness {
	h := &leaseHarness{
		wpa:   &fakeWPA{status: wpaStatusAssociated},
		clock: time.Date(2026, 11, 1, 14, 0, 0, 0, time.UTC),
	}
	h.r = &leaseRecovery{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:      func() time.Time { return h.clock },
		wpa:      h.wpa.run,
		inSetup:  func() bool { return h.setup },
		wlanBusy: func() bool { return h.busy },
	}
	return h
}

var (
	obsNoLease = leaseObservation{Iface: "wlan0", Carrier: true, HasLease: false, Reason: "no usable IPv4 address on wlan0"}
	obsLease   = leaseObservation{Iface: "wlan0", Carrier: true, HasLease: true}
)

// tick advances the clock by d and runs one check with obs.
func (h *leaseHarness) tick(d time.Duration, obs leaseObservation) {
	h.clock = h.clock.Add(d)
	h.r.step(obs)
}

func TestLeaseRecoveryReassociatesAfterTenMinutesWithoutLease(t *testing.T) {
	h := newLeaseHarness()
	h.tick(0, obsLease)
	h.tick(5*time.Minute, obsNoLease) // lease gone: clock starts
	h.tick(5*time.Minute, obsNoLease) // 5 min without
	if n := h.wpa.count("reassociate"); n != 0 {
		t.Fatalf("reassociated after only 5 min without a lease (%d calls)", n)
	}
	h.tick(5*time.Minute, obsNoLease) // 10 min without
	if n := h.wpa.count("reassociate"); n != 1 {
		t.Fatalf("want one reassociate after 10 min without a lease, got %d (calls %v)", n, h.wpa.calls)
	}
	if n := h.wpa.count("blacklist"); n != 0 {
		t.Fatalf("the first attempt must be a plain reassociate, got ignore-list calls: %v", h.wpa.calls)
	}
	for _, c := range h.wpa.calls {
		for _, banned := range []string{"remove_network", "add_network", "select_network", "save_config", "reconfigure", "set_network", "disable_network"} {
			if strings.Contains(c, banned) {
				t.Fatalf("lease recovery must never touch the network list, ran %q", c)
			}
		}
	}

	h.tick(5*time.Minute, obsLease) // the reassociation worked
	sec := h.r.debugSection().(map[string]any)
	if sec["attempts"] != 1 || sec["recovered"] != 1 || sec["failedInARow"] != 0 {
		t.Fatalf("debug section after a successful attempt: %v", sec)
	}
	hist := sec["history"].([]leaseAttempt)
	if len(hist) != 1 || hist[0].Outcome != "lease back" || !hist[0].CommandOK {
		t.Fatalf("history after a successful attempt: %+v", hist)
	}
}

func TestLeaseRecoveryLeavesAHealthyOrBrieflyDisturbedBoxAlone(t *testing.T) {
	h := newLeaseHarness()
	for i := 0; i < 20; i++ {
		h.tick(5*time.Minute, obsLease)
	}
	// A short outage (router reboot) that heals inside the threshold.
	h.tick(5*time.Minute, obsNoLease)
	h.tick(5*time.Minute, obsNoLease)
	h.tick(4*time.Minute, obsLease)
	h.tick(5*time.Minute, obsNoLease) // new episode, clock restarts
	h.tick(5*time.Minute, obsNoLease)
	if len(h.wpa.calls) != 0 {
		t.Fatalf("a healthy or briefly disturbed box must not run wpa_cli at all, got %v", h.wpa.calls)
	}
}

func TestLeaseRecoveryHandsOffInSetupMode(t *testing.T) {
	h := newLeaseHarness()
	h.setup = true
	h.tick(0, obsNoLease)
	for i := 0; i < 12; i++ {
		h.tick(5*time.Minute, obsNoLease)
	}
	if len(h.wpa.calls) != 0 {
		t.Fatalf("setup mode must keep STR off the radio entirely, got %v", h.wpa.calls)
	}
	if got := h.r.debugSection().(map[string]any)["lastSkip"]; !strings.Contains(got.(string), "setup") {
		t.Fatalf("skip reason should name setup mode, got %v", got)
	}
	// Setup ends, the condition persists: the watcher acts on the next tick.
	h.setup = false
	h.tick(5*time.Minute, obsNoLease)
	if n := h.wpa.count("reassociate"); n != 1 {
		t.Fatalf("want a reassociate once setup is over, got %d", n)
	}
}

func TestLeaseRecoveryHandsOffWhenNotAssociatedOrNotStation(t *testing.T) {
	cases := []struct {
		name   string
		status string
		obs    leaseObservation
		busy   bool
	}{
		{"scanning", strings.Replace(wpaStatusAssociated, "wpa_state=COMPLETED", "wpa_state=SCANNING", 1), obsNoLease, false},
		{"access point mode", strings.Replace(wpaStatusAssociated, "mode=station", "mode=AP", 1), obsNoLease, false},
		{"no configured network", strings.Replace(wpaStatusAssociated, "id=0\n", "", 1), obsNoLease, false},
		{"no carrier", wpaStatusAssociated, leaseObservation{Iface: "wlan0", Carrier: false}, false},
		{"str wifi change running", wpaStatusAssociated, obsNoLease, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newLeaseHarness()
			h.wpa.status = c.status
			h.busy = c.busy
			h.tick(0, c.obs)
			for i := 0; i < 6; i++ {
				h.tick(5*time.Minute, c.obs)
			}
			if n := h.wpa.count("reassociate"); n != 0 {
				t.Fatalf("must not reassociate (%s), calls %v", c.name, h.wpa.calls)
			}
		})
	}
}

func TestLeaseRecoveryRateLimitAndBackoff(t *testing.T) {
	h := newLeaseHarness()
	h.tick(0, obsNoLease)
	attemptTimes := []time.Time{}
	last := 0
	// Run 12 hours of 5-minute ticks with the lease never coming back.
	for i := 0; i < 12*12; i++ {
		h.tick(5*time.Minute, obsNoLease)
		if n := h.wpa.count("reassociate"); n != last {
			last = n
			attemptTimes = append(attemptTimes, h.clock)
		}
	}
	if len(attemptTimes) < 4 {
		t.Fatalf("want several attempts over 12 h, got %d", len(attemptTimes))
	}
	for i := 1; i < len(attemptTimes); i++ {
		gap := attemptTimes[i].Sub(attemptTimes[i-1])
		if gap < leaseAttemptGap {
			t.Fatalf("attempts %d and %d only %v apart, want >= %v", i, i+1, gap, leaseAttemptGap)
		}
		// Attempts 1..3 failed by the time attempt 4 is due: from then on the
		// gap is the back-off one.
		if i >= leaseBackoffAfter && gap < leaseBackoffGap {
			t.Fatalf("attempt %d came %v after the previous one; after %d failures the gap must be >= %v",
				i+1, gap, leaseBackoffAfter, leaseBackoffGap)
		}
	}
	// 10 min threshold, then attempts at +0, +30, +60, then every 2 h:
	// 12 h of ticks allow 3 + (12h - ~1h20)/2h = 3 + 5 = 8 at most.
	if len(attemptTimes) > 8 {
		t.Fatalf("too many attempts in 12 h: %d", len(attemptTimes))
	}
	sec := h.r.debugSection().(map[string]any)
	if sec["backoff"] != true {
		t.Fatalf("debug section should report back-off, got %v", sec)
	}

	// From the second attempt on the stuck access point goes on the
	// ignore list first, and is cleared once the lease is back.
	if n := h.wpa.count("blacklist aa:bb:cc:dd:ee:ff"); n != len(attemptTimes)-1 {
		t.Fatalf("want the stuck AP ignored on every attempt after the first (%d), got %d", len(attemptTimes)-1, n)
	}
	h.tick(5*time.Minute, obsLease)
	if n := h.wpa.count("blacklist clear"); n != 1 {
		t.Fatalf("want the ignore list cleared once the lease is back, got %d (calls %v)", n, h.wpa.calls)
	}
	if sec := h.r.debugSection().(map[string]any); sec["failedInARow"] != 0 || sec["backoff"] != false {
		t.Fatalf("a lease coming back must reset the back-off, got %v", sec)
	}
}

func TestLeaseRecoveryFailedCommandCountsAsFailure(t *testing.T) {
	h := newLeaseHarness()
	h.wpa.reply = map[string]string{"reassociate": "FAIL\n"}
	h.tick(0, obsNoLease)
	h.tick(10*time.Minute, obsNoLease)
	sec := h.r.debugSection().(map[string]any)
	if sec["failedInARow"] != 1 || sec["pending"] != false {
		t.Fatalf("a refused reassociate must count as a failed attempt, got %v", sec)
	}
}

func TestLeaseRecoveryUnsupportedChassisSaysSoOnce(t *testing.T) {
	h := newLeaseHarness()
	h.tick(0, leaseObservation{}) // no wlan interface: coprocessor chassis
	h.tick(time.Hour, leaseObservation{})
	if len(h.wpa.calls) != 0 {
		t.Fatalf("no wlan interface must mean no wpa_cli, got %v", h.wpa.calls)
	}
	if sec := h.r.debugSection().(map[string]any); sec["supported"] != false {
		t.Fatalf("want supported=false, got %v", sec)
	}

	h2 := newLeaseHarness()
	h2.wpa.err = errWPAMissing
	h2.tick(0, obsNoLease)
	h2.tick(10*time.Minute, obsNoLease)
	h2.tick(time.Hour, obsNoLease)
	if len(h2.wpa.calls) != 1 {
		t.Fatalf("a missing wpa_cli must be probed once and then left alone, got %v", h2.wpa.calls)
	}
	if sec := h2.r.debugSection().(map[string]any); sec["supported"] != false {
		t.Fatalf("want supported=false without wpa_cli, got %v", sec)
	}
}

func TestLeaseVerdict(t *testing.T) {
	cases := []struct {
		name        string
		wifiV4      []string
		gw, gwIface string
		arp         string
		other       bool
		want        bool
	}{
		{"healthy", []string{"192.0.2.10"}, "192.0.2.1", "wlan0", "reachable", false, true},
		{"idle gateway absent from arp is fine", []string{"192.0.2.10"}, "192.0.2.1", "wlan0", "absent", false, true},
		{"field case: address gone", nil, "", "", "unknown", false, false},
		{"link-local fallback only", []string{"169.254.3.4"}, "", "", "unknown", false, false},
		{"address but no default route", []string{"192.0.2.10"}, "", "", "unknown", false, false},
		{"gateway does not answer", []string{"192.0.2.10"}, "192.0.2.1", "wlan0", "incomplete", false, false},
		{"cable carries the box", nil, "192.0.2.1", "eth0", "reachable", true, true},
	}
	for _, c := range cases {
		got, why := leaseVerdict("wlan0", c.wifiV4, c.gw, c.gwIface, c.arp, c.other)
		if got != c.want {
			t.Errorf("%s: got %v (%s), want %v", c.name, got, why, c.want)
		}
	}
}

func TestParseWPAStatus(t *testing.T) {
	st := parseWPAStatus(wpaStatusAssociated)
	if st.State != "COMPLETED" || st.Mode != "station" || st.ID != "0" || st.SSID == "" || st.BSSID != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("parse: %+v", st)
	}
}

func TestHostapdRunning(t *testing.T) {
	dir := t.TempDir()
	mk := func(pid, comm string) {
		p := dir + "/" + pid
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p+"/comm", []byte(comm+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("1", "init")
	mk("200", "wpa_supplicant")
	if hostapdRunning(dir) {
		t.Fatal("no hostapd yet")
	}
	mk("300", "hostapd")
	if !hostapdRunning(dir) {
		t.Fatal("hostapd should be detected")
	}
}
