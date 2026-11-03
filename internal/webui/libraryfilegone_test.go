package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
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
