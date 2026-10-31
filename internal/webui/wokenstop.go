package webui

import (
	"bytes"
	"context"
	"html"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/netutil"
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
// to the master's item. When the master's quiet wake left that item paused in
// the same way, the member has to report it stopped too, or a member selected
// in an app reads "Paused" for a group the master reports stopped. So a zone
// join opens a short handoff window, and the first switch to another paused
// item inside it is put to the master's agent: only when the master reports
// that very item as stopped does the mark move there. A master the user paused
// for real reports PAUSE_STATE, and then the member's mark clears as before,
// so both report the pause the user made.
//
// The window is decided on the agent side. Every now_playing document the
// speaker's own bus carries goes through observe (NoteBoxNowPlaying), and two
// bounded reads of the speaker's own now_playing after the join stand in for a
// bus that is down. It does not wait for an app to poll /api/status at the
// right moment.
type wokenStop struct {
	mu       sync.Mutex
	set      bool
	source   string
	location string
	// gen counts every mark set or cleared, so a master's answer that comes
	// back after the mark moved on is dropped.
	gen uint64
	// joinedAt is the last zone join the firmware announced. Kept with or
	// without a mark: on the default-group path the member's join can arrive
	// before its own quiet wake has finished and set the mark.
	joinedAt time.Time
	// handoffUntil is the end of the window in which the mark may follow the
	// speaker onto one other paused item. Zero when no window is open.
	handoffUntil time.Time
	// armSeq counts the windows opened, so only the newest one runs its
	// fallback reads.
	armSeq uint64
	// pending is the item waiting for the master's word, nil otherwise.
	pending *wokenItem
}

// wokenItem is a source and station as now_playing names them.
type wokenItem struct {
	source, location string
}

// wokenCheck asks the master's agent about one item. handoff is true for a
// switch to another item (the mark moves there only on the master's word) and
// false for the marked item itself, set after the speaker had already joined a
// zone, when it may already be the master's item.
type wokenCheck struct {
	item    wokenItem
	gen     uint64
	handoff bool
}

// wokenStopHandoff is how long after a zone join a member's mark may follow
// the switch to the master's paused item. Measured on a live group: about two
// seconds from the zoneUpdated frame to the member's source switch.
const wokenStopHandoff = 15 * time.Second

// wokenHandoffReadsAt are the moments after a window opens at which the
// speaker's own now_playing is read once, while the window is still open and
// nothing has decided it yet. A fallback for a bus that is down or missed the
// switch; a var so the tests can shorten it.
var wokenHandoffReadsAt = []time.Duration{5 * time.Second, 11 * time.Second}

// mark records the source and station the quiet wake left paused. When the
// speaker joined a zone moments ago, the window is opened for what is left of
// it and a check comes back: the item may already be the master's.
func (w *wokenStop) mark(source, location string, now time.Time) (check *wokenCheck, armed uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.set, w.source, w.location = true, source, location
	w.gen++
	w.pending = nil
	w.handoffUntil = time.Time{}
	if w.joinedAt.IsZero() || !now.Before(w.joinedAt.Add(wokenStopHandoff)) {
		return nil, 0
	}
	w.handoffUntil = w.joinedAt.Add(wokenStopHandoff)
	w.armSeq++
	return &wokenCheck{item: wokenItem{source, location}, gen: w.gen}, w.armSeq
}

// noteJoin records a zone join and, when a mark is set, opens the handoff
// window. armed is the window's sequence number, 0 when none opened.
func (w *wokenStop) noteJoin(now time.Time) (armed uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.joinedAt = now
	if !w.set {
		return 0
	}
	w.handoffUntil = now.Add(wokenStopHandoff)
	w.armSeq++
	return w.armSeq
}

// undecided reports whether window seq is still the newest and still open with
// nothing decided, so a fallback read has something to do.
func (w *wokenStop) undecided(seq uint64, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.set && w.armSeq == seq && w.windowOpen(now)
}

// windowOpen reports whether the handoff window is open. Caller holds mu.
func (w *wokenStop) windowOpen(now time.Time) bool {
	return !w.handoffUntil.IsZero() && now.Before(w.handoffUntil)
}

// clear drops the mark. Caller holds mu.
func (w *wokenStop) clear() {
	w.set = false
	w.gen++
	w.pending = nil
	w.handoffUntil = time.Time{}
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

// parseWokenBody reads the play state, source and station out of a
// now_playing body: the firmware's own, a bus frame's <nowPlaying>, or another
// agent's /api/status.
func parseWokenBody(body []byte) (status string, item wokenItem) {
	if m := nowPlayingStatusRe.FindSubmatch(body); m != nil {
		status = string(m[1])
	}
	if m := nowPlayingSourceRe.FindSubmatch(body); m != nil {
		item.source = string(m[1])
	}
	if m := nowPlayingLocationRe.FindSubmatch(body); m != nil {
		item.location = html.UnescapeString(string(m[1]))
	}
	return status, item
}

// apply returns body with PAUSE_STATE reported as STOP_STATE while the mark
// holds for what body describes, and clears the mark once body shows the
// speaker playing, buffering, or on another source or station. body is the
// firmware's now_playing XML and stays that otherwise: clients regex-parse it.
// A check it starts is left to the caller (Server.observeWokenStop).
func (w *wokenStop) apply(body []byte) []byte {
	out, _ := w.applyAt(body, time.Now())
	return out
}

// applyAt is apply at the given time, returning the master check a switch
// inside the handoff window needs.
func (w *wokenStop) applyAt(body []byte, now time.Time) ([]byte, *wokenCheck) {
	status, item := parseWokenBody(body)
	rewrite, check := w.observe(status, item, now)
	if rewrite {
		return bytes.Replace(body, pauseStatePlayStatus, stopStatePlayStatus, 1), check
	}
	return body, check
}

// observe takes one look at the speaker's state and says whether a
// PAUSE_STATE in it is to be reported as stopped.
func (w *wokenStop) observe(status string, item wokenItem, now time.Time) (rewrite bool, check *wokenCheck) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.set {
		return false, nil
	}
	if status == "" && item.source == "" {
		// Neither a play state nor a source (an error reply): proves nothing.
		return false, nil
	}
	onMark := item.source == w.source && item.location == w.location
	switch {
	case onMark && status == "PAUSE_STATE":
		return true, nil
	case onMark && status == "":
		// The marked item without a play state proves nothing either.
		return false, nil
	case w.pending != nil && item == *w.pending && status == "PAUSE_STATE":
		// The candidate is still waiting for the master's word: served as the
		// firmware reports it until the master confirms.
		return false, nil
	case !onMark && status == "PAUSE_STATE" && w.pending == nil && w.windowOpen(now):
		// A group member switched to the master's item inside the window. The
		// master's agent decides; the window is used up either way.
		w.pending = &item
		w.handoffUntil = time.Time{}
		return false, &wokenCheck{item: item, gen: w.gen, handoff: true}
	case !onMark && (w.pending != nil || w.windowOpen(now)) &&
		item.source != "STANDBY" && status != "PLAY_STATE" && status != "STOP_STATE" && status != "PAUSE_STATE":
		// The zone join is moving the speaker between two items: a frame on
		// the way (buffering, no play state yet) proves nothing.
		return false, nil
	}
	// Playing again, stopped for real, or somewhere else (standby too): the
	// quiet wake's pause is over.
	w.clear()
	return false, nil
}

// masterWord is what the master's agent said about a checked item.
type masterWord int

const (
	// masterUnknown: no word on this item (not in a zone, this speaker leads,
	// the master's agent does not answer, or the master is on another item).
	masterUnknown masterWord = iota
	// masterStopped: the master reports this item as STOP_STATE, which for a
	// paused item is its own woken-stop mark.
	masterStopped
	// masterNotStopped: the master reports this item playing or paused, which
	// means somebody chose that state there.
	masterNotStopped
)

// resolve applies the master's word to a check, unless the mark moved on since
// it was asked. It reports what happened, "" when nothing did.
func (w *wokenStop) resolve(c wokenCheck, word masterWord) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.set || w.gen != c.gen {
		return ""
	}
	if c.handoff {
		if w.pending == nil || *w.pending != c.item {
			return ""
		}
		w.pending = nil
		if word == masterStopped {
			w.source, w.location = c.item.source, c.item.location
			return "moved"
		}
		w.clear()
		return "cleared"
	}
	if w.source != c.item.source || w.location != c.item.location {
		return ""
	}
	if word == masterNotStopped {
		w.clear()
		return "cleared"
	}
	return ""
}

// NoteBoxNowPlaying takes a <nowPlaying> document the speaker's own bus just
// carried, so the woken-stop mark follows the speaker without waiting for an
// app to poll. Must not block: the bus read loop calls it, and the master check
// a switch may need runs off it.
func (s *Server) NoteBoxNowPlaying(raw []byte) {
	s.observeWokenStop(raw)
}

// observeWokenStop is wokenStop.apply for this server: it starts the master
// check a switch inside the handoff window needs.
func (s *Server) observeWokenStop(body []byte) []byte {
	out, check := s.wokenStop.applyAt(body, time.Now())
	if check != nil {
		s.startWokenCheck(*check)
	}
	return out
}

// noteWokenStopJoin opens the handoff window on a zone join (NoteBoxZoneState)
// and starts the fallback reads when a mark is set.
func (s *Server) noteWokenStopJoin() {
	if seq := s.wokenStop.noteJoin(time.Now()); seq != 0 {
		s.startWokenHandoffReads(seq)
	}
}

// startWokenCheck asks the master about c off the caller's goroutine.
func (s *Server) startWokenCheck(c wokenCheck) {
	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), wokenCheckBudget)
		defer cancel()
		word, why := s.masterWordOn(ctx, c.item)
		s.logWokenResolve(c, s.wokenStop.resolve(c, word), why)
	}
	if s.wokenRunFn != nil {
		s.wokenRunFn(run)
		return
	}
	go run()
}

// wokenCheckBudget bounds one master check: two firmware reads on this
// speaker and the master's agent on up to two ports.
const wokenCheckBudget = 12 * time.Second

// logWokenResolve logs what a master check did to the mark.
func (s *Server) logWokenResolve(c wokenCheck, outcome, why string) {
	if s.logger == nil || outcome == "" {
		return
	}
	switch {
	case c.handoff && outcome == "moved":
		s.logger.Info("wake: the group's paused item is reported as stopped on this member too",
			"toSource", c.item.source, "master", why)
	case c.handoff:
		s.logger.Info("wake: the woken-stop mark does not follow the switch to another paused item; reported as the speaker reports it",
			"toSource", c.item.source, "master", why)
	default:
		s.logger.Info("wake: the group's master reports this item paused, so this member reports it paused too",
			"source", c.item.source, "master", why)
	}
}

// startWokenHandoffReads reads the speaker's own now_playing at the moments in
// wokenHandoffReadsAt, while window seq is the newest and still undecided. At
// most two loopback reads per zone join, and only while a mark is set.
func (s *Server) startWokenHandoffReads(seq uint64) {
	start := time.Now()
	run := func() {
		for _, at := range wokenHandoffReadsAt {
			if d := time.Until(start.Add(at)); d > 0 {
				time.Sleep(d)
			}
			if !s.wokenStop.undecided(seq, time.Now()) {
				return
			}
			np, err := s.readBoxNowWithin(snapshotReadTimeout)
			if err != nil {
				continue
			}
			s.observeWokenStop(np.Raw)
		}
	}
	if s.wokenRunFn != nil {
		s.wokenRunFn(run)
		return
	}
	go run()
}

// masterWordOn asks the master of the zone this speaker follows what it
// reports for item. why says where the word came from, for the log.
func (s *Server) masterWordOn(ctx context.Context, item wokenItem) (masterWord, string) {
	if s.boxHost == "" {
		return masterUnknown, "no box host"
	}
	z, err := s.fetchZone(ctx, s.boxHost)
	master := strings.TrimSpace(z.Master)
	if err != nil || master == "" {
		return masterUnknown, "not in a zone"
	}
	own := s.fetchDeviceID(ctx, s.boxHost)
	if own == "" || strings.EqualFold(own, master) {
		// This speaker leads (or cannot say who it is): an item change here is
		// not a member mirroring its master.
		return masterUnknown, "this speaker leads"
	}
	masterIP := strings.TrimSpace(z.SenderIP)
	if masterIP == "" {
		return masterUnknown, "no master address"
	}
	body := s.peerAgentStatus(ctx, masterIP)
	if len(body) == 0 {
		// An old master agent answers too, with the firmware's PAUSE_STATE, so
		// silence means unreachable (a series-I firewall between the two).
		return masterUnknown, "master's agent not answering"
	}
	status, got := parseWokenBody(body)
	if got != item {
		return masterUnknown, "master on another item"
	}
	if status == "STOP_STATE" {
		return masterStopped, "stopped"
	}
	return masterNotStopped, status
}

// peerAgentStatus reads another speaker's /api/status from its STR agent, on
// either agent port (the chassis decides which one answers). nil when neither
// does. The host is a bare LAN address and the dial is guarded, the same two
// gates fetchNowPlayingPeer applies.
func (s *Server) peerAgentStatus(ctx context.Context, ip string) []byte {
	if s.peerAgentStatusFn != nil {
		return s.peerAgentStatusFn(ctx, ip)
	}
	if !isLANPeer(ip) {
		return nil
	}
	client := netutil.GuardedClient(4 * time.Second)
	for _, port := range []string{"17008", "8888"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			"http://"+net.JoinHostPort(ip, port)+"/api/status", nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			continue
		}
		return body
	}
	return nil
}

// noteQuietWakeEnd logs how the quiet wake's resume watch left the speaker and
// sets the woken-stop mark when the firmware turned the STOP into a pause.
func (s *Server) noteQuietWakeEnd(np nowPlayingSnapshot, stops int) {
	s.logger.Info("wake: quiet wake resume watch ended", "source", np.Source, "playStatus", np.PlayStatus, "stops", stops)
	if np.PlayStatus != "PAUSE_STATE" {
		return
	}
	check, armed := s.wokenStop.mark(np.Source, np.Location, time.Now())
	s.logger.Info("wake: the speaker paused instead of stopping after the quiet wake; reporting it as stopped until it plays again", "source", np.Source)
	if check != nil {
		// The speaker joined a zone while the quiet wake was still running, so
		// what it paused on may already be the master's item.
		s.startWokenCheck(*check)
	}
	if armed != 0 {
		s.startWokenHandoffReads(armed)
	}
}
