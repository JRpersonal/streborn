package webui

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/JRpersonal/streborn/dlna"
	"github.com/JRpersonal/streborn/internal/mediaservers"
	"github.com/JRpersonal/streborn/internal/presets"
)

// The reporter's keys (#1366): a single track saved from MinimServer, stored
// as a radio-shaped preset whose file URL carries the server's address, cover
// on the same address.
const (
	libHealOldTrack = "http://192.0.2.32:9790/minimserver/*/Media.localized/Music/Band/Album/03*20Purpose*20For*20Pain.m4a"
	libHealNewTrack = "http://192.0.2.101:9790/minimserver/*/Media.localized/Music/Band/Album/03*20Purpose*20For*20Pain.m4a"
	libHealOldArt   = "http://192.0.2.32:9790/minimserver/*/Media.localized/Music/Band/Album/03*20Purpose*20For*20Pain.m4a/$!picture-1.jpg"
	libHealNewArt   = "http://192.0.2.101:9790/minimserver/*/Media.localized/Music/Band/Album/03*20Purpose*20For*20Pain.m4a/$!picture-1.jpg"
)

func libraryTrackKey(slot int, source, stream, art string) presets.Preset {
	return presets.Preset{Slot: slot, Type: "radio", Name: "Purpose For Pain", Source: source, StreamURL: stream, Art: art}
}

// libHealServer is a server with a preset store, the registry seeded, no
// address answering and nothing resolved over the network.
func libHealServer(t *testing.T, regs ...mediaservers.Server) *Server {
	t.Helper()
	s := healServer(t, regs...)
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.presets = store
	s.queue = newPlayQueue()
	return s
}

// #1366 on a key press: the folder key next to it was healed and played, the
// single-track key was handed to the speaker on the dead address and timed
// out. The key names its server, the store knows where that server lives now,
// so the file and its cover follow and the corrected key is persisted.
func TestLibraryKeyFollowsAServerThatMovedOnPress(t *testing.T) {
	s := libHealServer(t, mediaservers.Server{ID: "uuid:AAAA-BBBB", Name: "MinimServer",
		Location: "http://192.0.2.101:9791/AAAA-BBBB/Upnp/device.xml"})
	var probed []string
	s.queueHostAnswers = func(_ context.Context, raw string) bool {
		probed = append(probed, raw)
		return false
	}
	key := libraryTrackKey(4, "MinimServer", libHealOldTrack, libHealOldArt)
	if err := s.presets.SetSlot(key); err != nil {
		t.Fatal(err)
	}
	pressed, _ := s.presets.Get(4)
	s.healLibraryKeyIfMoved(context.Background(), 4, &pressed)

	if pressed.StreamURL != libHealNewTrack {
		t.Fatalf("file not moved in hand: %q", pressed.StreamURL)
	}
	if pressed.Art != libHealNewArt {
		t.Fatalf("cover not moved in hand: %q", pressed.Art)
	}
	if len(probed) != 1 || probed[0] != libHealOldTrack {
		t.Fatalf("probed %v, want the key's own file once", probed)
	}
	got, _ := s.presets.Get(4)
	if got.StreamURL != libHealNewTrack || got.Art != libHealNewArt {
		t.Fatalf("corrected key not persisted: %q / %q", got.StreamURL, got.Art)
	}
	// Still a library key afterwards: the next press plays the file directly.
	if libraryFileMime(got) != "audio/mp4" {
		t.Fatalf("healed key lost its library shape: mime %q", libraryFileMime(got))
	}
}

// A store that is stale too (only the agent's own browse writes it) does not
// stop the heal: the server is resolved once, bounded, and the key follows.
func TestLibraryKeyPressResolvesWhenTheStoreIsStaleToo(t *testing.T) {
	s := libHealServer(t, mediaservers.Server{ID: "uuid:AAAA-BBBB", Name: "MinimServer",
		Location: "http://192.0.2.32:9791/AAAA-BBBB/Upnp/device.xml"})
	resolved := 0
	s.queueHealResolve = func(_ context.Context, id string) (string, bool) {
		resolved++
		if id != "uuid:AAAA-BBBB" {
			t.Errorf("resolved %q, want the key's server", id)
		}
		return "http://192.0.2.101:9791/AAAA-BBBB/Upnp/device.xml", true
	}
	key := libraryTrackKey(4, "MinimServer", libHealOldTrack, libHealOldArt)
	changed, from, to := s.healLibraryKeyHost(context.Background(), &key)
	if !changed || from != "192.0.2.32" || to != "192.0.2.101" || resolved != 1 {
		t.Fatalf("want a rewrite 192.0.2.32 -> 192.0.2.101 after one resolve, got changed=%v %q -> %q resolved=%d", changed, from, to, resolved)
	}
	if key.StreamURL != libHealNewTrack || key.Art != libHealNewArt {
		t.Fatalf("key not rewritten as expected: %q / %q", key.StreamURL, key.Art)
	}
}

// A key whose own address still answers is never touched, whatever the store
// says: a stale store must not drag a working key back to a dead address.
func TestLibraryKeyWhoseHostAnswersIsLeftAlone(t *testing.T) {
	s := libHealServer(t, mediaservers.Server{ID: "uuid:AAAA-BBBB", Name: "MinimServer",
		Location: "http://192.0.2.32:9791/d.xml"})
	s.queueHostAnswers = func(context.Context, string) bool { return true }
	s.queueHealResolve = func(context.Context, string) (string, bool) {
		t.Error("resolved a server whose file answers")
		return "", false
	}
	key := libraryTrackKey(4, "MinimServer", libHealNewTrack, libHealNewArt)
	if err := s.presets.SetSlot(key); err != nil {
		t.Fatal(err)
	}
	pressed, _ := s.presets.Get(4)
	s.healLibraryKeyIfMoved(context.Background(), 4, &pressed)
	if pressed.StreamURL != libHealNewTrack || pressed.Art != libHealNewArt {
		t.Fatalf("touched a key whose server answers: %q / %q", pressed.StreamURL, pressed.Art)
	}
}

// A radio station is not a library key and is never probed, moved or
// persisted, even one on a .mp3 URL or one that happens to share the
// server's host.
func TestLibraryKeyHealNeverTouchesARadioStation(t *testing.T) {
	s := libHealServer(t, mediaservers.Server{ID: "uuid:AAAA-BBBB", Name: "MinimServer",
		Location: "http://192.0.2.101:9791/d.xml"})
	s.queueHostAnswers = func(_ context.Context, raw string) bool {
		t.Errorf("probed %q: a radio station is nobody's library key", raw)
		return false
	}
	stations := []presets.Preset{
		{Slot: 1, Type: "radio", Name: "Public", StreamURL: "http://stream.example.com:8000/live.mp3", Art: "https://cdn.example.com/logo.png"},
		{Slot: 2, Type: "radio", Name: "On the NAS host", StreamURL: "http://192.0.2.32:8000/live"},
		// A custom URL key (no Source) is not a library key either, even with
		// a file extension: it has its own probe (#830) and no server to follow.
		{Slot: 3, Type: "radio", Name: "Custom file", StreamURL: "http://192.0.2.32:9790/x/1.mp3"},
	}
	for _, p := range stations {
		if err := s.presets.SetSlot(p); err != nil {
			t.Fatal(err)
		}
		pressed := p
		s.healLibraryKeyIfMoved(context.Background(), p.Slot, &pressed)
		if pressed.StreamURL != p.StreamURL || pressed.Art != p.Art {
			t.Fatalf("slot %d touched: %q / %q", p.Slot, pressed.StreamURL, pressed.Art)
		}
		if got, _ := s.presets.Get(p.Slot); got.StreamURL != p.StreamURL {
			t.Fatalf("slot %d rewritten in the store: %q", p.Slot, got.StreamURL)
		}
	}
	// The server-wide heal skips them too.
	s.healQueuePresetsFromBrowse(context.Background(),
		dlna.Server{UDN: "uuid:AAAA-BBBB", Location: "http://192.0.2.101:9791/d.xml"},
		dlna.BrowseResult{Items: []dlna.Item{{StreamURL: "http://192.0.2.101:9790/m/9.m4a"}}})
	for _, p := range stations {
		if got, _ := s.presets.Get(p.Slot); got.StreamURL != p.StreamURL {
			t.Fatalf("slot %d moved by a browse: %q", p.Slot, got.StreamURL)
		}
	}
}

// #1366, the browse half: the app opened the server at its new address and
// told the agent. The single-track keys follow with the folder keys, without
// a press and without a network call: the one that names the server, and
// the one saved with the hold-to-save's fallback name, matched by the host it
// was saved on. Another server's key and a station stay where they are.
func TestBrowsePutsTheServersSingleTrackKeysOnItsNewAddress(t *testing.T) {
	s := libHealServer(t,
		mediaservers.Server{ID: "uuid:AAAA-BBBB", Name: "MinimServer", Location: "http://192.0.2.32:9791/AAAA-BBBB/Upnp/device.xml"},
		mediaservers.Server{ID: "uuid:CCCC-DDDD", Name: "Synology", Location: "http://192.0.2.60:50001/desc.xml"})
	s.queueHostAnswers = func(context.Context, string) bool {
		t.Error("probed the network; the browse already says where the server is")
		return false
	}
	named := libraryTrackKey(4, "MinimServer", libHealOldTrack, libHealOldArt)
	unnamed := libraryTrackKey(5, holdLibraryFallbackSource, "http://192.0.2.32:9790/minimserver/*/Music/Other/02*20Song.m4a", "")
	folder := queueKey("MinimServer", "http://192.0.2.32:9790/m/1.m4a")
	folder.Slot = 2
	other := libraryTrackKey(6, "Synology", "http://192.0.2.60:50002/x/1.flac", "http://192.0.2.60:50002/x/cover.jpg")
	station := presets.Preset{Slot: 1, Type: "radio", Name: "Public", StreamURL: "http://stream.example.com:8000/live.mp3"}
	for _, p := range []presets.Preset{named, unnamed, folder, other, station} {
		if err := s.presets.SetSlot(p); err != nil {
			t.Fatal(err)
		}
	}
	srv := dlna.Server{UDN: "uuid:AAAA-BBBB", Location: "http://192.0.2.101:9791/AAAA-BBBB/Upnp/device.xml", FriendlyName: "MinimServer"}
	s.healQueuePresetsFromBrowse(context.Background(), srv, dlna.BrowseResult{
		Items: []dlna.Item{{Title: "x", StreamURL: "http://192.0.2.101:9790/m/9.m4a"}},
	})

	if got, _ := s.presets.Get(4); got.StreamURL != libHealNewTrack || got.Art != libHealNewArt {
		t.Fatalf("named key not moved: %q / %q", got.StreamURL, got.Art)
	}
	if got, _ := s.presets.Get(5); got.StreamURL != "http://192.0.2.101:9790/minimserver/*/Music/Other/02*20Song.m4a" {
		t.Fatalf("key saved under the fallback name not moved by host: %q", got.StreamURL)
	}
	if got, _ := s.presets.Get(2); got.Items[0].URL != "http://192.0.2.101:9790/m/1.m4a" {
		t.Fatalf("folder key not moved alongside: %q", got.Items[0].URL)
	}
	if got, _ := s.presets.Get(6); got.StreamURL != "http://192.0.2.60:50002/x/1.flac" || got.Art != "http://192.0.2.60:50002/x/cover.jpg" {
		t.Fatalf("another server's key was moved: %q / %q", got.StreamURL, got.Art)
	}
	if got, _ := s.presets.Get(1); got.StreamURL != station.StreamURL {
		t.Fatalf("a radio station was moved: %q", got.StreamURL)
	}
}

// "Play folder" is a track sighting too: the single-track keys of that server
// follow the started queue, port included when the server came back on
// another port and the old one is dead.
func TestPlayFolderPutsTheServersSingleTrackKeysOnItsNewAddress(t *testing.T) {
	s := libHealServer(t, mediaservers.Server{ID: "uuid:AAAA-BBBB", Name: "MinimServer", Location: "http://192.0.2.32:9791/d.xml"})
	key := libraryTrackKey(4, "MinimServer", libHealOldTrack, libHealOldArt)
	if err := s.presets.SetSlot(key); err != nil {
		t.Fatal(err)
	}
	s.healQueuePresetsFromLiveQueue(recentCardCtx{key: "queue:uuid:AAAA-BBBB:0$=Playlist"},
		[]queueItem{{URL: "http://192.0.2.101:9800/m/1.m4a"}})
	got, _ := s.presets.Get(4)
	want := "http://192.0.2.101:9800/minimserver/*/Media.localized/Music/Band/Album/03*20Purpose*20For*20Pain.m4a"
	if got.StreamURL != want {
		t.Fatalf("key not moved to the new host and port: %q", got.StreamURL)
	}
	if got.Art != want+"/$!picture-1.jpg" {
		t.Fatalf("cover not moved with the file: %q", got.Art)
	}
}

// A key an older agent would have left half-healed, or one whose cover was
// saved from another address of the same server: the file sits on the live
// host, the cover does not. The cover follows the file, on a press and on a
// browse; a cover on another registered server, the speaker itself or
// loopback is not the server's and stays.
func TestLibraryKeyCoverFollowsTheFile(t *testing.T) {
	regs := []mediaservers.Server{
		{ID: "uuid:AAAA-BBBB", Name: "MinimServer", Location: "http://192.0.2.32:9791/d.xml"},
		{ID: "uuid:CCCC-DDDD", Name: "Synology", Location: "http://192.0.2.60:50001/desc.xml"},
	}

	t.Run("on a key press", func(t *testing.T) {
		s := libHealServer(t, regs...)
		s.queueHostAnswers = func(context.Context, string) bool { return true }
		key := libraryTrackKey(4, "MinimServer", libHealNewTrack, libHealOldArt)
		if err := s.presets.SetSlot(key); err != nil {
			t.Fatal(err)
		}
		pressed, _ := s.presets.Get(4)
		s.healLibraryKeyIfMoved(context.Background(), 4, &pressed)
		if pressed.Art != libHealNewArt || pressed.StreamURL != libHealNewTrack {
			t.Fatalf("cover not moved in hand: %q (file %q)", pressed.Art, pressed.StreamURL)
		}
		if got, _ := s.presets.Get(4); got.Art != libHealNewArt {
			t.Fatalf("cover not persisted: %q", got.Art)
		}
	})

	t.Run("on a browse", func(t *testing.T) {
		s := libHealServer(t, regs...)
		key := libraryTrackKey(4, "MinimServer", libHealNewTrack, libHealOldArt)
		if err := s.presets.SetSlot(key); err != nil {
			t.Fatal(err)
		}
		s.healQueuePresetsFromBrowse(context.Background(),
			dlna.Server{UDN: "uuid:AAAA-BBBB", Location: "http://192.0.2.101:9791/d.xml"}, dlna.BrowseResult{})
		if got, _ := s.presets.Get(4); got.Art != libHealNewArt || got.StreamURL != libHealNewTrack {
			t.Fatalf("cover not moved by the browse: %q (file %q)", got.Art, got.StreamURL)
		}
	})

	t.Run("covers that are not the server's stay", func(t *testing.T) {
		s := libHealServer(t, regs...)
		s.boxHost = "192.0.2.5"
		s.queueHostAnswers = func(context.Context, string) bool { return true }
		for _, art := range []string{
			"http://192.0.2.60:50002/not/ours.jpg",
			"http://192.0.2.5:8888/art?u=x",
			"http://127.0.0.1:8888/icon.png",
		} {
			key := libraryTrackKey(4, "MinimServer", libHealNewTrack, art)
			s.healLibraryKeyIfMoved(context.Background(), 4, &key)
			if key.Art != art {
				t.Fatalf("moved a cover that never was on the server: %q -> %q", art, key.Art)
			}
		}
	})
}

// Which server a single-track key belongs to must be certain. A name that
// matches nothing registered falls back to the host the file sits on, but
// only while exactly one registered server lives there.
func TestLibraryTrackKeyServerMatching(t *testing.T) {
	minim := mediaservers.Server{ID: "uuid:AAAA-BBBB", Name: "MinimServer", Location: "http://192.0.2.32:9791/d.xml"}
	plex := mediaservers.Server{ID: "uuid:EEEE-FFFF", Name: "Plex", Location: "http://192.0.2.32:32469/d.xml"}
	syno := mediaservers.Server{ID: "uuid:CCCC-DDDD", Name: "Synology", Location: "http://192.0.2.60:50001/desc.xml"}
	key := func(source string) presets.Preset { return libraryTrackKey(4, source, libHealOldTrack, "") }

	if reg, ok := libraryTrackKeyServer([]mediaservers.Server{minim, syno}, key("minimserver")); !ok || reg.ID != minim.ID {
		t.Fatalf("by name: got %+v %v", reg, ok)
	}
	if reg, ok := libraryTrackKeyServer([]mediaservers.Server{minim, syno}, key(holdLibraryFallbackSource)); !ok || reg.ID != minim.ID {
		t.Fatalf("by host: got %+v %v", reg, ok)
	}
	if _, ok := libraryTrackKeyServer([]mediaservers.Server{minim, plex, syno}, key(holdLibraryFallbackSource)); ok {
		t.Fatal("two servers on the key's host: picked one anyway")
	}
	if reg, ok := libraryTrackKeyServer([]mediaservers.Server{minim, plex, syno}, key("Plex")); !ok || reg.ID != plex.ID {
		t.Fatalf("the name decides between two servers on one host: got %+v %v", reg, ok)
	}
	if _, ok := libraryTrackKeyServer([]mediaservers.Server{syno}, key(holdLibraryFallbackSource)); ok {
		t.Fatal("picked a server on another host")
	}

	// The same rules on a sighting: a key named for another server is not
	// reg's even when it sits on reg's host.
	regs := []mediaservers.Server{minim, syno}
	if !libraryTrackKeyBelongsTo(regs, minim, "192.0.2.32", "192.0.2.101", key("MinimServer")) {
		t.Fatal("named key not recognised")
	}
	if !libraryTrackKeyBelongsTo(regs, minim, "192.0.2.32", "192.0.2.101", key(holdLibraryFallbackSource)) {
		t.Fatal("key on the store's old host not recognised")
	}
	if libraryTrackKeyBelongsTo(regs, minim, "192.0.2.32", "192.0.2.101", key("Synology")) {
		t.Fatal("another server's key taken by host")
	}
	if libraryTrackKeyBelongsTo(regs, minim, "192.0.2.99", "192.0.2.101", key(holdLibraryFallbackSource)) {
		t.Fatal("key on an unrelated host taken")
	}
	if libraryTrackKeyBelongsTo([]mediaservers.Server{minim, plex}, minim, "192.0.2.32", "192.0.2.101", key(holdLibraryFallbackSource)) {
		t.Fatal("key on a host shared by two servers taken by host")
	}
}

// A dead server that cannot be found anywhere leaves the key exactly as it
// was, and a key whose name matches nothing registered and whose host is no
// registered server's is not guessed at.
func TestLibraryKeyHealLeavesUncertainCasesAlone(t *testing.T) {
	t.Run("dead and not found", func(t *testing.T) {
		s := libHealServer(t, mediaservers.Server{ID: "uuid:AAAA-BBBB", Name: "MinimServer", Location: "http://192.0.2.32:9791/d.xml"})
		key := libraryTrackKey(4, "MinimServer", libHealOldTrack, libHealOldArt)
		if changed, _, _ := s.healLibraryKeyHost(context.Background(), &key); changed || key.StreamURL != libHealOldTrack {
			t.Fatalf("rewrote a key with nowhere to point it: %q", key.StreamURL)
		}
	})
	t.Run("server since removed", func(t *testing.T) {
		s := libHealServer(t, mediaservers.Server{ID: "uuid:CCCC-DDDD", Name: "Synology", Location: "http://192.0.2.60:50001/desc.xml"})
		key := libraryTrackKey(4, "MinimServer", libHealOldTrack, libHealOldArt)
		if changed, _, _ := s.healLibraryKeyHost(context.Background(), &key); changed {
			t.Fatal("pointed a key at a server it never came from")
		}
	})
}
