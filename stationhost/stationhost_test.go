package stationhost

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestShared(t *testing.T) {
	shared := []string{
		// #1773: the stream host of "Radio Eure du Blues", and its root domain.
		"radioeure.ddns.net", "ddns.net",
		"RADIO.NO-IP.ORG", "x.no-ip.com", "a.no-ip.biz", "a.no-ip.info", "a.noip.me",
		"a.hopto.org", "a.zapto.org", "a.sytes.net", "a.myftp.org", "a.myftp.biz",
		"a.servemp3.com", "a.serveftp.com", "a.redirectme.net",
		"members.dyndns.org", "x.dyndns.info", "x.dyndns.tv", "x.dnsalias.com", "x.dnsalias.net", "x.homeip.net",
		"blues.duckdns.org", "a.freeddns.org", "a.myddns.me", "a.dynu.net", "a.ddnsfree.com",
		"a.mooo.com", "a.afraid.org", "a.chickenkiller.com",
		"abc.ngrok.io", "abc-def.trycloudflare.com",
		// trailing dot, port, IP literals
		"radio.ddns.net.", "radio.ddns.net:8000",
		"192.0.2.7", "[2001:db8::1]", "2001:db8::1", "[2001:db8::1]:8000",
	}
	for _, h := range shared {
		if !Shared(h) {
			t.Errorf("%q is a shared host, its logo is the provider's", h)
		}
	}
	own := []string{
		"", "www.radio-eure.example", "radio-eure.example", "stream.example.de",
		// a name that only looks like a provider
		"myddns.net.example", "notddns.net", "ddns.network", "dyndns", "www.dyndns-radio.example",
	}
	for _, h := range own {
		if Shared(h) {
			t.Errorf("%q is a station's own host and must still be looked up", h)
		}
	}
}

// The desktop frontend keeps a copy of the lists in logos.js, because the
// logo chain saved on a key is built there. The copy must say the same.
func TestFrontendCopyMatches(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "desktop-app", "frontend", "src", "logos.js"))
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	read := func(name string) []string {
		m := regexp.MustCompile(`(?s)export const ` + name + ` = \[(.*?)\];`).FindStringSubmatch(js)
		if m == nil {
			t.Fatalf("logos.js lost %s", name)
		}
		var out []string
		for _, q := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(m[1], -1) {
			out = append(out, q[1])
		}
		return out
	}
	sorted := func(s []string) []string { c := slices.Clone(s); slices.Sort(c); return c }
	if got, want := sorted(read("SHARED_HOST_DOMAINS")), sorted(SharedDomains); !slices.Equal(got, want) {
		t.Errorf("logos.js SHARED_HOST_DOMAINS drifted from stationhost.SharedDomains:\n js %s\n go %s",
			strings.Join(got, " "), strings.Join(want, " "))
	}
	if got, want := sorted(read("SHARED_HOST_APEX_LABELS")), sorted(SharedApexLabels); !slices.Equal(got, want) {
		t.Errorf("logos.js SHARED_HOST_APEX_LABELS drifted: js %v, go %v", got, want)
	}
}
