package webui

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/boxcli"
	"github.com/JRpersonal/streborn/internal/netutil"
)

// Audio-notification ("announcement") support (#125), the cloud-free replacement
// for the firmware's /speaker TTS endpoint, which returns 403 without the dead
// Bose cloud app_key. STR drives it locally instead: it fetches the audio (so the
// modern TLS the box's UPnP player refuses is terminated here), plays it over
// UPnP AVTransport, and restores whatever was playing. Verified end to end on a
// Portable; see the spike notes for the hard constraints this design works around
// (the box UPnP player rejects https URIs; the radio stream proxy loops a finite
// file).

const (
	// maxAnnounceBytes caps a fetched announcement so a bad or huge URL cannot
	// exhaust the box's small RAM.
	maxAnnounceBytes = 8 << 20 // 8 MB
	// ttsChunkLimit is Google Translate TTS's practical per-request text length;
	// longer text is split and the MP3 chunks are concatenated.
	ttsChunkLimit = 180
	// announceAudioPath is the loopback URL the box pulls the announcement from.
	// Same host:port the stream proxy uses (the agent serves :8888; the box plays
	// it over loopback).
	announceAudioURL = "http://127.0.0.1:8888/announce/audio"
)

// Announcement timing. Variables so the tests do not wait for real seconds.
var (
	// announcePoll is the gap between two now_playing reads while the clip
	// starts and plays.
	announcePoll = 500 * time.Millisecond
	// announceStartPolls bounds the wait for the clip to start (12 x 500 ms).
	announceStartPolls = 12
	// announceTail is added to the clip's own length for the box's buffer.
	announceTail = 2 * time.Second
	// announceUnknownWait is the safe upper bound for a clip whose length
	// could not be read.
	announceUnknownWait = 20 * time.Second
	// announceWakeTimeout bounds the wake of a speaker that was in standby.
	announceWakeTimeout = 8 * time.Second
)

// nowPlayingSnapshot is the slice of the box's now_playing STR needs to restore
// playback after an announcement.
type nowPlayingSnapshot struct {
	Source     string
	Location   string
	ItemName   string
	PlayStatus string
}

// handleAnnounceAudio serves the most recently fetched announcement audio to the
// box exactly once, with a Content-Length so the player stops at the end. This is
// deliberately NOT the radio stream proxy: that reconnects on EOF (for endless
// live radio) and would loop a finite announcement. Loopback-only by nature; the
// box pulls it over UPnP.
func (s *Server) handleAnnounceAudio(w http.ResponseWriter, r *http.Request) {
	s.announceMu.Lock()
	audio := s.announceAudio
	mime := s.announceMime
	s.announceMu.Unlock()
	if len(audio) == 0 {
		http.Error(w, "no announcement queued", http.StatusNotFound)
		return
	}
	if mime == "" {
		mime = "audio/mpeg"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(audio)))
	w.Header().Set("Accept-Ranges", "none")
	_, _ = w.Write(audio)
}

// handleAnnounce plays a short spoken/audio notification on the speaker and then
// restores whatever was playing (or returns it to standby). Body:
//
//	POST /api/announce {"text":"...", "lang":"de", "url":"...", "volume":20}
//
// Either text (spoken via Google Translate TTS) or url (any audio URL, http or
// https) is required. lang defaults to en; volume (1..100) is optional and is
// restored afterwards. This is the expert/automation entry point (Home Assistant,
// scripts); the desktop app wraps it.
func (s *Server) handleAnnounce(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if s.renderer == nil || s.boxHost == "" {
		http.Error(w, "box not configured", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Text   string `json:"text"`
		Lang   string `json:"lang"`
		URL    string `json:"url"`
		Volume int    `json:"volume"`
	}
	if !decodeJSONRequest(w, r, 8<<10, &body) {
		return
	}
	body.Text = strings.TrimSpace(body.Text)
	body.URL = strings.TrimSpace(body.URL)
	if body.Text == "" && body.URL == "" {
		http.Error(w, "text or url required", http.StatusBadRequest)
		return
	}
	lang := strings.TrimSpace(body.Lang)
	if lang == "" {
		lang = "en"
	}

	// Fetch the audio up front (TLS terminated here; the box's UPnP player cannot
	// fetch https itself). Do this before touching playback so a bad URL fails
	// without having interrupted anything.
	audio, mime, err := fetchAnnounceAudio(r.Context(), body.Text, lang, body.URL)
	if err != nil {
		s.logger.Warn("announce: fetch failed", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "could not fetch announcement audio", "detail": err.Error()})
		return
	}
	// Silence in front of the speech, so the speaker's late amplifier unmute
	// does not swallow the first word (#826). Covers TTS and MP3 url clips;
	// any other format is left untouched.
	audio = withSilentLeadIn(audio, mime, announceLeadIn)
	title := body.Text
	if title == "" {
		title = "Announcement"
	}
	if len(title) > 64 {
		title = strings.TrimSpace(title[:64])
	}
	volume := body.Volume

	// Respond now and run the interrupt/play/resume cycle in the background. The
	// cycle takes ~5 s (play + resume), and holding the HTTP response open that
	// long made the desktop app's POST connection drop on BCO boxes: it arrives
	// via the :17008 PREROUTING REDIRECT, the long-held connection was reset
	// before the response, boxDo then fell through to :8888 (chipset-refused) and
	// surfaced "connection refused" even though the announcement had played fine
	// (Jens, 2026-06-17, taigan Portable). The audio fetch above stays
	// synchronous so a bad URL / TTS failure is still reported to the caller.
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "queued": true, "bytes": len(audio)})

	go func() {
		// Serialise against other box commands (play, volume) for the whole
		// interrupt/resume cycle so nothing interleaves mid-sequence. Publish the
		// audio for the box to fetch under the same lock, so a second announcement
		// cannot swap it out mid-play.
		s.boxCmdMu.Lock()
		defer s.boxCmdMu.Unlock()
		s.announceMu.Lock()
		s.announceAudio = audio
		s.announceMime = mime
		s.announceMu.Unlock()

		// Detached from the request: r.Context() is cancelled once the handler
		// returned above, so use a fresh background context for the whole cycle.
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
		defer cancel()
		s.runAnnouncement(ctx, audio, mime, title, volume)
	}()
}

// runAnnouncement is the interrupt/play/resume cycle behind handleAnnounce.
// The caller holds boxCmdMu and has published the audio.
func (s *Server) runAnnouncement(ctx context.Context, audio []byte, mime, title string, volume int) {
	prev := s.snapshotNowPlaying(ctx)
	wasStandby := prev.Source == "STANDBY" || prev.Source == ""
	changedVol := volume > 0
	prevVol := -1
	if changedVol {
		prevVol = s.readVolume(ctx)
	}

	if wasStandby {
		if err := boxcli.WakeAndWait(ctx, s.boxHost, announceWakeTimeout, s.logger); err != nil {
			s.logger.Warn("announce: wake failed, playing anyway", "err", err)
		}
	}

	if err := s.startAnnouncementClip(ctx, prev, title, mime, changedVol, volume); err != nil {
		s.logger.Warn("announce: play failed", "err", err,
			"prevSource", prev.Source, "prevLocation", truncateLoc(prev.Location))
		if errors.Is(err, errNativeStationKept) {
			// The handover already put the native station back on with the
			// speaker's own PLAY key; only the volume is left to undo.
			if changedVol && prevVol >= 0 {
				_ = s.announceSetVolume(ctx, prevVol)
			}
			return
		}
		s.restoreAfterAnnounce(ctx, prev, prevVol, wasStandby, changedVol)
		return
	}
	estDur := mp3DurationSec(audio)
	s.logger.Info("announce: playing", "bytes", len(audio), "estSec", fmt.Sprintf("%.1f", estDur), "title", title,
		"wasStandby", wasStandby, "prevSource", prev.Source, "prevStatus", prev.PlayStatus,
		"prevLocation", truncateLoc(prev.Location))

	s.waitAnnounceDone(ctx, estDur)
	s.restoreAfterAnnounce(ctx, prev, prevVol, wasStandby, changedVol)
}

// startAnnouncementClip stops what is playing, then sets the announcement
// volume, then plays the clip, in that order.
//
// The order is the fix for #1359: the volume used to be raised while the old
// source was still playing, and the clip push bypassed the native-station
// handover, so a native radio station blasted at announcement volume until the
// clip finally took over. Now the source is stopped first: a UPnP stream with
// a transport Stop, a station the speaker plays itself with its own STOP key
// (pushOverNativeStation for LOCAL_INTERNET_RADIO, which also checks that the
// speaker let go of it). The volume is only set once the clip is loaded and
// nothing else is audible. A push the box refuses in the wrong transport state
// is cleared and tried once more, like every other play.
func (s *Server) startAnnouncementClip(ctx context.Context, prev nowPlayingSnapshot, title, mime string, changedVol bool, volume int) error {
	src := strings.TrimSpace(prev.Source)
	switch {
	case strings.EqualFold(src, "UPNP"):
		if err := s.renderer.Stop(ctx); err != nil {
			s.logger.Debug("announce: transport stop before the clip returned", "err", err)
		}
	case boxOwnedSource(src) && !strings.EqualFold(src, nativeRadioSource):
		// Bluetooth, AUX and the like: the speaker plays them itself, so its
		// own STOP key is the way to silence them. A native radio station is
		// stopped by pushOverNativeStation below.
		s.stopNativeStation(ctx)
	}
	push := func() error {
		if err := s.renderer.SetURIMime(ctx, announceAudioURL, title, "", mime); err != nil {
			return fmt.Errorf("announce clip seturi: %w", err)
		}
		if changedVol {
			if err := s.announceSetVolume(ctx, clampVolume(volume)); err != nil {
				s.logger.Warn("announce: setting the announcement volume failed", "err", err)
			}
		}
		if err := s.renderer.Play(ctx); err != nil {
			return fmt.Errorf("announce clip play: %w", err)
		}
		return nil
	}
	return s.pushOverNativeStation(ctx, title, func() error {
		err := push()
		if !isWrongStateRejection(err) {
			return err
		}
		s.logger.Warn("announce: the speaker refused the clip in a wrong transport state, clearing the transport and pushing again", "err", err)
		s.clearTransportForReplay(ctx)
		return push()
	})
}

// truncateLoc shortens a now_playing location for the log: a native station
// descriptor is a long base64 blob.
func truncateLoc(loc string) string {
	const limit = 80
	if len(loc) <= limit {
		return loc
	}
	return loc[:limit] + "..."
}

// snapshotNowPlaying reads the box's current now_playing so STR can restore it
// after the announcement. Best-effort: a read error yields an empty snapshot.
func (s *Server) snapshotNowPlaying(ctx context.Context) nowPlayingSnapshot {
	if s.announceNowFn != nil {
		return s.announceNowFn(ctx)
	}
	return s.readBoxSnapshot(ctx)
}

// readVolume returns the box's current target volume, or -1 on error.
func (s *Server) readVolume(ctx context.Context) int {
	if s.announceVolumeFn != nil {
		return s.announceVolumeFn(ctx)
	}
	b, err := boxGet(ctx, "http://"+s.boxHost+":8090/volume", 4<<10)
	if err != nil {
		return -1
	}
	var v struct {
		Target int `xml:"targetvolume"`
	}
	if xml.Unmarshal(b, &v) == nil {
		return v.Target
	}
	return -1
}

// announceSetVolume writes the box volume for an announcement and its restore.
func (s *Server) announceSetVolume(ctx context.Context, vol int) error {
	if s.announceSetVolumeFn != nil {
		return s.announceSetVolumeFn(ctx, vol)
	}
	return boxapi.New(s.boxHost).SetVolume(ctx, vol)
}

// waitAnnounceDone blocks until the box has played the announcement to its end.
// The Portable does NOT reliably report STOP for a finite UPnP track (it sits in
// PLAY_STATE on the finished clip), so the clip's own duration drives the timing:
// wait the duration plus a small tail for the box's buffer. A real STOP, or the
// box moving off the announcement URL (firmware that does report it), ends the
// wait early. estDur<=0 (unparsable audio) falls back to a safe fixed cap.
func (s *Server) waitAnnounceDone(ctx context.Context, estDur float64) {
	// Wait for it to actually start playing (max 6s).
	for i := 0; i < announceStartPolls && ctx.Err() == nil; i++ {
		np := s.snapshotNowPlaying(ctx)
		if strings.Contains(np.Location, "/announce/audio") && np.PlayStatus == "PLAY_STATE" {
			break
		}
		time.Sleep(announcePoll)
	}
	wait := announceUnknownWait
	if estDur > 0 {
		wait = time.Duration(estDur*float64(time.Second)) + announceTail
	}
	deadline := time.Now().Add(wait)
	for ctx.Err() == nil && time.Now().Before(deadline) {
		np := s.snapshotNowPlaying(ctx)
		if !strings.Contains(np.Location, "/announce/audio") {
			return
		}
		if np.PlayStatus == "STOP_STATE" {
			return
		}
		time.Sleep(announcePoll)
	}
}

// mp3DurationSec estimates the playback length of a CBR MP3 (Google Translate TTS
// is constant-bitrate) from its first frame header: duration = bytes / (bitrate/8).
// Returns 0 if no valid frame header is found, so the caller uses a fixed cap.
func mp3DurationSec(b []byte) float64 {
	mpeg1L3 := []int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	mpeg2L3 := []int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
	limit := len(b) - 4
	if limit > 1<<16 {
		limit = 1 << 16 // the first frame is near the start; don't scan the whole clip
	}
	for i := 0; i < limit; i++ {
		if b[i] != 0xFF || b[i+1]&0xE0 != 0xE0 {
			continue
		}
		ver := (b[i+1] >> 3) & 0x03   // 3=MPEG1, 2=MPEG2, 0=MPEG2.5
		layer := (b[i+1] >> 1) & 0x03 // 1=Layer III
		if layer != 0x01 {
			continue
		}
		brIdx := (b[i+2] >> 4) & 0x0F
		if brIdx == 0 || brIdx == 0x0F {
			continue
		}
		kbps := mpeg2L3[brIdx]
		if ver == 0x03 {
			kbps = mpeg1L3[brIdx]
		}
		if kbps == 0 {
			continue
		}
		return float64(len(b)) * 8 / float64(kbps*1000)
	}
	return 0
}

// restoreAfterAnnounce puts the box back the way it was: the finished clip is
// stopped so it releases the transport, the volume goes back, and then either
// the speaker returns to standby (it was asleep before) or the previous source
// is resumed, see resumeAfterAnnounce.
func (s *Server) restoreAfterAnnounce(ctx context.Context, prev nowPlayingSnapshot, prevVol int, wasStandby, changedVol bool) {
	if s.renderer != nil {
		if err := s.renderer.Stop(ctx); err != nil {
			s.logger.Debug("announce: transport stop after the clip returned", "err", err)
		}
	}
	if changedVol && prevVol >= 0 {
		if err := s.announceSetVolume(ctx, prevVol); err != nil {
			s.logger.Warn("announce: volume restore failed", "err", err, "volume", prevVol)
		}
	}
	if wasStandby {
		if _, err := boxcli.Send(ctx, s.boxHost, "sys power"); err != nil {
			s.logger.Warn("announce: standby restore failed", "err", err)
		}
		return
	}
	s.resumeAfterAnnounce(ctx, prev)
}

// announceWasPlaying reports whether a now_playing status means the source was
// audible when the announcement interrupted it.
func announceWasPlaying(status string) bool {
	return status == "PLAY_STATE" || status == "BUFFERING_STATE"
}

// resumeAfterAnnounce brings back the source the announcement interrupted, the
// way that source is started, not by re-pushing whatever location the box
// reported (#1359).
//
//   - Only a source that was playing comes back. A paused or stopped one stays
//     as it was.
//   - A native station (LOCAL_INTERNET_RADIO) reports a relative
//     "/station?data=..." descriptor, which is not a URL the UPnP player can
//     fetch. It goes back through the speaker's own /select, the same call the
//     app's preset recall uses, with the stream the descriptor names as the
//     fallback.
//   - A UPnP stream STR started is pushed again through the wrong-state repair.
//   - Anything else (Bluetooth, AUX, sources the speaker owns) is left alone:
//     STR cannot start them, and pushing their location over UPnP only fails.
func (s *Server) resumeAfterAnnounce(ctx context.Context, prev nowPlayingSnapshot) {
	if !announceWasPlaying(prev.PlayStatus) {
		s.logger.Info("announce: the previous source was not playing, leaving it as it was",
			"source", prev.Source, "status", prev.PlayStatus)
		return
	}
	switch src := strings.TrimSpace(prev.Source); {
	case strings.EqualFold(src, nativeRadioSource):
		s.resumeNativeAfterAnnounce(ctx, prev)
	case strings.EqualFold(src, "UPNP"):
		s.resumeUPnPAfterAnnounce(ctx, prev)
	default:
		s.logger.Info("announce: the previous source is not one STR can resume, leaving it",
			"source", src, "location", truncateLoc(prev.Location))
	}
}

// resumeNativeAfterAnnounce brings a native station back: /select first, then
// the stream its descriptor names (or the last play, when it is that station)
// over UPnP.
func (s *Server) resumeNativeAfterAnnounce(ctx context.Context, prev nowPlayingSnapshot) {
	if prev.Location != "" {
		err := s.selectNativeStation(ctx, prev.Location, prev.ItemName)
		if err == nil {
			s.logger.Info("announce: resumed the native station through /select",
				"title", prev.ItemName, "location", truncateLoc(prev.Location))
			return
		}
		s.logger.Warn("announce: /select of the native station failed, falling back to a UPnP push",
			"err", err, "title", prev.ItemName)
	}
	streamURL, title := "", prev.ItemName
	if st, ok := DecodeNativeStation(prev.Location); ok {
		streamURL = st.StreamURL
		if title == "" {
			title = st.Name
		}
	} else if isAbsoluteHTTP(prev.Location) {
		streamURL = prev.Location
	}
	art, mime := "", ""
	if streamURL == "" {
		if lp, ok := s.lastPlayCopy(); ok && lp.boxURL != "" &&
			(lp.fromBox || (title != "" && strings.EqualFold(lp.title, title))) {
			streamURL, art, mime = lp.boxURL, lp.art, lp.mime
			if title == "" {
				title = lp.title
			}
		}
	}
	if streamURL == "" {
		s.logger.Warn("announce: no stream to resume the native station with",
			"title", prev.ItemName, "location", truncateLoc(prev.Location))
		return
	}
	if err := s.playWithWrongStateRepair(ctx, streamURL, title, art, mime); err != nil {
		s.logger.Warn("announce: resume failed", "err", err, "url", streamURL)
		return
	}
	s.logger.Info("announce: resumed the native station over UPnP", "url", streamURL, "title", title)
}

// resumeUPnPAfterAnnounce pushes the UPnP stream STR was playing again, with
// the artwork and mime of the last play when it is that stream.
func (s *Server) resumeUPnPAfterAnnounce(ctx context.Context, prev nowPlayingSnapshot) {
	loc := prev.Location
	if !isAbsoluteHTTP(loc) || strings.Contains(loc, "/announce/audio") {
		s.logger.Info("announce: the previous UPnP item has no stream to resume",
			"location", truncateLoc(loc))
		return
	}
	title, art, mime := prev.ItemName, "", guessAudioMime(loc)
	if lp, ok := s.lastPlayCopy(); ok && lp.boxURL == loc {
		art, mime = lp.art, lp.mime
		if title == "" {
			title = lp.title
		}
	}
	if err := s.playWithWrongStateRepair(ctx, loc, title, art, mime); err != nil {
		s.logger.Warn("announce: resume failed", "err", err, "url", loc)
		return
	}
	s.logger.Info("announce: resumed previous playback", "url", loc, "title", title)
}

// lastPlayCopy returns a copy of the last play, if there is one.
func (s *Server) lastPlayCopy() (lastPlayInfo, bool) {
	s.lastPlayMu.Lock()
	defer s.lastPlayMu.Unlock()
	if s.lastPlay == nil {
		return lastPlayInfo{}, false
	}
	return *s.lastPlay, true
}

// isAbsoluteHTTP reports whether loc is a URL the speaker's UPnP player can
// fetch.
func isAbsoluteHTTP(loc string) bool {
	l := strings.ToLower(strings.TrimSpace(loc))
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}

// fetchAnnounceAudio resolves the announcement to raw audio bytes: a caller URL
// is fetched as-is; otherwise text is spoken via Google Translate TTS, chunked to
// its length limit and the MP3 parts concatenated.
func fetchAnnounceAudio(ctx context.Context, text, lang, rawURL string) ([]byte, string, error) {
	var urls []string
	if rawURL != "" {
		if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
			return nil, "", fmt.Errorf("url must be http(s)")
		}
		urls = []string{rawURL}
	} else {
		for _, chunk := range chunkText(text, ttsChunkLimit) {
			urls = append(urls, googleTTSURL(chunk, lang))
		}
	}
	var all []byte
	mime := "audio/mpeg"
	for _, u := range urls {
		remaining := maxAnnounceBytes - len(all)
		b, ct, err := fetchAudioURL(ctx, u, remaining)
		if err != nil {
			return nil, "", err
		}
		all = append(all, b...)
		if rawURL != "" && ct != "" {
			mime = ct
		}
	}
	if len(all) == 0 {
		return nil, "", fmt.Errorf("empty audio")
	}
	return all, mime, nil
}

// googleTTSURL builds a keyless Google Translate TTS request for one text chunk.
func googleTTSURL(text, lang string) string {
	q := url.Values{}
	q.Set("ie", "UTF-8")
	q.Set("client", "tw-ob")
	q.Set("tl", lang)
	q.Set("q", text)
	return "https://translate.google.com/translate_tts?" + q.Encode()
}

// fetchAudioURL GETs an audio URL with a browser-ish User-Agent (Google Translate
// rejects some clients) and a size cap.
func fetchAudioURL(ctx context.Context, u string, limit int) ([]byte, string, error) {
	if limit <= 0 {
		return nil, "", fmt.Errorf("announcement too large")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (STR announcement)")
	cl := &http.Client{Timeout: 25 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("audio source returned %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, "", err
	}
	if len(b) > limit {
		return nil, "", fmt.Errorf("announcement too large")
	}
	return b, resp.Header.Get("Content-Type"), nil
}

// chunkText splits text into <=limit-byte pieces on word boundaries so each fits
// Google Translate TTS's per-request limit. A single over-long word is hard-cut.
func chunkText(text string, limit int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if len(text) <= limit {
		return []string{text}
	}
	var out []string
	cur := ""
	for _, word := range strings.Fields(text) {
		for len(word) > limit { // pathological single word
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			out = append(out, word[:limit])
			word = word[limit:]
		}
		switch {
		case cur == "":
			cur = word
		case len(cur)+1+len(word) > limit:
			out = append(out, cur)
			cur = word
		default:
			cur += " " + word
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// guessAudioMime infers a UPnP res mime for a stream URL we are re-pushing, so the
// box accepts the restore metadata.
func guessAudioMime(loc string) string {
	l := strings.ToLower(loc)
	switch {
	case strings.Contains(l, "/spotify/") || strings.Contains(l, ".ogg"):
		return "audio/ogg"
	case strings.Contains(l, ".flac"):
		return "audio/flac"
	case strings.Contains(l, ".wav"):
		return "audio/wav"
	case strings.Contains(l, ".aac"):
		return "audio/aac"
	default:
		return "audio/mpeg"
	}
}

// clampVolume bounds a requested volume to the box's 0..100 range.
func clampVolume(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// boxGet is a small GET helper for the box's read-only :8090 endpoints, for the
// speaker this agent runs ON. Its host comes from the agent's own configuration,
// so there is nothing for a caller to steer.
func boxGet(ctx context.Context, u string, limit int64) ([]byte, error) {
	return boxGetWith(ctx, &http.Client{Timeout: 4 * time.Second}, u, limit)
}

// boxGetPeer is the same read aimed at ANOTHER speaker, whose address arrived
// from outside: a zone member, a group the app formed, anything that reached the
// agent's unauthenticated LAN port. The port and path are fixed, so the worst a
// caller could do is point the speaker at a host of their choosing, and the
// dialer refuses the ones that matter (loopback, link-local, metadata). Private
// LAN addresses stay allowed, because that is where the other speakers live.
//
// Same rule as the artwork proxy earned on 2026-08-04: guard at DIAL time, not
// on the URL string, so a name that resolves inwards is caught too.
func boxGetPeer(ctx context.Context, u string, limit int64) ([]byte, error) {
	return boxGetWith(ctx, netutil.GuardedClient(4*time.Second), u, limit)
}

// peerBoxURL builds the :8090 URL for ANOTHER speaker, joining the host instead
// of interpolating it.
//
// The dial guard alone is not enough here, and that is worth spelling out. It
// asks which ADDRESS is being connected to; it cannot ask whether the address
// was smuggled. A member address of "192.0.2.9:80/removeGroup?x=" concatenated
// into "http://" + host + ":8090/now_playing" parses as host 192.0.2.9:80, path
// /removeGroup, and the guard is perfectly happy: that is a normal LAN address.
// Verified by running it on 2026-09-26. So the host is checked for being a bare
// address at the point where the URL is built, and "" comes back when it is not,
// which every caller turns into a request it cannot make.
func peerBoxURL(host, path string) string {
	if !isLANPeer(host) {
		return ""
	}
	return "http://" + net.JoinHostPort(host, "8090") + path
}

func boxGetWith(ctx context.Context, cl *http.Client, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}
