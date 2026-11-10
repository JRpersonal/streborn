package dlna

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestFetchableLocation(t *testing.T) {
	for loc, want := range map[string]bool{
		"http://192.0.2.10:49152/rootDesc.xml":  true,
		"https://192.0.2.10/desc.xml":           true,
		"HTTP://192.0.2.10:8200/rootDesc.xml":   true,
		" http://192.0.2.10:8200/rootDesc.xml ": true,
		// No address filter: a NAS behind a router or on a public address is
		// still a NAS.
		"http://203.0.113.7:8200/rootDesc.xml": true,
		"http://nas.example.com/desc.xml":      true,
		"http://[2001:db8::1]:8200/desc.xml":   true,
		"file:///etc/passwd":                   false,
		"ftp://192.0.2.10/desc.xml":            false,
		"gopher://192.0.2.10/":                 false,
		"//192.0.2.10/desc.xml":                false,
		"http:///desc.xml":                     false,
		"192.0.2.10:8200/desc.xml":             false,
		"rootDesc.xml":                         false,
		"":                                     false,
	} {
		if got := fetchableLocation(loc); got != want {
			t.Errorf("fetchableLocation(%q) = %v, want %v", loc, got, want)
		}
	}
}

// seedAnnounced puts n device locations on srv into the NOTIFY cache, the
// discovery input that needs no live SSDP on the test host, and removes them
// again when the test ends.
func seedAnnounced(t *testing.T, srv *httptest.Server, tag string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		usn := fmt.Sprintf("uuid:%s-%d::urn:schemas-upnp-org:device:MediaServer:1", tag, i)
		alive := notifyPacket(
			"CACHE-CONTROL: max-age=1800",
			fmt.Sprintf("LOCATION: %s/%s/%d.xml", srv.URL, tag, i),
			"NT: urn:schemas-upnp-org:device:MediaServer:1",
			"NTS: ssdp:alive",
			"USN: "+usn,
		)
		if got := announces.handlePacket(alive, time.Now()); got != "alive" {
			t.Fatalf("seeding the announce cache: action = %q", got)
		}
		t.Cleanup(func() {
			announces.handlePacket(notifyPacket(
				"NT: urn:schemas-upnp-org:device:MediaServer:1",
				"NTS: ssdp:byebye",
				"USN: "+usn,
			), time.Now())
		})
	}
}

// One fetch per SSDP LOCATION, all at once, is as many requests as the network
// cares to name. Far more locations than the cap must still all be fetched,
// but never more than the cap at a time.
func TestDescriptionFetchesAreCapped(t *testing.T) {
	const locations = 3 * maxDescriptionFetches
	var mu sync.Mutex
	inFlight, peak, served := 0, 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()
		// Long enough that every free slot is taken before the first one
		// comes back.
		time.Sleep(150 * time.Millisecond)
		mu.Lock()
		inFlight--
		served++
		mu.Unlock()
		http.NotFound(w, r)
	}))
	defer srv.Close()
	seedAnnounced(t, srv, "capped", locations)

	if _, err := DiscoverServers(context.Background(), 1500*time.Millisecond); err != nil {
		t.Fatalf("DiscoverServers: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if served != locations {
		t.Errorf("%d of %d descriptions were fetched: waiting for a slot must not drop a location", served, locations)
	}
	if peak > maxDescriptionFetches {
		t.Errorf("%d descriptions were fetched at the same time, the cap is %d", peak, maxDescriptionFetches)
	}
	if peak < maxDescriptionFetches/2 {
		t.Errorf("only %d fetches ran side by side: the cap must not turn the sweep serial", peak)
	}
}

// recordingHandler notes the message of every log record.
type recordingHandler struct {
	mu   sync.Mutex
	msgs map[string]int
	hit  chan string
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler            { return h }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.msgs[r.Message]++
	h.mu.Unlock()
	select {
	case h.hit <- r.Message:
	default:
	}
	return nil
}

// An early match returns the answer at once, but the SSDP probes used to stay
// in their socket read until the listen window closed, each holding a socket
// and a goroutine for an answer nobody was waiting for any more.
func TestEarlyMatchStopsTheSSDPProbes(t *testing.T) {
	const xml = `<?xml version="1.0"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <device>
    <deviceType>urn:schemas-upnp-org:device:MediaServer:1</deviceType>
    <friendlyName>Probe Stop NAS</friendlyName>
    <UDN>uuid:probe-stop-1</UDN>
    <serviceList>
      <service>
        <serviceType>urn:schemas-upnp-org:service:ContentDirectory:1</serviceType>
        <controlURL>/ctl/ContentDir</controlURL>
      </service>
    </serviceList>
  </device>
</root>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(xml))
	}))
	defer srv.Close()
	seedAnnounced(t, srv, "probe-stop", 1)

	rec := &recordingHandler{msgs: map[string]int{}, hit: make(chan string, 4096)}
	saved := Logger
	Logger = slog.New(rec)
	// "SSDP M-SEARCH done" is logged once every probe has returned, so waiting
	// for it below also means nothing of this sweep logs after the restore.
	defer func() { Logger = saved }()

	const window = 20 * time.Second
	begin := time.Now()
	_, ok, _, err := FindServer(context.Background(), window, func(s Server) bool {
		return s.UDN == "uuid:probe-stop-1"
	})
	if err != nil || !ok {
		t.Fatalf("FindServer: ok = %v err = %v", ok, err)
	}
	deadline := time.After(window / 2)
	for {
		select {
		case msg := <-rec.hit:
			if msg != "dlna: SSDP M-SEARCH done" {
				continue
			}
			if took := time.Since(begin); took >= window/2 {
				t.Errorf("the probes ended after %v of a %v window", took, window)
			}
			return
		case <-deadline:
			t.Fatalf("the SSDP probes were still listening %v after the early match", window/2)
		}
	}
}
