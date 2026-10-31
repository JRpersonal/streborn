package webui

import (
	"bytes"
	"html"
	"regexp"
	"sync"
	"time"
)

// wokenStop remembers that a quiet wake left the speaker paused although
// nobody paused it (#1629).
//
// The quiet wake of a group operation stops whatever the firmware resumed on
// power-on. A Pandora station has no stopped state: the firmware turns that
// STOP into PAUSE_STATE, so the speaker sits awake on the station, reported as
// paused, until its idle auto-off puts it into standby about 18 minutes later.
// Both apps lit that station's preset key as "paused" all that time. While the
// mark is set, /api/status reports STOP_STATE in place of PAUSE_STATE, so the
// apps show a stopped speaker and leave the key dark, as they do for any other
// station the quiet wake stopped.
//
// The mark only comes from the quiet wake. It is dropped the moment the
// speaker plays or buffers again, or is on another source or station, so a
// pause the user makes later (which needs playback first) keeps its label.
//
// One exception to "another source or station clears it": a group member. The
// quiet wake stops the member on its own resumed item and marks that item;
// about two seconds after the member joins the zone, its now_playing switches
// to the master's item, which the master's quiet wake left paused in the same
// way. Without a handoff the member's mark cleared on that switch and the
// member served the raw PAUSE_STATE while the master served STOP_STATE, so a
// member selected in an app read "Paused" for a stopped group. A zone join
// therefore arms a short handoff window (armHandoff): the first switch to
// another paused item inside it moves the mark to that item, once.
type wokenStop struct {
	mu           sync.Mutex
	set          bool
	source       string
	location     string
	handoffUntil time.Time
}

// wokenStopHandoff is how long after a zone join a member's mark may follow
// the switch to the master's paused item. Measured on a live group: about two
// seconds from the zoneUpdated frame to the member's source switch.
const wokenStopHandoff = 15 * time.Second

// wokenHandoff describes a mark that followed a group member onto the
// master's paused item, for the caller to log.
type wokenHandoff struct {
	fromSource, toSource string
}

// mark records the source and station the quiet wake left paused.
func (w *wokenStop) mark(source, location string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.set, w.source, w.location = true, source, location
	w.handoffUntil = time.Time{}
}

// armHandoff lets the mark follow the speaker onto one other paused item until
// the given time (a zone join, see the type comment). Without a mark it does
// nothing: a speaker the quiet wake left stopped has nothing to hand over.
func (w *wokenStop) armHandoff(until time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.set {
		return
	}
	w.handoffUntil = until
}

// active reports whether the mark is set (for tests and diagnostics).
func (w *wokenStop) active() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.set
}

var (
	nowPlayingSourceRe   = regexp.MustCompile(`source="([^"]*)"`)
	nowPlayingStatusRe   = regexp.MustCompile(`<playStatus>([^<]*)</playStatus>`)
	pauseStatePlayStatus = []byte("<playStatus>PAUSE_STATE</playStatus>")
	stopStatePlayStatus  = []byte("<playStatus>STOP_STATE</playStatus>")
)

// apply returns body with PAUSE_STATE reported as STOP_STATE while the mark
// holds for what body describes, and clears the mark once body shows the
// speaker playing, buffering, or on another source or station. body is the
// firmware's now_playing XML and stays that otherwise: clients regex-parse it.
func (w *wokenStop) apply(body []byte) []byte {
	out, _ := w.applyHandoff(body, time.Now())
	return out
}

// applyHandoff is apply at the given time. It also returns the handoff when
// the mark just followed a group member onto the master's paused item inside
// the armed window, so the caller can log it.
func (w *wokenStop) applyHandoff(body []byte, now time.Time) ([]byte, *wokenHandoff) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.set {
		return body, nil
	}
	status := ""
	if m := nowPlayingStatusRe.FindSubmatch(body); m != nil {
		status = string(m[1])
	}
	source := ""
	if m := nowPlayingSourceRe.FindSubmatch(body); m != nil {
		source = string(m[1])
	}
	location := ""
	if m := nowPlayingLocationRe.FindSubmatch(body); m != nil {
		location = html.UnescapeString(string(m[1]))
	}
	var handoff *wokenHandoff
	if status == "PAUSE_STATE" && (source != w.source || location != w.location) &&
		!w.handoffUntil.IsZero() && now.Before(w.handoffUntil) {
		// A group member just switched to the master's item, which the
		// master's quiet wake left paused: the mark follows, once.
		handoff = &wokenHandoff{fromSource: w.source, toSource: source}
		w.source, w.location = source, location
		w.handoffUntil = time.Time{}
	}
	if status != "PAUSE_STATE" || source != w.source || location != w.location {
		// Playing again, stopped for real, or somewhere else (standby too):
		// the quiet wake's pause is over. A body that carries neither a play
		// state nor a source (an error reply) proves nothing, so it leaves the
		// mark alone.
		if status != "" || source != "" {
			w.set = false
			w.handoffUntil = time.Time{}
		}
		return body, nil
	}
	return bytes.Replace(body, pauseStatePlayStatus, stopStatePlayStatus, 1), handoff
}

// applyWokenStop is wokenStop.apply with the one log line a handoff earns.
func (s *Server) applyWokenStop(body []byte) []byte {
	out, h := s.wokenStop.applyHandoff(body, time.Now())
	if h != nil && s.logger != nil {
		s.logger.Info("wake: the group's paused item is reported as stopped on this member too",
			"fromSource", h.fromSource, "toSource", h.toSource)
	}
	return out
}

// noteQuietWakeEnd logs how the quiet wake's resume watch left the speaker and
// sets the woken-stop mark when the firmware turned the STOP into a pause.
func (s *Server) noteQuietWakeEnd(np nowPlayingSnapshot, stops int) {
	s.logger.Info("wake: quiet wake resume watch ended", "source", np.Source, "playStatus", np.PlayStatus, "stops", stops)
	if np.PlayStatus != "PAUSE_STATE" {
		return
	}
	s.wokenStop.mark(np.Source, np.Location)
	s.logger.Info("wake: the speaker paused instead of stopping after the quiet wake; reporting it as stopped until it plays again", "source", np.Source)
}
