package webui

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/netutil"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/upnp"
)

// A single library track on a preset key (#1065).
//
// A track saved from the Library tab is stored as a radio-shaped preset whose
// StreamURL is the file on the LAN media server. The app has always recalled it
// by handing the speaker the file directly (#139). The speaker's own key did
// not: the key holds the slot as a native radio station, so the firmware
// fetched the file through the radio stream proxy like a live station.
//
// Measured on an ST10 (FW 27.0.6, 2026-10-06, an ALAC .m4a on MinimServer):
// the firmware's radio path opens the file, drops the connection after a few
// hundred KB and comes back with "Range: bytes=32768-". The proxy is built for
// live streams and treats the server's 206 answer as a failure, so the speaker
// logged AUDIO_ERROR_BAD_URL, went amber and gave up on every press. The same
// file started from the app played at once, and album and playlist keys were
// fine, because those are queue presets that RecallSlot already claims.
//
// So a single library track is claimed the same way: the press plays the file
// directly, exactly like the app's recall of the same key.

// libraryFileMime returns the MIME a library-track preset is played with when
// it is handed to the speaker directly, or "" when p is not one. A library
// track is a radio-shaped preset saved from the Library tab (Source names the
// media server) whose URL is a plain-HTTP file with a known audio extension.
func libraryFileMime(p presets.Preset) string {
	switch strings.ToLower(strings.TrimSpace(p.Type)) {
	case "spotify", "queue", presets.TypeNative:
		return ""
	}
	if p.Source == "" || !isPlainHTTPURL(p.StreamURL) {
		return ""
	}
	return mimeFromURL(p.StreamURL)
}

// playLibraryFilePresetLocked hands a library-track preset to the speaker as
// the file itself, records it as the last play and starts the recall verify.
// length is keyRecallLength's answer, taken before the caller locked the box.
// The caller holds boxCmdMu.
func (s *Server) playLibraryFilePresetLocked(ctx context.Context, p presets.Preset, mime string, length recallLength, recallStart time.Time) error {
	directURL := p.StreamURL
	dur := length.d
	// The length goes to the speaker as well, the way the plain play sends it:
	// without it the box reports total=0 and nothing can see the track end.
	var meta upnp.TrackMeta
	if dur > 0 {
		meta = upnp.TrackMeta{Duration: dur, Seekable: true}
	}
	// A native station the speaker plays swallows a bare push (#1065), so the
	// push, and the verify's re-push below, go through the handover helper.
	if err := s.pushOverNativeStation(ctx, p.Name, func() error {
		return s.renderer.PlayURLTrack(ctx, directURL, p.Name, p.Art, mime, meta)
	}); err != nil {
		return err
	}
	gen := s.setLastPlay(directURL, p.Name, p.Art, mime)
	name, art := p.Name, p.Art
	go s.verifyRecall(gen, recallStart, directURL, func(ctx context.Context, _ bool) {
		_ = s.pushOverNativeStation(ctx, name, func() error {
			return s.renderer.PlayURLTrack(ctx, directURL, name, art, mime, meta)
		})
	}, nil)
	// A finite file has no end event from the firmware: it freezes in
	// PLAY_STATE at the end (#380), so a track recalled from a key, from the
	// app or from the speaker's own button, kept showing as playing until the
	// auto-off timer (#1031). Give it the same end watch the plain play arms
	// (#844). With a known length the watch ends the track when that length
	// has run; without one only the frozen-position net could, which left the
	// key showing "playing" for ~20 s after the song had ended (#978).
	// The verify's re-push above does not bump the recall generation, so it
	// does not call the watch off.
	// A length that was not read from the file for this press is only a claim
	// (see keyRecallLength): the speaker echoes it back as its total, so its
	// position reaching that total proves nothing while it still climbs.
	s.armSingleTrackEnd(singleTrack{
		boxURL: directURL, title: name, art: art, mime: mime, meta: meta, dur: dur,
		claimedLength: dur > 0 && !length.fromFile,
	}, gen)
	return nil
}

// A key whose file changes under the same URL.
//
// A library key keeps the file's URL and, since v1.0.11, the length the file
// had when the key was saved (#978). Nothing refreshed that length. A nightly
// recording that a media server replaces under the same URL (MiniDLNA,
// .../MediaItems/<id>.m4a) kept the first night's 7:09 on the key, and every
// press since was stopped at 7:09: the speaker was handed 429 s, echoed it back
// as its total, and the end watch read a position at that total as the end of
// the file while the speaker was still playing it, its buffer full.
//
// So a press asks the file how long it is now. One HEAD tells its size; the
// length is read from the file (filelength.go) only when the size differs from
// the last read of that URL, so a file replaced in place gets a fresh length
// and an unchanged one costs one header round trip on the LAN. Nothing is
// written to the preset store: no flash write per key press.

const (
	// keyLengthCheckBudget bounds the whole check on a press: the HEAD, a
	// ranged GET when the HEAD says nothing, and the few small reads of a
	// length. A media server on the LAN answers in milliseconds; one that does
	// not falls back to the length stored on the key.
	keyLengthCheckBudget = 3 * time.Second
	// keyLengthDrift is how far the file's length may differ from the one
	// stored on the key before it counts as a different file. A server rounds
	// its DIDL length, so a second or two is noise.
	keyLengthDrift = 3 * time.Second
	// fileLengthsCap bounds the per-URL length cache; six keys and a handful
	// of custom URLs never come near it. The oldest entry goes first.
	fileLengthsCap = 32
)

// defaultKeyLengthClient is the client keyRecallLength asks the media server
// with when a Server sets no keyLengthClientFn: the probe's guarded client.
// The package tests replace it (TestMain) with one that returns nil, which
// skips the check, so a test with a placeholder server address never dials
// it; a test that exercises the check sets its own Server.keyLengthClientFn.
var defaultKeyLengthClient = func() *http.Client { return customFileProbeClient() }

// recallLength is the length a direct-file key recall plays with.
type recallLength struct {
	d time.Duration
	// fromFile: d was read exactly from the file at its URL for this press
	// (or from an earlier read of the same-sized file). A length stored on
	// the key, learned from an earlier play, or guessed from an MP3's bitrate
	// is only a claim about the file.
	fromFile bool
}

// fileLength is one cached read: the file's size when it was read and the
// length read from it (0 when none could be read).
type fileLength struct {
	size   int64
	length time.Duration
	exact  bool
}

// fileLengths caches the length read from each file, keyed by URL. The zero
// value is ready to use.
type fileLengths struct {
	mu    sync.Mutex
	byURL map[string]fileLength
	order []string
}

func (c *fileLengths) get(url string) (fileLength, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fl, ok := c.byURL[url]
	return fl, ok
}

func (c *fileLengths) put(url string, fl fileLength) {
	if url == "" || fl.size <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byURL == nil {
		c.byURL = make(map[string]fileLength)
	}
	if _, have := c.byURL[url]; !have {
		c.order = append(c.order, url)
		if len(c.order) > fileLengthsCap {
			delete(c.byURL, c.order[0])
			c.order = c.order[1:]
		}
	}
	c.byURL[url] = fl
}

// currentFileLength asks the server for the size of the file at url now and
// returns the length of that file: from the cache when the size matches the
// last read, else read from the file. readNow says the length was read on this
// call. ok is false when the server gave no size (unreachable, a stream, a
// server that serves no ranges); a size without a readable length is ok with
// length 0.
func currentFileLength(ctx context.Context, cl *http.Client, cache *fileLengths, url, mime string) (fl fileLength, readNow, ok bool) {
	if netutil.SafeHTTPURL(url) != nil {
		return fileLength{}, false, false
	}
	verdict, size := probeFileStep(ctx, cl, http.MethodHead, url)
	if verdict == fileUnknown {
		verdict, size = probeFileStep(ctx, cl, http.MethodGet, url)
	}
	if verdict != fileFinite || size <= 0 {
		return fileLength{}, false, false
	}
	if c, hit := cache.get(url); hit && c.size == size {
		return c, false, true
	}
	d, exact := readFileLengthKind(ctx, cl, url, mime, size)
	fl = fileLength{size: size, length: d, exact: exact}
	// A read cut short by the budget (a NAS disk still spinning up) says
	// nothing about the file; caching it would pin length 0 on this URL until
	// the file's size changes. Only a read that ran to its end is kept, so an
	// unreadable format is not re-read on every press either.
	if d > 0 || ctx.Err() == nil {
		cache.put(url, fl)
	}
	return fl, true, true
}

// keyRecallLength is the length a direct-file key is played with: the length
// of the file at its URL now, when it can be read exactly, else the length
// stored on the key or learned before (libraryPresetLength). Asks the media
// server; call it before taking boxCmdMu.
//
// A bitrate guess for an MP3 without a frame count does not replace a stored
// length: for a VBR file it is the less reliable of the two. The stored
// length then stays a claim, and the end watch does not take the speaker's
// echo of it as proof of the end.
func (s *Server) keyRecallLength(ctx context.Context, p presets.Preset, mime string) recallLength {
	stored := s.libraryPresetLength(p)
	clientFn := s.keyLengthClientFn
	if clientFn == nil {
		clientFn = defaultKeyLengthClient
	}
	cl := clientFn()
	if mime == "" || cl == nil {
		return recallLength{d: stored}
	}
	ctx, cancel := context.WithTimeout(ctx, keyLengthCheckBudget)
	defer cancel()
	start := time.Now()
	fl, readNow, ok := currentFileLength(ctx, cl, &s.fileLens, p.StreamURL, mime)
	if !ok || fl.length <= 0 || !fl.exact {
		if readNow || !ok {
			s.logger.Info("library key: no exact length from the file, using the stored one",
				"slot", p.Slot, "name", p.Name, "reachable", ok, "fileSec", int(fl.length.Seconds()),
				"storedSec", int(stored.Seconds()), "checkMs", time.Since(start).Milliseconds())
		}
		return recallLength{d: stored}
	}
	if readNow {
		if keyLen := time.Duration(p.DurationSec) * time.Second; keyLen > 0 && absDur(fl.length-keyLen) > keyLengthDrift {
			s.logger.Info("library key: the file changed since the key was saved, playing it with its current length",
				"slot", p.Slot, "name", p.Name, "keySec", p.DurationSec,
				"fileSec", int(fl.length.Seconds()), "size", fl.size)
		} else {
			s.logger.Info("library key: read the file's length",
				"slot", p.Slot, "name", p.Name, "fileSec", int(fl.length.Seconds()),
				"size", fl.size, "checkMs", time.Since(start).Milliseconds())
		}
	}
	return recallLength{d: fl.length, fromFile: true}
}

// libraryPresetLength is the length a library-track recall plays with: the one
// stored on the preset, else the one this agent learned when the same file was
// last played directly (a key saved before presets kept a length). 0 when
// neither is known. The media server's own metadata is not asked: the preset
// keeps only the file URL, not the server object a browse would need.
func (s *Server) libraryPresetLength(p presets.Preset) time.Duration {
	if p.DurationSec > 0 {
		return time.Duration(p.DurationSec) * time.Second
	}
	if d := s.trackLens.get(p.StreamURL); d > 0 {
		s.logger.Info("library key without a stored length: using the length learned from an earlier play",
			"name", p.Name, "trackSec", int(d.Seconds()))
		return d
	}
	// A custom URL preset that is a file: the length its probe read from the
	// file itself (#830, customfile.go).
	if d := s.customFileLength(p.StreamURL); d > 0 {
		return d
	}
	return 0
}

// fillLibraryPresetLength gives a library-track preset that arrives without a
// length the one this agent learned from playing the same file, so a save
// from an app that does not send it (an older desktop app, the phone remote's
// "save what plays") still stores one. It reports whether it filled one.
func (s *Server) fillLibraryPresetLength(p *presets.Preset) bool {
	if p.DurationSec > 0 || libraryFileMime(*p) == "" {
		return false
	}
	d := s.trackLens.get(p.StreamURL)
	if d < time.Second {
		return false
	}
	p.DurationSec = int(d / time.Second)
	return true
}

// Bounds on a length learned from where a track froze. A frozen position
// under learnedLengthMin is far more likely a stream that never really got
// going than a song; one over learnedLengthMax is no song either. Both are
// left unlearned, and the frozen-position net keeps ending those plays.
const (
	learnedLengthMin = 5 * time.Second
	learnedLengthMax = 4 * time.Hour
)

// learnSingleTrackLength runs when the end watch of a lone file decided the
// track is over (#978). A music-library song saved on a key before v1.0.11
// carries no length, so every press of that key ends only on the
// frozen-position net, 15 s after the song has actually finished. The position
// the box froze on IS the song's length, so it is remembered for this agent
// run and written once into every library key that holds this file without a
// length. The next press of such a key ends on time and never comes back here
// (its dur is no longer 0).
//
// Only the frozen-position net teaches: the wall-clock net needs a length to
// fire, and a known one is not relearned. One store write covers every
// matching key, and nothing is written when no key needs it.
//
// ranAhead says the box reported a position for this play further than it can
// have played: that position is not the song's length (a FRITZ!Box MP3 froze
// at 120 s sixteen seconds in), so nothing is learned from it.
func (s *Server) learnSingleTrackLength(tr singleTrack, net endNet, lastPos time.Duration, ranAhead bool) {
	if net != endNetFrozen || tr.dur > 0 || tr.boxURL == "" {
		return
	}
	if ranAhead {
		s.logger.Info("single track: not learning a length from where it froze, the position ran ahead of real time",
			"title", tr.title, "posSec", int(lastPos.Seconds()))
		return
	}
	if lastPos < learnedLengthMin || lastPos > learnedLengthMax {
		s.logger.Info("single track: not learning a length from where it froze, the position is implausible",
			"title", tr.title, "posSec", int(lastPos.Seconds()))
		return
	}
	s.trackLens.put(tr.boxURL, lastPos)
	if s.presets == nil {
		return
	}
	sec := int(lastPos / time.Second)
	var fill []presets.Preset
	for _, p := range s.presets.All() {
		if p.StreamURL != tr.boxURL || p.DurationSec != 0 || libraryFileMime(p) == "" {
			continue
		}
		p.DurationSec = sec
		fill = append(fill, p)
	}
	if len(fill) == 0 {
		s.logger.Info("single track: learned its length from where it froze",
			"title", tr.title, "trackSec", sec, "keysUpdated", 0)
		return
	}
	slots := make([]int, 0, len(fill))
	for _, p := range fill {
		slots = append(slots, p.Slot)
	}
	if err := s.presets.SetSlots(fill); err != nil {
		// The learned length still serves this agent run (libraryPresetLength
		// falls back to it); only a restart forgets it again.
		s.logger.Warn("single track: could not store the learned length on its keys",
			"title", tr.title, "trackSec", sec, "slots", slots, "err", err)
		return
	}
	s.logger.Info("single track: learned its length from where it froze and stored it on its keys",
		"title", tr.title, "trackSec", sec, "slots", slots)
}

// trackLengthsCap bounds the learned lengths. A household plays a few dozen
// files a day at most; the oldest entry goes first.
const trackLengthsCap = 64

// trackLengths remembers the length of each library file played directly,
// keyed by its URL. The zero value is ready to use.
type trackLengths struct {
	mu    sync.Mutex
	byURL map[string]time.Duration
	order []string
}

func (t *trackLengths) put(url string, d time.Duration) {
	if url == "" || d <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.byURL == nil {
		t.byURL = make(map[string]time.Duration)
	}
	if _, have := t.byURL[url]; !have {
		t.order = append(t.order, url)
		if len(t.order) > trackLengthsCap {
			delete(t.byURL, t.order[0])
			t.order = t.order[1:]
		}
	}
	t.byURL[url] = d
}

func (t *trackLengths) get(url string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.byURL[url]
}

// recallLibraryFileSlot is RecallSlot's branch for a single library track. It
// reports whether it claimed the press; a slot that holds anything else is
// left to the caller.
//
// A custom URL preset that a probe shows to be a finite file is claimed the
// same way (#830): through the radio proxy the speaker has no position to
// return to, and Play/Pause on the remote started the file over.
func (s *Server) recallLibraryFileSlot(ctx context.Context, slot int, p presets.Preset) bool {
	if s.renderer == nil {
		return false
	}
	// Same as the app's recall: a server that moved takes the key with it
	// (#1366, #919), so the key goes to the server's current address first.
	// A music server that is off is not acted on here (#1339): a hardware
	// press has no screen to say so, and the press goes ahead as it always did.
	_ = s.healLibraryKeyIfMoved(ctx, slot, &p)
	mime := s.directFileMime(ctx, p)
	if mime == "" {
		return false
	}
	// The file's length is asked for here, outside the box lock, like the MIME.
	length := s.keyRecallLength(ctx, p, mime)
	// The press supersedes any verify still running from an earlier one, and a
	// library queue that is still playing must not advance over this track
	// later (the same reasons NoteLastPlay gives for the radio keys).
	s.bumpRecallGen()
	s.stopQueue("a hardware preset key was pressed")
	s.ensureBoxReady(ctx)
	recallStart := time.Now()
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	s.logger.Info("preset slot recall (hardware): library track, playing the file directly",
		"slot", slot, "mime", mime, "custom", p.Source == "")
	if err := s.playLibraryFilePresetLocked(ctx, p, mime, length, recallStart); err != nil {
		s.logger.Warn("hardware library track recall failed", "slot", slot, "err", err)
	}
	return true
}
