package marge

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The firmware's flat store record for a Pandora station on a US speaker
// (#1101). The real body was never observed; this is the MargeAddPresetRequest
// shape the radio stores use, with the source id STR gives Pandora.
const pandoraFlatPresetBody = `<?xml version="1.0" encoding="UTF-8" ?><preset buttonNumber="1">` +
	`<sourceid>200</sourceid><name>Little Big Town Radio</name><username>listener@example.com</username>` +
	`<location>4071226281950183516</location><contentItemType>stationurl</contentItemType>` +
	`<containerArt>https://example.com/art.jpg</containerArt></preset>`

const pandoraPresetPath = "/streaming/account/stick@local/device/AABBCCDDEEFF/preset/1"

func TestSourceIDsOfTheNativeServicesAreNamed(t *testing.T) {
	if got := sourceNameForAccountID("200"); got != "PANDORA" {
		t.Fatalf("200 -> %q", got)
	}
	if got := sourceNameForAccountID("201"); got != "IHEART" {
		t.Fatalf("201 -> %q", got)
	}
	if got := sourceNameForAccountID("150"); got != "SOURCE#150" {
		t.Fatalf("an unknown id must stay opaque, got %q", got)
	}
}

// A Pandora station kept on a key is answered with the preset element, and
// its source element is the registered Pandora source /full carries, so the
// firmware gets back a record that names a source it knows.
func TestPandoraPresetStoreIsAnsweredWithTheRegisteredSource(t *testing.T) {
	var got HeldItem
	s := newNativeServer(t, false, "US", []Option{WithPresetKeeper(func(item HeldItem) error { got = item; return nil })})
	s.SetAccount(&AccountInfo{AccountEmail: "stick@local"})
	if w := serve(s, http.MethodPost, "/streaming/account/stick@local/source", pandoraAddSourceBody, "127.0.0.1:4000"); w.Code != http.StatusCreated {
		t.Fatalf("Pandora registration: status %d", w.Code)
	}

	w := serve(s, http.MethodPut, pandoraPresetPath, pandoraFlatPresetBody, "127.0.0.1:4000")
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `<preset buttonNumber="1">`) {
		t.Fatalf("status %d, body:\n%s", w.Code, body)
	}
	for _, want := range []string{
		`<name>Little Big Town Radio</name>`,
		`<location>4071226281950183516</location>`,
		`<source id="200" type="Audio">`,
		`<sourceproviderid>1</sourceproviderid>`,
		`<sourcename>PANDORA</sourcename>`,
		`<username>listener@example.com</username>`,
		`<contentItemType>stationurl</contentItemType>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("answer misses %s:\n%s", want, body)
		}
	}
	if got.Source != "PANDORA" || got.SourceID != "200" || got.Location != "4071226281950183516" ||
		got.SourceAccount != "listener@example.com" || got.Type != "stationurl" || got.Form != "flat" {
		t.Fatalf("keeper got %+v", got)
	}
}

// Without a registration (a carried-over account), the answer still names
// the source by its enum and the id the firmware quoted.
func TestPandoraPresetStoreWithoutARegistration(t *testing.T) {
	s := newKeeperServer(t, func(HeldItem) error { return nil })
	w := serve(s, http.MethodPut, pandoraPresetPath, pandoraFlatPresetBody, "127.0.0.1:4000")
	body := w.Body.String()
	if !strings.Contains(body, `<source id="200" type="Audio">`) || !strings.Contains(body, `<sourcename>PANDORA</sourcename>`) {
		t.Fatalf("answer:\n%s", body)
	}
}

// A reflected (carried-over) source id resolves through the account document.
func TestReflectedSourceIDResolves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reflect-sources.json")
	if err := os.WriteFile(path, []byte(`[{"source":"DEEZER","account":"listener@example.com"},{"source":"PANDORA","account":"listener@example.com"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newNativeServer(t, false, "", []Option{WithReflectSourcesPath(path)})
	if got := s.sourceNameForID("101"); got != "PANDORA" {
		t.Fatalf("101 -> %q (reflected list: %+v)", got, s.reflectedAccountSources())
	}
}
