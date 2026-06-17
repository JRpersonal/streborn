package webui

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/boxcli"
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

		prev := s.snapshotNowPlaying(ctx)
		wasStandby := prev.Source == "STANDBY" || prev.Source == ""
		changedVol := volume > 0
		prevVol := -1
		if changedVol {
			prevVol = s.readVolume(ctx)
		}

		if wasStandby {
			if err := boxcli.WakeAndWait(ctx, s.boxHost, 8*time.Second, s.logger); err != nil {
				s.logger.Warn("announce: wake failed, playing anyway", "err", err)
			}
		}
		if changedVol {
			_ = boxapi.New(s.boxHost).SetVolume(ctx, clampVolume(volume))
		}

		if err := s.renderer.PlayURLMime(ctx, announceAudioURL, title, "", mime); err != nil {
			s.logger.Warn("announce: play failed", "err", err)
			if changedVol && prevVol >= 0 {
				_ = boxapi.New(s.boxHost).SetVolume(ctx, prevVol)
			}
			return
		}
		estDur := mp3DurationSec(audio)
		s.logger.Info("announce: playing", "bytes", len(audio), "estSec", fmt.Sprintf("%.1f", estDur), "title", title, "wasStandby", wasStandby)

		s.waitAnnounceDone(ctx, estDur)
		s.restoreAfterAnnounce(ctx, prev, prevVol, wasStandby, changedVol)
	}()
}

// snapshotNowPlaying reads the box's current now_playing so STR can restore it
// after the announcement. Best-effort: a read error yields an empty snapshot.
func (s *Server) snapshotNowPlaying(ctx context.Context) nowPlayingSnapshot {
	var snap nowPlayingSnapshot
	b, err := boxGet(ctx, "http://"+s.boxHost+":8090/now_playing", 16<<10)
	if err != nil {
		return snap
	}
	var np struct {
		Source      string `xml:"source,attr"`
		ContentItem struct {
			Location string `xml:"location,attr"`
			ItemName string `xml:"itemName"`
		} `xml:"ContentItem"`
		PlayStatus string `xml:"playStatus"`
	}
	if xml.Unmarshal(b, &np) == nil {
		snap.Source = np.Source
		snap.Location = np.ContentItem.Location
		snap.ItemName = np.ContentItem.ItemName
		snap.PlayStatus = np.PlayStatus
	}
	return snap
}

// readVolume returns the box's current target volume, or -1 on error.
func (s *Server) readVolume(ctx context.Context) int {
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

// waitAnnounceDone blocks until the box has played the announcement to its end.
// The Portable does NOT reliably report STOP for a finite UPnP track (it sits in
// PLAY_STATE on the finished clip), so the clip's own duration drives the timing:
// wait the duration plus a small tail for the box's buffer. A real STOP, or the
// box moving off the announcement URL (firmware that does report it), ends the
// wait early. estDur<=0 (unparsable audio) falls back to a safe fixed cap.
func (s *Server) waitAnnounceDone(ctx context.Context, estDur float64) {
	// Wait for it to actually start playing (max 6s).
	for i := 0; i < 12 && ctx.Err() == nil; i++ {
		np := s.snapshotNowPlaying(ctx)
		if strings.Contains(np.Location, "/announce/audio") && np.PlayStatus == "PLAY_STATE" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	wait := 20 * time.Second // unknown duration: safe upper bound
	if estDur > 0 {
		wait = time.Duration((estDur + 2.0) * float64(time.Second))
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
		time.Sleep(500 * time.Millisecond)
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
		ver := (b[i+1] >> 3) & 0x03  // 3=MPEG1, 2=MPEG2, 0=MPEG2.5
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

// restoreAfterAnnounce puts the box back the way it was: volume first, then the
// previous stream (re-pushed with its real title so the display reverts), or back
// to standby if the box was asleep before.
func (s *Server) restoreAfterAnnounce(ctx context.Context, prev nowPlayingSnapshot, prevVol int, wasStandby, changedVol bool) {
	if changedVol && prevVol >= 0 {
		_ = boxapi.New(s.boxHost).SetVolume(ctx, prevVol)
	}
	if wasStandby {
		if _, err := boxcli.Send(ctx, s.boxHost, "sys power"); err != nil {
			s.logger.Warn("announce: standby restore failed", "err", err)
		}
		return
	}
	if prev.Location == "" || prev.Source == "STANDBY" {
		return // nothing re-pushable (e.g. it was Bluetooth/AUX, or idle)
	}
	if err := s.renderer.PlayURLMime(ctx, prev.Location, prev.ItemName, "", guessAudioMime(prev.Location)); err != nil {
		s.logger.Warn("announce: resume failed", "err", err, "url", prev.Location)
		return
	}
	s.logger.Info("announce: resumed previous playback", "url", prev.Location, "title", prev.ItemName)
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

// boxGet is a small GET helper for the box's read-only :8090 endpoints.
func boxGet(ctx context.Context, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	cl := &http.Client{Timeout: 4 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}
