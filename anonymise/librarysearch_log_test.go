package anonymise

import (
	"strings"
	"testing"
)

// The desktop's whole-library search summary (msg="library search", no colon)
// named the servers that did not answer in clear, the hostnames of a PC and a
// NAS among them (#1705 bundle, 2026-11-22). Both the old slice form and the
// new ", " list are masked, each name the way its server= line masks it.
func TestScrubPII_LibrarySearchSummaryServerLists(t *testing.T) {
	cases := []string{
		`time=2026-11-22T11:44:34.175Z level=INFO msg="library search" field=title servers=2 hits=1 unanswered="[Alexs-PC: alex:]" partial=true`,
		`time=2026-11-22T11:51:06.710Z level=INFO msg="library search" field=title servers=3 hits=2 unanswered=[AlexsNAS] partial=true`,
		`time=2026-11-22T12:04:24.945Z level=INFO msg="library search" field=title servers=3 hits=4 unanswered="AlexsNAS, Alexs-PC: alex:" incomplete="Alexs-Laptop: alex:" partial=true`,
	}
	for _, in := range cases {
		out := ScrubPII(in)
		for _, leak := range []string{"Alexs", "alex:"} {
			if strings.Contains(out, leak) {
				t.Errorf("%q survived:\n in: %s\nout: %s", leak, in, out)
			}
		}
		if !strings.Contains(out, "HOST#") {
			t.Errorf("no pseudonym in %s", out)
		}
	}
	// Same pseudonym as the per-server line, so a reader can follow the PC.
	perServer := ScrubPII(`level=WARN msg="library search: server could not be searched" server="Alexs-PC: alex:"`)
	summary := ScrubPII(`level=INFO msg="library search" unanswered="Alexs-PC: alex:"`)
	token := maskMediaServerName("Alexs-PC: alex:")
	if !strings.Contains(perServer, token) || !strings.Contains(summary, token) {
		t.Errorf("pseudonyms differ:\n%s\n%s", perServer, summary)
	}
	// An empty list stays readable.
	if out := ScrubPII(`msg="library search" unanswered=[] partial=false`); !strings.Contains(out, "unanswered=[]") {
		t.Errorf("empty list mangled: %s", out)
	}
}

// A Windows adapter the owner renamed is hashed; the default names stay.
func TestScrubPII_InterfaceNames(t *testing.T) {
	out := ScrubPII(`level=INFO msg="dlna: SSDP NOTIFY listener joined" comp=dlna iface=AlexsHomeNet`)
	if strings.Contains(out, "AlexsHomeNet") || !strings.Contains(out, "iface=IFACE#") {
		t.Errorf("renamed adapter survived: %s", out)
	}
	out = ScrubPII(`level=INFO msg="dlna: SSDP NOTIFY listener joined" comp=dlna iface="Alexs Home Net"`)
	if strings.Contains(out, "Alexs") {
		t.Errorf("quoted renamed adapter survived: %s", out)
	}
	for _, keep := range []string{"eth0", "wlan0", "en0", "Wi-Fi", "Ethernet", `"Ethernet 2"`, `"vEthernet (WSL)"`} {
		in := `level=INFO msg="dlna: SSDP NOTIFY listener joined" iface=` + keep
		if out := ScrubPII(in); out != in {
			t.Errorf("default name %s altered: %s", keep, out)
		}
	}
}
