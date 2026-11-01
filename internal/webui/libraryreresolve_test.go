package webui

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/JRpersonal/streborn/dlna"
	"github.com/JRpersonal/streborn/internal/mediaservers"
	"github.com/JRpersonal/streborn/internal/presets"
)

// fakeRenumberingServer is a media server in the shape of Windows media
// sharing after a restart (#919): every file URL it handed out before
// ("/WMPNSSv4/111/...") answers 404, the same tracks now live under
// "/WMPNSSv4/222/...", and its ContentDirectory knows them by object id.
type fakeRenumberingServer struct {
	ts *httptest.Server
	mu sync.Mutex
	// items is the folder c-1 as the server lists it now, id -> title.
	items [][2]string
	// knowsIDs false answers every BrowseMetadata with 701 No such object:
	// the server renumbered its object ids too.
	knowsIDs bool
	calls    []string
}

func newFakeRenumberingServer(t *testing.T, knowsIDs bool, items ...[2]string) *fakeRenumberingServer {
	t.Helper()
	f := &fakeRenumberingServer{items: items, knowsIDs: knowsIDs}
	f.ts = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.ts.Close)
	return f
}

func (f *fakeRenumberingServer) oldURL(name string) string {
	return f.ts.URL + "/WMPNSSv4/111/" + name + ".mp3"
}

func (f *fakeRenumberingServer) newURL(name string) string {
	return f.ts.URL + "/WMPNSSv4/222/" + name + ".mp3"
}

func (f *fakeRenumberingServer) srv() dlna.Server {
	return dlna.Server{UDN: "uuid:PC-MEDIA", FriendlyName: "PC: Media", CDSControlURL: f.ts.URL + "/cds"}
}

func (f *fakeRenumberingServer) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeRenumberingServer) itemXML(id, title string) string {
	return fmt.Sprintf(`<item id="%s" parentID="c-1"><dc:title>%s</dc:title>`+
		`<upnp:class>object.item.audioItem.musicTrack</upnp:class>`+
		`<res protocolInfo="http-get:*:audio/mpeg:*" duration="0:03:20">%s</res></item>`, id, title, f.newURL(id))
}

func (f *fakeRenumberingServer) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/WMPNSSv4/222/"):
		f.note("GET " + r.URL.Path)
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte{0})
		return
	case strings.HasPrefix(r.URL.Path, "/WMPNSSv4/"):
		f.note("GET " + r.URL.Path)
		http.NotFound(w, r)
		return
	case r.URL.Path != "/cds":
		http.NotFound(w, r)
		return
	}
	b, _ := io.ReadAll(r.Body)
	flag, id := soapValue(string(b), "BrowseFlag"), soapValue(string(b), "ObjectID")
	f.note(flag + " " + id)
	var didl strings.Builder
	switch flag {
	case "BrowseMetadata":
		found := ""
		for _, it := range f.items {
			if it[0] == id && f.knowsIDs {
				found = f.itemXML(it[0], it[1])
			}
		}
		if found == "" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault>` +
				`<detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>701</errorCode></UPnPError></detail>` +
				`</s:Fault></s:Body></s:Envelope>`))
			return
		}
		didl.WriteString(found)
	default:
		if id != "c-1" {
			didl.Reset()
		} else {
			for _, it := range f.items {
				didl.WriteString(f.itemXML(it[0], it[1]))
			}
		}
	}
	n := strings.Count(didl.String(), "<item ")
	result := `<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/">` +
		didl.String() + `</DIDL-Lite>`
	_, _ = fmt.Fprintf(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>`+
		`<u:BrowseResponse xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1"><Result>%s</Result>`+
		`<NumberReturned>%d</NumberReturned><TotalMatches>%d</TotalMatches></u:BrowseResponse></s:Body></s:Envelope>`,
		html.EscapeString(result), n, n)
}

func (f *fakeRenumberingServer) note(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

// reresolveServer is a library-heal server whose key host answers (the PC did
// not move, #919 is the path that changed) and whose media-server resolve
// hands back the fake server, counting the resolves.
func reresolveServer(t *testing.T, f *fakeRenumberingServer, resolves *[]string) *Server {
	t.Helper()
	s := libHealServer(t, mediaservers.Server{ID: "uuid:PC-MEDIA", Name: "PC: Media",
		Location: f.ts.URL + "/description.xml"})
	s.queueHostAnswers = func(context.Context, string) bool { return true }
	s.libraryServerResolve = func(_ context.Context, udn string) (dlna.Server, bool) {
		*resolves = append(*resolves, udn)
		return f.srv(), true
	}
	return s
}

func renumberedTrackKey(f *fakeRenumberingServer, ref *presets.TrackRef) presets.Preset {
	return presets.Preset{Slot: 6, Type: "radio", Name: "100% organic", Source: "PC: Media",
		StreamURL: f.oldURL("obj-7"), Track: ref}
}

// The reporter's key (#917): a single track from Windows media sharing, saved
// with its object id. After the PC restarted the stored URL answers 404; the
// press asks the server for the track's current URL by its id, plays that and
// keeps it.
func TestLibraryKeyFindsItsTrackAgainAfterTheServerRenumbered(t *testing.T) {
	f := newFakeRenumberingServer(t, true, [2]string{"obj-7", "100% organic"})
	var resolves []string
	s := reresolveServer(t, f, &resolves)
	ref := &presets.TrackRef{ServerUDN: "uuid:PC-MEDIA", ObjectID: "obj-7", ParentID: "c-1", Title: "100% organic"}
	if err := s.presets.SetSlot(renumberedTrackKey(f, ref)); err != nil {
		t.Fatal(err)
	}
	pressed, _ := s.presets.Get(6)
	s.healLibraryKeyIfMoved(context.Background(), 6, &pressed)

	if pressed.StreamURL != f.newURL("obj-7") {
		t.Fatalf("key not pointed at the track's new URL: %q", pressed.StreamURL)
	}
	if len(resolves) != 1 || resolves[0] != "uuid:PC-MEDIA" {
		t.Fatalf("resolved %v, want the key's server once", resolves)
	}
	got, _ := s.presets.Get(6)
	if got.StreamURL != f.newURL("obj-7") || got.Track == nil || got.Track.ObjectID != "obj-7" {
		t.Fatalf("corrected key not persisted with its ref: %+v", got)
	}
	if got.DurationSec != 200 {
		t.Errorf("length the server reported not kept: %d", got.DurationSec)
	}
	if libraryFileMime(got) != "audio/mpeg" {
		t.Fatalf("healed key lost its library shape")
	}
	// One look at the old file, one metadata call, nothing else: no crawl.
	want := []string{"GET /WMPNSSv4/111/obj-7.mp3", "BrowseMetadata obj-7"}
	if strings.Join(f.log(), "|") != strings.Join(want, "|") {
		t.Fatalf("server saw %v, want %v", f.log(), want)
	}

	// The next press finds the stored URL serving and asks nothing more.
	again, _ := s.presets.Get(6)
	s.healLibraryKeyIfMoved(context.Background(), 6, &again)
	if len(resolves) != 1 {
		t.Fatalf("a serving key was resolved again: %v", resolves)
	}
}

// A server that renumbered its object ids too: the track is found by its title
// in the folder it was saved from, and the key learns the new id.
func TestLibraryKeyFallsBackToTheTitleInItsFolder(t *testing.T) {
	f := newFakeRenumberingServer(t, false,
		[2]string{"new-1", "Another song"}, [2]string{"new-2", "100% organic"})
	var resolves []string
	s := reresolveServer(t, f, &resolves)
	ref := &presets.TrackRef{ServerUDN: "uuid:PC-MEDIA", ObjectID: "obj-7", ParentID: "c-1", Title: "100% Organic"}
	key := renumberedTrackKey(f, ref)
	if err := s.presets.SetSlot(key); err != nil {
		t.Fatal(err)
	}
	pressed, _ := s.presets.Get(6)
	s.healLibraryKeyIfMoved(context.Background(), 6, &pressed)
	if pressed.StreamURL != f.newURL("new-2") {
		t.Fatalf("not found by title: %q", pressed.StreamURL)
	}
	got, _ := s.presets.Get(6)
	if got.Track == nil || got.Track.ObjectID != "new-2" {
		t.Fatalf("key did not learn the new object id: %+v", got.Track)
	}
	if ref.ObjectID != "obj-7" {
		t.Fatalf("the caller's TrackRef was written through: %+v", ref)
	}
}

// A title that is not unique in the folder is not certain enough: the key is
// left as it is rather than pointed at the wrong song.
func TestLibraryKeyAmbiguousTitleIsLeftAlone(t *testing.T) {
	f := newFakeRenumberingServer(t, false,
		[2]string{"new-1", "Intro"}, [2]string{"new-2", "Intro"})
	var resolves []string
	s := reresolveServer(t, f, &resolves)
	key := renumberedTrackKey(f, &presets.TrackRef{ServerUDN: "uuid:PC-MEDIA", ObjectID: "obj-7", ParentID: "c-1", Title: "Intro"})
	if changed, _ := s.reresolveLibraryKey(context.Background(), 6, &key); changed {
		t.Fatal("an ambiguous title must not move the key")
	}
	if key.StreamURL != f.oldURL("obj-7") {
		t.Fatalf("key touched: %q", key.StreamURL)
	}
}

// A key saved before this change carries no ref: it is never probed and never
// resolved, exactly the old behaviour.
func TestLibraryKeyWithoutRefKeepsTheOldBehaviour(t *testing.T) {
	f := newFakeRenumberingServer(t, true, [2]string{"obj-7", "100% organic"})
	var resolves []string
	s := reresolveServer(t, f, &resolves)
	s.libraryURLServes = func(context.Context, string) bool {
		t.Error("probed a key without a ref")
		return true
	}
	key := renumberedTrackKey(f, nil)
	s.healLibraryKeyIfMoved(context.Background(), 6, &key)
	if key.StreamURL != f.oldURL("obj-7") || len(resolves) != 0 {
		t.Fatalf("old key changed: %q, resolves %v", key.StreamURL, resolves)
	}
}

// A folder key whose server renumbered: the folder is read once and every
// track is matched up, by object id where stored, else by title, the n-th of
// a title to the n-th.
func TestFolderKeyFindsItsTracksAgainAfterTheServerRenumbered(t *testing.T) {
	f := newFakeRenumberingServer(t, false,
		[2]string{"a", "Intro"}, [2]string{"b", "Song"}, [2]string{"c", "Intro"}, [2]string{"d", "Outro"})
	var resolves []string
	s := reresolveServer(t, f, &resolves)
	key := presets.Preset{Slot: 2, Type: "queue", Name: "Album", Source: "PC: Media",
		Folder: &presets.FolderRef{ServerUDN: "uuid:PC-MEDIA", ContainerID: "c-1"},
		Items: []presets.PresetItem{
			{URL: f.oldURL("x1"), Title: "Intro"},
			{URL: f.oldURL("x2"), Title: "Song", ObjectID: "b"},
			{URL: f.oldURL("x3"), Title: "Intro"},
			{URL: f.oldURL("x4"), Title: "Gone"},
		}}
	if err := s.presets.SetSlot(key); err != nil {
		t.Fatal(err)
	}
	pressed, _ := s.presets.Get(2)
	s.healQueuePresetIfMoved(context.Background(), 2, &pressed)
	want := []string{f.newURL("a"), f.newURL("b"), f.newURL("c"), f.oldURL("x4")}
	for i, w := range want {
		if pressed.Items[i].URL != w {
			t.Errorf("track %d = %q, want %q", i, pressed.Items[i].URL, w)
		}
	}
	got, _ := s.presets.Get(2)
	if got.Items[0].URL != f.newURL("a") || got.Items[0].ObjectID != "a" {
		t.Fatalf("corrected folder not persisted: %+v", got.Items[0])
	}
	browses := 0
	for _, c := range f.log() {
		if strings.HasPrefix(c, "BrowseDirectChildren") {
			browses++
		}
	}
	if browses != 1 {
		t.Fatalf("folder read %d times, want once: %v", browses, f.log())
	}
}

// A folder key without a FolderRef (saved before the field) is left alone.
func TestFolderKeyWithoutRefIsNotProbed(t *testing.T) {
	f := newFakeRenumberingServer(t, true)
	var resolves []string
	s := reresolveServer(t, f, &resolves)
	s.libraryURLServes = func(context.Context, string) bool {
		t.Error("probed a folder key without a ref")
		return true
	}
	key := presets.Preset{Slot: 2, Type: "queue", Source: "PC: Media",
		Items: []presets.PresetItem{{URL: f.oldURL("x1"), Title: "Intro"}}}
	if changed, _ := s.reresolveQueuePreset(context.Background(), 2, &key); changed {
		t.Fatal("changed a folder key without a ref")
	}
}

// A key saved from a listing this agent served (the phone page) gets the
// object id the listing carried, without the phone sending it.
func TestSavedLibraryKeyLearnsItsObjectFromTheListing(t *testing.T) {
	s := libHealServer(t)
	url := "http://192.0.2.5:10243/WMPNSSv4/111/1_abc.mp3"
	s.noteLibraryItems("uuid:PC-MEDIA", []dlna.Item{{ID: "obj-7", ParentID: "c-1", Title: "Song", StreamURL: url}})

	p := presets.Preset{Type: "radio", Name: "Song", Source: "PC: Media", StreamURL: url}
	s.fillLibraryRefs(&p)
	if p.Track == nil || p.Track.ObjectID != "obj-7" || p.Track.ServerUDN != "uuid:PC-MEDIA" || p.Track.ParentID != "c-1" {
		t.Fatalf("track ref not filled: %+v", p.Track)
	}
	// What the saver sent wins.
	own := presets.Preset{Type: "radio", Name: "Song", Source: "PC: Media", StreamURL: url,
		Track: &presets.TrackRef{ServerUDN: "uuid:OTHER", ObjectID: "o"}}
	s.fillLibraryRefs(&own)
	if own.Track.ObjectID != "o" {
		t.Fatalf("sender's ref overwritten: %+v", own.Track)
	}
	// A radio station is no library key.
	radio := presets.Preset{Type: "radio", Name: "Station", StreamURL: url}
	s.fillLibraryRefs(&radio)
	if radio.Track != nil {
		t.Fatal("a station without a server got a track ref")
	}
	// Folder tracks get their ids.
	q := presets.Preset{Type: "queue", Items: []presets.PresetItem{{URL: url}, {URL: "http://192.0.2.5/other.mp3"}}}
	s.fillLibraryRefs(&q)
	if q.Items[0].ObjectID != "obj-7" || q.Items[1].ObjectID != "" {
		t.Fatalf("folder ids: %+v", q.Items)
	}
}

func TestLibraryURLStatusVerdicts(t *testing.T) {
	for code, want := range map[int]bool{200: true, 206: true, 302: true, 405: true, 416: true,
		404: false, 410: false, 500: false, 503: false} {
		if got := libraryURLStatusServes(code); got != want {
			t.Errorf("status %d: serves=%v, want %v", code, got, want)
		}
	}
}
