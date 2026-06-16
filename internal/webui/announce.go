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
	s.announceMu.Lock()
	s.announceAudio = audio
	s.announceMime = mime
	s.announceMu.Unlock()

	// Serialise against other box commands (play, volume) for the whole
	// interrupt/resume cycle so nothing interleaves mid-sequence.
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()

	// Remember what to restore.
	prev := s.snapshotNowPlaying(r.Context())
	wasStandby := prev.Source == "STANDBY" || prev.Source == ""
	changedVol := body.Volume > 0
	prevVol := -1
	if changedVol {
		prevVol = s.readVolume(r.Context())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	if wasStandby {
		if err := boxcli.WakeAndWait(ctx, s.boxHost, 8*time.Second, s.logger); err != nil {
			s.logger.Warn("announce: wake failed, playing anyway", "err", err)
		}
	}
	if changedVol {
		_ = boxapi.New(s.boxHost).SetVolume(ctx, clampVolume(body.Volume))
	}

	title := body.Text
	if title == "" {
		title = "Announcement"
	}
	if len(title) > 64 {
		title = strings.TrimSpace(title[:64])
	}
	if err := s.renderer.PlayURLMime(ctx, announceAudioURL, title, "", mime); err != nil {
		s.logger.Warn("announce: play failed", "err", err)
		// Best-effort restore of volume before reporting.
		if changedVol && prevVol >= 0 {
			_ = boxapi.New(s.boxHost).SetVolume(ctx, prevVol)
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "could not play announcement", "detail": err.Error()})
		return
	}
	s.logger.Info("announce: playing", "bytes", len(audio), "title", title, "wasStandby", wasStandby)

	s.waitAnnounceDone(ctx)
	s.restoreAfterAnnounce(ctx, prev, prevVol, wasStandby, changedVol)

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bytes": len(audio)})
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

// waitAnnounceDone blocks until the box has played the announcement to its end:
// it first waits for the box to actually start playing the announcement URL, then
// waits for it to leave PLAY/BUFFERING. Bounded so a stuck box cannot wedge the
// request forever.
func (s *Server) waitAnnounceDone(ctx context.Context) {
	started := false
	for i := 0; i < 16 && ctx.Err() == nil; i++ {
		np := s.snapshotNowPlaying(ctx)
		if strings.Contains(np.Location, "/announce/audio") && np.PlayStatus == "PLAY_STATE" {
			started = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !started {
		return
	}
	for i := 0; i < 240 && ctx.Err() == nil; i++ {
		np := s.snapshotNowPlaying(ctx)
		if !strings.Contains(np.Location, "/announce/audio") {
			return
		}
		if np.PlayStatus != "PLAY_STATE" && np.PlayStatus != "BUFFERING_STATE" {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
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
