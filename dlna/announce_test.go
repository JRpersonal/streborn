package dlna

import (
	"strings"
	"testing"
	"time"
)

func notifyPacket(headers ...string) []byte {
	lines := append([]string{"NOTIFY * HTTP/1.1", "HOST: 239.255.255.250:1900"}, headers...)
	return []byte(strings.Join(lines, "\r\n") + "\r\n\r\n")
}

// TestAnnounceCache_AliveByebye guards the #341 fix: a media server on
// the same PC as the app announces itself only via NOTIFY ssdp:alive
// (it never answers same-host M-SEARCH on Windows), so the alive
// packet must land in the cache, and the matching byebye must retire
// every location announced under the same device uuid.
func TestAnnounceCache_AliveByebye(t *testing.T) {
	c := &announceCache{entries: map[string]announceEntry{}}
	now := time.Now()

	alive := notifyPacket(
		"CACHE-CONTROL: max-age=1800",
		"LOCATION: http://192.0.2.10:8200/rootDesc.xml",
		"NT: urn:schemas-upnp-org:device:MediaServer:1",
		"NTS: ssdp:alive",
		"USN: uuid:abc-123::urn:schemas-upnp-org:device:MediaServer:1",
	)
	if got := c.handlePacket(alive, now); got != "alive" {
		t.Fatalf("alive packet: action = %q, want %q", got, "alive")
	}
	locs := c.candidateLocations(now)
	if len(locs) != 1 || locs[0] != "http://192.0.2.10:8200/rootDesc.xml" {
		t.Fatalf("after alive: locations = %v, want the announced LOCATION", locs)
	}

	// byebye carries the USN but no LOCATION: matching must go via the
	// device uuid prefix.
	byebye := notifyPacket(
		"NT: urn:schemas-upnp-org:device:MediaServer:1",
		"NTS: ssdp:byebye",
		"USN: uuid:abc-123::urn:schemas-upnp-org:device:MediaServer:1",
	)
	if got := c.handlePacket(byebye, now); got != "byebye" {
		t.Fatalf("byebye packet: action = %q, want %q", got, "byebye")
	}
	if locs := c.candidateLocations(now); len(locs) != 0 {
		t.Fatalf("after byebye: locations = %v, want empty", locs)
	}
}

func TestAnnounceCache_IgnoresNonNotify(t *testing.T) {
	c := &announceCache{entries: map[string]announceEntry{}}
	msearchReply := []byte("HTTP/1.1 200 OK\r\nLOCATION: http://192.0.2.10:8200/rootDesc.xml\r\nST: ssdp:all\r\n\r\n")
	if got := c.handlePacket(msearchReply, time.Now()); got != "" {
		t.Errorf("M-SEARCH reply: action = %q, want ignored", got)
	}
	aliveNoLocation := notifyPacket("NTS: ssdp:alive", "USN: uuid:abc")
	if got := c.handlePacket(aliveNoLocation, time.Now()); got != "" {
		t.Errorf("alive without LOCATION: action = %q, want ignored", got)
	}
	if locs := c.candidateLocations(time.Now()); len(locs) != 0 {
		t.Errorf("cache = %v, want empty", locs)
	}
}

// TestAnnounceCache_ExpiredEntriesStayCandidates: an entry past its
// CACHE-CONTROL max-age must STAY a probe candidate for the retention
// window (a server that skipped one re-announce is usually still up;
// the description fetch filters genuinely dead ones, #341) and is only
// forgotten once expired for longer than announceExpiredRetention.
func TestAnnounceCache_ExpiredEntriesStayCandidates(t *testing.T) {
	c := &announceCache{entries: map[string]announceEntry{}}
	now := time.Now()
	alive := notifyPacket(
		"CACHE-CONTROL: max-age=60",
		"LOCATION: http://192.0.2.10:8200/rootDesc.xml",
		"NTS: ssdp:alive",
		"USN: uuid:abc-123",
	)
	c.handlePacket(alive, now)
	if locs := c.candidateLocations(now.Add(30 * time.Second)); len(locs) != 1 {
		t.Fatalf("at 30s of a 60s max-age: locations = %v, want 1", locs)
	}
	if locs := c.candidateLocations(now.Add(90 * time.Second)); len(locs) != 1 {
		t.Fatalf("at 90s of a 60s max-age: locations = %v, want kept as a candidate", locs)
	}
	past := now.Add(60 * time.Second).Add(announceExpiredRetention).Add(time.Minute)
	if locs := c.candidateLocations(past); len(locs) != 0 {
		t.Fatalf("past the retention window: locations = %v, want forgotten", locs)
	}
}

func TestParseMaxAge(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want time.Duration
	}{
		{"plain", "max-age=1800", 1800 * time.Second},
		{"with extra directives", "no-cache, max-age=900", 900 * time.Second},
		{"spaces and case", "Max-Age = 120", 120 * time.Second},
		{"missing header", "", defaultAnnounceMaxAge},
		{"garbage", "max-age=soon", defaultAnnounceMaxAge},
		{"zero", "max-age=0", defaultAnnounceMaxAge},
		{"capped", "max-age=999999999", maxAnnounceMaxAge},
	} {
		if got := parseMaxAge(tc.in); got != tc.want {
			t.Errorf("%s: parseMaxAge(%q) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
	}
}

// Every SoundTouch speaker announces itself over SSDP with the constant
// uuid:BO5EBO5E-F00D-F00D-FEED-<deviceID> prefix (measured 2026-08-24 on three
// chassis families, FW 27.0.6). The Bose-host accessor must return exactly the
// speakers, never the neighbourhood's other UPnP devices, and a byebye must
// retire the speaker like any other announcement.
func TestAnnounceCache_BoseHosts(t *testing.T) {
	c := &announceCache{entries: map[string]announceEntry{}}
	now := time.Now()

	speaker := notifyPacket(
		"CACHE-CONTROL: max-age=1800",
		"LOCATION: http://192.0.2.44:8091/XD/BO5EBO5E-F00D-F00D-FEED-AABBCCDDEEFF.xml",
		"NT: urn:schemas-upnp-org:device:MediaRenderer:1",
		"NTS: ssdp:alive",
		"USN: uuid:BO5EBO5E-F00D-F00D-FEED-AABBCCDDEEFF::urn:schemas-upnp-org:device:MediaRenderer:1",
	)
	tv := notifyPacket(
		"CACHE-CONTROL: max-age=1800",
		"LOCATION: http://192.0.2.99:9197/dmr",
		"NT: urn:schemas-upnp-org:device:MediaRenderer:1",
		"NTS: ssdp:alive",
		"USN: uuid:12345678-aaaa-bbbb-cccc-dddddddddddd::urn:schemas-upnp-org:device:MediaRenderer:1",
	)
	c.handlePacket(speaker, now)
	c.handlePacket(tv, now)

	hosts := c.boseHosts(now)
	if len(hosts) != 1 || hosts[0] != "192.0.2.44" {
		t.Fatalf("boseHosts = %v, want exactly the speaker's host", hosts)
	}

	// The same speaker announces several service variants under one uuid; the
	// host must not be duplicated.
	svc := notifyPacket(
		"CACHE-CONTROL: max-age=1800",
		"LOCATION: http://192.0.2.44:8091/XD/BO5EBO5E-F00D-F00D-FEED-AABBCCDDEEFF.xml",
		"NT: urn:schemas-upnp-org:service:AVTransport:1",
		"NTS: ssdp:alive",
		"USN: uuid:BO5EBO5E-F00D-F00D-FEED-AABBCCDDEEFF::urn:schemas-upnp-org:service:AVTransport:1",
	)
	c.handlePacket(svc, now)
	if hosts := c.boseHosts(now); len(hosts) != 1 {
		t.Fatalf("after a second service announcement: hosts = %v, want one", hosts)
	}

	byebye := notifyPacket(
		"NT: urn:schemas-upnp-org:device:MediaRenderer:1",
		"NTS: ssdp:byebye",
		"USN: uuid:BO5EBO5E-F00D-F00D-FEED-AABBCCDDEEFF::urn:schemas-upnp-org:device:MediaRenderer:1",
	)
	c.handlePacket(byebye, now)
	if hosts := c.boseHosts(now); len(hosts) != 0 {
		t.Fatalf("after byebye: hosts = %v, want none", hosts)
	}
}
