package boxlog

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

// Lines verbatim from the 2026-09-06 captures (Portable/taigan and kitchen
// ST10/rhino, firmware 27.0.6); only the SSID was replaced by a placeholder.
const (
	lineBadURL       = `Sep  6 18:02:11 taigan local0.info APServer[1877]: [(002101):AudioPathHttpStream:INFO]BAD_URL (DA) - HTTP Error indicated..returning 1`
	lineFirstFrame   = `Sep  6 18:02:11 taigan local0.err APServer[1877]: [(002101):Decoder:ERROR]Could not obtain first-frame 1`
	linePlayFailure  = `Sep  6 18:02:11 taigan local0.info BoseApp[1959]: [(002011):APAudioControl:INFO]ElementRequested - PlaybackFailure ERROR_BAD_URL : session 7`
	lineServerError  = `Sep  6 18:02:12 taigan local0.err APServer[1877]: [(002101):AudioIF:ERROR]SERVER ERROR: Server state has terminal error = BAD_URL and m_nMaxRetryAttempt =3`
	lineUnderrun     = `Sep  6 18:05:40 taigan local0.warn APServer[1877]: [(002107):RBDecoded:WARNING]ReadyToWrite Interrupted, bufferDepth: 0, capSize: 262144, totalSize: 262144, minBufferDepth: 0.5, need: 4096`
	lineSelect       = `Sep  6 18:02:10 taigan local0.info APServer[1877]: [(002101):AudioIF:INFO]CAudioInterface::Select(url), URL = http://192.0.2.10:17008/stream/3 and Retry Attempt = =0`
	lineStandby      = `Sep  6 18:20:01 taigan local0.info BoseApp[1959]: [(002001):HSM:INFO]SysCtlr: HandleMessage(EVT_STANDBY) >> On > StdOpOn > StdOp [ChangeState(On >> Standby), ...]`
	lineWake         = `Sep  6 18:31:15 taigan local0.info BoseApp[1959]: [(002001):HSM:INFO]SysCtlr: HandleMessage(EVT_SOURCE_SELECT) >> Standby [ChangeState(Standby >> ProductSrcChangeWaiting), ...]`
	lineSrcChange    = `Sep  6 18:31:16 taigan local0.info BoseApp[1959]: [(002001):HSM:INFO]SysCtlr: HandleMessage(EVT_SOURCE_READY) >> ProductSrcChangeWaiting [ChangeState(ProductSrcChangeWaiting >> On), ...]`
	lineLowPower     = `Sep  6 18:20:05 taigan local0.info scmmond[1201]: [(001201):Scmmond:INFO]operator(): Low power notification from client: SCMMOND_NETWORK_STANDBY`
	lineLowPowerWake = `Sep  6 18:31:14 taigan local0.info scmmond[1201]: [(001201):Scmmond:INFO]operator(): Low power notification from client: SCMMOND_WAKEUP_NETWORK_STANDBY`
	lineSleepStage   = `Sep  6 18:20:06 taigan local0.info scmmond[1201]: [(001201):Scmmond:INFO]Go Sleep stage 2`
	lineClock        = `Sep  6 18:20:06 taigan local0.info scmmond[1201]: [(001201):Scmmond:INFO]SCMMonD sets processor clock to 300`
	lineWiFiStatus   = `Sep  6 18:50:00 rhino local0.info BoseApp[1943]: [(002140):NetworkServicesController:INFO]WiFiStatus(<?xml version="1.0" encoding="UTF-8"?><WiFiStatus SSID="MyHomeNet" state="WIFI_STATION_CONNECTED" frequencyKHZ="5220000" signalDBM="-55" />)`
	lineWiFiQuality  = `Sep  6 18:50:00 rhino local0.info BoseApp[1943]: [(002140):NetworkServicesController:INFO]WiFiSignalStrengthDBMToQuality - RSSI Level dbm=-55 quality=Excellent`
	lineMargePresets = `Sep  6 18:52:30 rhino local0.err BoseApp[1943]: [(002150):MargeClient:ERROR]EXCEPTION in GetPresetsCB xml parsing: preset expected, but XML was 'account'`
	lineMargeUpdate  = `Sep  6 18:52:31 rhino local0.err BoseApp[1943]: [(002150):Presets:ERROR]UpdatePresetFailureCB slot 3`
	lineMargeRecent  = `Sep  6 18:52:32 rhino local0.err BoseApp[1943]: [(002150):MargeClient:ERROR]AddRecentCB Failed with status=500`
	lineSwamped      = `Sep  6 18:53:00 rhino local0.info BoseApp[1943]: [(002130):BCliTask:INFO]Dispatched 200>=200 before pend - getting swamped?`
	lineSTSSpam      = `Sep  6 18:53:00 rhino local0.err STSCertified[1950]: [(002992):Network:ERROR]Failed connect call for 127.0.0.1:30034 (fd 12): Connection refused`
	lineBDSP         = `Sep  6 18:53:00 rhino local0.debug APServer[1877]: [(002105):BDSP:DEBUG]tick`
	lineKernel       = `Sep  6 18:53:01 rhino kern.info kernel: usb 1-1: new high-speed USB device number 4 using musb-hdrc`
)

func TestParseLine(t *testing.T) {
	l, ok := ParseLine(lineBadURL)
	if !ok {
		t.Fatal("not parsed")
	}
	if l.Stamp != "Sep  6 18:02:11" || l.Process != "APServer" || l.Facility != "AudioPathHttpStream" || l.Level != "INFO" {
		t.Fatalf("fields: %+v", l)
	}
	if l.Message != "BAD_URL (DA) - HTTP Error indicated..returning 1" {
		t.Fatalf("message %q", l.Message)
	}
	k, ok := ParseLine(lineKernel)
	if !ok || k.Process != "kernel" || k.Facility != "" || !strings.HasPrefix(k.Message, "usb 1-1") {
		t.Fatalf("kernel line: ok=%v %+v", ok, k)
	}
	if _, ok := ParseLine("garbage"); ok {
		t.Fatal("garbage must not parse")
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		line string
		want Class
		ok   bool
	}{
		{lineBadURL, ClassPlayBadURL, true},
		{lineFirstFrame, ClassPlayNoFirstFrame, true},
		{linePlayFailure, ClassPlayFailure, true},
		{lineServerError, ClassPlayServerError, true},
		{lineUnderrun, ClassPlayUnderrun, true},
		{lineSelect, ClassPlaySelect, true},
		{lineStandby, ClassStandby, true},
		{lineWake, ClassWake, true},
		{lineSrcChange, "", false},
		{lineLowPower, ClassPowerEvent, true},
		{lineLowPowerWake, ClassPowerEvent, true},
		{lineSleepStage, ClassPowerSleep, true},
		{lineClock, ClassPowerSleep, true},
		{lineWiFiStatus, ClassWiFiStatus, true},
		{lineWiFiQuality, ClassWiFiQuality, true},
		{lineMargePresets, ClassMargeError, true},
		{lineMargeUpdate, ClassMargeError, true},
		{lineMargeRecent, ClassMargeError, true},
		{lineSwamped, ClassSwamped, true},
		{linePortableThumbsUpPress, "", false},
		{lineKernel, "", false},
	}
	for _, c := range cases {
		l, _ := ParseLine(c.line)
		got, ok := Classify(l)
		if ok != c.ok || got != c.want {
			t.Errorf("%q: got %q/%v want %q/%v", c.line, got, ok, c.want, c.ok)
		}
	}
}

func TestIsNoise(t *testing.T) {
	for _, l := range []string{lineNoise, lineSTSSpam, lineBDSP} {
		if !IsNoise(l) {
			t.Errorf("must be noise: %q", l)
		}
	}
	for _, l := range []string{lineBadURL, lineWiFiStatus, lineKernel, lineClock} {
		if IsNoise(l) {
			t.Errorf("must not be noise: %q", l)
		}
	}
}

func TestRedactSSID(t *testing.T) {
	got := Redact(lineWiFiStatus)
	if strings.Contains(got, "MyHomeNet") {
		t.Fatalf("SSID leaked: %s", got)
	}
	if !strings.Contains(got, `SSID="ssid#`) || !strings.Contains(got, `signalDBM="-55"`) || !strings.Contains(got, `frequencyKHZ="5220000"`) {
		t.Fatalf("redacted line lost its shape: %s", got)
	}
	if Redact(lineWiFiStatus) != got {
		t.Fatal("hash tag must be stable")
	}
	// The escaped form, as the line appears when quoted into another log.
	esc := `WiFiStatus SSID=\"MyHomeNet\" state=\"WIFI_STATION_CONNECTED\"`
	if r := Redact(esc); strings.Contains(r, "MyHomeNet") {
		t.Fatalf("escaped SSID leaked: %s", r)
	}
	// Untouched when there is nothing to do.
	if Redact(lineBadURL) != lineBadURL {
		t.Fatal("a line without SSID must pass unchanged")
	}
}

func TestObserveRingsAndRateLimit(t *testing.T) {
	f := newForensics()
	now := time.Date(2026, 9, 6, 18, 0, 0, 0, time.UTC)

	// Spam is dropped before anything is stored.
	if _, _, ok := f.observe(lineSTSSpam, now); ok {
		t.Fatal("spam classified")
	}
	if len(f.tailSnapshot()) != 0 {
		t.Fatal("spam reached the tail")
	}

	// First playback failure is logged, a repeat inside the gap is not, a
	// different class is, and after the gap the first class is again.
	ev, logIt, ok := f.observe(lineBadURL, now)
	if !ok || !logIt || ev.Class != ClassPlayBadURL {
		t.Fatalf("first bad url: ok=%v log=%v ev=%+v", ok, logIt, ev)
	}
	if _, logIt, ok = f.observe(lineBadURL, now.Add(5*time.Second)); !ok || logIt {
		t.Fatalf("repeat inside 30 s must stay out of the agent log (ok=%v log=%v)", ok, logIt)
	}
	if _, logIt, _ = f.observe(lineFirstFrame, now.Add(6*time.Second)); !logIt {
		t.Fatal("another class has its own limit")
	}
	if _, logIt, _ = f.observe(lineBadURL, now.Add(31*time.Second)); !logIt {
		t.Fatal("after the gap the class logs again")
	}

	// Standby and wake are logged each; a chronic flap inside 5 s is not.
	if _, logIt, _ = f.observe(lineStandby, now.Add(40*time.Second)); !logIt {
		t.Fatal("standby not logged")
	}
	if _, logIt, _ = f.observe(lineWake, now.Add(41*time.Second)); !logIt {
		t.Fatal("wake not logged")
	}
	if _, logIt, _ = f.observe(lineStandby, now.Add(42*time.Second)); logIt {
		t.Fatal("standby flap inside 5 s logged")
	}

	// Wi-Fi and the Select line never reach the agent log.
	for _, l := range []string{lineWiFiStatus, lineWiFiQuality, lineSelect, lineSleepStage} {
		if _, logIt, ok = f.observe(l, now.Add(time.Minute)); !ok || logIt {
			t.Fatalf("%q: ok=%v log=%v, want ring only", l, ok, logIt)
		}
	}

	// Swamped: once per ten minutes.
	if _, logIt, _ = f.observe(lineSwamped, now.Add(2*time.Minute)); !logIt {
		t.Fatal("swamped not logged")
	}
	if _, logIt, _ = f.observe(lineSwamped, now.Add(9*time.Minute)); logIt {
		t.Fatal("swamped repeat inside 10 min logged")
	}
	if _, logIt, _ = f.observe(lineSwamped, now.Add(13*time.Minute)); !logIt {
		t.Fatal("swamped after 10 min not logged")
	}

	// The stored Wi-Fi event carries the hash, not the network name.
	snap := f.eventsSnapshot()
	for _, e := range snap["events"].([]map[string]any) {
		if strings.Contains(e["message"].(string), "MyHomeNet") {
			t.Fatal("SSID leaked into the event ring")
		}
	}
	for _, l := range f.tailSnapshot() {
		if strings.Contains(l, "MyHomeNet") {
			t.Fatal("SSID leaked into the tail")
		}
	}
	if snap["noiseLines"].(uint64) != 1 {
		t.Fatalf("noise counter %v", snap["noiseLines"])
	}

	// Last playback failure: the newest failure class, within the window.
	pf, ok := f.lastPlaybackFailure(now.Add(35*time.Second), 30*time.Second)
	if !ok || pf.Class != ClassPlayBadURL {
		t.Fatalf("last failure: ok=%v %+v", ok, pf)
	}
	if _, ok = f.lastPlaybackFailure(now.Add(2*time.Minute), 30*time.Second); ok {
		t.Fatal("a stale failure must not count")
	}
}

func TestRingBounds(t *testing.T) {
	f := newForensics()
	now := time.Now()
	for i := 0; i < eventRingLen+50; i++ {
		f.observe(lineUnderrun, now.Add(time.Duration(i)*time.Second))
	}
	if n := len(f.eventsSnapshot()["events"].([]map[string]any)); n != eventRingLen {
		t.Fatalf("event ring holds %d, want %d", n, eventRingLen)
	}
	for i := 0; i < tailRingLen+50; i++ {
		f.observe(lineKernel, now)
	}
	tail := f.tailSnapshot()
	if len(tail) != tailRingLen {
		t.Fatalf("tail holds %d, want %d", len(tail), tailRingLen)
	}
	// Generic ring keeps insertion order and drops the oldest.
	r := newRing[int](3)
	for i := 1; i <= 5; i++ {
		r.push(i)
	}
	if got := r.items(); len(got) != 3 || got[0] != 3 || got[2] != 5 {
		t.Fatalf("ring items %v", got)
	}
	if last, ok := r.last(); !ok || last != 5 {
		t.Fatalf("ring last %v %v", last, ok)
	}
}

func TestTruncate(t *testing.T) {
	long := strings.Repeat("x", maxMessageLen+100)
	if got := truncate(long); len(got) != maxMessageLen+3 || !strings.HasSuffix(got, "...") {
		t.Fatalf("len %d", len(got))
	}
	// A multi-byte rune straddling the cut is dropped whole.
	s := strings.Repeat("a", maxMessageLen-1) + "ä" + "tail"
	if got := truncate(s); !strings.HasSuffix(got, "a...") {
		t.Fatalf("cut inside a rune: %q", got[len(got)-6:])
	}
}

// TestReaderLinePath: the reader feeds every line through the forensics and
// still decodes keys; LastPlaybackFailure answers within the window.
func TestReaderLinePath(t *testing.T) {
	r := New(slog.New(slog.DiscardHandler), nil, nil)
	now := time.Now()
	r.InjectLine(lineSTSSpam, now)
	r.InjectLine(lineServerError, now)
	r.InjectLine(linePortableThumbsUpPress, now)
	if ev, ok := r.LastPlaybackFailure(time.Minute); !ok || ev.Class != ClassPlayServerError {
		t.Fatalf("LastPlaybackFailure: %v %+v", ok, ev)
	}
	if _, ok := r.LastKeyEventWithin(time.Minute); !ok {
		t.Fatal("key line must still be decoded")
	}
	if tail := r.TailSnapshot(); len(tail) != 2 {
		t.Fatalf("tail %v", tail)
	}
	if snap := r.EventsSnapshot(); snap["classified"].(uint64) != 1 {
		t.Fatalf("events %v", snap)
	}
}
