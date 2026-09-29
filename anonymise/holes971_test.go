package anonymise

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
)

// The seven holes in #971, one test each, written from the shapes that were
// actually measured in the 72-bundle corpus rather than from invented examples.
// Every one of those bundles carried "Anonymized: true", and four of them are
// public, so each of these is a value that is already out.
//
// The values below are made up. What is copied from the corpus is the SHAPE:
// where the value sat, and what surrounded it.

func init() { SetSalt([]byte("test-salt")) }

// Hole 1: userPathRegex stops at whitespace, so a surname walked through.
// 9 of 72 bundles, and one household was confirmed twice because the same name
// was also its DNS search domain.
func TestASurnameInAUserFolderDoesNotSurvive(t *testing.T) {
	cases := []string{
		`logFile=C:\Users\Anna Schmidt\AppData\Roaming\STR\app.log`,
		`logFile=/Users/Anna Schmidt/Library/Logs/STR/app.log`,
		`home=/home/anna schmidt/.config/streborn`,
		// Three names, the outer bound of what is accepted.
		`C:\Users\Anna Maria Schmidt\Desktop\bundle.zip`,
	}
	for _, in := range cases {
		got := ScrubPII(in)
		if strings.Contains(strings.ToLower(got), "schmidt") {
			t.Errorf("surname survived\n in: %s\nout: %s", in, got)
		}
		if !strings.Contains(got, "<user>") {
			t.Errorf("no mask written\n in: %s\nout: %s", in, got)
		}
	}
}

// The spaced pattern must not become a licence to eat a log line. A path with
// no trailing separator is masked by the narrow pattern, and the words after it
// stay.
func TestTheSpacedUserPathDoesNotSwallowTheRestOfTheLine(t *testing.T) {
	in := `checked /Users/bob then opened /tmp/x and gave up`
	got := ScrubPII(in)
	for _, want := range []string{"then opened", "and gave up"} {
		if !strings.Contains(got, want) {
			t.Errorf("the pass ate the line: %q is gone\nout: %s", want, got)
		}
	}
	if strings.Contains(got, "bob") {
		t.Errorf("account name survived: %s", got)
	}
}

// Hole 2: a first name in a path with no /Users/ or /home/ to anchor on.
// 6 of 72 bundles, in stick and library paths on another drive.
func TestTheExportingAccountNameIsStruckWithoutAnAnchor(t *testing.T) {
	SetLocalNames("Annemarie", `C:\Users\Annemarie`)
	defer SetLocalNames()

	cases := []string{
		`stick=D:\Annemarie\str-stick\install.sh`,
		`library=E:\Musik\Annemarie\Sampler`,
		`note: exported by annemarie at 14:02`,
	}
	for _, in := range cases {
		got := ScrubPII(in)
		if strings.Contains(strings.ToLower(got), "annemarie") {
			t.Errorf("account name survived\n in: %s\nout: %s", in, got)
		}
	}
}

// A name too short or too common is not struck: doing so would gut the log
// rather than anonymise it, and none of those names identifies a person.
func TestGenericAccountNamesAreLeftAlone(t *testing.T) {
	SetLocalNames("root", "pi", "bose", "user")
	defer SetLocalNames()

	in := `mount /dev/root on / ; su - bose ; owner=user`
	if got := ScrubPII(in); got != in {
		t.Errorf("a generic name was struck and the line lost meaning:\n in: %s\nout: %s", in, got)
	}
}

// Hole 4: looksLikeAccountIdentity never ran over log text, so a Spotify
// identity shipped in clear. 7 of 72 bundles, one of them a spotify:user: URI
// in firstname.lastname shape.
func TestASpotifyIdentityInLogTextIsHashed(t *testing.T) {
	cases := []string{
		`play uri=spotify:user:anna.schmidt:playlist:37i9dQZF1DX`,
		`spotify recall account="anna.schmidt@example.com" slot=3`,
		`{"account":"anna.schmidt","product":"premium"}`,
		`login=anna.schmidt comp=spotify`,
		`mail from anna.schmidt@example.com about a preset`,
	}
	for _, in := range cases {
		got := ScrubPII(in)
		if strings.Contains(strings.ToLower(got), "anna.schmidt") {
			t.Errorf("identity survived\n in: %s\nout: %s", in, got)
		}
		if !strings.Contains(got, "ACCT#") {
			t.Errorf("no account token written\n in: %s\nout: %s", in, got)
		}
	}
}

// The account pass must not eat the attribute beside it, or a diagnostic loses
// the state it was read for.
func TestTheAccountPassKeepsTheAttributesAroundIt(t *testing.T) {
	in := `spotify recall account=anna.schmidt slot=3 source=SPOTIFY result=ok`
	got := ScrubPII(in)
	for _, want := range []string{"slot=3", "source=SPOTIFY", "result=ok"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is gone\nout: %s", want, got)
		}
	}
}

// And it has to be idempotent: the text goes through scrubPII more than once
// (sanitizeLog and anonymizeText both call it, nested structures field by
// field), and a pass that re-hashes its own output produces a moving token.
func TestTheAccountAndNamePassesAreIdempotent(t *testing.T) {
	in := `account="anna.schmidt" friendlyName=Kueche uri=spotify:user:anna.schmidt`
	once := ScrubPII(in)
	if twice := ScrubPII(once); twice != once {
		t.Errorf("not idempotent:\n1st: %s\n2nd: %s", once, twice)
	}
}

// Hole 6: /playback/container/<base64> was not unwrapped, so an identity
// survived inside it. Third time an encoded value has got out this way.
func TestAnIdentityInsideAContainerKeyIsReached(t *testing.T) {
	uri := "spotify:user:anna.schmidt:playlist:37i9dQZF1DX"
	enc := base64.RawURLEncoding.EncodeToString([]byte(uri))
	in := "preset 3 -> http://192.168.178.79:17008/playback/container/" + enc

	got := ScrubPII(in)
	if strings.Contains(got, enc) {
		t.Fatalf("the payload was left as it was, so the identity inside it shipped:\n%s", got)
	}
	// The rewritten payload must still decode, or the bundle stops being
	// evidence.
	i := strings.LastIndex(got, "/playback/container/")
	if i < 0 {
		t.Fatalf("the route itself was mangled: %s", got)
	}
	dec, err := base64.RawURLEncoding.DecodeString(got[i+len("/playback/container/"):])
	if err != nil {
		t.Fatalf("the rewritten payload does not decode: %v (%s)", err, got)
	}
	if strings.Contains(string(dec), "anna.schmidt") {
		t.Errorf("identity still inside the payload: %s", dec)
	}
	if !strings.Contains(string(dec), "spotify:user:") {
		t.Errorf("the payload lost its shape, so nobody can read it: %s", dec)
	}
}

// Hole 7, first half: the 12-hex pattern cut the last group out of every UUID,
// which is the value the media-server analysis is read for (#733).
func TestAUUIDIsNotCutInHalf(t *testing.T) {
	in := `server uuid:5435f503-c9e0-4ac0-ac67-58d3491f4b1a browse ok`
	got := ScrubPII(in)
	if strings.Contains(got, "ac67-DEV#") {
		t.Fatalf("the UUID was cut mid-value: %s", got)
	}
	if strings.Contains(got, "58d3491f4b1a") {
		t.Errorf("the UUID shipped in clear: %s", got)
	}
	if !strings.Contains(got, "UUID#") {
		t.Errorf("no UUID token written: %s", got)
	}
	// Stable, so two bundles can still be compared on it.
	if ScrubPII(in) != got {
		t.Error("the UUID token is not stable between two passes")
	}
}

// Hole 7, second half: the one UUID whose last group really IS a device id
// still has to be hashed, and hashed to the same token as the deviceID beside
// it, or a reader can no longer see that the UDN and the device are one
// speaker. This is the case a plain "leave anything dashed alone" rule breaks.
func TestTheBoseUDNStillHashesItsMACToTheDeviceToken(t *testing.T) {
	in := `location=http://192.168.178.21:8091/XD/BO5EBO5E-F00D-F00D-FEED-A1B2C3D4E5F6 deviceID=A1B2C3D4E5F6`
	got := ScrubPII(in)
	if strings.Contains(got, "A1B2C3D4E5F6") {
		t.Fatalf("the MAC shipped in clear inside the UDN: %s", got)
	}
	tok := regexp.MustCompile(`DEV#[0-9a-f]{8}`).FindAllString(got, -1)
	if len(tok) != 2 {
		t.Fatalf("want two device tokens, got %v in %s", tok, got)
	}
	if tok[0] != tok[1] {
		t.Errorf("the UDN and the deviceID hashed differently (%s vs %s), so the link is lost", tok[0], tok[1])
	}
	if !strings.Contains(got, "BO5EBO5E-F00D-F00D-FEED-") {
		t.Errorf("the readable prefix is gone: %s", got)
	}
}

// A friendly name written as a log attribute. The JSON and XML forms were
// already covered; this third one is how the factory default name shipped, and
// its last six hex are half the speaker's MAC.
func TestAFriendlyNameAsALogAttributeIsHashed(t *testing.T) {
	in := `state old="model=SoundTouch 20 friendlyName=Bose SoundTouch FD438B" new="model=SoundTouch 20"`
	got := ScrubPII(in)
	if strings.Contains(got, "FD438B") {
		t.Errorf("the name, and half the MAC with it, survived: %s", got)
	}
	if !strings.Contains(got, "NAME#") {
		t.Errorf("no name token written: %s", got)
	}
	if !strings.Contains(got, `new="model=SoundTouch 20"`) {
		t.Errorf("the pass ran past the closing quote: %s", got)
	}
}

// The scrub is run over the same text repeatedly. Everything it writes has to
// survive its own next pass unchanged, which is the property that kept biting
// in the SSID marker.
func TestTheWholePassIsIdempotentOnABundleShapedLine(t *testing.T) {
	SetLocalNames("Annemarie")
	defer SetLocalNames()

	in := strings.Join([]string{
		`time=10:02 comp=marge deviceID=A1B2C3D4E5F6 friendlyName=Kueche`,
		`ssid="Home Net 5G" mac=A1:B2:C3:D4:E5:F6 ip=192.168.178.21`,
		`server uuid:5435f503-c9e0-4ac0-ac67-58d3491f4b1a`,
		`log=C:\Users\Anna Schmidt\AppData\app.log account="anna@example.com"`,
		`stick=D:\Annemarie\str`,
	}, "\n")

	once := ScrubPII(in)
	twice := ScrubPII(once)
	if once != twice {
		t.Errorf("not idempotent\n1st:\n%s\n2nd:\n%s", once, twice)
	}
	for _, leak := range []string{"A1B2C3D4E5F6", "A1:B2:C3:D4:E5:F6", "192.168.178", "Schmidt", "Annemarie", "anna@example.com", "58d3491f4b1a", "Home Net"} {
		if strings.Contains(once, leak) {
			t.Errorf("%q survived:\n%s", leak, once)
		}
	}
}

// The account pass is the one that has to be conservative in the other
// direction. sourceAccount also holds a physical socket label, and those are the
// reason a bundle carries /sources at all: hashing them leaves it unable to
// answer which inputs a soundbar has. Caught by the desktop app's own tests when
// the first version of this pass hashed AUX and TV, which also destroyed the
// signal the /sources pass reads to decide whether a nickname beside an id is
// personal.
func TestASocketLabelIsNotAnAccount(t *testing.T) {
	in := `sourceItem source="AUX" sourceAccount="AUX" account=TV slot=1 label="CBL-Sat"`
	got := ScrubPII(in)
	for _, want := range []string{`sourceAccount="AUX"`, "account=TV", `label="CBL-Sat"`} {
		if !strings.Contains(got, want) {
			t.Errorf("%q was hashed although it names a socket, not a person\nout: %s", want, got)
		}
	}
}

// A firmware placeholder for an unlinked slot names nobody, and the input filter
// keys on exactly that suffix.
func TestAnUnlinkedSlotPlaceholderSurvives(t *testing.T) {
	in := `sourceAccount="SpotifyConnectUserName" status="UNAVAILABLE"`
	if got := ScrubPII(in); got != in {
		t.Errorf("a placeholder was hashed:\n in: %s\nout: %s", in, got)
	}
}

// An empty account is itself a diagnostic: a Spotify recall line with
// account="" means the preset was saved without a login, which is the
// legacy-dead-preset class (#45/#105). It must not turn into a token.
func TestAnEmptyAccountStaysEmpty(t *testing.T) {
	in := `spotify recall account="" slot=3 result=silent`
	if got := ScrubPII(in); got != in {
		t.Errorf("an empty account was masked, and the diagnostic with it:\n in: %s\nout: %s", in, got)
	}
}

// The kernel banner carries a build-host address, and MODEL-VARIANTS.md matches
// incoming diagnostics on that exact line. Found by running the new pass over
// real bundles, where it hashed the banner.
func TestTheKernelBuildHostIsNotAnAccount(t *testing.T) {
	in := `kernel="Linux version 3.14.43+ (epdbuild@hepdswbld04.bose.com) (gcc version 4.7.3)"`
	if got := ScrubPII(in); got != in {
		t.Errorf("the firmware fingerprint was hashed:\n in: %s\nout: %s", in, got)
	}
}

// Two speaker-name leaks measured in bundles that were attached to PUBLIC GitHub
// issues, 2026-09-29. Both had been shipping for a while: the first because slog
// quotes any value with a space in it, the second because nothing looked at the
// firmware's own hostname at all.
func TestSpeakerNamesDoNotLeaveInClear(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		// slog writes this whenever the chosen name contains a space, which is
		// most of them: "Living Room", "Kinderzimmer oben".
		{"a quoted friendlyName", `time=2026-09-28T10:00:00Z level=INFO msg="box named" friendlyName="Living Room" port=8888`},
		{"an unquoted friendlyName", `msg="box named" friendlyName=Kitchen port=8888`},
		{"a quoted pair name", `msg="pair formed" pair="Wohnzimmer Stereo" role=master`},
		// The firmware's own hostname, straight out of the box syslog.
		{"the firmware hostname", `Sep 28 11:25:11 hostname:SoundTouch-Kitchen daemon.info BoseApp: ready`},
		{"a hostname with a hyphen in the name", `hostname:SoundTouch-Living-Room daemon.info`},
	}
	secrets := []string{"Living Room", "Kitchen", "Wohnzimmer Stereo", "Living-Room"}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ScrubPII(c.in)
			for _, secret := range secrets {
				if strings.Contains(c.in, secret) && strings.Contains(got, secret) {
					t.Errorf("the speaker name %q survives into a public bundle:\n  in:  %s\n  out: %s", secret, c.in, got)
				}
			}
			if !strings.Contains(got, "NAME#") {
				t.Errorf("nothing was masked at all:\n  in:  %s\n  out: %s", c.in, got)
			}
		})
	}
}

// The vendor default carries half the MAC, so it is an identifier too and gets
// the same treatment.
func TestTheVendorDefaultHostnameIsMaskedToo(t *testing.T) {
	got := ScrubPII(`hostname:SoundTouch-FD438B daemon.info`)
	if strings.Contains(got, "FD438B") {
		t.Errorf("the MAC tail survives: %s", got)
	}
}
