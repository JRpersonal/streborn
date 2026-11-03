package webui

// A playback dropout that happened while this speaker's Wi-Fi signal was weak.
//
// Until this existed the signal grade was looked at only before an update and
// during setup. About fourteen owners wrote in over October 2026 about music
// that kept dropping out, and in every one of those threads the cause turned
// out to be poor reception at the speaker. Nothing on screen tied the dropout
// to the signal, so the owner suspected the station, the app or the speaker.
//
// The agent already sees the dropouts: the stream proxy reconnects when the
// station's connection breaks or stalls mid-stream (the radio and library
// path), and the Spotify drop recovery (maybeRePush) finds the speaker let go
// of a stream while it stayed on and idle, after ruling out a power-off, a
// stop, a source switch and a fresh play. At each such moment this file reads the speaker's CURRENT
// signal grade, with the same rule the update pre-flight uses in the desktop
// app (main.js doBoxUpdate): a live Wi-Fi reading of MARGINAL_SIGNAL or
// POOR_SIGNAL. When the grade is weak it keeps a marker, which the version
// envelope reports for an hour so the app can say "the Wi-Fi signal at this
// speaker is weak, which can cause dropouts" on that speaker.
//
// What counts as a live reading is deliberately narrow, exactly as narrow as
// the update gate:
//
//   - wpa chassis: wpa_supplicant's own RSSI (wlanlive), which also overrides
//     the firmware's stale /networkInfo after STR switched networks;
//   - other chassis: the firmware's /networkInfo signal on a connected
//     WIFI_INTERFACE.
//
// The coprocessor chassis' gabbo snapshot is NOT used. It is the firmware's
// reading at the last connection change, usually boot, and it false-alarmed on
// a Portable sitting next to its router (2026-07-12). Those speakers simply
// never get this marker; a wrong "your Wi-Fi is weak" would send an owner to
// move furniture for nothing.
//
// No timer, no polling: the reading is taken only when a dropout happens, at
// most once per weakWifiCheckEvery, and a reconnect storm costs one read.

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/wlanlive"
)

const (
	// weakWifiDropoutWindow is how long the marker is reported after the last
	// dropout seen on a weak signal. Long enough for an owner who heard the
	// music stop to open the app and find the reason; short enough that a
	// speaker which was moved closer stops being told about it the same
	// evening.
	weakWifiDropoutWindow = time.Hour
	// weakWifiCheckEvery bounds how often a dropout reads the signal. A
	// station that drops every few seconds would otherwise run wpa_cli or ask
	// the firmware for /networkInfo once per drop.
	weakWifiCheckEvery = time.Minute
	// weakWifiReadTimeout bounds one reading.
	weakWifiReadTimeout = 5 * time.Second
)

// weakWifiDropout is the marker. Zero value is ready to use.
type weakWifiDropout struct {
	mu        sync.Mutex
	at        time.Time // last dropout seen on a weak signal; zero = none
	signal    string    // the grade read then (MARGINAL_SIGNAL / POOR_SIGNAL)
	source    string    // what dropped: "radio" or "spotify"
	count     int       // dropouts on a weak signal inside the current window
	lastCheck time.Time // when a dropout last read the signal
	lastWeak  bool      // that reading was weak
	checking  bool      // a reading is in flight

	// readSignal stands in for the speaker's signal reading in tests. nil =
	// read the speaker (readDropoutWifiSignal). Per server rather than a
	// package variable, so a test swapping it cannot race a re-push goroutine
	// another test left running.
	readSignal func(context.Context) string
}

// weakSignalClass reports whether a signal class is one the app calls weak.
// The same two classes the update pre-flight warns on (main.js).
func weakSignalClass(sig string) bool {
	return sig == "MARGINAL_SIGNAL" || sig == "POOR_SIGNAL"
}

// liveWifiSignal picks the signal of the connected Wi-Fi interface from
// annotated /networkInfo interfaces, the same interface the update pre-flight
// reads: type WIFI_INTERFACE, state NETWORK_WIFI_CONNECTED. "" when there is
// none (a cable, a coprocessor chassis, a speaker between networks).
func liveWifiSignal(ifaces []boxapi.NetworkInterface) string {
	for _, ni := range ifaces {
		if ni.Type == "WIFI_INTERFACE" && strings.TrimSpace(ni.State) == "NETWORK_WIFI_CONNECTED" {
			return ni.Signal
		}
	}
	return ""
}

// readDropoutWifiSignal reads the speaker's current Wi-Fi signal class for a
// dropout ("" = no reading).
func (s *Server) readDropoutWifiSignal(ctx context.Context) string {
	s.weakWifi.mu.Lock()
	hook := s.weakWifi.readSignal
	s.weakWifi.mu.Unlock()
	if hook != nil {
		return hook(ctx)
	}
	if s.boxHost == "" {
		return ""
	}
	net, err := boxapi.New(s.boxHost).GetNetwork(ctx)
	if err != nil {
		return ""
	}
	var live *wlanlive.Status
	if iface, mech := detectWlanMechanism(); mech == "wpa" {
		if st := wlanlive.Read(ctx, iface); st.Associated {
			live = &st
		}
	}
	return dropoutSignalFrom(net.Interfaces, live)
}

// dropoutSignalFrom combines the firmware's interfaces with wpa_supplicant's
// live association (nil off the wpa chassis) the way the settings screen does,
// minus the gabbo snapshot and STR's own SSID: only live readings count here
// (see the file comment).
func dropoutSignalFrom(ifaces []boxapi.NetworkInterface, live *wlanlive.Status) string {
	annotateNetworkForDisplay(ifaces, live, "", "")
	return liveWifiSignal(ifaces)
}

// NotePlaybackDropout records that playback just dropped out (source names
// the path: "radio" for the stream proxy, "spotify" for the Spotify drop recovery) and
// checks the speaker's signal in the background. Never blocks the caller,
// which is the stream copy loop or the Spotify manager.
func (s *Server) NotePlaybackDropout(source string) {
	s.noteDropoutAt(source, time.Now(), true)
}

// noteDropoutAt is NotePlaybackDropout with the clock and the goroutine made
// explicit for tests: async=false reads the signal inline.
func (s *Server) noteDropoutAt(source string, now time.Time, async bool) {
	w := &s.weakWifi
	w.mu.Lock()
	if w.checking || (!w.lastCheck.IsZero() && now.Sub(w.lastCheck) < weakWifiCheckEvery) {
		// Inside the rate limit. If the last reading was weak, this dropout
		// still counts and keeps the marker fresh: the signal was weak less
		// than a minute ago, and it is the same stretch of trouble. The
		// reading clock is NOT moved, so a steady run of drops still reads the
		// signal again once a minute and notices when it recovers.
		if !w.checking && w.lastWeak && !w.at.IsZero() {
			w.at = now
			w.count++
		}
		w.mu.Unlock()
		return
	}
	w.checking = true
	w.lastCheck = now
	w.mu.Unlock()

	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), weakWifiReadTimeout)
		defer cancel()
		sig := s.readDropoutWifiSignal(ctx)
		s.recordDropoutSignal(source, sig, now)
	}
	if async {
		go run()
		return
	}
	run()
}

// recordDropoutSignal stores what the reading for a dropout at `at` found.
// A weak grade sets (or refreshes) the marker. A good grade clears it: the
// newest reading says this dropout was not the Wi-Fi, and the hint must not
// keep blaming a signal that has since recovered (the owner moved the
// speaker). An empty grade (no reading) leaves the marker as it was.
func (s *Server) recordDropoutSignal(source, sig string, at time.Time) {
	w := &s.weakWifi
	w.mu.Lock()
	w.checking = false
	weak := weakSignalClass(sig)
	switch {
	case weak:
		if w.at.IsZero() || at.Sub(w.at) > weakWifiDropoutWindow {
			w.count = 0
		}
		w.at = at
		w.lastWeak = true
		w.signal = sig
		w.source = source
		w.count++
	case sig != "":
		w.lastWeak = false
		w.at = time.Time{}
		w.signal = ""
		w.source = ""
		w.count = 0
	default:
		// No reading (a cable, a coprocessor chassis, the firmware too busy
		// to answer): no evidence either way, the marker stays as it was but
		// later drops inside the rate limit no longer refresh it.
		w.lastWeak = false
	}
	count := w.count
	w.mu.Unlock()
	if weak && s.logger != nil {
		s.logger.Info("playback dropout on a weak Wi-Fi signal",
			"source", source, "signal", sig, "countInWindow", count)
	}
}

// WeakWifiDropout reports the marker while it is recent: the signal class
// read at the last dropout, what dropped, and how long ago.
func (s *Server) WeakWifiDropout() (signal, source string, age time.Duration, ok bool) {
	return s.weakWifiDropoutAt(time.Now())
}

func (s *Server) weakWifiDropoutAt(now time.Time) (signal, source string, age time.Duration, ok bool) {
	w := &s.weakWifi
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.at.IsZero() {
		return "", "", 0, false
	}
	age = now.Sub(w.at)
	if age < 0 {
		age = 0
	}
	if age > weakWifiDropoutWindow {
		return "", "", 0, false
	}
	return w.signal, w.source, age, true
}

// WeakWifiDropoutSnapshot backs the weak_wifi_dropout debug section: the
// marker as it stands, including an expired one, for a diagnostic bundle.
func (s *Server) WeakWifiDropoutSnapshot() any {
	w := &s.weakWifi
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[string]any{"count": w.count}
	if !w.at.IsZero() {
		out["at"] = w.at.UTC().Format(time.RFC3339)
		out["signal"] = w.signal
		out["source"] = w.source
	}
	if !w.lastCheck.IsZero() {
		out["lastCheck"] = w.lastCheck.UTC().Format(time.RFC3339)
		out["lastCheckWeak"] = w.lastWeak
	}
	return out
}
