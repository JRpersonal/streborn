package webui

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The three failures of the #1339 run, word for word, plus the ones that
// must never be retried.
func TestNetworkErrorTextKnowsAnOutageFromARealFailure(t *testing.T) {
	for msg, want := range map[string]bool{
		`download: context canceled (and the Spotify engine could not be restored: download: Get "https://github.com/JRpersonal/streborn/releases/download/v1.0.17/go-librespot-armv7l": dial tcp: lookup github.com: i/o timeout)`: true,
		`could not check for updates: Get "https://github.com/JRpersonal/streborn/releases/latest/download/update.json": dial tcp: lookup github.com on 192.0.2.1:53: server misbehaving`:                                           true,
		`download: Get "https://github.com/JRpersonal/streborn/releases/download/v1.0.17/streborn-armv7l": dial tcp: lookup github.com on 192.0.2.1:53: server misbehaving`:                                                         true,
		"the download stalled, nothing arrived for 90 seconds (after 6239137 of 17825952 bytes)":                                                                                                                                    true,
		"no answer: dial tcp 192.0.2.4:8888: connect: connection refused":                                                                                                                                                           true,
		"download does not match the signed release (sha256 00ff)":                                                                                                                                                                  false,
		"the speaker refused (HTTP 403): update only allowed from LAN":                                                                                                                                                              false,
		"this speaker already runs v1.0.17, the latest release is v1.0.17":                                                                                                                                                          false,
		"signature does not verify with any trusted key":                                                                                                                                                                            false,
		"": false,
	} {
		if got := networkErrorText(msg); got != want {
			t.Errorf("networkErrorText(%q) = %v, want %v", msg, got, want)
		}
	}
	if !isNetworkError(fmt.Errorf("download: %w", &net.DNSError{Err: "server misbehaving", Name: "github.com"})) {
		t.Error("a DNS error is a network error")
	}
	if !isNetworkError(fmt.Errorf("%w (after 1 of 2 bytes)", errDownloadStalled)) {
		t.Error("a stalled download is a network error")
	}
	if isNetworkError(errors.New("download is larger than the 10 bytes the release lists")) {
		t.Error("a wrong size is not a network error")
	}
}

// A check that runs while the router's DNS is not up yet waits for it instead
// of failing within milliseconds (#1339: the LAN came back about 35 s before
// the DNS, and two speakers failed inside that window).
func TestSelfUpdateCheckWaitsForTheNetworkToComeBack(t *testing.T) {
	origVer := agentVersion
	t.Cleanup(func() { agentVersion = origVer })
	SetAgentVersion("v1.0.12")
	s, realBase := selfUpdateFixture(t, "v1.0.13", []byte("agent-binary"), []byte("engine-binary"), nil)

	// A port nobody listens on yet: the release host is "down".
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	b, _ := os.ReadFile(selfUpdateSourcePath)
	if err := os.WriteFile(selfUpdateSourcePath, []byte(strings.Replace(string(b), strings.TrimSuffix(realBase, "/"), "http://"+addr, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	origEvery, origFor := selfUpdateCheckRetryEvery, selfUpdateCheckRetryFor
	t.Cleanup(func() { selfUpdateCheckRetryEvery, selfUpdateCheckRetryFor = origEvery, origFor })
	selfUpdateCheckRetryEvery, selfUpdateCheckRetryFor = 50*time.Millisecond, 10*time.Second

	// The "router" comes back a little later, on the same address.
	target, _ := url.Parse(realBase)
	back := make(chan struct{})
	go func() {
		defer close(back)
		time.Sleep(300 * time.Millisecond)
		l2, err := net.Listen("tcp", addr)
		if err != nil {
			t.Errorf("relisten: %v", err)
			return
		}
		srv := httptest.NewUnstartedServer(httputil.NewSingleHostReverseProxy(target))
		_ = srv.Listener.Close()
		srv.Listener = l2
		srv.Start()
		t.Cleanup(srv.Close)
	}()

	st := postAction(t, s, "check")
	<-back
	if st.State != "idle" || st.Latest != "v1.0.13" || !st.Available {
		t.Fatalf("the check gave up instead of waiting for the network: %+v", st)
	}
}

// A signature that does not verify is never retried: it fails at once.
func TestSelfUpdateCheckDoesNotRetryAnUntrustedRelease(t *testing.T) {
	origEvery, origFor := selfUpdateCheckRetryEvery, selfUpdateCheckRetryFor
	t.Cleanup(func() { selfUpdateCheckRetryEvery, selfUpdateCheckRetryFor = origEvery, origFor })
	selfUpdateCheckRetryEvery, selfUpdateCheckRetryFor = time.Second, time.Minute
	_, stranger, _ := ed25519.GenerateKey(rand.Reader)
	s, _ := selfUpdateFixture(t, "v1.0.13", []byte("agent-binary"), []byte("engine-binary"), stranger)
	start := time.Now()
	st := postAction(t, s, "check")
	if st.State != "failed" || time.Since(start) > 900*time.Millisecond {
		t.Fatalf("status %+v after %v", st, time.Since(start))
	}
}

// The driving speaker starts a peer over when the peer's update failed only
// because the network was gone, and the row ends up done.
func TestFleetPeerNetworkFailureIsRetried(t *testing.T) {
	h := newFleetHarness(t, "v1.0.12", map[string]*fakePeer{
		"10.0.0.2": {name: "Kitchen", version: "v1.0.12", behaviour: "netfail"},
	})
	row := runPeer(t, h, "10.0.0.2")
	if row.State != fleetDone || row.Version != "v1.0.13" {
		t.Fatalf("row %+v", row)
	}
	if n := h.peers["10.0.0.2"].installs; n != 2 {
		t.Fatalf("%d installs, want a second one after the network failure", n)
	}
	if !slicesContains(h.states, "updating/network") {
		t.Errorf("the row never said it waits for the network: %v", h.states)
	}
}

// A peer that keeps failing on the network is given up after the retries,
// with its error, not looped on forever.
func TestFleetPeerNetworkRetriesAreBounded(t *testing.T) {
	h := newFleetHarness(t, "v1.0.12", map[string]*fakePeer{
		"10.0.0.2": {name: "Kitchen", version: "v1.0.12", behaviour: "netfail"},
	})
	orig := fleetNetRetries
	t.Cleanup(func() { fleetNetRetries = orig })
	fleetNetRetries = 0
	row := runPeer(t, h, "10.0.0.2")
	if row.State != fleetFailed || !strings.Contains(row.Reason, "server misbehaving") {
		t.Fatalf("row %+v", row)
	}
}

// Silence in the middle of a download is the network, not a restart: the
// row must not say "Restarting" (#1339, a row stuck on Restarting for the
// whole outage).
func TestFleetPeerSilenceMidDownloadIsNotARestart(t *testing.T) {
	h := newFleetHarness(t, "v1.0.12", map[string]*fakePeer{
		"10.0.0.2": {name: "Kitchen", version: "v1.0.12", behaviour: "outage"},
	})
	row := runPeer(t, h, "10.0.0.2")
	if row.State != fleetDone {
		t.Fatalf("row %+v", row)
	}
	if slicesContains(h.states, "updating/rebooting") || !slicesContains(h.states, "updating/offline") {
		t.Fatalf("states %v: want offline, never rebooting, while the download was cut", h.states)
	}
}

// A failure is only reported after the peer was asked once more what it
// runs: a speaker on the new version is done, whatever went before.
func TestFleetPeerFailureIsRecheckedAgainstTheRealVersion(t *testing.T) {
	h := newFleetHarness(t, "v1.0.12", map[string]*fakePeer{
		"10.0.0.2": {name: "Kitchen", version: "v1.0.13", behaviour: "current"},
	})
	base := fleetPeerBase("10.0.0.2", 8888)
	if v, ok := fleetPeerRunsTarget(base, "v1.0.13"); !ok || v != "v1.0.13" {
		t.Fatalf("fleetPeerRunsTarget = %q %v", v, ok)
	}
	if _, ok := fleetPeerRunsTarget(base, "v1.0.14"); ok {
		t.Fatal("an older version counted as the target")
	}
	_ = h
}

func slicesContains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// --- the phone card ---

// The card turns network errors into words, retries them, never calls a
// cut-off download a restart, refreshes its rows when a run ends, and shows
// that something is going on while it waits.
func TestPhoneCardHandlesANetworkOutage(t *testing.T) {
	page := readIndexHTML(t)
	js := page[strings.Index(page, "(function wireSelfUpdate(){"):strings.Index(page, "(function wireDiag(){")]
	for _, want := range []string{
		"function netErr(msg)",
		"function humanErr(msg)",
		"fill(T.suFail, humanErr(sp.reason))",
		"fill(T.suFail, humanErr(st.error))",
		"async function retryAfterNet(",
		"async function refreshRows()",
		"await refreshRows();",
		"if (sp.phase === 'offline') return",
		"if (sp.phase === 'network') return",
		"function goneWord(row)",
		// A single update clears a finished run, so its summary cannot hide
		// the progress or stay stale.
		"function dropFinishedRun()",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the update card lost %q", want)
		}
	}
	if !strings.Contains(page, ".msg.busy::before") || !strings.Contains(page, ".sufleet .s.busy::before") {
		t.Error("the waiting phases need their activity indicator")
	}
	bundles := strings.Count(indexHTML, "suHead:\"")
	for _, key := range []string{"sfNetErr", "sfNetWait", "sfNoAnswer"} {
		if got := len(regexp.MustCompile(`[{,]`+key+`:"`).FindAllString(indexHTML, -1)); got != bundles {
			t.Errorf("%s: in %d of %d locales", key, got, bundles)
		}
		for _, m := range regexp.MustCompile(key+`:"([^"]*)"`).FindAllStringSubmatch(indexHTML, -1) {
			if strings.ContainsAny(m[1], "–—") || strings.Contains(m[1], " - ") {
				t.Errorf("%s uses a dash: %q", key, m[1])
			}
		}
	}
}
