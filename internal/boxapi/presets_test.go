package boxapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// Golden samples: /presets shapes collected from the fixtures across the repo
// (internal/boxsnapshot, internal/boxws, internal/marge, cmd/agent), the
// ContentItem variants the firmware stores (STR's UPnP proxy, native radio,
// TuneIn, Spotify, Deezer, Pandora, a NAS album), the firmware quirks (empty
// list, a slot without a ContentItem, missing attributes, "&amp;" in a
// station URL) and malformed bodies. Every sample is read by a verbatim
// replica of the regex reader the agent's reconcile used and by ParsePresets;
// the two must agree except where presetDivergences says why not.
var presetsSamples = []struct {
	name string
	body string
}{
	{"snapshot fixture: STR proxy + Deezer", `<presets>
<preset id="1"><ContentItem source="UPNP" type="audio" location="http://127.0.0.1:8888/stream/1" sourceAccount="UPnPUserName" isPresetable="true"><itemName>1LIVE</itemName></ContentItem></preset>
<preset id="3"><ContentItem source="DEEZER" type="tracklisturl" location="/playlist/1234" sourceAccount="1456373802" isPresetable="true"><itemName>My Playlist</itemName></ContentItem></preset>
</presets>`},
	{"bus fixture: Deezer flow + radio with an empty account", `<presets>` +
		`<preset id="3"><ContentItem source="DEEZER" type="playlist" location="123456789" sourceAccount="1456373802"><itemName>My Flow</itemName></ContentItem></preset>` +
		`<preset id="1"><ContentItem source="LOCAL_INTERNET_RADIO" type="stationurl" location="http://wdr2.mp3" sourceAccount=""><itemName>WDR 2</itemName></ContentItem></preset>` +
		`</presets>`},
	{"firmware layout: declaration, timestamps, containerArt", `<?xml version="1.0" encoding="UTF-8" ?>
<presets>
  <preset id="1" createdOn="1700000000" updatedOn="1700000001">
    <ContentItem source="LOCAL_INTERNET_RADIO" type="stationurl" location="/station?data=eyJuYW1lIjoiV0RSIDUifQ==" sourceAccount="" isPresetable="true">
      <itemName>WDR 5</itemName>
      <containerArt>http://example.com/b.png</containerArt>
    </ContentItem>
  </preset>
  <preset id="2" createdOn="1700000002" updatedOn="1700000003">
    <ContentItem source="TUNEIN" type="stationurl" location="/v1/playback/station/s1234" sourceAccount="" isPresetable="true">
      <itemName>Radio Example</itemName>
      <containerArt>http://example.com/s1234q.png</containerArt>
    </ContentItem>
  </preset>
  <preset id="4" createdOn="1700000004" updatedOn="1700000005">
    <ContentItem source="SPOTIFY" type="uri" location="spotify:playlist:37i9dQZF1DXcBWIGoYBM5M" sourceAccount="spotify-user" isPresetable="true">
      <itemName>Playlist</itemName>
    </ContentItem>
  </preset>
  <preset id="5" createdOn="1700000006" updatedOn="1700000007">
    <ContentItem source="STORED_MUSIC" location="0$1$13$75" sourceAccount="device-id-here/0" isPresetable="true">
      <itemName>Random Access Memories</itemName>
    </ContentItem>
  </preset>
  <preset id="6" createdOn="1700000008" updatedOn="1700000009">
    <ContentItem source="PANDORA" type="stationurl" location="126740707481236361" sourceAccount="user@example.com" isPresetable="true">
      <itemName>Station</itemName>
    </ContentItem>
  </preset>
</presets>`},
	{"station URL with &amp; and a name with entities", `<presets><preset id="2"><ContentItem source="UPNP" type="audio" location="http://192.0.2.1:8888/stream/raw?u=abc&amp;x=1&amp;y=2" sourceAccount="UPnPUserName" isPresetable="true"><itemName>Pop &amp; Rock &lt;Live&gt; &quot;One&quot; New York&apos;s</itemName></ContentItem></preset></presets>`},
	{"entities in source, type and account", `<presets><preset id="5"><ContentItem source="A&amp;B" type="t&lt;1&gt;" location="x&quot;y&apos;z" sourceAccount="a&amp;b@example.com"><itemName>N</itemName></ContentItem></preset></presets>`},
	{"empty list", `<presets></presets>`},
	{"empty list, self-closing", `<?xml version="1.0" encoding="UTF-8" ?><presets />`},
	{"slot without a ContentItem", `<presets><preset id="2"></preset><preset id="3"><ContentItem source="TUNEIN" location="/v1/playback/station/s1"><itemName>Radio</itemName></ContentItem></preset></presets>`},
	{"ContentItem without location, name or account", `<presets><preset id="1"><ContentItem source="AUX" isPresetable="false" /></preset></presets>`},
	{"empty and self-closing itemName", `<presets><preset id="1"><ContentItem source="UPNP" location="http://127.0.0.1:8888/stream/1"><itemName></itemName></ContentItem></preset><preset id="2"><ContentItem source="UPNP" location="http://127.0.0.1:8888/stream/2"><itemName/></ContentItem></preset></presets>`},
	{"untrimmed itemName", `<presets><preset id="1"><ContentItem source="UPNP" location="http://127.0.0.1:8888/stream/1"><itemName>  Radio One </itemName></ContentItem></preset></presets>`},
	{"ids outside the six keys and a repeated id", `<presets><preset id="0"><ContentItem location="a"/></preset><preset id="7"><ContentItem location="b"/></preset><preset id="12"><ContentItem location="c"/></preset><preset id="3"><ContentItem location="d"/></preset><preset id="3"><ContentItem location="e"/></preset></presets>`},
	{"leading zero id", `<presets><preset id="04"><ContentItem source="UPNP" location="http://127.0.0.1:8888/stream/4"/></preset></presets>`},
	{"single preset block (marge fixture)", `<preset id="3"><ContentItem source="LOCAL_INTERNET_RADIO" type="stationurl" location="/station?data=abc" sourceAccount=""><itemName>WDR 5</itemName><containerArt>http://example.com/b.png</containerArt></ContentItem></preset>`},
	{"bus frame wrapper around the list", `<updates deviceID="device-id-here"><presetsUpdated><presets><preset id="1"><ContentItem source="UPNP" location="http://127.0.0.1:8888/stream/1"><itemName>One</itemName></ContentItem></preset></presets></presetsUpdated></updates>`},
	{"error reply", `<errors deviceID="device-id-here"><error value="401" name="HTTP_STATUS_UNAUTHORIZED" severity="Unknown">Unauthorized</error></errors>`},

	// Well-formed documents the regexes and an XML decoder read differently.
	{"id is not the first attribute", `<presets><preset createdOn="1700000000" id="2"><ContentItem source="UPNP" location="http://127.0.0.1:8888/stream/2"><itemName>Two</itemName></ContentItem></preset></presets>`},
	{"single-quoted attributes", `<presets><preset id='2'><ContentItem source='UPNP' location='http://127.0.0.1:8888/stream/2'><itemName>Two</itemName></ContentItem></preset></presets>`},
	{"numeric character reference in the location", `<presets><preset id="2"><ContentItem source="UPNP" location="http://192.0.2.1:8888/stream/raw?u=abc&#38;x=1"><itemName>Two</itemName></ContentItem></preset></presets>`},
	{"numeric character reference in the name", `<presets><preset id="2"><ContentItem source="UPNP" location="http://127.0.0.1:8888/stream/2"><itemName>Caf&#233;</itemName></ContentItem></preset></presets>`},
	{"bare apostrophe in an attribute", `<presets><preset id="2"><ContentItem source="STORED_MUSIC" location="0$albums$O'Brien" sourceAccount="device-id-here/0"><itemName>O'Brien</itemName></ContentItem></preset></presets>`},
	{"CDATA itemName", `<presets><preset id="2"><ContentItem source="UPNP" location="http://127.0.0.1:8888/stream/2"><itemName><![CDATA[Pop & Rock]]></itemName></ContentItem></preset></presets>`},
	{"Latin-1 umlaut in itemName", `<presets><preset id="2"><ContentItem source="UPNP" location="http://127.0.0.1:8888/stream/2"><itemName>K` + "\xfc" + `che</itemName></ContentItem></preset></presets>`},
	{"non-numeric id", `<presets><preset id="x"><ContentItem location="a"/></preset><preset id="2"><ContentItem location="b"/></preset></presets>`},

	// Malformed bodies: the decoder refuses them, the salvage path reads them
	// with the reconcile's own regexes.
	{"truncated mid-document", `<?xml version="1.0" encoding="UTF-8" ?><presets><preset id="1"><ContentItem source="UPNP" type="audio" location="http://127.0.0.1:8888/stream/1" sourceAccount="UPnPUserName"><itemName>One &amp; Only</itemName></ContentItem></preset><preset id="2"><ContentItem source="UPNP" location="http://127.0.0.1:8888/str`},
	{"bare ampersand in a station URL", `<presets><preset id="2"><ContentItem source="UPNP" type="audio" location="http://192.0.2.1:8888/stream/raw?u=abc&x=1" sourceAccount="a&amp;b"><itemName>Pop & Rock</itemName></ContentItem></preset></presets>`},
	{"unclosed preset swallows the next one", `<presets><preset id="1"><ContentItem source="UPNP" location="http://127.0.0.1:8888/stream/1"><preset id="2"><ContentItem source="TUNEIN" location="/v1/playback/station/s1"/></preset></presets>`},
	{"missing root close", `<presets><preset id="6"><ContentItem source="TUNEIN" type="stationurl" location="/v1/playback/station/s9" sourceAccount=""><itemName>Nine</itemName></ContentItem></preset>`},
	{"not XML at all", `not xml at all`},
	{"HTML error page", `<html><body><h1>404 Not Found</h1><hr></body></html>`},
	{"empty body", ``},
	{"whitespace body", "  \n"},
}

// legacyPresetEntry is the reconcile's boxPresetEntry before the typed reader.
type legacyPresetEntry struct {
	Slot                                  int
	Location, Name, Source, Type, Account string
}

// The regexes of cmd/agent/reconcile.go as they stood, verbatim.
var (
	legacyPresetBlock    = regexp.MustCompile(`(?s)<preset id="(\d+)".*?</preset>`)
	legacyPresetLocation = regexp.MustCompile(`location="([^"]*)"`)
	legacyPresetItemName = regexp.MustCompile(`<itemName>([^<]*)</itemName>`)
	legacyPresetSource   = regexp.MustCompile(`source="([^"]*)"`)
	legacyPresetType     = regexp.MustCompile(`type="([^"]*)"`)
	legacyPresetAccount  = regexp.MustCompile(`sourceAccount="([^"]*)"`)

	legacyEntityUnescape = strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'")
	legacyEntityEscape = strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
)

func legacyIsBoxSlot(slot int) bool { return slot >= 1 && slot <= 6 }

// legacyFetchBoxPresetsFull is the body of cmd/agent's fetchBoxPresetsFull as
// it stood: Location, Source, Type and Account left as the document spells
// them, Name with the five predefined entities reversed.
func legacyFetchBoxPresetsFull(body []byte) []legacyPresetEntry {
	var out []legacyPresetEntry
	for _, blk := range legacyPresetBlock.FindAllStringSubmatch(string(body), -1) {
		slot := 0
		fmt.Sscanf(blk[1], "%d", &slot)
		if !legacyIsBoxSlot(slot) {
			continue
		}
		e := legacyPresetEntry{Slot: slot}
		if m := legacyPresetLocation.FindStringSubmatch(blk[0]); m != nil {
			e.Location = m[1]
		}
		if m := legacyPresetItemName.FindStringSubmatch(blk[0]); m != nil {
			e.Name = legacyEntityUnescape.Replace(m[1])
		}
		if m := legacyPresetSource.FindStringSubmatch(blk[0]); m != nil {
			e.Source = m[1]
		}
		if m := legacyPresetType.FindStringSubmatch(blk[0]); m != nil {
			e.Type = m[1]
		}
		if m := legacyPresetAccount.FindStringSubmatch(blk[0]); m != nil {
			e.Account = m[1]
		}
		out = append(out, e)
	}
	return out
}

// typedAsLegacy is the mapping cmd/agent's boxPresetEntries applies to a
// typed read to keep handing out the legacy spelling: a parsed document has
// its decoded attributes escaped again, a salvaged one is passed through with
// the name unescaped.
func typedAsLegacy(doc *Presets) []legacyPresetEntry {
	var out []legacyPresetEntry
	for _, p := range doc.Items {
		if !legacyIsBoxSlot(p.ID) {
			continue
		}
		ci := p.ContentItem
		e := legacyPresetEntry{Slot: p.ID}
		if doc.Parsed {
			e.Location = legacyEntityEscape.Replace(ci.Location)
			e.Name = ci.ItemName
			e.Source = legacyEntityEscape.Replace(ci.Source)
			e.Type = legacyEntityEscape.Replace(ci.Type)
			e.Account = legacyEntityEscape.Replace(ci.SourceAccount)
		} else {
			e.Location = ci.Location
			e.Name = legacyEntityUnescape.Replace(ci.ItemName)
			e.Source = ci.Source
			e.Type = ci.Type
			e.Account = ci.SourceAccount
		}
		out = append(out, e)
	}
	return out
}

// presetDivergences names the samples where the typed reader, mapped back to
// the legacy spelling, deliberately answers differently from the regex
// reader, with the reason. Anything not listed must agree exactly.
var presetDivergences = map[string]string{
	"id is not the first attribute":               "the block regex needed id directly after `<preset `; the decoder reads the attribute wherever it stands, so the slot is listed instead of silently missing (the firmware writes id first)",
	"single-quoted attributes":                    "the regexes only knew double quotes; the decoder reads both (the firmware writes double quotes)",
	"numeric character reference in the location": "the regex kept `&#38;` as written; decoded and escaped again it is `&amp;`, the same text once a consumer unescapes it (and the old five-entity unescape left `&#38;` undecoded)",
	"numeric character reference in the name":     "the five-entity unescape left `&#233;` in the name; the decoder reads it as the character",
	"bare apostrophe in an attribute":             "a literal ' inside a double-quoted attribute comes back as `&apos;` after the re-escape; the same text once a consumer unescapes it",
	"CDATA itemName":                              "the itemName regex stopped at the `<` of a CDATA section and returned no name; the decoder reads the text",
	"Latin-1 umlaut in itemName":                  "the regex passed the Latin-1 byte through; the typed reader repairs it to UTF-8 like the now_playing reader does",
	"single preset block (marge fixture)":         "the block regex matched a <preset> anywhere; the typed reader lists the children of the document root, and a bare <preset> root has none (GET /presets always answers with a <presets> root)",
	"bus frame wrapper around the list":           "same: a list nested below the root is not the /presets document (the bus frames have their own reader in internal/boxws)",
}

func TestPresetsGoldenAgreesWithLegacyRegexReader(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range presetsSamples {
		legacy := legacyFetchBoxPresetsFull([]byte(s.body))
		typed := typedAsLegacy(ParsePresets([]byte(s.body)))
		equal := reflect.DeepEqual(legacy, typed)
		if reason, listed := presetDivergences[s.name]; listed {
			seen[s.name] = true
			if equal {
				t.Errorf("%s: listed as divergent (%s) but legacy and typed agree on %+v", s.name, reason, typed)
			}
			continue
		}
		if !equal {
			t.Errorf("%s:\n legacy %+v\n typed  %+v", s.name, legacy, typed)
		}
	}
	for name := range presetDivergences {
		if !seen[name] {
			t.Errorf("presetDivergences lists %q but no sample is named that", name)
		}
	}
}

// The divergent samples, pinned to what the typed mapping answers, so "it
// differs" cannot hide a wrong answer.
func TestPresetsDivergencesArePinned(t *testing.T) {
	want := map[string][]legacyPresetEntry{
		"id is not the first attribute":               {{Slot: 2, Location: "http://127.0.0.1:8888/stream/2", Name: "Two", Source: "UPNP"}},
		"single-quoted attributes":                    {{Slot: 2, Location: "http://127.0.0.1:8888/stream/2", Name: "Two", Source: "UPNP"}},
		"numeric character reference in the location": {{Slot: 2, Location: "http://192.0.2.1:8888/stream/raw?u=abc&amp;x=1", Name: "Two", Source: "UPNP"}},
		"numeric character reference in the name":     {{Slot: 2, Location: "http://127.0.0.1:8888/stream/2", Name: "Café", Source: "UPNP"}},
		"bare apostrophe in an attribute":             {{Slot: 2, Location: "0$albums$O&apos;Brien", Name: "O'Brien", Source: "STORED_MUSIC", Account: "device-id-here/0"}},
		"CDATA itemName":                              {{Slot: 2, Location: "http://127.0.0.1:8888/stream/2", Name: "Pop & Rock", Source: "UPNP"}},
		"Latin-1 umlaut in itemName":                  {{Slot: 2, Location: "http://127.0.0.1:8888/stream/2", Name: "Küche", Source: "UPNP"}},
		"single preset block (marge fixture)":         nil,
		"bus frame wrapper around the list":           nil,
	}
	for _, s := range presetsSamples {
		w, ok := want[s.name]
		if !ok {
			continue
		}
		if got := typedAsLegacy(ParsePresets([]byte(s.body))); !reflect.DeepEqual(got, w) {
			t.Errorf("%s:\n got  %+v\n want %+v", s.name, got, w)
		}
		delete(want, s.name)
	}
	for name := range want {
		t.Errorf("no sample named %q", name)
	}
}

func presetsSample(t *testing.T, name string) []byte {
	t.Helper()
	for _, s := range presetsSamples {
		if s.name == name {
			return []byte(s.body)
		}
	}
	t.Fatalf("no sample named %q", name)
	return nil
}

// The typed fields themselves: decoded, in document order, nothing dropped.
func TestParsePresetsTypedFields(t *testing.T) {
	doc := ParsePresets(presetsSample(t, "firmware layout: declaration, timestamps, containerArt"))
	if !doc.Parsed {
		t.Fatal("a well-formed list was not parsed")
	}
	want := []Preset{
		{ID: 1, ContentItem: ContentItem{Source: "LOCAL_INTERNET_RADIO", Type: "stationurl", Location: "/station?data=eyJuYW1lIjoiV0RSIDUifQ==", IsPresetable: true, ItemName: "WDR 5", ContainerArt: "http://example.com/b.png"}},
		{ID: 2, ContentItem: ContentItem{Source: "TUNEIN", Type: "stationurl", Location: "/v1/playback/station/s1234", IsPresetable: true, ItemName: "Radio Example", ContainerArt: "http://example.com/s1234q.png"}},
		{ID: 4, ContentItem: ContentItem{Source: "SPOTIFY", Type: "uri", Location: "spotify:playlist:37i9dQZF1DXcBWIGoYBM5M", SourceAccount: "spotify-user", IsPresetable: true, ItemName: "Playlist"}},
		{ID: 5, ContentItem: ContentItem{Source: "STORED_MUSIC", Location: "0$1$13$75", SourceAccount: "device-id-here/0", IsPresetable: true, ItemName: "Random Access Memories"}},
		{ID: 6, ContentItem: ContentItem{Source: "PANDORA", Type: "stationurl", Location: "126740707481236361", SourceAccount: "user@example.com", IsPresetable: true, ItemName: "Station"}},
	}
	if !reflect.DeepEqual(doc.Items, want) {
		t.Errorf("items:\n got  %+v\n want %+v", doc.Items, want)
	}

	// The escaping mismatch this reader ends: location and name both decoded.
	doc = ParsePresets(presetsSample(t, "station URL with &amp; and a name with entities"))
	p, ok := doc.Slot(2)
	if !ok {
		t.Fatal("slot 2 not found")
	}
	if got, want := p.ContentItem.Location, "http://192.0.2.1:8888/stream/raw?u=abc&x=1&y=2"; got != want {
		t.Errorf("location = %q, want %q", got, want)
	}
	if got, want := p.ContentItem.ItemName, `Pop & Rock <Live> "One" New York's`; got != want {
		t.Errorf("itemName = %q, want %q", got, want)
	}
	doc = ParsePresets(presetsSample(t, "entities in source, type and account"))
	if p, _ := doc.Slot(5); p.ContentItem != (ContentItem{Source: "A&B", Type: "t<1>", Location: `x"y'z`, SourceAccount: "a&b@example.com", ItemName: "N"}) {
		t.Errorf("entities: %+v", p.ContentItem)
	}
}

func TestParsePresetsQuirks(t *testing.T) {
	for _, name := range []string{"empty list", "empty list, self-closing", "error reply", "single preset block (marge fixture)", "bus frame wrapper around the list"} {
		doc := ParsePresets(presetsSample(t, name))
		if !doc.Parsed || len(doc.Items) != 0 {
			t.Errorf("%s: parsed=%v items=%+v, want a parsed document without slots", name, doc.Parsed, doc.Items)
		}
	}

	// A slot without a ContentItem is listed, empty; a missing attribute is "".
	doc := ParsePresets(presetsSample(t, "slot without a ContentItem"))
	if len(doc.Items) != 2 || doc.Items[0] != (Preset{ID: 2}) || doc.Items[1].ContentItem.Location != "/v1/playback/station/s1" {
		t.Errorf("slot without a ContentItem: %+v", doc.Items)
	}
	doc = ParsePresets(presetsSample(t, "ContentItem without location, name or account"))
	if len(doc.Items) != 1 || doc.Items[0] != (Preset{ID: 1, ContentItem: ContentItem{Source: "AUX"}}) {
		t.Errorf("missing attributes: %+v", doc.Items)
	}

	// The name is the element's text as written; trimming is the caller's.
	doc = ParsePresets(presetsSample(t, "untrimmed itemName"))
	if got := doc.Items[0].ContentItem.ItemName; got != "  Radio One " {
		t.Errorf("itemName = %q, want it untrimmed", got)
	}

	// Every <preset> is kept, whatever its id; Slot finds the first.
	doc = ParsePresets(presetsSample(t, "ids outside the six keys and a repeated id"))
	var ids []int
	for _, p := range doc.Items {
		ids = append(ids, p.ID)
	}
	if !reflect.DeepEqual(ids, []int{0, 7, 12, 3, 3}) {
		t.Errorf("ids = %v", ids)
	}
	if p, ok := doc.Slot(3); !ok || p.ContentItem.Location != "d" {
		t.Errorf("Slot(3) = %+v, %v, want the first of the two", p, ok)
	}
	if _, ok := doc.Slot(4); ok {
		t.Error("Slot(4) found a slot the list does not have")
	}
	doc = ParsePresets(presetsSample(t, "leading zero id"))
	if len(doc.Items) != 1 || doc.Items[0].ID != 4 {
		t.Errorf("leading zero id: %+v", doc.Items)
	}
	// An id that is not a number is 0, which is no key.
	doc = ParsePresets(presetsSample(t, "non-numeric id"))
	if len(doc.Items) != 2 || doc.Items[0].ID != 0 || doc.Items[1].ID != 2 {
		t.Errorf("non-numeric id: %+v", doc.Items)
	}
}

// A body the decoder refuses is salvaged with the regexes, undecoded, and
// says so.
func TestParsePresetsSalvage(t *testing.T) {
	doc := ParsePresets(presetsSample(t, "bare ampersand in a station URL"))
	if doc.Parsed {
		t.Fatal("a bare & is not well-formed XML, yet Parsed is true")
	}
	want := []Preset{{ID: 2, ContentItem: ContentItem{Source: "UPNP", Type: "audio",
		Location: "http://192.0.2.1:8888/stream/raw?u=abc&x=1", SourceAccount: "a&amp;b", ItemName: "Pop & Rock"}}}
	if !reflect.DeepEqual(doc.Items, want) {
		t.Errorf("salvaged:\n got  %+v\n want %+v", doc.Items, want)
	}

	doc = ParsePresets(presetsSample(t, "truncated mid-document"))
	if doc.Parsed || len(doc.Items) != 1 || doc.Items[0].ID != 1 || doc.Items[0].ContentItem.ItemName != "One &amp; Only" {
		t.Errorf("truncated: parsed=%v items=%+v, want the one complete block, undecoded", doc.Parsed, doc.Items)
	}

	// The unclosed first block runs to the first </preset>, as it always did:
	// one entry, with the first location in it.
	doc = ParsePresets(presetsSample(t, "unclosed preset swallows the next one"))
	if doc.Parsed || len(doc.Items) != 1 || doc.Items[0].ID != 1 || doc.Items[0].ContentItem.Location != "http://127.0.0.1:8888/stream/1" {
		t.Errorf("unclosed: parsed=%v items=%+v", doc.Parsed, doc.Items)
	}

	for _, name := range []string{"not XML at all", "empty body", "whitespace body"} {
		doc := ParsePresets(presetsSample(t, name))
		if doc.Parsed || len(doc.Items) != 0 {
			t.Errorf("%s: parsed=%v items=%+v, want unparsed and empty", name, doc.Parsed, doc.Items)
		}
	}
}

func TestClientPresets(t *testing.T) {
	status := http.StatusOK
	body := presetsSamples[0].body
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/presets" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := New("127.0.0.1")
	c.HTTP = &http.Client{Transport: rewriteTo(srv.Listener.Addr().String())}

	doc, err := c.Presets(context.Background())
	if err != nil {
		t.Fatalf("Presets: %v", err)
	}
	if string(doc.Raw) != body {
		t.Errorf("Raw differs from the wire body")
	}
	if !doc.Parsed || len(doc.Items) != 2 || doc.Items[1].ContentItem.Source != "DEEZER" {
		t.Errorf("parsed=%v items=%+v", doc.Parsed, doc.Items)
	}

	// A non-200 answer is the caller's to judge: an error, no document.
	status = http.StatusInternalServerError
	doc, err = c.Presets(context.Background())
	var be *BoxError
	if err == nil || !errors.As(err, &be) || doc != nil {
		t.Errorf("status 500: doc=%v err=%v, want a *BoxError and no document", doc, err)
	}
}
