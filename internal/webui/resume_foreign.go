// Foreign-content guard for the automatic power-on resume.
//
// The DO_NOT_RESUME restore that drives ResumeLastPlay is not only sent on a
// power-on. The firmware sends the identical frame when it tears STR's UPnP
// source down, and one of the ways it does that is a failed skip: the box
// cannot skip a UPnP source, answers QPLAY_SKIP_*_FAILED and drops the source
// (see webui.HardwareSkip and internal/boxws). During that drop the box reports
// no play state at all, so the busy check ResumeLastPlay relies on reads "awake
// and idle" and the resume pushes STR's last station.
//
// That is harmless while the speaker is STR's. It is not harmless when somebody
// is streaming to the speaker from something else: pressing Next on the remote
// during a Music Assistant stream made the speaker jump to an STR station, which
// the reporter described as the speaker wanting to switch presets (Christian1985l,
// discussion #827).
//
// boxws now suppresses the wake for the skip it can see on the bus. This is the
// second, independent half: whatever the frame claimed, a speaker that is
// carrying content STR does not serve belongs to whoever put it there, and the
// automatic resume stands down.
//
// Freshness is part of the test, not an extra. A ContentItem alone says what the
// box is TUNED to, not what is happening now: the firmware restores the last
// selection when it comes out of standby, so a user whose evening ended on Music
// Assistant is still carrying that foreign item the next morning. Standing down
// on it would silently turn the power-on resume off for that user for good, with
// one log line to show for it. So the stand-down needs the item AND a live play
// status in the same reading (PLAY / BUFFERING / PAUSE): that is a speaker
// somebody is using right now. A restored selection on a box that reports no
// play state is history, and the resume proceeds.

package webui

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// strAgentPorts are the ports an STR agent answers audio on: 8888 direct (sm2
// chassis) and 17008 through the PREROUTING REDIRECT (BCO chassis), the latter
// also being the mirror proxy a group follower pulls from. A location on either
// comes from an STR agent, this box's own or another speaker's.
var strAgentPorts = map[string]bool{"8888": true, "17008": true}

// locationServedBySTR reports whether a box-facing content location is one STR
// itself put there. ownStream is the resume target (the stream STR last played),
// used for the one shape that is ours without carrying an STR address: a media
// library track, which STR pushes straight from the user's NAS.
//
// It answers TRUE for anything it cannot judge. The guard exists to keep STR off
// a speaker that demonstrably belongs to someone else right now, and an empty,
// relative or unparseable location demonstrates nothing - refusing to resume on
// it would break the ordinary power-on case (a bare INVALID_SOURCE with no
// ContentItem is the normal wake signature, see boxStuckSelection).
func locationServedBySTR(loc, ownStream string) bool {
	loc = strings.TrimSpace(loc)
	if loc == "" {
		return true
	}
	// A native radio ContentItem is a station descriptor, not a stream address:
	// the box resolves it through STR's own LOCAL_INTERNET_RADIO adapter. The
	// speaker reports it in the relative "/station?data=" form, which has no
	// host to judge anyway.
	if strings.Contains(loc, "/station?data=") {
		return true
	}
	u, err := url.Parse(loc)
	if err != nil || u.Host == "" {
		return true // not an address we can attribute: see the contract above
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1":
		return true // this agent's own proxy
	}
	if strAgentPorts[u.Port()] {
		return true
	}
	// The library case: STR pushed a track from a media server, so the box
	// carries the server's address, not STR's. The resume target names that
	// same server (the queue plays one server at a time), which is what tells
	// this apart from a stream a foreign controller pushed.
	if hp := hostPortOf(ownStream); hp != "" && hp == hostPortOf(loc) {
		return true
	}
	return false
}

// foreignLocation returns the first location among locs that STR does not serve.
// Callers pass what the box reported at the wake and what it reports now, so a
// content item that was only visible in one of the two readings still counts.
func foreignLocation(locs []string, ownStream string) (loc string, foreign bool) {
	for _, l := range locs {
		if !locationServedBySTR(l, ownStream) {
			return l, true
		}
	}
	return "", false
}

// boxReading is one now_playing read: every ContentItem location it carried,
// and whether the box reported live playback at that moment. The two travel
// together because the verdict needs both from the SAME instant - a location
// from one reading and a play status from another would say nothing about
// whether that location is what is playing.
type boxReading struct {
	locations []string
	playing   bool
}

// readBox reads the box's now_playing once. Best effort: an unreachable or
// unreadable box returns the zero reading, which the caller reads as "no
// evidence of foreign content" and proceeds exactly as before.
func (s *Server) readBox() boxReading {
	if s.nowPlayingBodyFn != nil {
		return parseBoxReading(s.nowPlayingBodyFn())
	}
	if s.boxHost == "" {
		return boxReading{}
	}
	cl := &http.Client{Timeout: 4 * time.Second}
	resp, err := cl.Get("http://" + s.boxHost + ":8090/now_playing")
	if err != nil {
		return boxReading{}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	return parseBoxReading(string(b))
}

// parseBoxReading turns one now_playing body into a reading. PAUSE counts as
// live: a paused foreign stream is still somebody's speaker, and pushing a
// station over it is the same interruption.
func parseBoxReading(body string) boxReading {
	switch nowPlayingStatus(body) {
	case "PLAY_STATE", "BUFFERING_STATE", "PAUSE_STATE":
		return boxReading{locations: contentLocations(body), playing: true}
	}
	return boxReading{locations: contentLocations(body)}
}

// contentLocations pulls the location attributes out of a now_playing body,
// in document order.
func contentLocations(body string) []string {
	ms := reNowPlayLocation.FindAllStringSubmatch(body, -1)
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m[1])
	}
	return out
}

// foreignContentStandDown reports whether the box is PLAYING content STR does
// not serve, and logs the stand-down with the host so a diagnostic bundle names
// what was playing instead. prior is what the box reported when the wake
// arrived, ownStream the resume target.
//
// Two readings are consulted, each on its own: the one taken at the wake instant
// (the teardown is over in a second or two, so it is the only one that can still
// catch it) and one taken now. A foreign location without a live play status in
// the same reading is a restored selection, not a speaker in use - see the file
// header - and does not stand the resume down.
func (s *Server) foreignContentStandDown(prior boxReading, ownStream string) bool {
	// The live read is taken only when the prior one did not already decide:
	// building both eagerly cost a :8090 round trip on every wake, including
	// the ordinary ones this guard has nothing to say about.
	readers := []func() boxReading{
		func() boxReading { return prior },
		s.readBox,
	}
	for _, read := range readers {
		rd := read()
		loc, foreign := foreignLocation(rd.locations, ownStream)
		if !foreign {
			continue
		}
		if !rd.playing {
			// Logged: this is the case that used to cancel the resume, so a
			// bundle from a user who wonders why it did NOT has to show it.
			s.logger.Info("wake resume: the speaker's last selection is content STR does not serve, but nothing is playing, so it is not evidence of a live foreign stream; resuming",
				"host", hostPortOf(loc))
			continue
		}
		// The host, not the whole URL: it is what identifies the other
		// controller, and a full media URL can carry a token.
		s.logger.Info("wake resume: the speaker is playing content STR does not serve, not resuming",
			"host", hostPortOf(loc))
		return true
	}
	return false
}
