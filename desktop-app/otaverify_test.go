package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The fake SoundTouch 10 of the 2026-09-06 bundle: its agent answers on one
// port and its firewall DROPs the other, so a connection there hangs until
// the client gives up. droppingPort is a listener that accepts and never
// answers, which is what a DROP looks like once the SYN is through (the real
// one never even connects; either way the client sees a timeout).
func droppingPort(t *testing.T) (port int, hits *int) {
	t.Helper()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	return listenPort(t, srv), &n
}

func agentPort(t *testing.T, build string) (port int, hits *int) {
	t.Helper()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"version":"v0.9.75","build":"`+build+`"}`)
	}))
	t.Cleanup(srv.Close)
	return listenPort(t, srv), &n
}

// pairPorts installs the alt-port seam so the two fake ports stand in for
// :8888 and :17008 for each other.
func pairPorts(t *testing.T, a, b int) {
	t.Helper()
	old := altAgentPortFor
	altAgentPortFor = func(p int) int {
		if p == a {
			return b
		}
		return a
	}
	t.Cleanup(func() { altAgentPortFor = old })
}

func fastTestApp() *App {
	return &App{httpClient: &http.Client{Timeout: 400 * time.Millisecond}, logger: slog.Default()}
}

func useTempJournal(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := otaJournalDir
	otaJournalDir = dir
	t.Cleanup(func() { otaJournalDir = old })
	return filepath.Join(dir, "ota-history.log")
}

func readJournal(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read journal: %v", err)
	}
	return string(b)
}

// When no port answers, the error must speak for the port that matters (the
// first candidate: the cached port, else the caller's), not for whichever
// port was tried last. The bundle's verify line quoted :17008, the port the
// box drops by design, for an update that had gone through :8888.
func TestBoxDoErrorLeadsWithThePortThatMatters(t *testing.T) {
	dead := deadPort(t)
	drop, dropHits := droppingPort(t)
	pairPorts(t, dead, drop)

	a := fastTestApp()
	_, err := a.boxDo("127.0.0.1", dead, http.MethodGet, "/api/agent/version", "", "")
	if err == nil {
		t.Fatal("boxDo succeeded against a dead port and a dropping one")
	}
	msg := err.Error()
	first := strings.SplitN(msg, "\n", 2)[0]
	if !strings.Contains(first, ":"+itoa(dead)+"/api/agent/version") {
		t.Errorf("error does not lead with the caller's port :%d:\n%s", dead, msg)
	}
	if !strings.Contains(first, "(also tried :"+itoa(drop)+": timed out)") {
		t.Errorf("error does not summarise the other port :%d as timed out:\n%s", drop, msg)
	}
	if strings.Count(first, "/api/agent/version") != 1 {
		t.Errorf("the other port's full error was pasted in as well:\n%s", msg)
	}
	if *dropHits != 1 {
		t.Errorf("dropping port was tried %d times, want 1", *dropHits)
	}
	if !isTransportNotReady(err) {
		t.Error("the combined error lost its transport-failure nature (callers classify on it)")
	}
}

// Every post-OTA version poll must ask the port the update went through
// first. The port cache cannot guarantee that: the first failed probe of the
// rebooting box evicts it, and a record carrying the other port then puts the
// dropped port first on every pass, a full timeout each time.
func TestPostOTAVerifyAsksTheUpdatePortFirst(t *testing.T) {
	agent, agentHits := agentPort(t, "b1")
	drop, dropHits := droppingPort(t)
	pairPorts(t, agent, drop)

	a := fastTestApp()
	a.rememberOTAPort("127.0.0.1", agent)
	// The cache is empty (the reboot evicted it) and the record says :drop.
	start := time.Now()
	ver, err := a.BoxAgentVersion("127.0.0.1", drop)
	if err != nil {
		t.Fatalf("BoxAgentVersion: %v", err)
	}
	if ver["build"] != "b1" {
		t.Errorf("build = %q, want b1", ver["build"])
	}
	if *dropHits != 0 {
		t.Errorf("the dropped port was tried %d times before the update port", *dropHits)
	}
	if *agentHits != 1 {
		t.Errorf("agent asked %d times, want 1", *agentHits)
	}
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Errorf("poll took %v: it waited on the dropped port first", d)
	}

	// Without the memo the record's port decides, and the poll pays the
	// timeout on the dropped port first: the behaviour this test pins down
	// as gone for the post-OTA case.
	b := fastTestApp()
	if _, err := b.BoxAgentVersion("127.0.0.1", drop); err != nil {
		t.Fatalf("plain BoxAgentVersion: %v", err)
	}
	if *dropHits != 1 {
		t.Errorf("control: dropped port hit %d times, want 1 (the plain path still goes record-port first)", *dropHits)
	}
}

// A box that is off the network for the whole verify window gets an
// "unreachable" verdict; when discovery then finds it running this app's
// build, the journal gets exactly one corrective line, and the memo is gone so
// the next cycle adds nothing.
func TestUnreachableVerdictIsCorrectedByALaterDiscoverySighting(t *testing.T) {
	journal := useTempJournal(t)
	dead := deadPort(t)
	dead2 := deadPort(t)
	pairPorts(t, dead, dead2)
	oldBuild := appBuild
	appBuild = "b2"
	t.Cleanup(func() { appBuild = oldBuild })

	a := fastTestApp()
	a.rememberOTAPort("127.0.0.1", dead)
	if cls := a.ClassifyOTAResult("127.0.0.1", dead); cls != "unreachable" {
		t.Fatalf("ClassifyOTAResult = %q, want unreachable", cls)
	}
	j := readJournal(t, journal)
	if !strings.Contains(j, "NOT CONFIRMED - box unreachable") {
		t.Fatalf("journal lacks the unreachable verdict:\n%s", j)
	}
	if !strings.Contains(j, ":"+itoa(dead)+"/api/agent/version") {
		t.Errorf("verdict does not quote the update port :%d:\n%s", dead, j)
	}

	// A stock-only sighting or one on the old build does not confirm.
	a.confirmLateOTA(map[string]BoxInfo{"127.0.0.1": {Host: "127.0.0.1", Kind: "stock"}}, nil)
	if strings.Contains(readJournal(t, journal), "confirmed late by discovery") {
		t.Fatal("a stock sighting was taken as confirmation")
	}

	a.confirmLateOTA(map[string]BoxInfo{"127.0.0.1": {Host: "127.0.0.1", Port: 8888, Kind: "str", Version: "v0.9.75", Build: "b2"}}, nil)
	j = readJournal(t, journal)
	if !strings.Contains(j, "confirmed late by discovery - box is on build b2") || !strings.Contains(j, "found on :8888") {
		t.Fatalf("journal lacks the corrective line:\n%s", j)
	}
	a.confirmLateOTA(map[string]BoxInfo{"127.0.0.1": {Host: "127.0.0.1", Port: 8888, Kind: "str", Version: "v0.9.75", Build: "b2"}}, nil)
	if n := strings.Count(readJournal(t, journal), "confirmed late by discovery"); n != 1 {
		t.Errorf("corrective line written %d times, want exactly 1", n)
	}
	if _, ok := a.postOTAPort("127.0.0.1"); ok {
		t.Error("the memo survived its confirmation")
	}
}

// The other outcome a late sighting can have: the box came back on its OLD
// build. The journal must say that too, once, rather than staying on
// "unreachable" as if the box had never returned.
func TestLateSightingOnTheOldBuildIsJournaledOnce(t *testing.T) {
	journal := useTempJournal(t)
	oldBuild := appBuild
	appBuild = "b3"
	t.Cleanup(func() { appBuild = oldBuild })

	a := fastTestApp()
	a.noteOTAUnconfirmed("192.0.2.9", "unreachable")
	seen := map[string]BoxInfo{"192.0.2.9": {Host: "192.0.2.9", Port: 17008, Kind: "str", Version: "v0.9.70", Build: "old"}}
	a.confirmLateOTA(seen, nil)
	a.confirmLateOTA(seen, nil)
	j := readJournal(t, journal)
	if strings.Count(j, "seen again by discovery") != 1 || !strings.Contains(j, "build old") {
		t.Errorf("journal:\n%s", j)
	}
}

// The quick refresh (RefreshKnownBoxes) feeds the CACHED record into seen when
// only the stock :8090 answered, which is exactly how a rebooting box looks
// while its agent is not up yet. That record still carries the OLD build, and
// must not be mistaken for a sighting of the box on it: the journal would end
// on a false "runs the old build" verdict and the memo would be gone before
// the genuine sighting arrives.
func TestPresenceOnlyRefreshDoesNotCountAsALateSighting(t *testing.T) {
	journal := useTempJournal(t)
	oldBuild := appBuild
	appBuild = "b4"
	t.Cleanup(func() { appBuild = oldBuild })

	a := fastTestApp()
	a.discCache = map[string]discEntry{
		// SerialNumber and Model set so the refresh does not go asking :8090/info.
		"192.0.2.12": {box: BoxInfo{Host: "192.0.2.12", Port: 8888, Kind: "str", Version: "v0.9.70", Build: "old", PortVerified: true, SerialNumber: "SN", Model: "SoundTouch 10"}, seen: time.Now()},
	}
	a.rememberOTAPort("192.0.2.12", 8888)
	a.noteOTAUnconfirmed("192.0.2.12", "unreachable")

	// Pass 1: agent still down, stock :8090 up (mid-boot).
	a.probeSTRFn = func(context.Context, string) (BoxInfo, bool) { return BoxInfo{}, false }
	a.portOpenFn = func(string, int, int) bool { return true }
	if _, err := a.RefreshKnownBoxes(); err != nil {
		t.Fatal(err)
	}
	j := readJournal(t, journal)
	if strings.Contains(j, "seen again by discovery") || strings.Contains(j, "confirmed late by discovery") {
		t.Fatalf("a stock-only refresh was taken as a sighting:\n%s", j)
	}
	if _, ok := a.postOTAPort("192.0.2.12"); !ok {
		t.Fatal("the memo was burnt by a stock-only refresh")
	}

	// Pass 2: the agent answers on the new build; now the corrective line is due.
	a.probeSTRFn = func(context.Context, string) (BoxInfo, bool) {
		return BoxInfo{Host: "192.0.2.12", Port: 8888, Kind: "str", Version: "v0.9.75", Build: "b4", PortVerified: true, SerialNumber: "SN", Model: "SoundTouch 10"}, true
	}
	if _, err := a.RefreshKnownBoxes(); err != nil {
		t.Fatal(err)
	}
	j = readJournal(t, journal)
	if !strings.Contains(j, "confirmed late by discovery - box is on build b4") || strings.Contains(j, "seen again by discovery") {
		t.Fatalf("journal after the genuine sighting:\n%s", j)
	}
	if _, ok := a.postOTAPort("192.0.2.12"); ok {
		t.Error("the memo survived its confirmation")
	}
}

// A confirmed update (the frontend's own verdict) or a fresh failed attempt
// drops the memo, so a much later discovery sighting cannot append a
// corrective line to an attempt that already has its verdict.
func TestConfirmedOutcomeDropsTheMemo(t *testing.T) {
	useTempJournal(t)
	a := fastTestApp()
	a.rememberOTAPort("192.0.2.5", 8888)
	a.noteOTAUnconfirmed("192.0.2.5", "unreachable")
	a.RecordOTAOutcome("192.0.2.5", "confirmed: box is on build x (stability window passed)")
	if _, ok := a.postOTAPort("192.0.2.5"); ok {
		t.Error("memo survived a confirmed outcome")
	}
	a.otaVerifyMu.Lock()
	_, still := a.otaVerify["192.0.2.5"]
	a.otaVerifyMu.Unlock()
	if still {
		t.Error("unconfirmed record survived a confirmed outcome")
	}
}

// A refused connection is a transport failure in every language. Windows
// words it in the system language, and the English keyword match alone did
// not recognise the German text: on a German PC boxDo stopped at the first
// refused port and never tried the other. Found by the fake box above on a
// German Windows; this pins the fallback itself.
func TestRefusedPortFallsThroughToTheOtherPortInAnyLocale(t *testing.T) {
	dead := deadPort(t)
	agent, agentHits := agentPort(t, "b7")
	pairPorts(t, dead, agent)

	a := fastTestApp()
	resp, err := a.boxDo("127.0.0.1", dead, http.MethodGet, "/api/agent/version", "", "")
	if err != nil {
		t.Fatalf("boxDo did not fall through to the answering port: %v", err)
	}
	resp.Body.Close()
	if *agentHits != 1 {
		t.Errorf("agent asked %d times, want 1", *agentHits)
	}
	if p, ok := a.cachedPort("127.0.0.1"); !ok || p != agent {
		t.Errorf("cached port = %d (present=%v), want %d", p, ok, agent)
	}

	// The refusal itself, as the http client reports it on this OS.
	_, err = http.DefaultClient.Get("http://127.0.0.1:" + itoa(dead) + "/api/agent/version")
	if err == nil {
		t.Fatal("dead port answered")
	}
	if !isTransportNotReady(err) {
		t.Errorf("a refused dial is not recognised as a transport failure: %v", err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
