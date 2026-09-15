package webui

import (
	"strings"
	"time"
)

// masterResumeForZone decides what the master should play again after a group
// change tore its session down, given the box's live now_playing and the last
// stream STR itself pushed.
//
// The rule it replaces was "the box reports a play state, so re-push
// s.lastPlay". That conflates two questions. lastPlay is only ever the last
// stream STR ITSELF pushed: it is written on NAND and read back at start, so it
// survives reboots and can be a week old. A busy box is not proof that the box
// is busy with that stream.
//
// Spotify is where it shows. When the user presses play on the "(STR)" Connect
// entry, the agent points the box at the engine's Ogg proxy directly
// (cmd/agent/main.go, renderer.PlayURLMime of /spotify/stream.ogg) without
// recording it as a play, so lastPlay still holds whatever station ran before.
// Forming a group then dropped the live playlist and started that old station on
// every speaker in the group. Christopher Stark, 2026-09-15, SoundTouch 20 plus
// SoundTouch 10: "wenn auf dem Hauptlautsprecher gerade meine Spotify-Playlist
// laeuft und ich den zweiten Lautsprecher dazunehme, wechseln beide Lautsprecher
// auf das zuletzt gespielte Preset".
//
// The discriminator is the ContentItem LOCATION, not the source name, and it is
// the one the periodic mirror reconcile already uses: slaveMirrorAction compares
// np.Location against the stream URL for exactly this reason, after the
// unguarded version "hijacked a slave's Spotify playback with the master's
// persisted last station every 5 minutes (#342)". Forming a zone is the one
// place that guard was never applied.
//
// blocked reports that the master is audibly on something this box cannot push
// at all (Bluetooth, AUX, the box's own Spotify). Then nothing is pushed and no
// other source is substituted: a silent group is recoverable with one key press,
// a group playing last Tuesday's station over a live session is not.
func masterResumeForZone(np nowPlayingSnapshot, ref *lastPlayInfo) (resume *lastPlayInfo, blocked bool, reason string) {
	if np.Source == "" {
		// :8090 did not answer. The caller only gets here because the box
		// reported itself busy, so the historical guess beats leaving the
		// freshly formed group silent.
		return ref, false, "now-playing unreadable, re-pushing the stream STR played last"
	}
	if ref != nil && ref.boxURL != "" && np.Location == ref.boxURL {
		return ref, false, "the master is playing STR's own stream"
	}

	stream, title, art := "", np.ItemName, ""
	if su, name, img, ok := decodeOrionStationLocation(np.Location); ok {
		// A native radio selection wraps stream URL, name and artwork in the
		// location; the same shape partnerResumeForPair unpacks for #705.
		stream, art = su, img
		if name != "" {
			title = name
		}
	} else if strings.HasPrefix(np.Location, "http://") || strings.HasPrefix(np.Location, "https://") {
		// Any UPnP push, STR's Ogg proxy included, carries the stream URL
		// directly. The master's own loopback address stays loopback: it is
		// pushed back to the same box it came from, so lanURLForPeer (which
		// exists to rewrite a PEER's 127.0.0.1) must not touch it.
		stream = np.Location
	}
	if stream == "" {
		return nil, true, "the master is on " + np.Source + ", which carries no stream STR could re-push"
	}
	return &lastPlayInfo{
			boxURL: stream,
			title:  title,
			art:    art,
			mime:   mimeFromURL(stream),
			ts:     time.Now(),
		}, false,
		"the master is playing something STR did not start, carrying that stream over instead of the recorded one"
}
