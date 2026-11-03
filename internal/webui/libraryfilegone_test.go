package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/JRpersonal/streborn/dlna"
	"github.com/JRpersonal/streborn/internal/mediaservers"
	"github.com/JRpersonal/streborn/internal/presets"
)

// A library key whose music server answers but no longer has the file (#919,
// #733, #726): a Windows Media Player library that was lost and re-indexed, a
// MiniDLNA that replaces its files every night, a FRITZ!Box media server
// pointed at another USB stick. The agent log said "the server no longer knows
// the track by its id" and "could not find the track again", the speaker then
// answered AUDIO_ERROR_BAD_URL, and the user was told nothing. The server-off
// refusal (#1339) did not apply: the server was on.

// The server answers, does not know the id, and the folder the track was
// saved from no longer lists its title: the file is gone, and the verdict
// names the server the key recorded.
func TestLibraryKeyWhoseServerNoLongerHasTheFileIsCalledGone(t *testing.T) {
	f := newFakeRenumberingServer(t, false, [2]string{"new-1", "Another song"})
	var resolves []string
	s := reresolveServer(t, f, &resolves)
	key := renumberedTrackKey(f, &presets.TrackRef{ServerUDN: "uuid:PC-MEDIA", ObjectID: "obj-7", ParentID: "c-1", Title: "100% organic"})
	v := s.healLibraryKeyIfMoved(context.Background(), 6, &key)
	if v == nil || !v.FileGone || v.Server != "PC: Media" {
		t.Fatalf("verdict = %+v, want PC: Media with the file gone", v)
	}
	if key.StreamURL != f.oldURL("obj-7") {
		t.Fatalf("a gone file moved the key: %q", key.StreamURL)
	}
}

// A key that stored only the object id: the server's 701 is the answer.
func TestLibraryKeyWithOnlyAnObjectIDIsCalledGoneOnTheServersNo(t *testing.T) {
	f := newFakeRenumberingServer(t, false)
	var resolves []string
	s := reresolveServer(t, f, &resolves)
	key := renumberedTrackKey(f, &presets.TrackRef{ServerUDN: "uuid:PC-MEDIA", ObjectID: "obj-7"})
	if v := s.healLibraryKeyIfMoved(context.Background(), 6, &key); v == nil || !v.FileGone {
		t.Fatalf("verdict = %+v, want the file gone", v)
	}
}

// The verdict falls back to the name the server announces when the key
// recorded none.
func TestLibraryFileGoneNamesTheServerItAnnounces(t *testing.T) {
	v := libraryKeyFileGone("  ", dlna.Server{FriendlyName: "FRITZ!Box Media"})
	if v.Server != "FRITZ!Box Media" || !v.FileGone {
		t.Fatalf("verdict = %+v", v)
	}
	if v := libraryKeyFileGone("MinimServer", dlna.Server{FriendlyName: "other"}); v.Server != "MinimServer" {
		t.Fatalf("the key's own name lost: %+v", v)
	}
}

// Not gone, only uncertain: a title listed twice. The press goes ahead.
func TestLibraryKeyWithAnAmbiguousTitleIsNotCalledGone(t *testing.T) {
	f := newFakeRenumberingServer(t, false, [2]string{"new-1", "Intro"}, [2]string{"new-2", "Intro"})
	var resolves []string
	s := reresolveServer(t, f, &resolves)
	key := renumberedTrackKey(f, &presets.TrackRef{ServerUDN: "uuid:PC-MEDIA", ObjectID: "obj-7", ParentID: "c-1", Title: "Intro"})
	if v := s.healLibraryKeyIfMoved(context.Background(), 6, &key); v != nil {
		t.Fatalf("an ambiguous title was called %+v", v)
	}
}

// Not gone either: a server whose ContentDirectory stopped taking connections
// mid-press says nothing about what it holds.
func TestLibraryKeyIsNotCalledGoneWhenTheServerStopsAnswering(t *testing.T) {
	f := newFakeRenumberingServer(t, false)
	var resolves []string
	s := reresolveServer(t, f, &resolves)
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL + "/cds"
	dead.Close()
	s.libraryServerResolve = func(context.Context, string) (dlna.Server, bool) {
		return dlna.Server{UDN: "uuid:PC-MEDIA", FriendlyName: "PC: Media", CDSControlURL: deadURL}, true
	}
	key := renumberedTrackKey(f, &presets.TrackRef{ServerUDN: "uuid:PC-MEDIA", ObjectID: "obj-7", ParentID: "c-1", Title: "100% organic"})
	if v := s.healLibraryKeyIfMoved(context.Background(), 6, &key); v != nil {
		t.Fatalf("a server that stopped answering was called %+v", v)
	}
}

// The track found again is still the heal it always was, not a verdict.
func TestLibraryKeyFoundAgainIsNoVerdict(t *testing.T) {
	f := newFakeRenumberingServer(t, true, [2]string{"obj-7", "100% organic"})
	var resolves []string
	s := reresolveServer(t, f, &resolves)
	key := renumberedTrackKey(f, &presets.TrackRef{ServerUDN: "uuid:PC-MEDIA", ObjectID: "obj-7"})
	if v := s.healLibraryKeyIfMoved(context.Background(), 6, &key); v != nil {
		t.Fatalf("a healed key was called %+v", v)
	}
	if key.StreamURL != f.newURL("obj-7") {
		t.Fatalf("not healed: %q", key.StreamURL)
	}
}

func goneFolderKey(f *fakeRenumberingServer, container string) presets.Preset {
	return presets.Preset{Slot: 2, Type: "queue", Name: "Album", Source: "PC: Media",
		Folder: &presets.FolderRef{ServerUDN: "uuid:PC-MEDIA", ContainerID: container},
		Items: []presets.PresetItem{
			{URL: f.oldURL("x1"), Title: "Intro"},
			{URL: f.oldURL("x2"), Title: "Song"},
		}}
}

// A folder key: the folder is gone (the server lists nothing under its id),
// or it no longer holds any of the key's tracks.
func TestFolderKeyWhoseServerNoLongerHasTheMusicIsCalledGone(t *testing.T) {
	for _, c := range []struct {
		name, container string
		items           [][2]string
	}{
		{"folder gone", "c-9", [][2]string{{"a", "Intro"}}},
		{"none of its tracks", "c-1", [][2]string{{"a", "Other"}, {"b", "Else"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeRenumberingServer(t, false, c.items...)
			var resolves []string
			s := reresolveServer(t, f, &resolves)
			key := goneFolderKey(f, c.container)
			v := s.healQueuePresetIfMoved(context.Background(), 2, &key)
			if v == nil || !v.FileGone || v.Server != "PC: Media" {
				t.Fatalf("verdict = %+v, want PC: Media with the music gone", v)
			}
		})
	}
}

// A folder that still lists a track where the key already points is not gone,
// whatever the probe of the first track said.
func TestQueueItemsListedSeesATrackStillWhereTheKeyPoints(t *testing.T) {
	items := []presets.PresetItem{{URL: "http://192.0.2.5/a.mp3"}, {URL: "http://192.0.2.5/b.mp3"}}
	if !queueItemsListed(items, []dlna.Item{{StreamURL: "http://192.0.2.5/b.mp3"}}) {
		t.Fatal("a listed track was not seen")
	}
	if queueItemsListed(items, []dlna.Item{{StreamURL: "http://192.0.2.5/c.mp3"}, {}}) {
		t.Fatal("an unlisted track was seen")
	}
}

// goneRecallServer is a recall test server whose slot 3 holds a single track
// of the fake server, registered, with the server answering.
func goneRecallServer(t *testing.T, f *fakeRenumberingServer, key presets.Preset) (*Server, *soapRecorder) {
	t.Helper()
	s, rec := newPlayTestServer(t)
	store, err := mediaservers.Load(filepath.Join(t.TempDir(), "mediaservers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(mediaservers.Server{ID: "uuid:PC-MEDIA", Name: "PC: Media",
		Location: f.ts.URL + "/description.xml"}); err != nil {
		t.Fatal(err)
	}
	s.mediaServers = store
	s.queueHealResolve = func(context.Context, string) (string, bool) { return "", false }
	s.queueHostAnswers = func(context.Context, string) bool { return true }
	s.libraryServerResolve = func(context.Context, string) (dlna.Server, bool) { return f.srv(), true }
	key.Slot = 3
	if err := s.presets.SetSlot(key); err != nil {
		t.Fatal(err)
	}
	return s, rec
}

// The press itself: POST /api/play/<slot> answers with the file-gone code and
// the server's name, never "offline", and the speaker is handed nothing.
func TestPlaySlotRefusesALibraryKeyWhoseFileIsGone(t *testing.T) {
	for _, c := range []struct {
		name string
		key  func(f *fakeRenumberingServer) presets.Preset
	}{
		{"single track", func(f *fakeRenumberingServer) presets.Preset {
			return renumberedTrackKey(f, &presets.TrackRef{ServerUDN: "uuid:PC-MEDIA", ObjectID: "obj-7", ParentID: "c-1", Title: "100% organic"})
		}},
		{"folder", func(f *fakeRenumberingServer) presets.Preset { return goneFolderKey(f, "c-9") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeRenumberingServer(t, false, [2]string{"new-1", "Another song"})
			s, rec := goneRecallServer(t, f, c.key(f))
			w := httptest.NewRecorder()
			s.handlePlaySlot(w, httptest.NewRequest(http.MethodPost, "/api/play/3", nil))
			if w.Code != http.StatusBadGateway {
				t.Fatalf("status %d, want 502: %s", w.Code, w.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not JSON: %v %s", err, w.Body.String())
			}
			if body["code"] != libraryFileGoneCode || body["server"] != "PC: Media" || body["gone"] != true {
				t.Fatalf("body = %v, want the file-gone code, the server and gone:true", body)
			}
			if _, has := body["offline"]; has {
				t.Fatalf("a server that answers was called offline: %v", body)
			}
			if !strings.HasPrefix(body["error"].(string), "The music server PC: Media answers, but no longer has the music on this key.") {
				t.Fatalf("error = %q", body["error"])
			}
			if rec.has("SetAVTransportURI") || rec.has("Play") {
				t.Fatalf("the speaker was handed the missing file: %v", rec.list())
			}
		})
	}
}

func TestLibraryFileGoneRefusalWithoutAName(t *testing.T) {
	s := libHealServer(t)
	ref := s.libraryKeyRefusal(1, "Song", &libraryKeyUnplayable{FileGone: true})
	if ref.Code != libraryFileGoneCode || !strings.HasPrefix(ref.Error, "The music server answers, but") {
		t.Fatalf("refusal = %+v", ref)
	}
	if strings.ContainsAny(ref.Error, "–—") || strings.Contains(ref.Error, " - ") {
		t.Fatalf("dash in the sentence: %q", ref.Error)
	}
	// The server-off verdict still gets its own refusal.
	if off := s.libraryKeyRefusal(1, "Song", &libraryKeyUnplayable{Server: "X"}); off.Code != libraryServerOfflineCode {
		t.Fatalf("server off answered %+v", off)
	}
}

// A hardware press has no screen: it goes ahead, and the log says why.
func TestHardwarePressLogsAGoneFile(t *testing.T) {
	var buf bytes.Buffer
	s := &Server{logger: slog.New(slog.NewTextHandler(&buf, nil))}
	s.logHardwareLibraryVerdict(4, "Song", nil)
	if buf.Len() != 0 {
		t.Fatalf("a key that plays logged %q", buf.String())
	}
	s.logHardwareLibraryVerdict(4, "Song", &libraryKeyUnplayable{Server: "PC: Media", FileGone: true})
	if !strings.Contains(buf.String(), "answers but no longer has its music") || !strings.Contains(buf.String(), "PC: Media") {
		t.Fatalf("log = %q", buf.String())
	}
}

// The phone remote turns the refusal into a notice that names the server, in
// every locale it ships.
func TestPhoneRemoteExplainsAMusicFileThatIsGone(t *testing.T) {
	for _, want := range []string{
		"function libFileGoneNotice(",
		"code.indexOf('library-file-gone') >= 0) return libFileGoneNotice(detail)",
		"fmt(T.libFileGone, { name: name })",
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("the phone page lost %q", want)
		}
	}
	bundles := strings.Count(indexHTML, "now:\"")
	for _, key := range []string{"libFileGone", "libFileGoneAny"} {
		if got := len(regexp.MustCompile(key+`:"`).FindAllString(indexHTML, -1)); got != bundles {
			t.Errorf("%s: %d locale bundles but %d keys", key, bundles, got)
		}
	}
	if got := len(regexp.MustCompile(`libFileGone:"[^"]*\{name\}`).FindAllString(indexHTML, -1)); got != bundles {
		t.Errorf("libFileGone names the server in %d of %d locales", got, bundles)
	}
	if got := len(regexp.MustCompile(`libFileGoneAny:"[^"{]*"`).FindAllString(indexHTML, -1)); got != bundles {
		t.Errorf("libFileGoneAny has a placeholder in some of %d locales", bundles)
	}
}

// scriptedCDS is a ContentDirectory whose every answer the test writes: answer
// gets the Browse flag, object id and starting index and returns the status
// and body to send, or hangup to drop the connection without a word (a server
// restarting mid-press).
type scriptedCDS struct {
	ts     *httptest.Server
	answer func(flag, id string, start int) (status int, body string, hangup bool)
}

func newScriptedCDS(t *testing.T, answer func(flag, id string, start int) (int, string, bool)) *scriptedCDS {
	t.Helper()
	c := &scriptedCDS{answer: answer}
	c.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		start, _ := strconv.Atoi(soapValue(string(b), "StartingIndex"))
		status, body, hangup := c.answer(soapValue(string(b), "BrowseFlag"), soapValue(string(b), "ObjectID"), start)
		if hangup {
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
			panic(http.ErrAbortHandler)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(c.ts.Close)
	return c
}

func (c *scriptedCDS) srv() dlna.Server {
	return dlna.Server{UDN: "uuid:PC-MEDIA", FriendlyName: "PC: Media", CDSControlURL: c.ts.URL + "/cds"}
}

// browseOK is a 200 Browse answer listing didl, total being what the server
// claims the folder holds in all.
func browseOK(didl string, returned, total int) (int, string, bool) {
	result := `<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/">` +
		didl + `</DIDL-Lite>`
	return http.StatusOK, fmt.Sprintf(`<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>`+
		`<u:BrowseResponse xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1"><Result>%s</Result>`+
		`<NumberReturned>%d</NumberReturned><TotalMatches>%d</TotalMatches></u:BrowseResponse></s:Body></s:Envelope>`,
		html.EscapeString(result), returned, total), false
}

func upnpFaultAnswer(code string) (int, string, bool) {
	return http.StatusInternalServerError, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault>` +
		`<faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring>` +
		`<detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>` + code + `</errorCode></UPnPError></detail>` +
		`</s:Fault></s:Body></s:Envelope>`, false
}

func scriptedTrack(id, title string) string {
	return fmt.Sprintf(`<item id="%s" parentID="c-1"><dc:title>%s</dc:title>`+
		`<upnp:class>object.item.audioItem.musicTrack</upnp:class>`+
		`<res protocolInfo="http-get:*:audio/mpeg:*">http://192.0.2.5/new/%s.mp3</res></item>`, id, title, id)
}

func scriptedFolder(id, title string) string {
	return fmt.Sprintf(`<container id="%s" parentID="artist"><dc:title>%s</dc:title>`+
		`<upnp:class>object.container.album.musicAlbum</upnp:class></container>`, id, title)
}

// scriptedServer is a library-heal server whose keys' files never serve and
// whose music server is the scripted one.
func scriptedServer(t *testing.T, c *scriptedCDS) *Server {
	t.Helper()
	s := libHealServer(t, mediaservers.Server{ID: "uuid:PC-MEDIA", Name: "PC: Media", Location: c.ts.URL + "/description.xml"})
	s.libraryURLServes = func(context.Context, string) bool { return false }
	s.queueHostAnswers = func(context.Context, string) bool { return true }
	s.libraryServerResolve = func(context.Context, string) (dlna.Server, bool) { return c.srv(), true }
	return s
}

func scriptedTrackKey(ref *presets.TrackRef) presets.Preset {
	return presets.Preset{Slot: 6, Type: "radio", Name: "Song", Source: "PC: Media",
		StreamURL: "http://192.0.2.5/old/obj-7.mp3", Track: ref}
}

func scriptedFolderKey(container string) presets.Preset {
	return presets.Preset{Slot: 2, Type: "queue", Name: "Artist", Source: "PC: Media",
		Folder: &presets.FolderRef{ServerUDN: "uuid:PC-MEDIA", ContainerID: container},
		Items: []presets.PresetItem{
			{URL: "http://192.0.2.5/old/x1.mp3", Title: "Intro"},
			{URL: "http://192.0.2.5/old/x2.mp3", Title: "Song"},
		}}
}

// A big flat folder (an "All music" container) whose second page fails: the
// title may sit on that page, so the track is not called gone.
func TestLibraryKeyIsNotCalledGoneWhenALaterPageOfItsFolderFails(t *testing.T) {
	for _, c := range []struct {
		name string
		fail func() (int, string, bool)
	}{
		{"connection dropped", func() (int, string, bool) { return 0, "", true }},
		{"HTTP 500", func() (int, string, bool) { return http.StatusInternalServerError, "busy", false }},
	} {
		t.Run(c.name, func(t *testing.T) {
			cds := newScriptedCDS(t, func(flag, id string, start int) (int, string, bool) {
				switch {
				case flag == "BrowseMetadata":
					return upnpFaultAnswer("701")
				case id == "c-1" && start == 0:
					return browseOK(scriptedTrack("a", "Other")+scriptedTrack("b", "Else"), 2, 400)
				default:
					return c.fail()
				}
			})
			s := scriptedServer(t, cds)
			key := scriptedTrackKey(&presets.TrackRef{ServerUDN: "uuid:PC-MEDIA", ObjectID: "obj-7", ParentID: "c-1", Title: "Song"})
			if _, dead := s.reresolveLibraryKey(context.Background(), 6, &key); dead != nil {
				t.Fatalf("a folder read in part was taken as the server's answer: %+v", dead)
			}
		})
	}
}

// A server that refuses the speaker, is busy or fails the action has said
// nothing about its library: no verdict, the press goes ahead. Only its "no
// such object" counts.
func TestLibraryKeyIsCalledGoneOnlyOnTheServersNoSuchObject(t *testing.T) {
	for _, c := range []struct {
		name   string
		answer func() (int, string, bool)
		gone   bool
	}{
		{"701 no such object", func() (int, string, bool) { return upnpFaultAnswer("701") }, true},
		{"501 action failed", func() (int, string, bool) { return upnpFaultAnswer("501") }, false},
		{"401 invalid action", func() (int, string, bool) { return upnpFaultAnswer("401") }, false},
		{"HTTP 503 rescanning", func() (int, string, bool) { return http.StatusServiceUnavailable, "rescanning", false }, false},
		{"HTTP 403 access list", func() (int, string, bool) { return http.StatusForbidden, "<html>forbidden</html>", false }, false},
		{"garbled answer", func() (int, string, bool) { return http.StatusOK, "<s:Envelope", false }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			cds := newScriptedCDS(t, func(string, string, int) (int, string, bool) { return c.answer() })
			s := scriptedServer(t, cds)
			key := scriptedTrackKey(&presets.TrackRef{ServerUDN: "uuid:PC-MEDIA", ObjectID: "obj-7"})
			_, dead := s.reresolveLibraryKey(context.Background(), 6, &key)
			if got := dead != nil && dead.FileGone; got != c.gone {
				t.Fatalf("verdict = %+v, want gone %v", dead, c.gone)
			}
		})
	}
}

// The same answers on the track's folder: a 503 or 403 there is no verdict
// either, a 710 "no such container" is.
func TestLibraryKeyFolderAnswerCountsOnlyAsNoSuchContainer(t *testing.T) {
	for _, c := range []struct {
		name   string
		answer func() (int, string, bool)
		gone   bool
	}{
		{"710 no such container", func() (int, string, bool) { return upnpFaultAnswer("710") }, true},
		{"HTTP 503", func() (int, string, bool) { return http.StatusServiceUnavailable, "busy", false }, false},
		{"HTTP 403", func() (int, string, bool) { return http.StatusForbidden, "no", false }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			cds := newScriptedCDS(t, func(flag, _ string, _ int) (int, string, bool) {
				if flag == "BrowseMetadata" {
					return upnpFaultAnswer("701")
				}
				return c.answer()
			})
			s := scriptedServer(t, cds)
			key := scriptedTrackKey(&presets.TrackRef{ServerUDN: "uuid:PC-MEDIA", ObjectID: "obj-7", ParentID: "c-1", Title: "Song"})
			_, dead := s.reresolveLibraryKey(context.Background(), 6, &key)
			if got := dead != nil && dead.FileGone; got != c.gone {
				t.Fatalf("verdict = %+v, want gone %v", dead, c.gone)
			}
		})
	}
}

// A folder of folders (#1682) whose album folders cannot be read: the walk
// found no song, but because it looked nowhere, not because there is none.
func TestFolderKeyIsNotCalledGoneWhenItsSubFoldersCannotBeRead(t *testing.T) {
	for _, c := range []struct {
		name string
		fail func() (int, string, bool)
	}{
		{"connection dropped", func() (int, string, bool) { return 0, "", true }},
		{"HTTP 500", func() (int, string, bool) { return http.StatusInternalServerError, "busy", false }},
	} {
		t.Run(c.name, func(t *testing.T) {
			cds := newScriptedCDS(t, func(flag, id string, _ int) (int, string, bool) {
				if flag == "BrowseDirectChildren" && id == "artist" {
					return browseOK(scriptedFolder("al-1", "First")+scriptedFolder("al-2", "Second"), 2, 2)
				}
				return c.fail()
			})
			s := scriptedServer(t, cds)
			key := scriptedFolderKey("artist")
			if _, dead := s.reresolveQueuePreset(context.Background(), 2, &key); dead != nil {
				t.Fatalf("a walk whose folders all failed was called %+v", dead)
			}
		})
	}
}

// One album of the artist read, the other failed: the key's songs may be in
// the one that failed, so the songs that were read not matching is no verdict.
func TestFolderKeyIsNotCalledGoneWhenOneSubFolderFails(t *testing.T) {
	cds := newScriptedCDS(t, func(flag, id string, _ int) (int, string, bool) {
		switch {
		case flag != "BrowseDirectChildren":
			return http.StatusInternalServerError, "", false
		case id == "artist":
			return browseOK(scriptedFolder("al-1", "First")+scriptedFolder("al-2", "Second"), 2, 2)
		case id == "al-1":
			return browseOK(scriptedTrack("o1", "Other"), 1, 1)
		default:
			return 0, "", true
		}
	})
	s := scriptedServer(t, cds)
	key := scriptedFolderKey("artist")
	if _, dead := s.reresolveQueuePreset(context.Background(), 2, &key); dead != nil {
		t.Fatalf("a walk with a skipped album was called %+v", dead)
	}
}

// A flat folder key whose second page fails: what was read holds none of the
// key's songs, but the rest of the folder was never seen.
func TestFolderKeyIsNotCalledGoneWhenALaterPageFails(t *testing.T) {
	cds := newScriptedCDS(t, func(flag, id string, start int) (int, string, bool) {
		if flag == "BrowseDirectChildren" && id == "c-1" && start == 0 {
			return browseOK(scriptedTrack("o1", "Other"), 1, 400)
		}
		return 0, "", true
	})
	s := scriptedServer(t, cds)
	key := scriptedFolderKey("c-1")
	if _, dead := s.reresolveQueuePreset(context.Background(), 2, &key); dead != nil {
		t.Fatalf("a folder read in part was called %+v", dead)
	}
}

// The whole tree read, every album answered, none holds the key's songs: that
// is the server's answer, and the key is called gone.
func TestFolderKeyIsCalledGoneWhenTheWholeTreeWasRead(t *testing.T) {
	cds := newScriptedCDS(t, func(flag, id string, _ int) (int, string, bool) {
		switch {
		case flag != "BrowseDirectChildren":
			return http.StatusInternalServerError, "", false
		case id == "artist":
			return browseOK(scriptedFolder("al-1", "First")+scriptedFolder("al-2", "Second"), 2, 2)
		case id == "al-1":
			return browseOK(scriptedTrack("o1", "Other"), 1, 1)
		default:
			return browseOK(scriptedTrack("o2", "Else"), 1, 1)
		}
	})
	s := scriptedServer(t, cds)
	key := scriptedFolderKey("artist")
	if _, dead := s.reresolveQueuePreset(context.Background(), 2, &key); dead == nil || !dead.FileGone {
		t.Fatalf("verdict = %+v, want the music gone", dead)
	}
}

// The tree bookkeeping the verdict rests on.
func TestFolderTreeCompleteOnlyWithoutGaps(t *testing.T) {
	if !(folderTree{}).complete() {
		t.Fatal("a clean walk is not complete")
	}
	for _, tr := range []folderTree{{cut: true}, {cutDepth: true}, {partial: true}} {
		if tr.complete() {
			t.Fatalf("%+v counted as complete", tr)
		}
	}
}
