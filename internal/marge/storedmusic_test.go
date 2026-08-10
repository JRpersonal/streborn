package marge

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

func testStoredMusicLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// The media-server source must differ from the known-good radio source in
// exactly three values and nothing else. A source rendered into the account
// document that omits an element the firmware expects is the documented way to
// break the WHOLE document rather than just that entry, so the element set and
// order are part of the contract, not a style choice.
func TestStoredMusicSourceMatchesRadioSourceShape(t *testing.T) {
	got := storedMusicSourcesXML([]registeredSource{
		{Username: "fa095ecc-uuid/0", Name: "AVM FRITZ!Mediaserver"},
	})

	elements := func(s string) []string {
		var out []string
		for _, part := range strings.Split(s, "<") {
			if i := strings.IndexAny(part, " >/"); i > 0 {
				out = append(out, part[:i])
			}
		}
		return out
	}
	wantShape := elements(staticRadioSourceXML())
	gotShape := elements(got)
	if len(wantShape) != len(gotShape) {
		t.Fatalf("element count differs from the radio source:\n radio: %v\n music: %v", wantShape, gotShape)
	}
	for i := range wantShape {
		if wantShape[i] != gotShape[i] {
			t.Errorf("element %d differs: radio %q, music %q", i, wantShape[i], gotShape[i])
		}
	}

	for _, want := range []string{
		`<sourceproviderid>7</sourceproviderid>`,
		`<sourcename>STORED_MUSIC</sourcename>`,
		`<username>fa095ecc-uuid/0</username>`,
		`<name>AVM FRITZ!Mediaserver</name>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	// id 3 belongs to the radio source and must not be reused.
	if strings.Contains(got, `<source id="3"`) {
		t.Errorf("media server reused the radio source id 3:\n%s", got)
	}
}

func TestStoredMusicEmptyRendersNothing(t *testing.T) {
	if got := storedMusicSourcesXML(nil); got != "" {
		t.Errorf("no servers must render nothing, got %q", got)
	}
}

// SetStoredMusicSources drops entries with no account: an empty username would
// advertise a source the box can never resolve.
func TestSetStoredMusicSourcesSkipsEmptyAccounts(t *testing.T) {
	s := New(testStoredMusicLogger())
	s.SetStoredMusicSources([]StoredMusicSource{
		{Account: "", Name: "broken"},
		{Account: "abc/0", Name: "good"},
	})
	got := s.storedMusicXML()
	if strings.Contains(got, "broken") {
		t.Errorf("an entry without an account must not be advertised:\n%s", got)
	}
	if !strings.Contains(got, "abc/0") {
		t.Errorf("the valid entry is missing:\n%s", got)
	}
}

// The account document the box polls at boot must carry the media servers, or
// the pull-based persistence does not exist.
func TestAccountResponsesCarryStoredMusic(t *testing.T) {
	s := New(testStoredMusicLogger())
	s.SetStoredMusicSources([]StoredMusicSource{{Account: "fa095ecc-uuid/0", Name: "FRITZ"}})
	if got := s.storedMusicXML(); !strings.Contains(got, "fa095ecc-uuid/0") {
		t.Fatalf("storedMusicXML lost the server: %s", got)
	}
}
