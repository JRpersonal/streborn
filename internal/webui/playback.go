// Play endpoints, webhook config, last-play tracking and recently-played notes.

package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/atomicfile"
	"github.com/JRpersonal/streborn/internal/boxurl"
	"github.com/JRpersonal/streborn/internal/presets"
	"github.com/JRpersonal/streborn/internal/recent"
	"github.com/JRpersonal/streborn/internal/upnp"
	"github.com/JRpersonal/streborn/internal/webhooks"
)

// ---- Play / Pause / Stop ----

type playRequest struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	Icon  string `json:"icon"` // albumArtURI for box display
	UUID  string `json:"uuid"` // optional, for click tracking
	// Mime is the source codec MIME (audio/flac, audio/mp4, ...) for a network
	// library track, so the box decodes it correctly. Empty for radio -> the
	// renderer defaults to audio/mpeg.
	Mime string `json:"mime"`
	// Homepage is the station website (radio only), recorded into Recently-played
	// so a card can offer a "website" link like the radio search rows do (#135).
	Homepage string `json:"homepage"`
	// Codec is the station codec as reported by radio-browser ("MP3", "AAC",
	// "AAC+", ...). It selects the DIDL protocolInfo MIME for RADIO plays: the
	// box keys its decoder off that MIME, and an HE-AAC station labelled with
	// the fixed audio/mpeg default played SILENCE while the proxy forwarded
	// its bytes fine (#252, "Absolut Relax"). Deliberately a separate field
	// from Mime, which doubles as the "library file, play direct" marker
	// (#139) and must not be set for radio.
	Codec string `json:"codec"`
}

// handleWebhooks gets (GET) or replaces (PUT) the webhook config. The config
// holds the user's HTTP request(s) fired on a box trigger (today: the remote
// thumbs keys, see boxws OnThumbActivity).
func (s *Server) handleWebhooks(w http.ResponseWriter, r *http.Request) {
	if s.webhooks == nil {
		http.Error(w, "webhooks not configured", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.webhooks.Get())
	case http.MethodPut:
		var c webhooks.Config
		if !decodeJSONRequest(w, r, 1<<16, &c) {
			return
		}
		if err := s.webhooks.Set(c); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, s.webhooks.Get())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleWebhooksTest fires an action immediately so the user can verify their
// URL from the app without pressing a key on the box. Body is an optional
// webhooks.Action; when absent or empty, the configured thumb action is fired.
func (s *Server) handleWebhooksTest(w http.ResponseWriter, r *http.Request) {
	if s.webhooks == nil {
		http.Error(w, "webhooks not configured", http.StatusServiceUnavailable)
		return
	}
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	var a webhooks.Action
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&a)
	}
	// Fall back to the saved thumb action only when nothing testable was posted.
	// Configured() (not a bare URL check) so a udp/wol action, which has no URL,
	// is testable too (#187).
	if !a.Configured() {
		a = s.webhooks.Get().Thumb
	}
	if !a.Configured() {
		http.Error(w, "nothing to test (configure the action first)", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	code, err := s.webhooks.Fire(ctx, a)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": code >= 200 && code < 400, "status": code})
}

func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	if s.renderer == nil {
		http.Error(w, "renderer not configured (set --box-host)", http.StatusServiceUnavailable)
		return
	}
	var req playRequest
	if !decodeJSONRequest(w, r, 1<<16, &req) {
		return
	}
	if req.URL == "" {
		http.Error(w, "url required", http.StatusBadRequest)
		return
	}
	s.ensureBoxReady(r.Context())
	// Detach the play from the request context (#252, same pattern as the
	// preset recall): the standby wake above can outlast the app's HTTP
	// timeout, and a caller that gave up must not cancel the playback it asked
	// for mid-start ("context canceled" right after a slow wake).
	playCtx, playCancel := context.WithTimeout(context.WithoutCancel(r.Context()), playDetachTimeout)
	defer playCancel()
	// A single play replaces any active library queue, so stop auto-advancing.
	s.stopQueue()
	// Ad-hoc radio: the box leaves any Spotify source; suppress the #14
	// auto-attach so it does not jump back to Spotify.
	if s.spotifySwitchedAway != nil {
		s.spotifySwitchedAway(playCtx)
	}
	// Decide how the box reaches the audio.
	//
	// Radio and any HTTPS source go through our loopback stream proxy: it hands
	// the box a stable URL, lets Bose UPnP play HTTPS (which it cannot do
	// itself), and reconnects transparently on CDN token expiry.
	//
	// A network library track is different. It carries its codec MIME (set by
	// the caller) and is a finite file on a LAN media server that supports HTTP
	// range requests, exactly the case the Bose app played directly. Routing it
	// through the radio proxy breaks it two ways: the proxy ignores the box's
	// Range requests, so the box cannot read a FLAC's stream header and sits at
	// "stream starting", and, built for endless radio, it treats the upstream
	// EOF that ends a file as a dropout and reconnects, replaying or garbling
	// the track (the mid-track noise). So a plain-HTTP library file is handed to
	// the box directly, like the Bose app did; only radio or an HTTPS library
	// source still needs the proxy (#139).
	playDirect := req.Mime != "" && isPlainHTTPURL(req.URL)
	playURL := boxurl.RawStream(req.URL)
	if playDirect {
		playURL = req.URL
	}
	s.logger.Info("play request", "direct", playDirect, "mime", req.Mime, "codec", req.Codec, "url", req.URL)
	// An explicit app play overrides any earlier stop latch and anchors the
	// standby-flip discriminator (#419).
	s.NoteUserPlay()
	// Advertise the real codec to the box when the caller knows it (a network
	// library track carries its DLNA-reported MIME, e.g. audio/flac, audio/mp4).
	// The box keys its decoder off this protocolInfo MIME, so a FLAC/ALAC/M4A
	// file mislabelled as audio/mpeg is rejected (AUDIO_ERROR_BAD_URL) while an
	// MP3 plays (#139). Radio derives the MIME from the station codec instead:
	// an AAC/HE-AAC station must be labelled audio/aac or the box decodes it as
	// MPEG and plays silence (#252); MP3/unknown keeps the audio/mpeg default.
	mime := req.Mime
	if mime == "" {
		mime = upnp.MimeForCodec(req.Codec)
	}
	// A station started from the app (the play button in the radio search) goes
	// the same way a preset key does when the speaker can take it: the speaker
	// activates the station and fetches the stream itself. Only radio qualifies;
	// a network library file is a finite ranged file and must keep the direct
	// route (#139). Any refusal falls through to the UPnP push below.
	if !playDirect && req.Mime == "" {
		if loc := s.nativePresetLocation(req.Title, playURL, req.Icon); loc != "" {
			if err := s.selectNativeStation(playCtx, loc, req.Title); err == nil {
				s.logger.Info("play request: started natively, the speaker fetches the stream itself",
					"title", req.Title, "url", req.URL)
				s.setLastPlay(playURL, req.Title, req.Icon, mime)
				s.recentNoteCard("radio", req.URL, req.Title, req.Icon, req.URL, "", req.Homepage)
				writeJSON(w, http.StatusOK, map[string]string{"status": "playing", "url": req.URL})
				return
			} else {
				s.logger.Warn("play request: the speaker refused the native station, falling back to the UPnP push",
					"title", req.Title, "err", err)
			}
		}
	}
	playErr := s.playWithWrongStateRepair(playCtx, playURL, req.Title, req.Icon, mime)
	if playErr != nil {
		if isGroupedRejection(playErr) {
			s.writeGroupedPlayError(w, playErr)
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "Station could not be played",
			"detail": guessErrorReason(playErr),
			"url":    req.URL,
		})
		return
	}
	s.setLastPlay(playURL, req.Title, req.Icon, mime)
	// Recently-played (#135): a network-library file carries a MIME; radio does
	// not. Record the original URL as the replayable card target, not the proxy.
	if req.Mime != "" {
		s.recentNoteCard("upnp", req.URL, req.Title, req.Icon, req.URL, "", "")
	} else {
		s.recentNoteCard("radio", req.URL, req.Title, req.Icon, req.URL, "", req.Homepage)
	}
	// radio-browser click-tracking moved app-side (the app fires RadioClick
	// when it starts playback) so the box no longer needs the radiobrowser pkg.
	writeJSON(w, http.StatusOK, map[string]string{"status": "playing", "url": req.URL})
}

func (s *Server) handlePlaySlot(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	s.boxCmdMu.Lock()
	defer s.boxCmdMu.Unlock()
	if s.renderer == nil {
		http.Error(w, "renderer not configured", http.StatusServiceUnavailable)
		return
	}
	if s.presets == nil {
		http.Error(w, "presets store not initialized", http.StatusServiceUnavailable)
		return
	}
	slotStr := strings.TrimPrefix(r.URL.Path, "/api/play/")
	slot, err := strconv.Atoi(slotStr)
	if err != nil {
		http.Error(w, "invalid slot", http.StatusBadRequest)
		return
	}
	p, ok := s.presets.Get(slot)
	if !ok {
		http.Error(w, "preset not configured", http.StatusNotFound)
		return
	}
	// An explicit preset recall overrides any earlier stop latch and anchors the
	// standby-flip discriminator (#419). Before the wake: the wake itself can
	// flip the source states around.
	s.NoteUserPlay()
	s.ensureBoxReady(r.Context())
	// recallStart anchors the verify's stand-down decision: a deliberate user
	// stop/pause/power-off that arrives AFTER this moment must end the verify
	// retries (the rolling userStopWindow alone expires between the 5s ticks).
	recallStart := time.Now()
	// Detach every box-facing step of this recall from the request context
	// (#252): the standby wake above can outlast the app's HTTP timeout, and a
	// caller that gave up must not cancel the playback it asked for mid-start.
	// Previously only the radio branch was detached; the Spotify, library and
	// queue recalls still died with "context canceled" after a slow wake.
	playCtx, playCancel := context.WithTimeout(context.WithoutCancel(r.Context()), playDetachTimeout)
	defer playCancel()
	// A queue preset (a saved DLNA folder) recalls into the agent play-queue
	// instead of the single-URL play below: reload its ordered tracks with the
	// saved shuffle flag and start from the first. We already hold boxCmdMu, so
	// use the *Locked variant (startQueue would re-lock and deadlock).
	if p.Type == "queue" {
		items := presetItemsToQueue(p.Items)
		if len(items) == 0 {
			http.Error(w, "preset has no playable tracks", http.StatusUnprocessableEntity)
			return
		}
		s.logger.Info("preset slot recall (app): queue", "slot", slot, "tracks", len(items), "shuffle", p.Shuffle)
		card := recentCardCtx{key: fmt.Sprintf("queue:slot:%d", slot), name: p.Name, art: p.Art}
		if err := s.startQueueLocked(playCtx, items, 0, p.Shuffle, repeatOff, card); err != nil {
			if isGroupedRejection(err) {
				s.writeGroupedPlayError(w, err)
				return
			}
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": "Folder could not be played", "detail": guessErrorReason(err),
				"slot": slot, "name": p.Name,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "playing", "slot": slot, "name": p.Name, "type": "queue"})
		return
	}
	// Every non-queue recall replaces an active library queue. Without this the
	// queue watcher kept evaluating the OLD track's timing and, when its
	// wall-clock net tripped minutes later, yanked playback from the station
	// the user explicitly chose back to the next queue track.
	s.stopQueue()
	// Heal a legacy mis-saved Spotify preset before recall: older versions could
	// store a Spotify selection as a non-spotify preset whose stream URL encoded
	// the Spotify container (e.g. /playback/container/<base64 spotify:...>). The
	// radio path would then stream-proxy a scheme-less URL and the box would get
	// nothing, which is the "Service not available" recall failure (#45/#105).
	// Recover the URI and route to the Spotify path; if it is a Spotify stream
	// with no recoverable URI, tell the user to re-save instead of pushing a
	// doomed /stream/<slot>.
	if p.Type != "spotify" && p.StreamURL != "" && !isHTTPURL(p.StreamURL) {
		if uri := legacySpotifyURI(p.StreamURL); uri != "" {
			p.Type, p.URI = "spotify", uri
			s.logger.Info("preset recall: healed legacy spotify preset", "slot", slot, "uri", uri)
		} else if looksLikeSpotifyStreamURL(p.StreamURL) {
			s.logger.Warn("preset recall: spotify preset has no replayable URI", "slot", slot, "url", p.StreamURL)
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error": "This Spotify preset was saved in an older version and can't be replayed. Please open the playlist and save it to the preset again.",
				"code":  "spotify-preset-unreplayable",
				"slot":  slot, "name": p.Name,
			})
			return
		}
	}
	// Spotify presets have no playable HTTP StreamURL. Mirror the hardware-press
	// recall (cmd/agent playSpotifyPreset) so a soft recall behaves identically:
	//  1. wait out a cold go-librespot (auth not finished) instead of pointing
	//     the box at a not-yet-flowing stream, which starves and detaches after
	//     ~30s and forced the user to press the preset a second time,
	//  2. mark the recall so ServeOgg drives the new track from its start,
	//  3. point the box at THIS slot's stream first (now_playing shows the name
	//     and buffers) and load the playlist audio after, so the box buffers
	//     until audio flows.
	// Log every app-side slot recall so a remote "recall does nothing" report
	// (ST20 #45) shows the preset shape that was attempted.
	s.logger.Info("preset slot recall (app)", "slot", slot, "type", p.Type, "hasURI", p.URI != "", "account", p.Account)
	if p.Type == "spotify" && p.URI == "" {
		s.logger.Warn("spotify preset recall (app): type=spotify but empty URI, falling through to radio path", "slot", slot, "name", p.Name)
	}
	if p.Type == "spotify" && p.URI != "" {
		if s.spotifyPlay == nil {
			s.logger.Warn("spotify preset recall (app): Spotify not configured on this box", "slot", slot)
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"error": "Spotify not configured", "slot": slot, "name": p.Name,
			})
			return
		}
		// A speaker that has never been logged into Spotify AND holds no live
		// session has no way for go-librespot to start playback on its own, so the
		// recall would silently do nothing (#45 Pierre: saved preset account="" and
		// go-librespot not running). Tell the user how to fix it instead of
		// optimistically reporting "playing" and failing in the background. Gate on
		// CanRecall (live session OR persisted credential), NOT a persisted
		// credential alone: a box with a live-but-never-persisted zeroconf session
		// plays Spotify fine yet reports not-logged-in, and gating on the credential
		// alone wrongly refused its recall (Patrick, ST10, 2026-06-24).
		// Checked on the detached context: on a slow wake the request context
		// is already cancelled here and the probe would misreport "not picked
		// in Spotify yet" (422) for a speaker that is logged in fine (#252).
		if s.spotifyCanRecall != nil && !s.spotifyCanRecall(playCtx) {
			s.logger.Info("spotify preset recall (app): speaker not logged into Spotify", "slot", slot)
			// STR plays Spotify through this speaker as a Spotify Connect receiver
			// (the go-librespot sidecar), not via any Bose account link. The
			// speaker has to be picked in Spotify once so it stores a credential.
			// The desktop app branches on the code, so this wording is free to be
			// the accurate, non-Bose-linking instruction.
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error": "This speaker has not been picked in Spotify yet. In the Spotify app on a device on the same Wi-Fi, tap the Connect/devices icon, choose this speaker and play any track once. After that this preset will recall on its own.",
				"code":  "spotify-not-logged-in",
				"slot":  slot, "name": p.Name,
			})
			return
		}
		// A free/open Spotify account cannot do the autonomous on-demand playback a
		// recall needs (it can only play when the phone app drives it), so the
		// recall would silently fail. Tell the user it needs Premium instead (#45).
		if s.spotifyPremiumRequired != nil && s.spotifyPremiumRequired() {
			s.logger.Info("spotify preset recall (app): account is free/open, recall needs Premium", "slot", slot)
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error": "This speaker's Spotify account is free. Spotify preset recall needs Spotify Premium.",
				"code":  "spotify-premium-required",
				"slot":  slot, "name": p.Name,
			})
			return
		}
		// Mark the recall and point the box at THIS slot's stream FIRST (the box
		// shows the name and buffers), then answer the request right away. The
		// slow part (waiting out a cold go-librespot + loading the playlist audio
		// + verify) runs in the background, so the box buffers until audio flows
		// instead of starving and detaching, AND the desktop POST does not block
		// on a 12s cold-start wait (which playPost would mis-report as the box not
		// being ready, i.e. "speaker is still starting").
		if s.spotifySetRecalling != nil {
			s.spotifySetRecalling()
		}
		slotURL := boxurl.SpotifySlot(slot)
		if err := s.renderer.PlayURLMime(playCtx, slotURL, p.Name, p.Art, "audio/ogg"); err != nil {
			if isGroupedRejection(err) {
				s.writeGroupedPlayError(w, err)
				return
			}
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": "Spotify stream could not be played", "detail": guessErrorReason(err),
				"slot": slot, "name": p.Name,
			})
			return
		}
		gen := s.setLastPlay(slotURL, p.Name, p.Art, "audio/ogg")
		s.recentNoteCard("spotify", p.URI, p.Name, p.Art, p.URI, p.Account, "") // #135
		// normalizeSpotifyURI on recall heals presets that stored an ephemeral
		// station context before the save-side unwrap existed.
		uri, name, art, account, shuffle := normalizeSpotifyURI(p.URI), p.Name, p.Art, p.Account, p.Shuffle
		go func() {
			bg := context.Background()
			t0 := time.Now()
			warm := s.spotifyReady == nil || s.spotifyReady()
			if s.spotifyReady != nil && !s.spotifyReady() {
				s.logger.Info("spotify soft recall: waiting for go-librespot ready (cold start)", "slot", slot)
				for i := 0; i < 24 && !s.spotifyReady(); i++ {
					time.Sleep(500 * time.Millisecond)
				}
			}
			keyDenied := false
			if err := s.spotifyPlay(bg, uri, account, shuffle); err != nil {
				// An audio-key denial means Spotify refuses this account/session
				// the decryption keys: every additional Play just triggers
				// another engine skip-storm (one key request per track) and feeds
				// the account throttle that keeps the denial alive (429, field
				// 2026-07-26: 51-track storms per press). Remember it so the
				// verify below never fires the recovery re-Play into that state.
				keyDenied = strings.Contains(err.Error(), "audio key denied")
				s.logger.Warn("spotify play (initial) failed, will verify+retry", "slot", slot, "err", err, "keyDenied", keyDenied)
			}
			s.logger.Info("spotify soft recall: context load issued", "slot", slot, "warm", warm, "loadAfterMs", time.Since(t0).Milliseconds())
			s.verifyRecall(gen, recallStart, slotURL, func(ctx context.Context, lastAttempt bool) {
				// Re-point the box at the stream WITHOUT re-Play on the early
				// tries: ServeOgg resumes go-librespot on attach, so this
				// re-attaches without reshuffling/restarting the track (a re-Play
				// every retry was the "same song restarts a few seconds in" bug,
				// fixed for hardware in v0.7.4 but previously still present here).
				// Only the last attempt does a full re-Play, to recover a genuine
				// cold-boot auth race where the playlist never loaded at all - and
				// never on an audio-key denial, where it would only amplify the
				// skip storm.
				if lastAttempt && !keyDenied {
					_ = s.spotifyPlay(ctx, uri, account, shuffle)
				}
				_ = s.renderer.PlayURLMime(ctx, slotURL, name, art, "audio/ogg")
			}, s.spotifyStreaming)
		}()
		writeJSON(w, http.StatusOK, map[string]any{"status": "playing", "slot": slot, "name": p.Name, "type": "spotify"})
		return
	}
	// Radio recall: tell Spotify the box switched away so its #14 auto-attach
	// does not yank the box back to a still-advancing go-librespot.
	if s.spotifySwitchedAway != nil {
		s.spotifySwitchedAway(playCtx)
	}
	// A library preset (saved from the Library tab, so Source is set) points at a
	// finite file on a LAN media server. Recall it like the Library play path:
	// hand the box the file directly with its codec MIME, bypassing the radio
	// stream proxy that stalls a FLAC on Range and garbles it on EOF (#139). The
	// MIME is re-derived from the URL because the preset store does not keep it;
	// an unknown extension falls through to the proxy path unchanged.
	if p.Source != "" && isPlainHTTPURL(p.StreamURL) {
		if mime := mimeFromURL(p.StreamURL); mime != "" {
			directURL := p.StreamURL
			s.logger.Info("preset slot recall (app): direct library file", "slot", slot, "mime", mime)
			if err := s.renderer.PlayURLMime(playCtx, directURL, p.Name, p.Art, mime); err != nil {
				if isGroupedRejection(err) {
					s.writeGroupedPlayError(w, err)
					return
				}
				writeJSON(w, http.StatusBadGateway, map[string]any{
					"error": "Track could not be played", "detail": guessErrorReason(err),
					"slot": slot, "name": p.Name,
				})
				return
			}
			gen := s.setLastPlay(directURL, p.Name, p.Art, mime)
			s.recentNoteCard("upnp", p.StreamURL, p.Name, p.Art, p.StreamURL, "", "") // #135
			name, art := p.Name, p.Art
			go s.verifyRecall(gen, recallStart, directURL, func(ctx context.Context, _ bool) {
				_ = s.renderer.PlayURLMime(ctx, directURL, name, art, mime)
			}, nil)
			writeJSON(w, http.StatusOK, map[string]any{"status": "playing", "slot": slot, "name": p.Name})
			return
		}
	}
	// Use the stream proxy URL so playback continues even after token
	// expiry (Bose sees the stable loopback URL).
	playURL := boxurl.StreamSlot(slot)
	// Start it the way the speaker's own key would, when this speaker can take
	// the native form. Otherwise the app path pushes UPnP over a station the
	// speaker is already playing natively, which drops the audio and flips the
	// source back to the form the native work exists to avoid. Falls through to
	// the UPnP push on any refusal, so a speaker that cannot do this keeps
	// exactly today's behaviour. See nativeselect.go.
	if loc := s.nativePresetLocation(p.Name, playURL, p.Art); loc != "" {
		if err := s.selectNativeStation(playCtx, loc, p.Name); err == nil {
			s.logger.Info("preset slot recall (app): started natively, the speaker fetches the stream itself",
				"slot", slot, "name", p.Name)
			// Still record it as the last play: the power-on resume reads this,
			// and skipping it would silently cost the user their "switch on and
			// the last station comes back" behaviour.
			s.setLastPlay(playURL, p.Name, p.Art, upnp.MimeForCodecOrURL(p.Codec, p.StreamURL))
			s.recentNoteCard("radio", p.StreamURL, p.Name, p.Art, p.StreamURL, "", p.Homepage) // #135
			writeJSON(w, http.StatusOK, map[string]any{"status": "playing", "slot": slot, "name": p.Name})
			return
		} else {
			s.logger.Warn("preset slot recall (app): the speaker refused the native station, falling back to the UPnP push",
				"slot", slot, "name", p.Name, "err", err)
		}
	}
	// A preset saved from an AAC/HE-AAC station carries its codec: label the
	// stream audio/aac in the DIDL so the box picks the right decoder. The
	// fixed audio/mpeg label made those stations play silence (#252). A preset
	// stored without a codec falls back to reading it off the station URL.
	mime := upnp.MimeForCodecOrURL(p.Codec, p.StreamURL)
	playErr := s.playWithWrongStateRepair(playCtx, playURL, p.Name, p.Art, mime)
	if playErr != nil {
		// Log the failed radio recall: this 502 used to be returned with no
		// agent-side trace at all, so a remote diagnostic bundle showed a recall
		// that apparently never happened (#252).
		s.logger.Warn("preset slot recall (app): radio play failed",
			"slot", slot, "name", p.Name, "playURL", playURL, "err", playErr)
		if isGroupedRejection(playErr) {
			s.writeGroupedPlayError(w, playErr)
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":  "Station could not be played",
			"detail": guessErrorReason(playErr),
			"slot":   slot,
			"name":   p.Name,
		})
		return
	}
	gen := s.setLastPlay(playURL, p.Name, p.Art, mime)
	s.recentNoteCard("radio", p.StreamURL, p.Name, p.Art, p.StreamURL, "", p.Homepage) // #135
	name, art := p.Name, p.Art
	go s.verifyRecall(gen, recallStart, playURL, func(ctx context.Context, _ bool) {
		if mime != "" {
			_ = s.renderer.PlayURLMime(ctx, playURL, name, art, mime)
		} else {
			_ = s.renderer.PlayURL(ctx, playURL, name, art)
		}
	}, nil)
	writeJSON(w, http.StatusOK, map[string]any{"status": "playing", "slot": slot, "name": p.Name})
}

// setLastPlay records the box-facing stream + metadata for the auto-re-push. A
// fresh play resets the re-push state (rePushes=0, failed=false), so a stream
// that was previously declared dead gets a clean slate when the user plays it
// again. It returns the new recall generation; a recall passes it to
// verifyRecall so the verify stands down as soon as a later play bumps it.
func (s *Server) setLastPlay(boxURL, title, art, mime string) uint64 {
	now := time.Now()
	s.lastPlayMu.Lock()
	s.lastPlay = &lastPlayInfo{boxURL: boxURL, title: title, art: art, mime: mime, ts: now}
	s.recallGen++
	gen := s.recallGen
	// A fresh play is an explicit user "play this" and may well be a different
	// stream: re-arm the auto-resume crash-loop guard (#381).
	s.resumeAttempts = 0
	s.lastResumeAt = time.Time{}
	s.lastPlayMu.Unlock()
	// Persist so the power-on resume survives an agent restart over a long
	// standby (#119). Plays are user-paced, so this is a rare, cheap NAND write.
	s.persistLastPlay(boxURL, title, art, mime, now, 0, time.Time{})
	// If this speaker leads a mirror group, bring the others onto the new
	// stream now rather than at the next 5-minute reconcile. See
	// kickMirrorAfterPlay for why that wait reads to users as a lost group.
	s.kickMirrorAfterPlay()
	return gen
}

// persistedLastPlay is the on-NAND shape of the last-played stream (the resume
// target). The runtime re-push counters are deliberately omitted: a reload is a
// fresh start. The auto-resume guard counters (#381) ARE persisted: they exist
// precisely to survive the reboot a crashing resume causes.
type persistedLastPlay struct {
	BoxURL string    `json:"boxURL"`
	Title  string    `json:"title"`
	Art    string    `json:"art"`
	Mime   string    `json:"mime"`
	TS     time.Time `json:"ts"`
	// ResumeAttempts / LastResumeAt: the auto-resume crash-loop guard state
	// (#381, see resume_guard.go). Absent in files from older agents, which
	// unmarshals to the zero values = guard disarmed.
	ResumeAttempts int       `json:"resumeAttempts,omitempty"`
	LastResumeAt   time.Time `json:"lastResumeAt,omitzero"`
}

// persistLastPlay writes the resume target to NAND durably (fsync + rename), so
// a power loss mid-write cannot leave a torn OR zero-byte file. A plain
// write+rename left last-play.json at 0 bytes after an overnight standby
// power-cut (the same loss that wiped presets.json, 2026-07-15). Best-effort,
// no-op without a configured path.
func (s *Server) persistLastPlay(boxURL, title, art, mime string, ts time.Time, resumeAttempts int, lastResumeAt time.Time) {
	if s.lastPlayPath == "" {
		return
	}
	b, err := json.Marshal(persistedLastPlay{BoxURL: boxURL, Title: title, Art: art, Mime: mime, TS: ts,
		ResumeAttempts: resumeAttempts, LastResumeAt: lastResumeAt})
	if err != nil {
		return
	}
	// Warn, not Debug: on a full NAND this is the ONLY trace of why the
	// power-on resume has no station to bring back after the next standby
	// (#119, ST30 with a full /mnt/nv).
	if err := atomicfile.WriteFile(s.lastPlayPath, b, 0o644); err != nil {
		s.logger.Warn("last-play persist failed", "err", err)
	}
}

// loadLastPlay restores the persisted resume target at agent start, so a power
// press after an overnight standby (which often restarts the agent) still brings
// the last station back instead of leaving the box on its native "Preset not
// assigned" (#119 Klaus). Best-effort; absent/corrupt is just no resume target.
func (s *Server) loadLastPlay() {
	if s.lastPlayPath == "" {
		return
	}
	b, err := os.ReadFile(s.lastPlayPath)
	if err != nil {
		return
	}
	var p persistedLastPlay
	if json.Unmarshal(b, &p) != nil || p.BoxURL == "" || p.TS.IsZero() {
		return
	}
	s.lastPlayMu.Lock()
	s.lastPlay = &lastPlayInfo{boxURL: p.BoxURL, title: p.Title, art: p.Art, mime: p.Mime, ts: p.TS}
	// Restore the auto-resume guard count (#381): after a crash-caused reboot
	// this is what tells the automatic resume it is looping.
	s.resumeAttempts = p.ResumeAttempts
	s.lastResumeAt = p.LastResumeAt
	s.lastPlayMu.Unlock()
	s.logger.Info("last-play restored from NAND for power-on resume", "title", p.Title, "ageMin", int(time.Since(p.TS).Minutes()), "resumeAttempts", p.ResumeAttempts)
}

// NoteLastPlay records a stream the agent pushed to the box OUTSIDE the webui
// (the hardware preset recall in cmd/agent goes straight to the renderer). It
// lets the auto-re-push (#4) and the power-button wake-resume work for hardware
// presses too, which otherwise left lastPlay unset and the box un-resumable.
// It returns the new recall generation so the hardware verify can stand down
// as soon as a later play (hardware or app) supersedes it, mirroring the soft
// path's verifyRecall.
func (s *Server) NoteLastPlay(boxURL, title, art, mime string) uint64 {
	// A hardware recall is by definition a non-queue play (cmd/agent routes a
	// queue preset through RecallSlot, which never reaches here): drop any
	// active library queue so its watcher does not advance over the user's new
	// choice minutes later.
	s.stopQueue()
	return s.setLastPlay(boxURL, title, art, mime)
}

// RecallGeneration returns the current recall generation (bumped by every
// stream push recorded via setLastPlay, hardware and app alike). The hardware
// recall verifies in cmd/agent compare it against the generation of their own
// recall: a mismatch means a newer play started and the stale verify must
// stand down instead of re-pushing its old URL over the user's newest choice
// ("pressed 2, got 1").
func (s *Server) RecallGeneration() uint64 {
	s.lastPlayMu.Lock()
	defer s.lastPlayMu.Unlock()
	return s.recallGen
}

// bumpRecallGen advances the recall generation without recording a lastPlay.
// Used by recall paths whose own setLastPlay only runs on success (the queue
// preset start), so the claim itself already supersedes any older verify loop.
func (s *Server) bumpRecallGen() {
	s.lastPlayMu.Lock()
	s.recallGen++
	s.lastPlayMu.Unlock()
}

// StandbyStoppedAfter reports whether STR saw this box's UPnP source drop to
// STANDBY (a power-off) strictly after t. The hardware recall verify anchors
// this to its press time: unlike the rolling RecentlyPoweredOff window, the
// stamp cannot expire between two verify ticks, so a power-off mid-recall
// reliably stands the re-push down for the recall's whole lifetime (#197).
func (s *Server) StandbyStoppedAfter(t time.Time) bool {
	s.standbyStopMu.Lock()
	defer s.standbyStopMu.Unlock()
	return !s.lastStandbyStop.IsZero() && s.lastStandbyStop.After(t)
}

// SetTransportCommandHook wires the webui's own renderer into the gabbo
// classifier's own-command excusal (boxws.NoteOwnTransportCommand): re-pushes,
// resumes and the standby-bounce clear all make the box emit STOP_STATE frames
// that must not latch a phantom user stop. No-op without a renderer.
func (s *Server) SetTransportCommandHook(fn func()) {
	if s.renderer != nil {
		s.renderer.OnTransportCommand = fn
	}
}

// --- Recently played (#135) ---
//
// These record the user's listening history into the capped, debounced ring.
// They are deliberately the only box-side work the feature adds: each call is a
// cheap in-RAM append (Add does no I/O), reusing events the agent already
// processes (play handlers, the ICY title callback, the hardware-preset gabbo
// event). All are no-ops when the recent store is not wired.

// recentNoteCard records a user-chosen source (radio station, Spotify playlist,
// NAS file) as the start of a Recently-played card. For radio and Spotify it also
// remembers the card so the live track callbacks can hang tracks under it.
func (s *Server) recentNoteCard(source, key, name, art, url, account, homepage string) {
	if s.recent == nil || key == "" {
		return
	}
	s.recent.Add(recent.Entry{Source: source, CardKey: key, CardName: name, CardArt: art, CardURL: url, Account: account, Homepage: homepage})
	s.recentMu.Lock()
	switch source {
	case "radio":
		s.recentRadioCard = recentCardCtx{key: key, name: name, art: art, url: url, homepage: homepage}
	case "spotify":
		// A fresh card resets the de-dup so the playlist's first song is recorded
		// even if its name happens to match the previous card's last track.
		s.recentSpotifyCard = recentCardCtx{key: key, name: name, art: art, url: url, account: account}
	}
	s.recentMu.Unlock()
}

// recentNoteQueueCard records a DLNA folder played as an auto-advancing queue as
// the start of a "library" (upnp) Recently-played card, and remembers it so each
// track the queue pushes is hung under it (like radio ICY tracks under a station).
// The replay target is the first track's URL; clicking the card re-plays it.
func (s *Server) recentNoteQueueCard(key, name, art, url string) {
	s.recentMu.Lock()
	s.recentQueueCard = recentCardCtx{key: key, name: name, art: art, url: url}
	s.recentMu.Unlock()
	s.recentNoteCard("upnp", key, name, art, url, "", "")
}

// recentNoteQueueTrack hangs the track the play-queue just started under the
// current folder card. No-op until a queue card has been recorded.
func (s *Server) recentNoteQueueTrack(track string) {
	if s.recent == nil || track == "" {
		return
	}
	s.recentMu.Lock()
	c := s.recentQueueCard
	s.recentMu.Unlock()
	if c.key == "" {
		return
	}
	s.recent.Add(recent.Entry{Source: "upnp", CardKey: c.key, CardName: c.name, CardArt: c.art, CardURL: c.url, Track: track})
}

// recentClearQueueCard forgets the active folder card so later tracks (a single
// play, a new radio station) are not mis-attributed to a folder that has stopped.
func (s *Server) recentClearQueueCard() {
	s.recentMu.Lock()
	s.recentQueueCard = recentCardCtx{}
	s.recentMu.Unlock()
}

// recentNoteRadioTrack hangs a live radio track (the ICY StreamTitle) under the
// current radio card. No-op until a radio card has been recorded.
func (s *Server) recentNoteRadioTrack(track string) {
	if s.recent == nil || track == "" {
		return
	}
	s.recentMu.Lock()
	c := s.recentRadioCard
	s.recentMu.Unlock()
	if c.key == "" {
		return
	}
	s.recent.Add(recent.Entry{Source: "radio", CardKey: c.key, CardName: c.name, CardArt: c.art, CardURL: c.url, Track: track, Homepage: c.homepage})
}

// NoteRecentSpotifyTrack hangs a live Spotify song under the current Spotify card.
// Wired to the Spotify manager's onTrack hook. No-op until a Spotify card has been
// recorded (a track that starts outside an STR recall has no card to attach to).
func (s *Server) NoteRecentSpotifyTrack(track, artist string) {
	if s.recent == nil || track == "" {
		return
	}
	s.recentMu.Lock()
	c := s.recentSpotifyCard
	s.recentMu.Unlock()
	if c.key == "" {
		return
	}
	// Store artist-first ("Artist - Title"). The view's formatTrack reads " - " as
	// the Shoutcast "Artist - Title" order, so this renders the artist on the lead
	// line exactly like radio, instead of mislabelling the song title as artist.
	full := track
	if artist != "" {
		full = artist + " - " + track
	}
	s.recent.Add(recent.Entry{Source: "spotify", CardKey: c.key, CardName: c.name, CardArt: c.art, CardURL: c.url, Track: full, Account: c.account})
}

// NoteRecentPreset records a hardware-preset press into Recently-played. The
// agent's gabbo handler calls it because the hardware recall goes straight to
// the renderer, bypassing the webui play handlers.
func (s *Server) NoteRecentPreset(p presets.Preset) {
	if p.Type == "spotify" {
		s.recentNoteCard("spotify", p.URI, p.Name, p.Art, p.URI, p.Account, "")
		return
	}
	s.recentNoteCard("radio", p.StreamURL, p.Name, p.Art, p.StreamURL, "", p.Homepage)
}

// handleRecent serves this box's recently-played ring (#135), oldest-first. The
// desktop app reads every box's ring and does the merge + source-card grouping;
// the box just returns its capped list.
func (s *Server) handleRecent(w http.ResponseWriter, r *http.Request) {
	if s.recent == nil {
		if r.Method == http.MethodDelete {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": 0})
			return
		}
		writeJSON(w, http.StatusOK, []recent.Entry{})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.recent.All())
	case http.MethodDelete:
		// DELETE /api/recent?all=1            -> clear the whole ring (explicit)
		// DELETE /api/recent?cardKey=X&ts=Y   -> remove the ONE card at that ts
		// "all=1" is REQUIRED to clear: a delete-card request with a missing/stale
		// cardKey must never fall through to wiping everything (that was the bug
		// where deleting one entry removed all older ones). Flush() now so the
		// change survives an immediate reboot instead of waiting out the debounce.
		removed := 0
		switch {
		case r.URL.Query().Get("all") == "1":
			s.recent.Clear()
		case r.URL.Query().Get("cardKey") != "":
			removed = s.recent.DeleteCardAt(r.URL.Query().Get("cardKey"), r.URL.Query().Get("ts"))
		default:
			http.Error(w, "specify all=1 to clear, or cardKey+ts to remove one card", http.StatusBadRequest)
			return
		}
		if err := s.recent.Flush(); err != nil {
			s.logger.Warn("recent: flush after delete failed", "err", err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
