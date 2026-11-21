package boxsources

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A store shaped like a speaker's: a Deezer login, an empty radio entry and
// a Spotify entry with the token_version_3 type. All values are placeholders.
const sampleStore = `<?xml version="1.0" encoding="UTF-8" ?>
<sources>
  <source displayName="Listener Name" secret="deezer-secret-value" secretType="token">
    <sourceKey type="DEEZER" account="0000000000" />
  </source>
  <source secret="" secretType="">
    <sourceKey type="LOCAL_INTERNET_RADIO" account="" />
  </source>
  <source displayName="user@example.com" secret="spotify-secret-value" secretType="token_version_3">
    <sourceKey type="SPOTIFY" account="user@example.com" />
  </source>
</sources>`

func TestParse(t *testing.T) {
	entries, err := Parse([]byte(sampleStore))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %+v", entries)
	}
	d := entries[0]
	if d.Type != "DEEZER" || d.Account != "0000000000" || d.Secret != "deezer-secret-value" || d.SecretType != "token" || d.DisplayName != "Listener Name" {
		t.Fatalf("deezer = %+v", d)
	}
	if entries[1].Type != "LOCAL_INTERNET_RADIO" || entries[1].Secret != "" {
		t.Fatalf("radio = %+v", entries[1])
	}
}

func TestParseEmptyAndForeign(t *testing.T) {
	for _, in := range []string{"", "<sources/>", "<other><thing/></other>"} {
		entries, err := Parse([]byte(in))
		if err != nil || len(entries) != 0 {
			t.Errorf("Parse(%q) = %+v, %v", in, entries, err)
		}
	}
}

func TestSummaryCarriesNoSecretNoAccountNoName(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1", "Sources.xml"), []byte(sampleStore), 0o600); err != nil {
		t.Fatal(err)
	}
	s := DebugSummary(filepath.Join(dir, "*", "Sources.xml"))
	if !s.Present || s.Error != "" || len(s.Entries) != 3 {
		t.Fatalf("summary = %+v", s)
	}
	want := []SummaryEntry{
		{Source: "DEEZER", SecretType: "token", HasSecret: true},
		{Source: "LOCAL_INTERNET_RADIO"},
		{Source: "SPOTIFY", SecretType: "token_version_3", HasSecret: true},
	}
	for i, w := range want {
		if s.Entries[i] != w {
			t.Errorf("entry %d = %+v, want %+v", i, s.Entries[i], w)
		}
	}
	js, _ := json.Marshal(s)
	for _, leak := range []string{"deezer-secret-value", "spotify-secret-value", "0000000000", "user@example.com", "Listener Name"} {
		if strings.Contains(string(js), leak) {
			t.Fatalf("summary carries %q: %s", leak, js)
		}
	}
}

func TestSummaryMissingFile(t *testing.T) {
	s := DebugSummary(filepath.Join(t.TempDir(), "*", "Sources.xml"))
	if s.Present || s.Error != "" || len(s.Entries) != 0 {
		t.Fatalf("summary = %+v", s)
	}
}

func TestSummaryUnreadableKeepsNoText(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Sources.xml"), []byte(`<sources><source secret="leak-me"><sourceKey type="DEEZER"`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := DebugSummary(filepath.Join(dir, "Sources.xml"))
	js, _ := json.Marshal(s)
	if strings.Contains(string(js), "leak-me") {
		t.Fatalf("summary carries the secret: %s", js)
	}
	if !s.Present || s.Error == "" {
		t.Fatalf("summary = %+v", s)
	}
}

func TestSafeReplacesNonEnumValues(t *testing.T) {
	entries := []Entry{{Type: "DEEZER", SecretType: "a token with spaces=leak", Secret: "x"}}
	got := Summarize(entries)
	if got[0].SecretType != "<other>" || !got[0].HasSecret {
		t.Fatalf("summary = %+v", got)
	}
}
