package mdnshost

import (
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"

	"github.com/miekg/dns"
)

// The label has to be unique per speaker and survive a rename, which is why it
// comes from the device ID and not from the friendly name.
func TestTheLabelComesFromTheDeviceID(t *testing.T) {
	cases := map[string]string{
		"000C8A96488D": "str-96488d", // Jens' SoundTouch 30
		"7CEC79F9ECA2": "str-f9eca2", // a SoundTouch 10
		"08DF1F0C9870": "str-0c9870", // the Portable
		"DEV#28669F56": "str-669f56", // the firmware's prefixed form, punctuation dropped
	}
	for id, want := range cases {
		if got := LabelFor(id); got != want {
			t.Errorf("LabelFor(%q) = %q, want %q", id, got, want)
		}
	}
	if got := LabelFor(""); got != "" {
		t.Errorf("an empty device ID must produce no label, got %q", got)
	}
}

// Three SoundTouch 10s on one network all claim rhino.local today. Whatever
// replaces it must not be able to collide.
func TestTwoSpeakersNeverShareALabel(t *testing.T) {
	ids := []string{"000C8A96488D", "7CEC79F9ECA2", "E0E5CF89A5A2", "884AEA785412", "08DF1F0C9870"}
	seen := map[string]string{}
	for _, id := range ids {
		l := LabelFor(id)
		if prev, dup := seen[l]; dup {
			t.Fatalf("%s and %s both produce %q", prev, id, l)
		}
		seen[l] = id
	}
}

// A label the engine's mDNS library rejects is not cosmetic: the engine treats
// a failed registration as fatal and exits, so a bad label would cost Spotify
// altogether rather than just the advert.
func TestAnUnsafeLabelIsRefused(t *testing.T) {
	bad := []string{
		"", " ", "str 96488d", "str.96488d", "STR-96488D",
		"-leading-hyphen", "büro", "str-96488d.local",
		strings.Repeat("a", 64),
	}
	for _, l := range bad {
		if ValidLabel(l) {
			t.Errorf("ValidLabel(%q) = true, want false", l)
		}
	}
	for _, l := range []string{"str-96488d", "a", "str-0c9870"} {
		if !ValidLabel(l) {
			t.Errorf("ValidLabel(%q) = false, want true", l)
		}
	}
}

// Start must refuse rather than come up half working, because the caller uses
// its success as the condition for pointing the engine at the name.
func TestStartRefusesWhatItCannotPublish(t *testing.T) {
	if _, err := Start(t.Context(), testLogger(), "not a label", net.IPv4(192, 0, 2, 1)); err == nil {
		t.Error("a label that cannot be published was accepted")
	}
	if _, err := Start(t.Context(), testLogger(), "str-96488d", nil); err == nil {
		t.Error("a responder was started with no address to hand out")
	}
}

// The whole design rests on this: we answer for our own name and for nothing
// else. Answering for the chassis name, or for a name the Bose firmware
// publishes, would put two responders on one name.
func TestItAnswersOnlyItsOwnName(t *testing.T) {
	r := &Responder{label: "str-96488d", fqdn: "str-96488d.local.", ip: net.IPv4(192, 0, 2, 7).To4()}

	answered := func(name string, qtype uint16) bool {
		msg := new(dns.Msg)
		msg.SetQuestion(dns.Fqdn(name), qtype)
		var got []dns.RR
		for _, q := range msg.Question {
			if !strings.EqualFold(q.Name, r.fqdn) {
				continue
			}
			if q.Qtype != dns.TypeA && q.Qtype != dns.TypeANY {
				continue
			}
			got = append(got, r.record())
		}
		return len(got) > 0
	}

	if !answered("str-96488d.local", dns.TypeA) {
		t.Error("the responder does not answer for its own name")
	}
	if !answered("STR-96488D.local", dns.TypeA) {
		t.Error("the name comparison must be case insensitive")
	}
	for _, n := range []string{
		"mojo.local", "rhino.local", "taigan.local", // the chassis names
		"Wohnzimmer.local", "Bose-SM2-7cec79f9eca2.local", // the firmware's own
		"str-0c9870.local",            // another speaker
		"_spotify-connect._tcp.local", // not ours to answer
		"_streborn._tcp.local",        // not even our own service
	} {
		if answered(n, dns.TypeA) {
			t.Errorf("the responder answered for %q, which does not belong to it", n)
		}
	}
	if answered("str-96488d.local", dns.TypeSRV) {
		t.Error("only address questions may be answered, not SRV")
	}
	if answered("str-96488d.local", dns.TypePTR) {
		t.Error("only address questions may be answered, not PTR")
	}
}

// The record is ours alone, so it carries the cache-flush bit and the address
// the speaker is actually reachable on.
func TestTheRecordSaysWhatItShould(t *testing.T) {
	r := &Responder{label: "str-96488d", fqdn: "str-96488d.local.", ip: net.IPv4(192, 0, 2, 7).To4()}
	a, ok := r.record().(*dns.A)
	if !ok {
		t.Fatal("the published record is not an address record")
	}
	if a.Hdr.Name != "str-96488d.local." {
		t.Errorf("name = %q", a.Hdr.Name)
	}
	if a.Hdr.Class&0x8000 == 0 {
		t.Error("the cache-flush bit is not set on a name we own exclusively")
	}
	if a.Hdr.Ttl != ttlSeconds {
		t.Errorf("ttl = %d, want %d", a.Hdr.Ttl, ttlSeconds)
	}
	if !a.A.Equal(net.IPv4(192, 0, 2, 7).To4()) {
		t.Errorf("address = %v", a.A)
	}
}

// The engine's mDNS library appends the domain itself, so it must be handed the
// bare label. "str-96488d.local" would become "str-96488d.local.local".
func TestTheEngineIsHandedTheBareLabel(t *testing.T) {
	r := &Responder{label: "str-96488d", fqdn: "str-96488d.local."}
	if strings.Contains(r.Label(), ".") {
		t.Errorf("Label() = %q, which the engine would turn into %q.local", r.Label(), r.Label())
	}
	if r.Name() != "str-96488d.local" {
		t.Errorf("Name() = %q", r.Name())
	}
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
