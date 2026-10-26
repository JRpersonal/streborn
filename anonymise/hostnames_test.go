package anonymise

import (
	"encoding/json"
	"strings"
	"testing"
)

// The line that leaked: MinimServer names itself after the machine it runs on,
// macOS names the machine after its owner, and the agent logs the server's
// friendly name when discovery misses it. A public bundle carried it in clear.
const missedServerLine = `time=2026-10-25T09:12:44.101Z level=INFO msg="library search: discovery missed a registered server, re-probed its last known address" server=MinimServer[Alexs-MacBook-Pro.local]`

func TestScrubPII_MediaServerHostInFriendlyName(t *testing.T) {
	out := ScrubPII(missedServerLine)
	for _, leak := range []string{"Alex", "MacBook"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q survived:\n in: %s\nout: %s", leak, missedServerLine, out)
		}
	}
	// The product is what the line is read for, and the message must stay.
	want := "server=MinimServer[HOST#" + hashShort("alexs-macbook-pro") + ".local]"
	if !strings.Contains(out, want) {
		t.Errorf("want %s in\n%s", want, out)
	}
	if !strings.Contains(out, "discovery missed a registered server") {
		t.Errorf("message text lost: %s", out)
	}
}

// Names that are not plain ASCII: an apostrophe (straight and typographic), a
// space, and letters outside ASCII. A slog value with a space is quoted.
func TestScrubPII_MediaServerHostApostropheAndUnicode(t *testing.T) {
	cases := []struct{ in, leak string }{
		{`server="MinimServer[Alex's Mac mini]"`, "Alex"},
		{`server="MinimServer[Alex’s Mac mini]"`, "Alex"},
		{`server=MinimServer[Jöns-iMac.local]`, "Jöns"},
		{`server=MinimServer[Zoë-NAS]`, "Zoë"},
		{`server=MinimServer[Ålands-MacBook-Air.local.]`, "Ålands"},
		{`{"server":"MinimServer[Alexs-MacBook-Pro.local]"}`, "Alex"},
	}
	for _, c := range cases {
		out := ScrubPII(c.in)
		if strings.Contains(out, c.leak) {
			t.Errorf("%q survived:\n in: %s\nout: %s", c.leak, c.in, out)
		}
		if !strings.Contains(out, "MinimServer[HOST#") {
			t.Errorf("product or token missing: %s", out)
		}
	}
	// JSON stays JSON.
	var v map[string]string
	if err := json.Unmarshal([]byte(ScrubPII(`{"server":"MinimServer[Alex's Mac mini]"}`)), &v); err != nil {
		t.Fatalf("masked JSON no longer parses: %v", err)
	}
}

// A bare .local name anywhere in a line, in a URL, in quotes, with an
// apostrophe or with letters outside ASCII.
func TestScrubPII_LocalHostNames(t *testing.T) {
	cases := []struct{ in, leak string }{
		{`msg="dlna: described" location=http://Alexs-MacBook-Pro.local:9790/desc.xml`, "Alex"},
		{`resolved 'Alexs-MacBook-Pro.local' to an address`, "Alex"},
		{`host=Alex's-iMac.local`, "Alex"},
		{`host=Alex’s-iMac.local`, "Alex"},
		{`host=Jöns-MacBook-Pro.local`, "Jöns"},
		{`host=K-chePortable.local`, "chePortable"},
		{`target=str-0000c3.local:17008`, "0000c3"},
		{`browse hit Kitchen._soundtouch._tcp.local`, "Kitchen"},
	}
	for _, c := range cases {
		out := ScrubPII(c.in)
		if strings.Contains(out, c.leak) {
			t.Errorf("%q survived:\n in: %s\nout: %s", c.leak, c.in, out)
		}
		if !strings.Contains(out, "HOST#") {
			t.Errorf("no token in %s", out)
		}
	}
	// The quote stays around the token, the port after it, the service type
	// after an instance.
	if out := ScrubPII(`resolved 'Alexs-MacBook-Pro.local' to`); !strings.Contains(out, `'HOST#`+hashShort("alexs-macbook-pro")+`.local'`) {
		t.Errorf("quoted name mangled: %s", out)
	}
	if out := ScrubPII(`target=str-0000c3.local:17008`); !strings.HasSuffix(out, ".local:17008") {
		t.Errorf("port lost: %s", out)
	}
	if out := ScrubPII(`browse hit Kitchen._soundtouch._tcp.local`); !strings.HasSuffix(out, "._soundtouch._tcp.local") {
		t.Errorf("service type lost: %s", out)
	}
}

// The same machine gets the same token whichever way it is written, so a
// reader can follow it from the server name to its address.
func TestScrubPII_HostTokenSameAcrossSpellings(t *testing.T) {
	want := "HOST#" + hashShort("alexs-macbook-pro")
	for _, in := range []string{
		`server=MinimServer[Alexs-MacBook-Pro.local]`,
		`server=MinimServer[Alexs-MacBook-Pro]`,
		`location=http://alexs-macbook-pro.local:9790/`,
		`host=ALEXS-MACBOOK-PRO.local.`,
	} {
		if out := ScrubPII(in); !strings.Contains(out, want) {
			t.Errorf("want %s in %s", want, out)
		}
	}
}

// What must survive: things that look like either shape and identify nobody.
func TestScrubPII_HostNamesLeaveTheRestAlone(t *testing.T) {
	keep := []string{
		`cp run-override.sh /mnt/nv/rc.local`,
		`browsing _spotify-connect._tcp.local for 3s`,
		`Browsing _streborn._tcp.local for 8 seconds...`,
		`chassis host mojo.local answered`,
		`resolver: localhost.localdomain`,
		`file=foo.locale`,
		`headers=map[Content-Type:[text/xml]]`,
		`Oct 25 09:12:44 BoseApp[1234]: started`,
		`slots[3] empty`,
		`SpGetVolume [returned value: 42]`,
		`server=MinimServer[HOST#0123abcd.local]`,
	}
	for _, in := range keep {
		if out := ScrubPII(in); out != in {
			t.Errorf("altered:\n in: %s\nout: %s", in, out)
		}
	}
	// A token another pass already wrote is not hashed again.
	out := ScrubPII(`hostname:SoundTouch-Kitchen.local`)
	if strings.Count(out, "#") != 1 || !strings.Contains(out, "SoundTouch-NAME#") {
		t.Errorf("firmware host double-masked: %s", out)
	}
}

// Running the scrub twice changes nothing the first run did not.
func TestScrubPII_HostNamesIdempotent(t *testing.T) {
	once := ScrubPII(missedServerLine + "\n" + `location=http://Alexs-MacBook-Pro.local:9790/`)
	if twice := ScrubPII(once); twice != once {
		t.Errorf("second pass changed the text:\n1: %s\n2: %s", once, twice)
	}
}
