// Read-only probes of the box state on :8090 (now_playing scraping).

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

// firstAttr extracts the first value of an XML attribute (e.g. source="X",
// location="Y") from a now_playing document. Empty if absent.
func firstAttr(doc, name string) string {
	key := name + `="`
	i := strings.Index(doc, key)
	if i < 0 {
		return ""
	}
	rest := doc[i+len(key):]
	if j := strings.IndexByte(rest, '"'); j >= 0 {
		return rest[:j]
	}
	return ""
}

// firstElem extracts the first simple element's text (e.g. <itemName>X</itemName>)
// from a now_playing document. Empty if absent.
func firstElem(doc, name string) string {
	key := "<" + name + ">"
	i := strings.Index(doc, key)
	if i < 0 {
		return ""
	}
	rest := doc[i+len(key):]
	if j := strings.IndexByte(rest, '<'); j >= 0 {
		return rest[:j]
	}
	return ""
}

// probeClient is shared by every now_playing probe. Some of them sit on
// 10-second reconcile cadences, so the client (and its connection pool) is
// allocated once instead of per call.
var probeClient = &http.Client{Timeout: 4 * time.Second}

// fetchNowPlaying reads the box's :8090/now_playing document once. Every
// probe below is a pure verdict over this document, so each stays testable
// against canned XML without hardware or a fixed :8090 listener. ok is false
// on any transport error, which every verdict maps to its zero value.
func fetchNowPlaying(boxHost string) (doc string, ok bool) {
	if boxHost == "" {
		boxHost = "127.0.0.1"
	}
	resp, err := probeClient.Get("http://" + boxHost + ":8090/now_playing")
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return string(b), true
}

// boxInSetupOOB reports whether BoseApp's /setup says the box is still
// in out-of-box setup (SETUP_AP_OOB). Pushing presets in that state
// fails with "MargeHSM is in the wrong state" and only spams the log,
// so the reconciler waits until the box has joined a network. On any
// read error we return false (proceed) so a firmware whose /setup
// differs never silently stops reconciling on a working box.
func boxInSetupOOB(boxHost string) bool {
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s:8090/setup", boxHost))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return strings.Contains(string(body), "SETUP_AP_OOB")
}

// lastN returns the last n characters of s.
func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// boxNowPlayingSource returns the source attribute of the box's now_playing
// (e.g. "SETUP", "STANDBY", "UPNP", "INVALID_SOURCE"), or "" on any error.
func boxNowPlayingSource(boxHost string) string {
	doc, ok := fetchNowPlaying(boxHost)
	if !ok {
		return ""
	}
	return firstAttr(doc, "source")
}

// leaveSetupSourceWatcher clears a stuck out-of-box SETUP source so the box can
// play. It checks soon after boot (the common case: a fresh network install
// that never went through Bose's app onboarding), retries a few times to catch
// a box whose agent came up before the firmware settled, then drops to a slow
// maintenance poll. A POST /setup SETUP_LEAVE is harmless when the box is not
// in setup, so a stray check costs nothing. See boxapi.LeaveSetup.
func leaveSetupSourceWatcher(ctx context.Context, boxHost string, logger *slog.Logger) {
	if boxHost == "" {
		boxHost = "127.0.0.1"
	}
	client := boxapi.New(boxHost)
	check := func() {
		if boxNowPlayingSource(boxHost) != "SETUP" {
			return
		}
		lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := client.LeaveSetup(lctx)
		cancel()
		if err != nil {
			logger.Warn("leave-setup: could not clear the out-of-box SETUP source", "err", err)
			return
		}
		logger.Info("leave-setup: cleared the box's stuck out-of-box SETUP source so it can play (no power-cycle needed)")
	}
	// Prompt initial sweep. This used to be eight tries over two minutes, which
	// is the wrong shape: a box does not only enter SETUP during boot. One seen
	// on 2026-08-06 entered it forty seconds AFTER the fast window closed and
	// then sat there unattended for two minutes and thirteen seconds, because
	// the next check was five minutes out. It happened to leave on its own.
	//
	// Forty tries over ten minutes covers the whole settling period after a
	// fresh install, which is when this actually happens. The check costs one
	// local /now_playing read that returns immediately, and it is a READ: it
	// cannot touch the deep-standby countdown the way a write would.
	//
	// The slow maintenance poll below is deliberately left at five minutes. It
	// exists for the rare case of a box re-entering SETUP long after boot, and
	// tightening it would multiply a background poll on a speaker with 120 MB
	// of RAM for a case the wider window above already covers.
	for i := 0; i < 40; i++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}
		check()
	}
	// Maintenance: a box can re-enter the SETUP source after a firmware event,
	// so keep a slow watch running.
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
		}
	}
}

// boxIsPlaying reports whether the Bose box is actively rendering audio (any
// source), so the memory guard never reboots mid-playback. Best-effort: any
// error or a non-play state counts as not playing.
func boxIsPlaying(boxHost string) bool {
	doc, ok := fetchNowPlaying(boxHost)
	return ok && nowPlayingIsPlaying(doc)
}

// nowPlayingIsPlaying is boxIsPlaying's verdict over an already-fetched
// now_playing document: a play or buffering state on any source.
func nowPlayingIsPlaying(doc string) bool {
	return strings.Contains(doc, "PLAY_STATE") || strings.Contains(doc, "BUFFERING_STATE")
}

// boxNowPlayingSummary returns compact now_playing evidence for the recall
// settle logs: the active source, the box's own itemName (what a display model
// like the Wave/ST20 shows) and the playStatus. It answers two open field
// questions at once: which side of the Wave wrong-state race won (audio
// without name vs name without audio - itemName empty on the winning STR push,
// #469/Wave 2026-07-25), and whether a verify success was the box's stale
// same-slot ContentItem rather than a real fetch (#419 Finding 1).
func boxNowPlayingSummary(boxHost string) (source, itemName, playStatus string) {
	doc, ok := fetchNowPlaying(boxHost)
	if !ok {
		return "", "", ""
	}
	return nowPlayingSummary(doc)
}

// nowPlayingSummary is boxNowPlayingSummary's verdict over an already-fetched
// now_playing document.
func nowPlayingSummary(doc string) (source, itemName, playStatus string) {
	return firstAttr(doc, "source"), firstElem(doc, "itemName"), firstElem(doc, "playStatus")
}

// nudgeStuckSource is the sys-power-nudge decision: the box still reports
// INVALID_SOURCE at the third verify attempt and no nudge ran yet. Bounded to
// exactly one nudge per recall; earlier attempts give the normal wake+re-push
// a chance first.
func nudgeStuckSource(attempt int, nudged bool, source string) bool {
	return attempt == 3 && !nudged && source == "INVALID_SOURCE"
}

// boxPlayingURL reports whether the box is in a play/buffering state AND its
// now_playing actually points at wantURL, the URL this recall pushed. It is the
// success signal for the radio recall verify.
//
// The location check is what a bare play-state check misses: a box that rejects
// the recall (1036 UpnpRcvdContentItemInWrongState, chronic on the Wave) keeps
// reporting the PREVIOUS stream's play state while never fetching the new one,
// so the verify passed at its first tick and the user was left with a display
// that shows the station and no audio at all.
//
// Deliberately forgiving in one direction: firmware whose now_playing carries NO
// location at all falls back to the plain play-state verdict, so a model that
// simply does not report a location does not end up in an endless re-push loop.
func boxPlayingURL(boxHost, wantURL string) bool {
	doc, ok := fetchNowPlaying(boxHost)
	return ok && nowPlayingIsURL(doc, wantURL)
}

// nowPlayingIsURL is boxPlayingURL's verdict over an already-fetched
// now_playing document; split out so the discriminator is testable without
// hardware or a fixed :8090 listener.
func nowPlayingIsURL(doc, wantURL string) bool {
	if !strings.Contains(doc, "PLAY_STATE") && !strings.Contains(doc, "BUFFERING_STATE") {
		return false
	}
	// Compare on the path (e.g. "/stream/5"), not the whole URL: the box echoes
	// the location it was given, but host spellings differ across the paths that
	// build these URLs (loopback vs the box's LAN address).
	want := streamPath(wantURL)
	if want == "" || !strings.Contains(doc, `location="`) {
		return true // nothing to compare against; keep the old play-state verdict
	}
	return strings.Contains(doc, want)
}

// streamPath reduces an STR stream URL to the path+query the box echoes back in
// now_playing ("http://127.0.0.1:8888/stream/5" -> "/stream/5"), so a location
// comparison does not depend on which host spelling built the URL. Returns ""
// for anything that is not an STR stream URL, which disables the comparison.
func streamPath(u string) string {
	i := strings.Index(u, "/stream/")
	if i < 0 {
		return ""
	}
	return u[i:]
}

// boxPlayingSpotify reports whether the box's now_playing is STR's Spotify
// stream in a play/buffering state. It is the reliable success signal for the
// Spotify recall verify, where a bare play-state check (boxIsPlaying) and a
// bare Streaming() check each fail one way: right after a press the box can
// bounce off STR's preset to the PREVIOUS (radio) preset, which boxIsPlaying
// reads as "playing" and would wrongly skip recovery (the first-press
// double-tap); while Streaming() flaps to false even when the box is happily
// playing Spotify, and re-pointing on that flap re-attaches the box and
// restarts the track. The now_playing location tells the two apart.
func boxPlayingSpotify(boxHost string) bool {
	doc, ok := fetchNowPlaying(boxHost)
	return ok && nowPlayingIsSpotify(doc)
}

// nowPlayingIsSpotify is boxPlayingSpotify's verdict over an already-fetched
// now_playing document: STR's Spotify stream in a play or buffering state.
func nowPlayingIsSpotify(doc string) bool {
	return strings.Contains(doc, "spotify/stream") && nowPlayingIsPlaying(doc)
}

// boxReallyPlayingSpotify is the strict form of boxPlayingSpotify: the box is on
// the Spotify stream AND actually in PLAY_STATE (audio flowing), not merely
// BUFFERING. The verify loop uses it to avoid re-pointing, and thereby disrupting,
// a box that has genuinely started playing after a transient 1036 wrong-state flap
// on a preset->preset switch.
func boxReallyPlayingSpotify(boxHost string) bool {
	doc, ok := fetchNowPlaying(boxHost)
	return ok && nowPlayingReallySpotify(doc)
}

// nowPlayingReallySpotify is boxReallyPlayingSpotify's verdict over an
// already-fetched now_playing document: the Spotify stream in PLAY_STATE
// proper, not merely BUFFERING.
func nowPlayingReallySpotify(doc string) bool {
	return strings.Contains(doc, "spotify/stream") && strings.Contains(doc, "PLAY_STATE")
}
