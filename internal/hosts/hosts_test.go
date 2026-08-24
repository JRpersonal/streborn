package hosts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The block OpenCloudTouch left in the persistent /etc/hosts of the migrated
// SoundTouch 10 in #698, verbatim except the mod's LAN server IP, which is
// replaced with a documentation placeholder per the repo's no-real-LAN-IPs
// rule (the filter keys on non-loopback, not on any specific address). Only
// that one box has been measured; the unmarked-line tests below cover
// variants.
const octBlock = `# OCT-START
192.0.2.8	bose.vtuner.com	# OpenCloudTouch redirect
192.0.2.8	bose2.vtuner.com	# OpenCloudTouch redirect
192.0.2.8	primary5.vtuner.com	# OpenCloudTouch redirect
192.0.2.8	primary6.vtuner.com	# OpenCloudTouch redirect
192.0.2.8	streaming.bose.com	# OpenCloudTouch redirect
192.0.2.8	bmx.bose.com	# OpenCloudTouch redirect
192.0.2.8	api.bosesoundtouch.com	# OpenCloudTouch redirect
192.0.2.8	content.api.bose.io	# OpenCloudTouch redirect
192.0.2.8	events.api.bosecm.com	# OpenCloudTouch redirect
192.0.2.8	worldwide.bose.com	# OpenCloudTouch redirect
192.0.2.8	update.bose.com	# OpenCloudTouch redirect
192.0.2.8	analytics.bose.com	# OpenCloudTouch redirect
192.0.2.8	telemetry.bose.com	# OpenCloudTouch redirect
# OCT-END`

func applyTo(t *testing.T, content string, entries []Entry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(path, nil)
	if err := m.Apply(entries); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A base carrying the marked OpenCloudTouch block must come out without it:
// with first-match-wins resolution the leftover block sat above STR's
// appended one and kept BoseApp/STSCertified in SYN_SENT against the dead
// OCT server (#698). Unrelated user entries must survive.
func TestApplyFiltersOCTBlock(t *testing.T) {
	base := "127.0.0.1\tlocalhost.localdomain\t\tlocalhost\n\n" +
		octBlock + "\n" +
		"192.168.1.50\tmynas.local\n"
	got := applyTo(t, base, DefaultEntries())

	if strings.Contains(got, "OCT") {
		t.Errorf("OCT markers survived Apply:\n%s", got)
	}
	if strings.Contains(got, "192.0.2.8") {
		t.Errorf("OCT server IP survived Apply:\n%s", got)
	}
	if !strings.Contains(got, "192.168.1.50\tmynas.local") {
		t.Errorf("unrelated user entry was dropped:\n%s", got)
	}
	if !strings.Contains(got, "localhost.localdomain") {
		t.Errorf("localhost line was dropped:\n%s", got)
	}
	if n := strings.Count(got, beginMarker); n != 1 {
		t.Errorf("want exactly 1 STR block, got %d:\n%s", n, got)
	}
	// The resolver must now only ever see loopback for the redirected hosts.
	for _, l := range strings.Split(got, "\n") {
		if strings.Contains(l, "streaming.bose.com") && !strings.HasPrefix(l, "127.0.0.1") {
			t.Errorf("streaming.bose.com still resolves foreign: %q", l)
		}
	}
	if len(ForeignFiltered()) != 15 { // 13 redirects + 2 marker lines
		t.Errorf("ForeignFiltered: want 15 lines, got %d: %v",
			len(ForeignFiltered()), ForeignFiltered())
	}
}

// A clean base must produce byte-identical output to the pre-#698 composition:
// boxes that never saw another mod must not change at all.
func TestApplyCleanBaseByteIdentical(t *testing.T) {
	base := "127.0.0.1\tlocalhost.localdomain\t\tlocalhost\n"
	entries := []Entry{{IP: "127.0.0.1", Host: "streaming.bose.com"}}
	got := applyTo(t, base, entries)

	want := "127.0.0.1\tlocalhost.localdomain\t\tlocalhost\n\n" +
		beginMarker + "\n" +
		"127.0.0.1\tstreaming.bose.com\n" +
		endMarker + "\n"
	if got != want {
		t.Errorf("clean base changed:\nwant %q\ngot  %q", want, got)
	}
	if len(ForeignFiltered()) != 0 {
		t.Errorf("ForeignFiltered on clean base: %v", ForeignFiltered())
	}
}

// A rival redirect without the OCT markers (another mod version, or a
// hand-edited leftover) must be filtered by the generic layer: any Bose cloud
// hostname pointed at a non-loopback address fights STR's own redirect.
// Loopback mappings and unrelated hosts stay.
func TestApplyFiltersUnmarkedForeignRedirect(t *testing.T) {
	base := "127.0.0.1\tlocalhost\n" +
		"192.0.2.8\tstreaming.bose.com\n" +
		"192.0.2.8\tbose.vtuner.com\n" +
		"127.0.0.1\tbmx.bose.com\n" + // loopback: cannot steer the box away from STR
		"192.168.1.50\tmynas.local\n"
	got := applyTo(t, base, DefaultEntries())

	if strings.Contains(got, "192.0.2.8") {
		t.Errorf("foreign bose redirect survived:\n%s", got)
	}
	if !strings.Contains(got, "127.0.0.1\tbmx.bose.com") {
		t.Errorf("loopback bose mapping was dropped:\n%s", got)
	}
	if !strings.Contains(got, "192.168.1.50\tmynas.local") {
		t.Errorf("unrelated user entry was dropped:\n%s", got)
	}
	filtered := ForeignFiltered()
	if len(filtered) != 2 {
		t.Errorf("ForeignFiltered: want 2 lines, got %v", filtered)
	}
}

// An opening marker without a closing one must not swallow the rest of the
// file; the per-line layer still neutralizes the actual redirects.
func TestApplyUnterminatedOCTBlockKeepsTail(t *testing.T) {
	base := "127.0.0.1\tlocalhost\n" +
		"# OCT-START\n" +
		"192.0.2.8\tstreaming.bose.com\n" +
		"192.168.1.50\tmynas.local\n"
	got := applyTo(t, base, DefaultEntries())

	if !strings.Contains(got, "192.168.1.50\tmynas.local") {
		t.Errorf("unterminated block swallowed user entry:\n%s", got)
	}
	if strings.Contains(got, "192.0.2.8") {
		t.Errorf("foreign redirect survived unterminated block:\n%s", got)
	}
}

// Apply must stay idempotent with the filter in place: a second run replaces
// STR's own block instead of stacking a new one.
func TestApplyTwiceKeepsOneBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	base := "127.0.0.1\tlocalhost\n" + octBlock + "\n"
	if err := os.WriteFile(path, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(path, nil)
	for i := 0; i < 2; i++ {
		if err := m.Apply(DefaultEntries()); err != nil {
			t.Fatalf("Apply #%d: %v", i+1, err)
		}
	}
	b, _ := os.ReadFile(path)
	if n := strings.Count(string(b), beginMarker); n != 1 {
		t.Errorf("want exactly 1 STR block after two Applies, got %d:\n%s", n, b)
	}
}

func TestContainsOCTBlock(t *testing.T) {
	if !ContainsOCTBlock([]byte("x\n# OCT-START\ny\n")) {
		t.Error("marker block not detected")
	}
	if ContainsOCTBlock([]byte("127.0.0.1\tlocalhost\n")) {
		t.Error("clean file misdetected")
	}
	// The marker must be a line of its own, not a substring of a comment.
	if ContainsOCTBlock([]byte("# see docs about # OCT-START markers\n")) {
		t.Error("substring misdetected as marker line")
	}
}
