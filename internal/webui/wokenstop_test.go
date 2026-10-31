package webui

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

const pandoraLoc = "126740707481236361"

func pandoraBody(status string) string {
	return `<nowPlaying deviceID="device-id-here" source="PANDORA" sourceAccount="user@example.com">` +
		`<ContentItem source="PANDORA" type="tracklisturl" location="` + pandoraLoc + `" sourceAccount="user@example.com" isPresetable="true">` +
		`<itemName>Station</itemName></ContentItem><playStatus>` + status + `</playStatus></nowPlaying>`
}

// servedStatus serves body as the cached now_playing and returns what
// /api/status hands the apps.
func servedStatus(s *Server, body string) string {
	s.statusMu.Lock()
	s.statusBody, s.statusCode, s.statusAt = []byte(body), http.StatusOK, time.Now()
	s.statusMu.Unlock()
	return pollStatus(s).Body.String()
}

// #1629: the quiet wake of a group operation stops what the firmware resumed.
// A Pandora station turns that STOP into PAUSE_STATE, and both apps then lit
// its key as paused for the 18 minutes until the speaker's idle auto-off. The
// watch now marks that pause, and /api/status reports the speaker stopped
// until it plays again.
func TestQuietWakePandoraPauseIsReportedStopped(t *testing.T) {
	var logBuf bytes.Buffer
	s := &Server{logger: slog.New(slog.NewTextHandler(&logBuf, nil)), boxHost: "192.0.2.10"}
	stops := 0
	s.quietWakeNowPlayingFn = func(context.Context, string) nowPlayingSnapshot {
		st := "PLAY_STATE"
		if stops > 0 {
			st = "PAUSE_STATE"
		}
		return nowPlayingSnapshot{Source: "PANDORA", Location: pandoraLoc, PlayStatus: st}
	}
	s.quietWakeStopFn = func(context.Context, string) error { stops++; return nil }
	s.stopQuietWakeResume()

	if !s.wokenStop.active() {
		t.Fatal("a quiet wake that ended on PAUSE_STATE set no woken-stop mark")
	}
	if !strings.Contains(logBuf.String(), "quiet wake resume watch ended") || !strings.Contains(logBuf.String(), "playStatus=PAUSE_STATE") {
		t.Fatalf("the watch's final state was not logged:\n%s", logBuf.String())
	}
	got := servedStatus(s, pandoraBody("PAUSE_STATE"))
	if !strings.Contains(got, "<playStatus>STOP_STATE</playStatus>") || strings.Contains(got, "PAUSE_STATE") {
		t.Fatalf("woken pause served as %q, want STOP_STATE", got)
	}
	// Served again on the next poll: the mark is not used up by one read.
	if got := servedStatus(s, pandoraBody("PAUSE_STATE")); !strings.Contains(got, "STOP_STATE") {
		t.Fatalf("second poll served %q, want STOP_STATE", got)
	}

	// The user plays the station: the mark is gone, and a pause the user
	// makes from there keeps its paused label.
	if got := servedStatus(s, pandoraBody("PLAY_STATE")); !strings.Contains(got, "PLAY_STATE") {
		t.Fatalf("playing body served as %q", got)
	}
	if s.wokenStop.active() {
		t.Fatal("the mark survived the speaker playing again")
	}
	if got := servedStatus(s, pandoraBody("PAUSE_STATE")); !strings.Contains(got, "PAUSE_STATE") {
		t.Fatalf("a pause the user made was served as %q, want PAUSE_STATE", got)
	}
}

func TestWokenStopClearsOnAnotherSourceOrStation(t *testing.T) {
	for name, body := range map[string]string{
		"standby":       `<nowPlaying deviceID="device-id-here" source="STANDBY"><ContentItem source="STANDBY" isPresetable="false" /></nowPlaying>`,
		"other station": strings.Replace(pandoraBody("PAUSE_STATE"), pandoraLoc, "999", 1),
		"buffering":     pandoraBody("BUFFERING_STATE"),
	} {
		var w wokenStop
		w.mark("PANDORA", pandoraLoc, time.Now())
		out := w.apply([]byte(body))
		if string(out) != body {
			t.Errorf("%s: body rewritten to %q", name, out)
		}
		if w.active() {
			t.Errorf("%s: mark survived", name)
		}
	}
	// An error reply says nothing about the speaker: the mark stays.
	var w wokenStop
	w.mark("PANDORA", pandoraLoc, time.Now())
	w.apply([]byte(`<errors><error name="HTTP_STATUS_NOT_FOUND" /></errors>`))
	if !w.active() {
		t.Error("an error reply cleared the mark")
	}
}

const groupAlbumLoc = "0$albums$*a2"

// storedMusicBody is the master's resumed item as a member's now_playing shows
// it once the member mirrors the master.
func storedMusicBody(status string) string {
	return `<nowPlaying deviceID="device-id-here" source="STORED_MUSIC" sourceAccount="device-id-here/0">` +
		`<ContentItem source="STORED_MUSIC" location="` + groupAlbumLoc + `" sourceAccount="device-id-here/0" isPresetable="true">` +
		`<itemName>Album</itemName></ContentItem><playStatus>` + status + `</playStatus></nowPlaying>`
}

const (
	testMasterID = "MASTER-DEVICE-ID"
	testMemberID = "MEMBER-DEVICE-ID"
	testMasterIP = "192.0.2.20"
)

// groupMemberServer is a speaker following testMasterID, whose agent answers
// /api/status with what masterStatus returns (nil: the master's agent does not
// answer). Master checks and fallback reads run inline, so a test sees their
// result the moment the call that started them returns. The returned slice
// records every address the master's agent was asked at.
func groupMemberServer(logBuf *bytes.Buffer, masterStatus func() []byte) (*Server, *[]string) {
	s := &Server{logger: slog.New(slog.NewTextHandler(logBuf, nil)), boxHost: "192.0.2.10"}
	s.fetchZoneFn = func(context.Context, string) (boxapi.Zone, error) {
		return boxapi.Zone{Master: testMasterID, SenderIP: testMasterIP}, nil
	}
	s.fetchDeviceIDFn = func(context.Context, string) string { return testMemberID }
	asked := &[]string{}
	s.peerAgentStatusFn = func(_ context.Context, ip string) []byte {
		*asked = append(*asked, ip)
		if masterStatus == nil {
			return nil
		}
		return masterStatus()
	}
	s.wokenRunFn = func(f func()) { f() }
	return s, asked
}

// masterSays answers the master check with body.
func masterSays(body string) func() []byte {
	return func() []byte { return []byte(body) }
}

// withHandoffReads sets the fallback reads after a zone join for one test
// (nil: none, for a test that drives the bus frames itself).
func withHandoffReads(t *testing.T, at []time.Duration) {
	t.Helper()
	old := wokenHandoffReadsAt
	wokenHandoffReadsAt = at
	t.Cleanup(func() { wokenHandoffReadsAt = old })
}

// #1629 follow-up: a group member is quiet-woken, stopped and marked on its
// own resumed item (PANDORA here), then joins the zone and about two seconds
// later mirrors the master's item, which the master's quiet wake left paused
// too. The member's own bus carries the switch, the master's agent confirms it
// reports that item stopped, and the mark follows the member there. The first
// app poll comes long after the window and still reads STOP_STATE.
func TestWokenStopFollowsMemberOntoMastersItem(t *testing.T) {
	withHandoffReads(t, nil)
	var logBuf bytes.Buffer
	s, asked := groupMemberServer(&logBuf, masterSays(storedMusicBody("STOP_STATE")))
	s.wokenStop.mark("PANDORA", pandoraLoc, time.Now())
	s.NoteBoxZoneState(testMasterID) // zone joined: opens the handoff window
	s.NoteBoxNowPlaying([]byte(storedMusicBody("PAUSE_STATE")))
	if len(*asked) != 1 || (*asked)[0] != testMasterIP {
		t.Fatalf("master's agent asked at %v, want once at %s", *asked, testMasterIP)
	}
	if !s.wokenStop.active() || s.wokenStop.source != "STORED_MUSIC" || s.wokenStop.location != groupAlbumLoc {
		t.Fatalf("mark not moved to the master's item: set=%v %s %s", s.wokenStop.active(), s.wokenStop.source, s.wokenStop.location)
	}
	// Long after the window: the mark sits on the item now, so it holds.
	s.wokenStop.mu.Lock()
	s.wokenStop.joinedAt = time.Now().Add(-time.Hour)
	s.wokenStop.mu.Unlock()
	got := servedStatus(s, storedMusicBody("PAUSE_STATE"))
	if !strings.Contains(got, "<playStatus>STOP_STATE</playStatus>") {
		t.Fatalf("the master's paused item served on the member as %q, want STOP_STATE", got)
	}
	if !strings.Contains(logBuf.String(), "reported as stopped on this member too") || !strings.Contains(logBuf.String(), "toSource=STORED_MUSIC") {
		t.Fatalf("handoff not logged:\n%s", logBuf.String())
	}
	if got := servedStatus(s, storedMusicBody("PAUSE_STATE")); !strings.Contains(got, "STOP_STATE") {
		t.Fatalf("second poll served %q, want STOP_STATE", got)
	}
	if n := strings.Count(logBuf.String(), "reported as stopped on this member too"); n != 1 {
		t.Fatalf("handoff logged %d times, want once", n)
	}
	if len(*asked) != 1 {
		t.Fatalf("master asked %d times, want once", len(*asked))
	}
}

// With the bus silent, the agent's own read after the join finds the switch:
// nobody has to poll the member inside the window.
func TestWokenStopHandoffFoundByOwnReadAfterJoin(t *testing.T) {
	withHandoffReads(t, []time.Duration{0, 0})
	var logBuf bytes.Buffer
	s, _ := groupMemberServer(&logBuf, masterSays(storedMusicBody("STOP_STATE")))
	reads := 0
	s.boxNowReadFn = func(context.Context) (*boxapi.NowPlaying, error) {
		reads++
		return boxapi.ParseNowPlaying([]byte(storedMusicBody("PAUSE_STATE"))), nil
	}
	s.wokenStop.mark("PANDORA", pandoraLoc, time.Now())
	s.NoteBoxZoneState(testMasterID)
	if reads != 1 {
		t.Fatalf("own reads after the join = %d, want 1 (the second has nothing left to decide)", reads)
	}
	if s.wokenStop.source != "STORED_MUSIC" {
		t.Fatalf("mark not moved by the own read: %s", s.wokenStop.source)
	}
	if got := servedStatus(s, storedMusicBody("PAUSE_STATE")); !strings.Contains(got, "STOP_STATE") {
		t.Fatalf("first poll served %q, want STOP_STATE", got)
	}
}

// The fallback reads run only while a mark waits for a decision: a speaker
// without a mark reads nothing after a join, and one whose own item has not
// changed yet reads twice and then stops.
func TestWokenStopOwnReadsAreBounded(t *testing.T) {
	withHandoffReads(t, []time.Duration{0, 0})
	var logBuf bytes.Buffer
	s, _ := groupMemberServer(&logBuf, nil)
	reads := 0
	body := storedMusicBody("PAUSE_STATE")
	s.boxNowReadFn = func(context.Context) (*boxapi.NowPlaying, error) {
		reads++
		return boxapi.ParseNowPlaying([]byte(body)), nil
	}
	s.NoteBoxZoneState(testMasterID)
	if reads != 0 {
		t.Fatalf("a speaker without a mark read its state %d times after a join", reads)
	}
	body = pandoraBody("PAUSE_STATE") // still on its own item: nothing decided
	s.wokenStop.mark("PANDORA", pandoraLoc, time.Now())
	s.NoteBoxZoneState(testMasterID)
	if reads != 2 || !s.wokenStop.active() {
		t.Fatalf("reads = %d (want 2), mark set=%v", reads, s.wokenStop.active())
	}
}

// A master the user paused for real reports PAUSE_STATE. The sleeping member
// whose own item came back paused joins, switches to that item, and keeps it
// paused too: both report the pause the user made. No word from the master
// (not reached, or on another item) keeps the firmware's state as well.
func TestWokenStopNoHandoffOntoMastersRealPause(t *testing.T) {
	withHandoffReads(t, nil)
	for name, master := range map[string]func() []byte{
		"master paused":      masterSays(storedMusicBody("PAUSE_STATE")),
		"master not reached": nil,
		"master elsewhere":   masterSays(pandoraBody("STOP_STATE")),
	} {
		var logBuf bytes.Buffer
		s, _ := groupMemberServer(&logBuf, master)
		s.wokenStop.mark("PANDORA", pandoraLoc, time.Now())
		s.NoteBoxZoneState(testMasterID)
		s.NoteBoxNowPlaying([]byte(storedMusicBody("PAUSE_STATE")))
		if s.wokenStop.active() {
			t.Errorf("%s: mark followed the member onto an item the master does not report stopped", name)
		}
		if got := servedStatus(s, storedMusicBody("PAUSE_STATE")); !strings.Contains(got, "PAUSE_STATE") {
			t.Errorf("%s: served %q, want PAUSE_STATE", name, got)
		}
		if !strings.Contains(logBuf.String(), "does not follow the switch") {
			t.Errorf("%s: refused handoff not logged:\n%s", name, logBuf.String())
		}
	}
}

// On the master itself an item change inside the window is not a member
// mirroring anybody: no handoff, and no peer is asked.
func TestWokenStopNoHandoffOnMaster(t *testing.T) {
	withHandoffReads(t, nil)
	var logBuf bytes.Buffer
	s, asked := groupMemberServer(&logBuf, masterSays(storedMusicBody("STOP_STATE")))
	s.fetchDeviceIDFn = func(context.Context, string) string { return testMasterID }
	s.wokenStop.mark("PANDORA", pandoraLoc, time.Now())
	s.NoteBoxZoneState(testMasterID)
	s.NoteBoxNowPlaying([]byte(storedMusicBody("PAUSE_STATE")))
	if s.wokenStop.active() || len(*asked) != 0 {
		t.Fatalf("master: mark set=%v, peer asked %v", s.wokenStop.active(), *asked)
	}
}

// Before the paused item arrives the member can pass through a frame with no
// settled state; inside the window that frame does not end the mark.
func TestWokenStopHandoffSurvivesFrameOnTheWay(t *testing.T) {
	withHandoffReads(t, nil)
	var logBuf bytes.Buffer
	s, _ := groupMemberServer(&logBuf, masterSays(storedMusicBody("STOP_STATE")))
	s.wokenStop.mark("PANDORA", pandoraLoc, time.Now())
	s.NoteBoxZoneState(testMasterID)
	s.NoteBoxNowPlaying([]byte(storedMusicBody("BUFFERING_STATE")))
	if !s.wokenStop.active() {
		t.Fatal("a frame on the way to the master's item cleared the mark")
	}
	s.NoteBoxNowPlaying([]byte(storedMusicBody("PAUSE_STATE")))
	if s.wokenStop.source != "STORED_MUSIC" {
		t.Fatalf("mark not moved: %s", s.wokenStop.source)
	}
	// Without a window the same frame ends the mark, as before.
	var w wokenStop
	w.mark("PANDORA", pandoraLoc, time.Now())
	w.apply([]byte(storedMusicBody("BUFFERING_STATE")))
	if w.active() {
		t.Fatal("buffering on another item without a window kept the mark")
	}
}

// Default-group path: the master's wake request timed out and it formed the
// zone while the member's quiet wake was still running, so the member's join
// arrived before its mark. The mark opens the window for what is left of it,
// and the switch that follows still hands off.
func TestWokenStopJoinBeforeMarkStillHandsOff(t *testing.T) {
	withHandoffReads(t, nil)
	var logBuf bytes.Buffer
	s, asked := groupMemberServer(&logBuf, masterSays(storedMusicBody("STOP_STATE")))
	s.NoteBoxZoneState(testMasterID) // the join, before the quiet wake ends
	s.noteQuietWakeEnd(nowPlayingSnapshot{Source: "PANDORA", Location: pandoraLoc, PlayStatus: "PAUSE_STATE"}, 1)
	// The marked item was put to the master (it could already have been the
	// master's); the master is on another item, so the mark stays.
	if !s.wokenStop.active() || s.wokenStop.source != "PANDORA" || len(*asked) != 1 {
		t.Fatalf("mark after the join: set=%v %s, asked %v", s.wokenStop.active(), s.wokenStop.source, *asked)
	}
	s.NoteBoxNowPlaying([]byte(storedMusicBody("PAUSE_STATE")))
	if s.wokenStop.source != "STORED_MUSIC" {
		t.Fatalf("mark not handed off after a join that came first: %s", s.wokenStop.source)
	}
}

// Same path, but the switch happened during the quiet wake: the watch ends on
// the master's item, which the user paused there. The master says so, and the
// member does not report it stopped.
func TestWokenStopMarkAfterJoinOnMastersRealPauseClears(t *testing.T) {
	withHandoffReads(t, nil)
	var logBuf bytes.Buffer
	s, _ := groupMemberServer(&logBuf, masterSays(storedMusicBody("PAUSE_STATE")))
	s.NoteBoxZoneState(testMasterID)
	s.noteQuietWakeEnd(nowPlayingSnapshot{Source: "STORED_MUSIC", Location: groupAlbumLoc, PlayStatus: "PAUSE_STATE"}, 1)
	if s.wokenStop.active() {
		t.Fatal("member marked the master's real pause as a woken stop")
	}
	if !strings.Contains(logBuf.String(), "reports this item paused") {
		t.Fatalf("not logged:\n%s", logBuf.String())
	}
}

func TestWokenStopWithoutHandoffClearsOnAnotherItem(t *testing.T) {
	var w wokenStop
	w.mark("PANDORA", pandoraLoc, time.Now())
	body := storedMusicBody("PAUSE_STATE")
	if out := w.apply([]byte(body)); string(out) != body {
		t.Fatalf("body rewritten to %q without a zone join", out)
	}
	if w.active() {
		t.Fatal("mark survived another item without a handoff window")
	}
}

// After the handoff, the speaker playing clears the mark as usual, and a pause
// the user makes from there keeps its label.
func TestWokenStopHandoffThenPlayClears(t *testing.T) {
	withHandoffReads(t, nil)
	var logBuf bytes.Buffer
	s, _ := groupMemberServer(&logBuf, masterSays(storedMusicBody("STOP_STATE")))
	s.wokenStop.mark("PANDORA", pandoraLoc, time.Now())
	s.NoteBoxZoneState(testMasterID)
	s.NoteBoxNowPlaying([]byte(storedMusicBody("PAUSE_STATE")))
	if out := servedStatus(s, storedMusicBody("PAUSE_STATE")); !strings.Contains(out, "STOP_STATE") {
		t.Fatalf("handoff served %q, want STOP_STATE", out)
	}
	s.NoteBoxNowPlaying([]byte(storedMusicBody("PLAY_STATE")))
	if s.wokenStop.active() {
		t.Fatal("mark survived the group playing")
	}
	if out := servedStatus(s, storedMusicBody("PAUSE_STATE")); !strings.Contains(out, "PAUSE_STATE") {
		t.Fatalf("a pause the user made was served as %q, want PAUSE_STATE", out)
	}
}

func TestWokenStopJoinWithoutMarkOpensNoWindow(t *testing.T) {
	var w wokenStop
	if seq := w.noteJoin(time.Now()); seq != 0 || w.active() || !w.handoffUntil.IsZero() {
		t.Fatal("a join without a mark opened a window")
	}
	if out := w.apply([]byte(storedMusicBody("PAUSE_STATE"))); !bytes.Contains(out, pauseStatePlayStatus) {
		t.Fatalf("unmarked pause served as %q", out)
	}
}

func TestWokenStopExpiredHandoffClears(t *testing.T) {
	var w wokenStop
	w.mark("PANDORA", pandoraLoc, time.Now())
	w.noteJoin(time.Now().Add(-wokenStopHandoff - time.Second))
	body := storedMusicBody("PAUSE_STATE")
	out, check := w.applyAt([]byte(body), time.Now())
	if string(out) != body || check != nil {
		t.Fatalf("body rewritten to %q (check %v) after the window ran out", out, check)
	}
	if w.active() {
		t.Fatal("mark survived another item after the window ran out")
	}
}

// Only one handoff per window: a second item change inside it clears.
func TestWokenStopHandsOffOnce(t *testing.T) {
	withHandoffReads(t, nil)
	var logBuf bytes.Buffer
	s, asked := groupMemberServer(&logBuf, masterSays(storedMusicBody("STOP_STATE")))
	s.wokenStop.mark("PANDORA", pandoraLoc, time.Now())
	s.NoteBoxZoneState(testMasterID)
	s.NoteBoxNowPlaying([]byte(storedMusicBody("PAUSE_STATE")))
	if !s.wokenStop.active() {
		t.Fatal("first handoff cleared the mark")
	}
	body := strings.Replace(storedMusicBody("PAUSE_STATE"), groupAlbumLoc, "0$albums$*a9", 1)
	if out := servedStatus(s, body); out != body {
		t.Fatalf("second item change rewritten to %q", out)
	}
	if s.wokenStop.active() || len(*asked) != 1 {
		t.Fatalf("mark followed a second item change (set=%v, asked %v)", s.wokenStop.active(), *asked)
	}
}

// A master answer that comes back after the mark moved on changes nothing.
func TestWokenStopLateMasterAnswerIsDropped(t *testing.T) {
	var w wokenStop
	w.mark("PANDORA", pandoraLoc, time.Now())
	w.noteJoin(time.Now())
	_, check := w.applyAt([]byte(storedMusicBody("PAUSE_STATE")), time.Now())
	if check == nil || !check.handoff {
		t.Fatalf("a switch inside the window started no check: %+v", check)
	}
	w.apply([]byte(storedMusicBody("PLAY_STATE"))) // the group plays meanwhile
	w.mark("PANDORA", pandoraLoc, time.Now())      // and a new quiet wake marks again
	if got := w.resolve(*check, masterStopped); got != "" {
		t.Fatalf("stale answer applied: %q", got)
	}
	if w.source != "PANDORA" {
		t.Fatalf("stale answer moved the mark to %s", w.source)
	}
}

func TestQuietWakeStoppedSpeakerSetsNoMark(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), boxHost: "192.0.2.10"}
	stops := 0
	s.quietWakeNowPlayingFn = func(context.Context, string) nowPlayingSnapshot {
		st := "PLAY_STATE"
		if stops > 0 {
			st = "STOP_STATE"
		}
		return nowPlayingSnapshot{Source: "LOCAL_INTERNET_RADIO", Location: "http://192.0.2.1/s", PlayStatus: st}
	}
	s.quietWakeStopFn = func(context.Context, string) error { stops++; return nil }
	s.stopQuietWakeResume()
	if s.wokenStop.active() {
		t.Fatal("a speaker the quiet wake stopped got a woken-stop mark")
	}
	// Without a mark, a paused speaker is served as paused.
	if got := servedStatus(s, pandoraBody("PAUSE_STATE")); !strings.Contains(got, "PAUSE_STATE") {
		t.Fatalf("unmarked pause served as %q", got)
	}
}
