package webui

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/JRpersonal/streborn/internal/mediaservers"
	"github.com/JRpersonal/streborn/internal/presets"
)

func healServer(t *testing.T, regs ...mediaservers.Server) *Server {
	t.Helper()
	store, err := mediaservers.Load(filepath.Join(t.TempDir(), "mediaservers.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	for _, r := range regs {
		if err := store.Add(r); err != nil {
			t.Fatalf("seed store: %v", err)
		}
	}
	return &Server{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		mediaServers: store,
		// Every address answers unless a test says otherwise, and nothing is
		// resolved over the network: these tests never leave the process.
		queueHostAnswers: func(context.Context, string) bool { return true },
		queueHealResolve: func(context.Context, string) (string, bool) { return "", false },
	}
}

func queueKey(source string, urls ...string) presets.Preset {
	p := presets.Preset{Slot: 1, Type: "queue", Name: "Jazz", Source: source}
	for _, u := range urls {
		p.Items = append(p.Items, presets.PresetItem{URL: u, Title: "t"})
	}
	return p
}

// The reported case: the router gives the NAS a new lease and every folder key
// pointing at it dies at once. The key records which server it came from, and the
// store knows where that server lives now, so nothing about it is unknowable.
func TestQueuePresetFollowsAServerThatMoved(t *testing.T) {
	s := healServer(t, mediaservers.Server{ID: "AAAA-BBBB", Name: "MinimServer",
		Location: "http://192.0.2.50:9790/desc.xml"})
	p := queueKey("MinimServer",
		"http://192.0.2.11:9790/music/1.flac",
		"http://192.0.2.11:9790/music/2.flac")
	p.Items[0].Art = "http://192.0.2.11:9790/art/1.jpg"

	changed, from, to := s.healQueuePresetHost(context.Background(), &p)
	if !changed || from != "192.0.2.11" || to != "192.0.2.50" {
		t.Fatalf("want a rewrite 192.0.2.11 -> 192.0.2.50, got changed=%v %q -> %q", changed, from, to)
	}
	// The PORT and the path are not ours to correct: a DLNA server's content port
	// is often not its description port.
	if p.Items[0].URL != "http://192.0.2.50:9790/music/1.flac" || p.Items[1].URL != "http://192.0.2.50:9790/music/2.flac" {
		t.Fatalf("tracks not rewritten as expected: %+v", p.Items)
	}
	if p.Items[0].Art != "http://192.0.2.50:9790/art/1.jpg" {
		t.Fatalf("cover left on the old address: %q", p.Items[0].Art)
	}
}

// Everything that is not certain must leave the preset alone. A wrong rewrite is
// worse than a dead key: the key at least fails honestly.
func TestQueuePresetHealLeavesUncertainCasesAlone(t *testing.T) {
	live := mediaservers.Server{ID: "AAAA-BBBB", Name: "MinimServer", Location: "http://192.0.2.50:9790/desc.xml"}

	t.Run("server still at that address", func(t *testing.T) {
		s := healServer(t, live)
		p := queueKey("MinimServer", "http://192.0.2.50:9790/music/1.flac")
		if changed, _, _ := s.healQueuePresetHost(context.Background(), &p); changed {
			t.Fatal("rewrote a key whose server never moved")
		}
	})

	t.Run("name matches nothing registered", func(t *testing.T) {
		// The server this key came from is no longer a music source. Guessing at
		// the one that is would silently point the key at a stranger's library.
		s := healServer(t, live)
		p := queueKey("Plex", "http://192.0.2.11:32469/music/1.flac")
		if changed, _, _ := s.healQueuePresetHost(context.Background(), &p); changed {
			t.Fatal("pointed a key at a server it never came from")
		}
	})

	t.Run("no name and several servers", func(t *testing.T) {
		s := healServer(t, live, mediaservers.Server{ID: "CCCC-DDDD", Name: "Synology",
			Location: "http://192.0.2.60:50001/desc.xml"})
		p := queueKey("", "http://192.0.2.11:9790/music/1.flac")
		if changed, _, _ := s.healQueuePresetHost(context.Background(), &p); changed {
			t.Fatal("picked a server for a key that does not say where it came from")
		}
	})

	t.Run("not a folder key", func(t *testing.T) {
		s := healServer(t, live)
		p := presets.Preset{Slot: 1, Type: "radio", StreamURL: "http://192.0.2.11:8000/stream"}
		if changed, _, _ := s.healQueuePresetHost(context.Background(), &p); changed {
			t.Fatal("touched a radio key")
		}
	})
}

// A folder can hold tracks from more than one server (a saved list built across
// two). Only the half that moved may be rewritten.
func TestQueuePresetHealMovesOnlyWhatMoved(t *testing.T) {
	s := healServer(t, mediaservers.Server{ID: "AAAA-BBBB", Name: "MinimServer",
		Location: "http://192.0.2.50:9790/desc.xml"})
	p := queueKey("MinimServer",
		"http://192.0.2.11:9790/music/1.flac",
		"http://192.0.2.77:8200/other/2.mp3")

	changed, _, _ := s.healQueuePresetHost(context.Background(), &p)
	if !changed {
		t.Fatal("no rewrite at all")
	}
	if p.Items[1].URL != "http://192.0.2.77:8200/other/2.mp3" {
		t.Fatalf("a track on a different server was moved too: %q", p.Items[1].URL)
	}
}

func TestRewriteURLHostKeepsEverythingElse(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http://192.0.2.11:9790/a%20b/1.flac?x=1", "http://192.0.2.50:9790/a%20b/1.flac?x=1"},
		{"http://192.0.2.11/1.flac", "http://192.0.2.50/1.flac"},
	}
	for _, c := range cases {
		got, ok := rewriteURLHost(c.in, "192.0.2.11", "192.0.2.50")
		if !ok || got != c.want {
			t.Errorf("rewriteURLHost(%q) = %q (%v), want %q", c.in, got, ok, c.want)
		}
	}
	if _, ok := rewriteURLHost("http://192.0.2.99/1.flac", "192.0.2.11", "192.0.2.50"); ok {
		t.Error("rewrote a URL that was not on the old host")
	}
	if _, ok := rewriteURLHost("", "192.0.2.11", "192.0.2.50"); ok {
		t.Error("rewrote an empty URL")
	}
}

// MinimServer's paths carry "*" escapes and a "$!picture" cover suffix. A host
// rewrite must hand back every other byte unchanged, or the healed key would
// point at a file the server does not have.
func TestRewriteURLHostKeepsMinimServerPaths(t *testing.T) {
	in := "http://192.0.2.32:9790/minimserver/*/Music/Scott*20Stapp/Higher*20Power/04*20What*20I*20Deserve.m4a/$!picture-14953-6889439.jpg"
	want := "http://192.0.2.101:9790/minimserver/*/Music/Scott*20Stapp/Higher*20Power/04*20What*20I*20Deserve.m4a/$!picture-14953-6889439.jpg"
	if got, ok := rewriteURLHost(in, "192.0.2.32", "192.0.2.101"); !ok || got != want {
		t.Fatalf("rewriteURLHost = %q (%v), want %q", got, ok, want)
	}
}

// #1366: the media server moved overnight. The store still held the address it
// had when the server was enabled (only the agent's own browse writes it, and the
// desktop app browses the server itself), so store and key agreed on the dead
// address and the heal stood down. The key press then pushed every track to an
// address nobody answered at, the speaker timed out three times in a row, and the
// key showed an error while "Play folder" on the same playlist worked.
func TestQueuePresetHealDoesNotTrustAStaleStore(t *testing.T) {
	s := healServer(t, mediaservers.Server{ID: "AAAA-BBBB", Name: "MinimServer",
		Location: "http://192.0.2.32:9791/AAAA-BBBB/Upnp/device.xml"})
	var probed []string
	s.queueHostAnswers = func(_ context.Context, raw string) bool {
		probed = append(probed, raw)
		return false
	}
	resolved := 0
	s.queueHealResolve = func(_ context.Context, id string) (string, bool) {
		resolved++
		if id != "AAAA-BBBB" {
			t.Errorf("resolved %q, want the key's server", id)
		}
		return "http://192.0.2.101:9791/AAAA-BBBB/Upnp/device.xml", true
	}
	p := queueKey("MinimServer",
		"http://192.0.2.32:9790/minimserver/*/Music/1.m4a",
		"http://192.0.2.32:9790/minimserver/*/Music/2.m4a")
	p.Items[0].Art = "http://192.0.2.32:9790/minimserver/*/Music/1.m4a/$!picture-1.jpg"
	p.Art = "http://192.0.2.32:9790/minimserver/*/Music/cover.jpg"

	changed, from, to := s.healQueuePresetHost(context.Background(), &p)
	if !changed || from != "192.0.2.32" || to != "192.0.2.101" {
		t.Fatalf("want a rewrite 192.0.2.32 -> 192.0.2.101, got changed=%v %q -> %q", changed, from, to)
	}
	if len(probed) != 1 || resolved != 1 {
		t.Fatalf("probed %v, resolved %d times; want one probe and one resolve", probed, resolved)
	}
	if p.Items[1].URL != "http://192.0.2.101:9790/minimserver/*/Music/2.m4a" {
		t.Fatalf("track left behind: %q", p.Items[1].URL)
	}
	// The covers moved with the tracks: the key's tile showed the placeholder
	// because its cover pointed at the dead address too.
	if p.Items[0].Art != "http://192.0.2.101:9790/minimserver/*/Music/1.m4a/$!picture-1.jpg" {
		t.Fatalf("track cover left behind: %q", p.Items[0].Art)
	}
	if p.Art != "http://192.0.2.101:9790/minimserver/*/Music/cover.jpg" {
		t.Fatalf("folder cover left behind: %q", p.Art)
	}
}

// The probe is what keeps the normal case free: a server that answers where the
// store and the key say is never resolved, and a dead server that cannot be
// found anywhere else leaves the key exactly as it was.
func TestQueuePresetHealProbeOutcomes(t *testing.T) {
	reg := mediaservers.Server{ID: "AAAA-BBBB", Name: "MinimServer", Location: "http://192.0.2.32:9791/desc.xml"}

	t.Run("answers", func(t *testing.T) {
		s := healServer(t, reg)
		s.queueHealResolve = func(context.Context, string) (string, bool) {
			t.Error("resolved a server that answered at its address")
			return "", false
		}
		p := queueKey("MinimServer", "http://192.0.2.32:9790/music/1.flac")
		if changed, _, _ := s.healQueuePresetHost(context.Background(), &p); changed {
			t.Fatal("rewrote a key whose server answers")
		}
	})

	t.Run("dead and not found", func(t *testing.T) {
		s := healServer(t, reg)
		s.queueHostAnswers = func(context.Context, string) bool { return false }
		p := queueKey("MinimServer", "http://192.0.2.32:9790/music/1.flac")
		if changed, _, _ := s.healQueuePresetHost(context.Background(), &p); changed {
			t.Fatal("rewrote a key with nowhere to point it")
		}
		if p.Items[0].URL != "http://192.0.2.32:9790/music/1.flac" {
			t.Fatalf("key touched: %q", p.Items[0].URL)
		}
	})
}

// The heal must not write into the preset store behind its lock: the store
// hands out presets whose Items share its own backing array.
func TestQueuePresetHealLeavesTheStoredCopyAlone(t *testing.T) {
	s := healServer(t, mediaservers.Server{ID: "AAAA-BBBB", Name: "MinimServer",
		Location: "http://192.0.2.50:9790/desc.xml"})
	store, err := presets.Load(filepath.Join(t.TempDir(), "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSlot(queueKey("MinimServer", "http://192.0.2.11:9790/music/1.flac")); err != nil {
		t.Fatal(err)
	}
	p, _ := store.Get(1)
	if changed, _, _ := s.healQueuePresetHost(context.Background(), &p); !changed {
		t.Fatal("no rewrite")
	}
	if stored, _ := store.Get(1); stored.Items[0].URL != "http://192.0.2.11:9790/music/1.flac" {
		t.Fatalf("the stored key changed without a SetSlot: %q", stored.Items[0].URL)
	}
}

func liveQueueServer(t *testing.T, regs ...mediaservers.Server) *Server {
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

// #1366, the part a key press cannot reach: "Play folder" played the playlist a
// key holds, from the server's new address, and the key stayed dark with a
// placeholder for a cover, because its tracks still named the old address. The
// started queue is where the server lives now; the server's keys and the
// store follow it, without a single network call.
func TestPlayFolderPutsTheServersKeysOnItsNewAddress(t *testing.T) {
	s := liveQueueServer(t,
		mediaservers.Server{ID: "uuid:AAAA-BBBB", Name: "MinimServer", Location: "http://192.0.2.32:9791/AAAA-BBBB/Upnp/device.xml"},
		mediaservers.Server{ID: "uuid:CCCC-DDDD", Name: "Synology", Location: "http://192.0.2.60:50001/desc.xml"})
	s.queueHostAnswers = func(context.Context, string) bool {
		t.Error("probed the network; the queue already says where the server is")
		return false
	}
	playlist := queueKey("MinimServer", "http://192.0.2.32:9790/m/1.m4a", "http://192.0.2.32:9790/m/2.m4a")
	playlist.Slot = 3
	playlist.Items[0].Art = "http://192.0.2.32:9790/m/1.m4a/$!picture-1.jpg"
	album := queueKey("MinimServer", "http://192.0.2.32:9790/r/1.m4a")
	album.Slot, album.Art = 2, "http://192.0.2.32:9790/r/cover.jpg"
	other := queueKey("Synology", "http://192.0.2.60:50002/x/1.mp3")
	other.Slot = 4
	for _, p := range []presets.Preset{playlist, album, other} {
		if err := s.presets.SetSlot(p); err != nil {
			t.Fatal(err)
		}
	}
	items := []queueItem{
		{URL: "http://192.0.2.101:9790/m/1.m4a", Title: "one"},
		{URL: "http://192.0.2.101:9790/m/2.m4a", Title: "two"},
	}
	s.healQueuePresetsFromLiveQueue(recentCardCtx{key: "queue:uuid:AAAA-BBBB:0$=Playlist", source: "MinimServer"}, items)

	got3, _ := s.presets.Get(3)
	if got3.Items[0].URL != "http://192.0.2.101:9790/m/1.m4a" || got3.Items[1].URL != "http://192.0.2.101:9790/m/2.m4a" {
		t.Fatalf("playlist key not moved: %+v", got3.Items)
	}
	if got3.Items[0].Art != "http://192.0.2.101:9790/m/1.m4a/$!picture-1.jpg" {
		t.Fatalf("playlist cover not moved: %q", got3.Items[0].Art)
	}
	got2, _ := s.presets.Get(2)
	if got2.Items[0].URL != "http://192.0.2.101:9790/r/1.m4a" || got2.Art != "http://192.0.2.101:9790/r/cover.jpg" {
		t.Fatalf("album key on the same server not moved: %+v art %q", got2.Items, got2.Art)
	}
	got4, _ := s.presets.Get(4)
	if got4.Items[0].URL != "http://192.0.2.60:50002/x/1.mp3" {
		t.Fatalf("another server's key was moved: %q", got4.Items[0].URL)
	}
	var loc string
	for _, r := range s.mediaServers.List() {
		if r.Name == "MinimServer" {
			loc = r.Location
		}
	}
	if loc != "http://192.0.2.101:9791/AAAA-BBBB/Upnp/device.xml" {
		t.Fatalf("store not moved (port and path must stay): %q", loc)
	}

	// The same folder now plays and its key lights up (the app reads queue.slot).
	s.queue.load(items, 0, false, repeatOff)
	if got := s.queueSnapshotWithCard().Slot; got != 3 {
		t.Fatalf("queue.slot = %d, want 3", got)
	}
}

// Two registered servers with the same name: which one moved is not certain, so
// no key and no store entry is touched.
func TestPlayFolderLeavesKeysAloneWhenTheServerNameIsAmbiguous(t *testing.T) {
	s := liveQueueServer(t,
		mediaservers.Server{ID: "uuid:AAAA-BBBB", Name: "MinimServer", Location: "http://192.0.2.32:9791/a.xml"},
		mediaservers.Server{ID: "uuid:EEEE-FFFF", Name: "MinimServer", Location: "http://192.0.2.33:9791/b.xml"})
	key := queueKey("MinimServer", "http://192.0.2.32:9790/m/1.m4a")
	if err := s.presets.SetSlot(key); err != nil {
		t.Fatal(err)
	}
	s.healQueuePresetsFromLiveQueue(recentCardCtx{key: "queue:uuid:AAAA-BBBB:0"},
		[]queueItem{{URL: "http://192.0.2.101:9790/m/1.m4a"}})
	if got, _ := s.presets.Get(1); got.Items[0].URL != "http://192.0.2.32:9790/m/1.m4a" {
		t.Fatalf("key moved on an ambiguous name: %q", got.Items[0].URL)
	}
}

// A folder started with "Play folder" that a key already holds lights that key
// (#1366), the lowest one when two hold it. A queue a key started keeps naming
// only that key, and a folder no key holds lights none.
func TestPlayFolderLightsTheKeyThatHoldsIt(t *testing.T) {
	s := queueSaveTestServer(t)
	tracks := []queueItem{{URL: "http://192.0.2.10:9000/disk/01.mp3"}, {URL: "http://192.0.2.10:9000/disk/02.mp3"}}
	for _, slot := range []int{5, 3} {
		p := queueKey("NAS", tracks[0].URL, tracks[1].URL)
		p.Slot = slot
		if err := s.presets.SetSlot(p); err != nil {
			t.Fatal(err)
		}
	}
	s.queue.load(tracks, 0, false, repeatOff)
	s.recentQueueCard = recentCardCtx{key: "queue:uuid:abc:64$1"}
	s.queueFolder = s.recentQueueCard
	if got := s.queueSnapshotWithCard().Slot; got != 3 {
		t.Fatalf("queue.slot = %d, want 3", got)
	}
	if !s.queuePlaysSlot(3) || s.queuePlaysSlot(5) {
		t.Fatal("a press on key 3 must count as its folder playing, key 5 not")
	}

	// Started from key 5: the card names it, and key 3 is not lit as well.
	s.recentQueueCard = recentCardCtx{key: "queue:slot:5"}
	s.queueFolder = s.recentQueueCard
	if got := s.queueSnapshotWithCard().Slot; got != 0 {
		t.Fatalf("queue from key 5: queue.slot = %d, want 0 (the card names the key)", got)
	}

	// A different folder lights nothing.
	s.queue.load([]queueItem{{URL: "http://192.0.2.10:9000/disk/99.mp3"}}, 0, false, repeatOff)
	s.recentQueueCard = recentCardCtx{key: "queue:uuid:abc:64$2"}
	s.queueFolder = s.recentQueueCard
	if got := s.queueSnapshotWithCard().Slot; got != 0 {
		t.Fatalf("other folder: queue.slot = %d, want 0", got)
	}
}
