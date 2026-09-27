package dnsboot

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The order of nameservers on a speaker, and why it is worth a test.
//
// Go's resolver walks the list in order with a five second timeout each. The
// firmware gives a station five to ten seconds to start before it gives up. So a
// speaker whose resolv.conf asks a public fallback first pays longer to look up
// a station than the firmware is willing to wait, and NOTHING plays. Not one
// station: everything.
//
// That is the file a real speaker was found with, reported as "one station only
// works on one device". The speaker beside it, same household, same station,
// played fine, and the only difference was that its agent had started after the
// interface came up.

func withStagedResolv(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "resolv.conf")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	old := ourResolv
	ourResolv = p
	t.Cleanup(func() { ourResolv = old })
}

func staged(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(ourResolv)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// nameserverOrder is the addresses in the order a resolver would try them.
func nameserverOrder(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "nameserver")
		if !ok {
			continue
		}
		if f := strings.Fields(rest); len(f) > 0 {
			out = append(out, f[0])
		}
	}
	return out
}

// The file as it was found on the speaker that could not play anything.
const foundOnTheSpeaker = `# written by STR (dnsboot): the box had no usable nameserver.
nameserver 1.1.1.1
nameserver 8.8.8.8
search lan # wlan0
nameserver 192.168.1.2 # wlan0
`

func TestTheHouseholdResolverIsAskedBeforeThePublicFallbacks(t *testing.T) {
	withStagedResolv(t, foundOnTheSpeaker)

	if !demoteBootstrapResolvers(quietLogger()) {
		t.Fatal("nothing was rewritten, so the speaker keeps paying ten seconds a lookup")
	}
	got := nameserverOrder(staged(t))
	want := []string{"192.168.1.2", "1.1.1.1", "8.8.8.8"}
	if len(got) != len(want) {
		t.Fatalf("nameservers = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("nameservers = %v, want %v", got, want)
		}
	}
	// The fallbacks are DEMOTED, not deleted: a household resolver that stops
	// answering later is the case this package exists for.
	if !strings.Contains(staged(t), "1.1.1.1") {
		t.Error("the fallback was removed rather than moved behind")
	}
	// The search domain has to survive, or short names stop resolving.
	if !strings.Contains(staged(t), "search lan") {
		t.Error("the search domain was dropped")
	}
}

func TestItIsIdempotent(t *testing.T) {
	withStagedResolv(t, foundOnTheSpeaker)
	demoteBootstrapResolvers(quietLogger())
	first := staged(t)
	if demoteBootstrapResolvers(quietLogger()) {
		t.Fatal("a second pass rewrote a file that was already right; on a speaker that is a write every thirty seconds")
	}
	if staged(t) != first {
		t.Error("the file changed on a pass that reported no change")
	}
}

func TestAFileWithNoHouseholdResolverIsLeftAlone(t *testing.T) {
	// The interface is still not up. The fallbacks are all this speaker has and
	// taking them away would leave it with nothing.
	withStagedResolv(t, "# written by STR (dnsboot): the box had no usable nameserver.\nnameserver 1.1.1.1\nnameserver 8.8.8.8\n")
	before := staged(t)
	if demoteBootstrapResolvers(quietLogger()) {
		t.Fatal("the only resolvers this speaker has were rewritten")
	}
	if staged(t) != before {
		t.Error("the file was changed")
	}
}

func TestAFileThisPackageDidNotWriteIsNeverTouched(t *testing.T) {
	// A speaker whose own resolv.conf is fine. Rewriting somebody else's file
	// is how a working speaker gets broken.
	body := "search lan # wlan0\nnameserver 192.168.1.2 # wlan0\nnameserver 1.1.1.1\n"
	withStagedResolv(t, body)
	if demoteBootstrapResolvers(quietLogger()) {
		t.Fatal("a file without the STR header was rewritten")
	}
	if staged(t) != body {
		t.Error("the file was changed")
	}
}

func TestAnAlreadyCorrectOrderIsNotRewritten(t *testing.T) {
	withStagedResolv(t, "# written by STR (dnsboot): the box had no usable nameserver.\nnameserver 192.168.1.2 # wlan0\nnameserver 1.1.1.1\n")
	if demoteBootstrapResolvers(quietLogger()) {
		t.Fatal("a file already in the right order was rewritten")
	}
}

func TestAMissingFileIsNotAnError(t *testing.T) {
	old := ourResolv
	ourResolv = filepath.Join(t.TempDir(), "does-not-exist")
	t.Cleanup(func() { ourResolv = old })
	if demoteBootstrapResolvers(quietLogger()) {
		t.Error("reported a write on a speaker where this package never acted")
	}
}
