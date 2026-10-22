package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/presets"
)

// Holding a hardware preset key while STR's own stream plays (#1217).
//
// Everything STR plays itself reaches the speaker as a UPnP push: the Spotify
// stream, a single music-library file, a library folder's queue. The firmware
// refuses its own hold-to-store for a UPnP push (a white double blink) and
// never asks marge to keep anything, so the speaker-side keeper in
// cmd/agent/holdstore.go is never reached. The agent therefore spots the hold
// itself (cmd/agent/holdsave.go) and asks here to store what plays, through
// the very same save paths the app's "save what plays" uses: the queue
// preset the folder save builds, and the per-slot PUT with all of its rules
// (Spotify account stamping on a live save, shuffle and repeat carried over,
// a library song's learned length, the one-station-one-key refusal of #836,
// and the box registration that makes the key fire).

// HoldSaved is what a hold on a preset key stored.
type HoldSaved struct {
	Slot int
	// Kind is spotify, queue or library.
	Kind string
	Name string
	// Unchanged is true when the key already held exactly this, so nothing
	// was written.
	Unchanged bool
	// SwappedWith is the key the item already sat on when the hold swapped
	// the two keys instead of refusing; 0 for a plain save. SwappedName is
	// what that key holds now, "" when it became empty.
	SwappedWith int
	SwappedName string
}

// HoldSaveError is a save the preset rules refused, with the code the app's
// save paths answer with (already-on-slot, spotify-uri-unplayable, ...).
type HoldSaveError struct {
	Status int
	Code   string
	// OtherSlot and OtherName name the key the item already sits on, for
	// already-on-slot.
	OtherSlot int
	OtherName string
	Message   string
}

func (e *HoldSaveError) Error() string {
	if e.Code == "already-on-slot" {
		return fmt.Sprintf("%q is already on key %d", e.OtherName, e.OtherSlot)
	}
	if e.Message != "" {
		return fmt.Sprintf("%s (%s)", e.Code, e.Message)
	}
	return fmt.Sprintf("%s (status %d)", e.Code, e.Status)
}

var (
	// ErrHoldNotUPnP: the speaker plays something of its own (a native
	// station, Bluetooth, AUX, standby). Its own hold-to-store handles that.
	ErrHoldNotUPnP = errors.New("the speaker is not playing a UPnP push")
	// ErrHoldUnknownStream: a UPnP push STR cannot name (another app's
	// stream, or a play STR no longer remembers).
	ErrHoldUnknownStream = errors.New("the speaker plays a stream STR did not start or no longer knows")
)

// holdSaveUA marks the internal PUT in the "preset write accepted" line, so a
// bundle shows which writes came from a held key.
const holdSaveUA = "STR speaker key hold"

// HoldSaveLive stores what STR plays right now on slot. It reads the
// speaker's now-playing first: only a UPnP push qualifies, and the location
// decides between Spotify, a folder queue and a single library file.
func (s *Server) HoldSaveLive(ctx context.Context, slot int) (HoldSaved, error) {
	if s.presets == nil {
		return HoldSaved{}, errors.New("presets store not initialized")
	}
	if slot < 1 || slot > 6 {
		return HoldSaved{}, fmt.Errorf("invalid slot %d", slot)
	}
	src, loc, ok := s.boxNowPlaying(ctx)
	if !ok {
		return HoldSaved{}, errors.New("the speaker's now-playing could not be read")
	}
	if !strings.EqualFold(strings.TrimSpace(src), "UPNP") {
		return HoldSaved{}, fmt.Errorf("%w (source %s)", ErrHoldNotUPnP, src)
	}
	// Spotify first: its location is unambiguous, and a queue that was
	// still marked active underneath must not win over what is audible.
	if looksLikeSpotifyStreamURL(loc) {
		uri := ""
		if s.spotifyContext != nil {
			uri = normalizeSpotifyURI(s.spotifyContext())
		}
		if uri == "" {
			uri = legacySpotifyURI(loc)
		}
		if uri == "" {
			return HoldSaved{}, &HoldSaveError{Code: "spotify-context-unknown",
				Message: "Spotify plays, but the engine names no playlist, album or track"}
		}
		// "Spotify" is the placeholder the per-slot save replaces with the
		// playlist's own title, exactly as for the app's save.
		p := presets.Preset{Slot: slot, Name: "Spotify", Type: "spotify", URI: uri}
		return s.holdSaveOrSwap(ctx, p, "spotify", true)
	}
	if q, ok := s.LiveQueuePreset(slot); ok {
		if cur, have := s.presets.Get(slot); have && cur.Type == "queue" &&
			samePresetItems(cur, q) && cur.Name == q.Name && cur.Source == q.Source {
			s.NoteQueueSavedSlot(slot)
			return HoldSaved{Slot: slot, Kind: "queue", Name: cur.Name, Unchanged: true}, nil
		}
		if err := s.storeQueuePreset(ctx, q); err != nil {
			return HoldSaved{}, err
		}
		// The folder keeps its own card (Library and Recently-played match on
		// it); the key it now sits on is recorded beside it, so the key lights
		// up as playing (#1217).
		s.NoteQueueSavedSlot(slot)
		s.logger.Info("queue preset save: stored the playing folder on a key",
			"slot", q.Slot, "name", q.Name, "source", q.Source, "tracks", len(q.Items),
			"shuffle", q.Shuffle, "from", "speaker key hold")
		return HoldSaved{Slot: slot, Kind: "queue", Name: q.Name}, nil
	}
	if p, ok := s.liveLibraryFilePreset(slot, loc); ok {
		return s.holdSaveOrSwap(ctx, p, "library", false)
	}
	return HoldSaved{}, ErrHoldUnknownStream
}

// holdSaveOrSwap saves p through the per-slot PUT, and when the preset rules
// refuse it because the item already sits on another key, swaps the two keys
// instead: the held key gets the item, the other key gets what the held key
// had (or ends up empty). A held key is an explicit "put it HERE", and users
// expect the item to come over rather than a refusal. The app keeps its
// refusal, which offers the move itself. Nothing is ever deleted, so the
// loss-free promise of #836 holds.
func (s *Server) holdSaveOrSwap(ctx context.Context, p presets.Preset, kind string, live bool) (HoldSaved, error) {
	saved, err := s.holdSaveThroughPut(ctx, p, kind, live)
	var refusal *HoldSaveError
	if !errors.As(err, &refusal) || refusal.Code != "already-on-slot" ||
		refusal.OtherSlot < 1 || refusal.OtherSlot > 6 || refusal.OtherSlot == p.Slot {
		return saved, err
	}
	return s.holdSwapKeys(ctx, p.Slot, refusal.OtherSlot, kind)
}

// holdSwapKeys exchanges the presets on keys slot and other in one store
// write and brings the speaker's own keys along.
func (s *Server) holdSwapKeys(ctx context.Context, slot, other int, kind string) (HoldSaved, error) {
	item, ok := s.presets.Get(other)
	if !ok {
		return HoldSaved{}, fmt.Errorf("key %d no longer holds the item", other)
	}
	old, hadOld := s.presets.Get(slot)
	if err := s.presets.SwapSlots(slot, other); err != nil {
		return HoldSaved{}, err
	}
	item.Slot = slot
	old.Slot = other
	oldName := ""
	if hadOld {
		oldName = old.Name
	}
	// Same forensic rule as every other accepted write (#758).
	s.logger.Info("preset swap accepted",
		"slot", slot, "now", item.Name, "otherSlot", other, "otherNow", oldName,
		"type", item.Type, "from", "speaker-key", "ua", holdSaveUA)
	if !hadOld {
		// Same as the move: the tombstone keeps a trailing presetsUpdated
		// from resurfacing the emptied key as a foreign entry.
		s.forgetBoxPreset(other)
	}
	if s.boxHost != "" {
		boxCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		// The item goes onto its new key first, so it is never absent from
		// the speaker's own list.
		if err := s.writeStorePresetToBox(boxCtx, item); err != nil {
			s.logger.Warn("box preset sync failed", "slot", slot, "err", err)
		}
		if hadOld {
			if err := s.writeStorePresetToBox(boxCtx, old); err != nil {
				s.logger.Warn("box preset sync failed", "slot", other, "err", err)
			}
		} else if err := boxcli.RemovePreset(boxCtx, s.boxHost, other); err != nil {
			s.logger.Warn("preset swap: box-side RemovePreset failed", "slot", other, "err", err)
		}
		cancel()
	}
	return HoldSaved{Slot: slot, Kind: kind, Name: item.Name, SwappedWith: other, SwappedName: oldName}, nil
}

// liveLibraryFilePreset is the single library song STR handed the speaker
// directly, as the preset the app's library save stores (#1065): the file's
// own URL, title and art, and the media server's name when STR knows the
// server. The length is filled by the per-slot save from what the play
// learned (#978). ok is false unless the speaker plays exactly that file.
func (s *Server) liveLibraryFilePreset(slot int, loc string) (presets.Preset, bool) {
	s.lastPlayMu.Lock()
	lp := s.lastPlay
	s.lastPlayMu.Unlock()
	if lp == nil || lp.fromBox || lp.mime == "" || !isPlainHTTPURL(lp.boxURL) {
		return presets.Preset{}, false
	}
	if strings.TrimSpace(loc) != lp.boxURL {
		return presets.Preset{}, false
	}
	source := s.libraryServerNameFor(lp.boxURL)
	if source == "" {
		source = holdLibraryFallbackSource
	}
	name := strings.TrimSpace(lp.title)
	if name == "" {
		name = libraryFileNameFromURL(lp.boxURL)
	}
	return presets.Preset{
		Slot:      slot,
		Name:      name,
		StreamURL: lp.boxURL,
		Type:      "radio",
		Art:       lp.art,
		Source:    source,
	}, true
}

// holdLibraryFallbackSource names the server of a held library song when no
// registered media server matches its host. A library-song key needs a
// non-empty Source: that is what makes a press hand the file to the speaker
// directly (libraryFileMime) instead of through the radio proxy, which cannot
// play a finite file (#139).
const holdLibraryFallbackSource = "Music library"

// libraryFileNameFromURL is the last path element, for a file played without
// a title.
func libraryFileNameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(track)"
	}
	p := strings.TrimSuffix(u.Path, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[i+1:]
	}
	if un, err := url.PathUnescape(p); err == nil {
		p = un
	}
	if p == "" {
		return "(track)"
	}
	return p
}

// libraryServerNameFor names the registered media server a file URL lives
// on, matched by host, "" when none matches.
func (s *Server) libraryServerNameFor(fileURL string) string {
	if s.mediaServers == nil {
		return ""
	}
	fu, err := url.Parse(fileURL)
	if err != nil || fu.Hostname() == "" {
		return ""
	}
	for _, reg := range s.mediaServers.List() {
		lu, err := url.Parse(reg.Location)
		if err != nil {
			continue
		}
		if strings.EqualFold(lu.Hostname(), fu.Hostname()) {
			return reg.Name
		}
	}
	return ""
}

// samePresetItems reports whether two queue presets carry the same tracks in
// the same order.
func samePresetItems(a, b presets.Preset) bool {
	if len(a.Items) != len(b.Items) || a.Shuffle != b.Shuffle {
		return false
	}
	for i := range a.Items {
		if a.Items[i].URL != b.Items[i].URL {
			return false
		}
	}
	return true
}

// holdSaveThroughPut runs p through the per-slot PUT handler in process, so a
// hold obeys every rule an app save does without a second copy of them.
func (s *Server) holdSaveThroughPut(ctx context.Context, p presets.Preset, kind string, live bool) (HoldSaved, error) {
	return s.saveThroughPut(ctx, p, kind, live, holdSaveUA, "speaker-key")
}

// saveThroughPut is holdSaveThroughPut for any caller: ua and remote are what
// the PUT logs and decides its display notice on.
func (s *Server) saveThroughPut(ctx context.Context, p presets.Preset, kind string, live bool, ua, remote string) (HoldSaved, error) {
	if cur, have := s.presets.Get(p.Slot); have && holdSaveSameItem(cur, p) {
		return HoldSaved{Slot: p.Slot, Kind: kind, Name: cur.Name, Unchanged: true}, nil
	}
	body, err := json.Marshal(p)
	if err != nil {
		return HoldSaved{}, err
	}
	target := "/api/presets/" + strconv.Itoa(p.Slot)
	if live {
		target += "?save=live"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, bytes.NewReader(body))
	if err != nil {
		return HoldSaved{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", ua)
	req.RemoteAddr = remote
	rec := &holdSaveRecorder{header: http.Header{}}
	s.handlePresetSlot(rec, req)
	if rec.status() == http.StatusOK {
		var saved presets.Preset
		if err := json.Unmarshal(rec.body.Bytes(), &saved); err != nil || saved.Name == "" {
			saved.Name = p.Name
		}
		return HoldSaved{Slot: p.Slot, Kind: kind, Name: saved.Name}, nil
	}
	var refusal struct {
		Code  string `json:"code"`
		Slot  int    `json:"slot"`
		Name  string `json:"name"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.body.Bytes(), &refusal)
	if refusal.Code == "" {
		refusal.Code = "save-failed"
		refusal.Error = strings.TrimSpace(rec.body.String())
	}
	return HoldSaved{}, &HoldSaveError{Status: rec.status(), Code: refusal.Code,
		OtherSlot: refusal.Slot, OtherName: refusal.Name, Message: refusal.Error}
}

// holdSaveSameItem reports whether the key already holds the item a hold
// would store, so a repeated hold is no write: same Spotify context, or same
// library file.
func holdSaveSameItem(cur, p presets.Preset) bool {
	if cur.Type != p.Type {
		return false
	}
	if p.Type == "spotify" {
		return p.URI != "" && cur.URI == p.URI
	}
	return p.StreamURL != "" && cur.StreamURL == p.StreamURL
}

// holdSaveRecorder is the minimal ResponseWriter for the in-process PUT
// (net/http/httptest registers a command-line flag, so it stays out of the
// agent binary).
type holdSaveRecorder struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (r *holdSaveRecorder) Header() http.Header { return r.header }

func (r *holdSaveRecorder) WriteHeader(code int) {
	if r.code == 0 {
		r.code = code
	}
}

func (r *holdSaveRecorder) Write(b []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return r.body.Write(b)
}

func (r *holdSaveRecorder) status() int {
	if r.code == 0 {
		return http.StatusOK
	}
	return r.code
}

var (
	holdNowSourceRe   = regexp.MustCompile(`<nowPlaying[^>]*\ssource="([^"]*)"`)
	holdNowLocationRe = regexp.MustCompile(`<ContentItem[^>]*\slocation="([^"]*)"`)
)

// boxNowPlaying reads the speaker's active source and the location it plays.
func (s *Server) boxNowPlaying(ctx context.Context) (source, location string, ok bool) {
	if s.boxNowFn != nil {
		return s.boxNowFn(ctx)
	}
	b, err := s.boxNowPlayingDoc(ctx)
	if err != nil {
		return "", "", false
	}
	return parseNowPlayingSourceLocation(b)
}

// boxNowPlayingDoc fetches the speaker's :8090/now_playing document.
func (s *Server) boxNowPlayingDoc(ctx context.Context) ([]byte, error) {
	if s.boxNowDocFn != nil {
		return s.boxNowDocFn(ctx)
	}
	if s.boxHost == "" {
		return nil, errors.New("box host not configured")
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, "http://"+s.boxHost+":8090/now_playing", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 64<<10))
}

// parseNowPlayingSourceLocation pulls the source and the ContentItem location
// out of a now_playing document.
func parseNowPlayingSourceLocation(b []byte) (source, location string, ok bool) {
	m := holdNowSourceRe.FindSubmatch(b)
	if m == nil {
		return "", "", false
	}
	source = string(m[1])
	if l := holdNowLocationRe.FindSubmatch(b); l != nil {
		location = html.UnescapeString(string(l[1]))
	}
	return source, location, true
}

// SetHoldSaveTestFn wires the switch behind POST /api/debug/hold-save-test.
func (s *Server) SetHoldSaveTestFn(fn func(on bool, d time.Duration) time.Time) {
	s.holdSaveTestFn = fn
}

// handleHoldSaveTest answers POST /api/debug/hold-save-test
// {"acceptAppKeys":true,"minutes":10}. While it is on, a preset key sent over
// the speaker's own :8090/key counts for the hold-to-save as a physical one
// does, so the path can be verified without a hand on the speaker. Test only:
// it lives in RAM, switches itself off after the given minutes (at most 30)
// and is gone after a restart.
func (s *Server) handleHoldSaveTest(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if s.holdSaveTestFn == nil {
		http.Error(w, "hold-to-save is not wired", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		AcceptAppKeys bool `json:"acceptAppKeys"`
		Minutes       int  `json:"minutes"`
	}
	if !decodeJSONRequest(w, r, 1024, &req) {
		return
	}
	if req.Minutes <= 0 {
		req.Minutes = 10
	}
	if req.Minutes > 30 {
		req.Minutes = 30
	}
	until := s.holdSaveTestFn(req.AcceptAppKeys, time.Duration(req.Minutes)*time.Minute)
	s.logger.Info("hold-to-save: test switch for keys sent over the speaker's API",
		"acceptAppKeys", req.AcceptAppKeys, "until", until.Format(time.RFC3339), "from", r.RemoteAddr)
	out := map[string]any{"acceptAppKeys": req.AcceptAppKeys}
	if req.AcceptAppKeys {
		out["until"] = until.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, out)
}
