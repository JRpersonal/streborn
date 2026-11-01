package webui

import (
	"context"
	"encoding/json"
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

// #1339: a library key pressed in the app or on the phone while its music
// server is switched off. The agent's log knew ("registered server absent",
// "connection refused"), the press still handed the speaker the dead file, and
// the user saw nothing. The old Bose app said "server unavailable".

const (
	offTrackURL  = "http://192.0.2.101:9790/minimserver/*/Music/Song.flac"
	offTrackURL2 = "http://192.0.2.101:9790/minimserver/*/Music/Other.flac"
)

func offRegs() mediaservers.Server {
	return mediaservers.Server{ID: "uuid:AAAA-BBBB", Name: "MinimServer",
		Location: "http://192.0.2.101:9791/AAAA-BBBB/Upnp/device.xml"}
}

func offTrackKey(slot int, ref *presets.TrackRef) presets.Preset {
	return presets.Preset{Slot: slot, Type: "radio", Name: "Song", Source: "MinimServer",
		StreamURL: offTrackURL, Track: ref}
}

func offFolderKey(slot int, source string) presets.Preset {
	return presets.Preset{Slot: slot, Type: "queue", Name: "Album", Source: source,
		Folder: &presets.FolderRef{ServerUDN: "uuid:AAAA-BBBB", ContainerID: "c-1"},
		Items: []presets.PresetItem{
			{URL: offTrackURL, Title: "Song"},
			{URL: offTrackURL2, Title: "Other"},
		}}
}

// offProbes wires the three network questions a press can ask: does the file
// serve, does anything answer at its address, and can the server be found.
// Each is counted.
type offProbes struct {
	serves, hostAnswers, found bool
	fileProbes, hostProbes     int
	resolves                   int
}

func (o *offProbes) wire(s *Server) {
	s.libraryURLServes = func(context.Context, string) bool { o.fileProbes++; return o.serves }
	s.queueHostAnswers = func(context.Context, string) bool { o.hostProbes++; return o.hostAnswers }
	s.libraryServerResolve = func(context.Context, string) (dlna.Server, bool) {
		o.resolves++
		return dlna.Server{}, o.found
	}
}

func TestLibraryKeyNamesItsServerWhenTheServerIsOff(t *testing.T) {
	s := libHealServer(t, offRegs())
	o := &offProbes{}
	o.wire(s)
	key := offTrackKey(4, &presets.TrackRef{ServerUDN: "uuid:AAAA-BBBB", ObjectID: "obj-1"})
	off := s.healLibraryKeyIfMoved(context.Background(), 4, &key)
	if off == nil || off.Server != "MinimServer" {
		t.Fatalf("verdict = %+v, want MinimServer off", off)
	}
	if key.StreamURL != offTrackURL {
		t.Fatalf("an off server moved the key: %q", key.StreamURL)
	}
}

// A server that answers at the file's address but cannot be placed by
// discovery is not off: saying "not answering" would be wrong, so the press
// goes ahead the way it always did.
func TestLibraryKeyWhoseHostAnswersIsNotCalledOff(t *testing.T) {
	s := libHealServer(t, offRegs())
	o := &offProbes{hostAnswers: true}
	o.wire(s)
	key := offTrackKey(4, &presets.TrackRef{ServerUDN: "uuid:AAAA-BBBB", ObjectID: "obj-1"})
	if off := s.healLibraryKeyIfMoved(context.Background(), 4, &key); off != nil {
		t.Fatalf("a host that answers was called off: %+v", off)
	}
}

// A key whose file serves pays nothing beyond what it paid before: the host
// heal's one connect and the one ranged GET. No resolve, no second connect.
func TestLibraryKeyThatServesCostsNothingExtra(t *testing.T) {
	s := libHealServer(t, offRegs())
	o := &offProbes{serves: true, hostAnswers: true}
	o.wire(s)
	key := offTrackKey(4, &presets.TrackRef{ServerUDN: "uuid:AAAA-BBBB", ObjectID: "obj-1"})
	if off := s.healLibraryKeyIfMoved(context.Background(), 4, &key); off != nil {
		t.Fatalf("a serving key was called off: %+v", off)
	}
	if o.fileProbes != 1 || o.hostProbes != 1 || o.resolves != 0 {
		t.Fatalf("probes file=%d host=%d resolves=%d, want 1/1/0", o.fileProbes, o.hostProbes, o.resolves)
	}
}

// A key saved before it stored its server's identity keeps its old behaviour:
// no probe, no verdict.
func TestLibraryKeyWithoutServerIdentityIsNeverCalledOff(t *testing.T) {
	s := libHealServer(t, offRegs())
	o := &offProbes{}
	o.wire(s)
	key := offTrackKey(4, nil)
	if off := s.healLibraryKeyIfMoved(context.Background(), 4, &key); off != nil {
		t.Fatalf("a key without a ref was called off: %+v", off)
	}
	if o.fileProbes != 0 || o.resolves != 0 {
		t.Fatalf("a key without a ref was probed: file=%d resolves=%d", o.fileProbes, o.resolves)
	}
	folder := offFolderKey(2, "MinimServer")
	folder.Folder = nil
	if off := s.healQueuePresetIfMoved(context.Background(), 2, &folder); off != nil {
		t.Fatalf("a folder key without a ref was called off: %+v", off)
	}
}

// A folder key names its server too, by the registered name when the key
// itself names none.
func TestFolderKeyNamesItsServerWhenTheServerIsOff(t *testing.T) {
	s := libHealServer(t, offRegs())
	o := &offProbes{}
	o.wire(s)
	key := offFolderKey(2, "")
	off := s.healQueuePresetIfMoved(context.Background(), 2, &key)
	if off == nil || off.Server != "MinimServer" {
		t.Fatalf("verdict = %+v, want MinimServer off by its registered name", off)
	}
}

// offPlayServer is a recall test server whose library keys belong to a
// registered MinimServer, with the probes wired by o.
func offPlayServer(t *testing.T, o *offProbes) (*Server, *soapRecorder) {
	t.Helper()
	s, rec := newPlayTestServer(t)
	store, err := mediaservers.Load(filepath.Join(t.TempDir(), "mediaservers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(offRegs()); err != nil {
		t.Fatal(err)
	}
	s.mediaServers = store
	s.queueHealResolve = func(context.Context, string) (string, bool) { return "", false }
	o.wire(s)
	return s, rec
}

// The press itself: POST /api/play/<slot> answers with the server's name and
// the speaker is handed nothing, for a single track and for a folder.
func TestPlaySlotRefusesALibraryKeyWhoseServerIsOff(t *testing.T) {
	for _, c := range []struct {
		name string
		key  presets.Preset
	}{
		{"single track", offTrackKey(3, &presets.TrackRef{ServerUDN: "uuid:AAAA-BBBB", ObjectID: "obj-1"})},
		{"folder", offFolderKey(3, "MinimServer")},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, rec := offPlayServer(t, &offProbes{})
			if err := s.presets.SetSlot(c.key); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			s.handlePlaySlot(w, httptest.NewRequest(http.MethodPost, "/api/play/3", nil))
			if w.Code != http.StatusBadGateway {
				t.Fatalf("status %d, want 502: %s", w.Code, w.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not JSON: %v %s", err, w.Body.String())
			}
			if body["code"] != libraryServerOfflineCode || body["server"] != "MinimServer" || body["offline"] != true {
				t.Fatalf("body = %v, want the offline code, the server and offline:true", body)
			}
			if body["error"] != "The music server MinimServer is not answering. Switch it on and try again." {
				t.Fatalf("error = %q", body["error"])
			}
			if rec.has("SetAVTransportURI") || rec.has("Play") {
				t.Fatalf("the speaker was handed the dead file: %v", rec.list())
			}
		})
	}
}

// The same key with its server switched on plays as before.
func TestPlaySlotStillPlaysALibraryKeyThatServes(t *testing.T) {
	o := &offProbes{serves: true, hostAnswers: true}
	s, rec := offPlayServer(t, o)
	if err := s.presets.SetSlot(offTrackKey(3, &presets.TrackRef{ServerUDN: "uuid:AAAA-BBBB", ObjectID: "obj-1"})); err != nil {
		t.Fatal(err)
	}
	if _, ref := s.playSlot(context.Background(), 3); ref != nil {
		t.Fatalf("a serving key was refused: %+v", ref)
	}
	if !rec.has("Play") {
		t.Fatalf("the serving key never reached the speaker: %v", rec.list())
	}
	if o.resolves != 0 {
		t.Fatalf("a serving key resolved its server %d times", o.resolves)
	}
}

// The phone remote turns the refusal into a notice that names the server, in
// every locale it ships, and says the same for a folder replay that found the
// server off (that used to read "The speaker is not reachable right now").
func TestPhoneRemoteNamesAMusicServerThatIsOff(t *testing.T) {
	for _, want := range []string{
		"function libServerOffNotice(",
		"code.indexOf('library-server-offline') >= 0) return libServerOffNotice(detail)",
		"fmt(T.libSrvOff, { name: name })",
		"r.data.offline ? libServerOffNotice(r.data)",
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("the phone page lost %q", want)
		}
	}
	bundles := strings.Count(indexHTML, "now:\"")
	for _, key := range []string{"libSrvOff", "libSrvOffAny"} {
		if got := len(regexp.MustCompile(key+`:"`).FindAllString(indexHTML, -1)); got != bundles {
			t.Errorf("%s: %d locale bundles but %d keys", key, bundles, got)
		}
	}
	if got := len(regexp.MustCompile(`libSrvOff:"[^"]*\{name\}`).FindAllString(indexHTML, -1)); got != bundles {
		t.Errorf("libSrvOff names the server in %d of %d locales", got, bundles)
	}
}

func TestLibraryServerOfflineRefusalWithoutAName(t *testing.T) {
	s := libHealServer(t)
	ref := s.libraryServerOfflineRefusal(1, "Album", &libraryServerOffline{})
	if ref.Code != libraryServerOfflineCode || ref.Error != "The music server is not answering. Switch it on and try again." {
		t.Fatalf("refusal = %+v", ref)
	}
}
