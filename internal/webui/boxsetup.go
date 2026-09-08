package webui

// The firmware's own setup episodes.
//
// Field case (#873, two independent reporters, rhino ST10, v0.9.74,
// 2026-09-06): after a stick-free network install the speaker comes back with
// STR running and then flips between its out-of-box SETUP source and
// INVALID_SOURCE for several minutes, its Wi-Fi light blinking fast, dropping
// off the LAN at least once. The desktop app sees none of that: from the PC
// the speaker is simply silent, so the install wait times out and reports a
// failure for an install that in fact succeeded.
//
// The agent, running ON the speaker, does see it: the gabbo bus reports the
// source flip and, about five seconds after it, a <setupAPUpdated>true</>
// frame - the firmware saying in its own words that it is raising its setup
// access point. Counting those episodes here is what lets the app say
// afterwards what the speaker was doing during a blackout it could not
// observe.
//
// Deliberately in memory only: no timer, no poll, no NAND write. This runs on
// every speaker STR is installed on, and a counter that costs a flash write is
// not worth a diagnostic (SCHONE die Box-Hardware).

import (
	"sync"
	"time"
)

const (
	// boxSetupActiveWindow is how long a raised setup counts as ACTIVE with no
	// further evidence. The episodes measured on the reporter's box repeated
	// every two to five minutes, so a window shorter than that would report
	// "over" between two flips of the same episode.
	boxSetupActiveWindow = 5 * time.Minute
	// boxSetupRecentWindow is how long a finished episode is still worth
	// reporting. It has to outlive the whole thing a user watches: the observed
	// episodes ran about fifteen minutes and the app's own wait budget plus its
	// late re-check sits on top of that.
	boxSetupRecentWindow = 45 * time.Minute
)

// boxSetupState is the counter behind BoxSetup. Zero value is "never seen".
type boxSetupState struct {
	mu sync.Mutex
	// episodes counts observed entries into setup, from either origin (the
	// gabbo setupAPUpdated frame or the poll seeing source=SETUP).
	episodes int
	// clears counts the SETUP_LEAVE calls STR made in response.
	clears int
	// lastAt is when setup was last observed, lastClear when STR last cleared
	// it. Their order is what separates "raised and still standing" from
	// "raised and answered".
	lastAt    time.Time
	lastClear time.Time
	// firstAt is the start of the current episode run, so a bundle can say how
	// long the speaker has been doing this.
	firstAt time.Time
}

// NoteBoxSetupEpisode records that the firmware entered its own setup state.
// Called from the gabbo setupAPUpdated frame and from the poll watcher, so a
// missed WebSocket frame still counts.
//
// Repeat observations inside the active window fold into the SAME episode: the
// firmware re-raises setup every couple of minutes while an episode lasts, and
// counting each flip separately would turn one incident into fifteen.
func (s *Server) NoteBoxSetupEpisode() {
	now := time.Now()
	s.boxSetup.mu.Lock()
	defer s.boxSetup.mu.Unlock()
	if s.boxSetup.lastAt.IsZero() || now.Sub(s.boxSetup.lastAt) > boxSetupActiveWindow {
		s.boxSetup.episodes++
		s.boxSetup.firstAt = now
	}
	s.boxSetup.lastAt = now
}

// NoteBoxSetupCleared records that STR cleared the setup source (a successful
// SETUP_LEAVE). It does not end the episode: the whole point of #873 is that
// the firmware raises it again afterwards, and a counter that reset on every
// clear would report zero at exactly the moment the user is looking.
func (s *Server) NoteBoxSetupCleared() {
	now := time.Now()
	s.boxSetup.mu.Lock()
	defer s.boxSetup.mu.Unlock()
	s.boxSetup.clears++
	s.boxSetup.lastClear = now
}

// BoxSetup reports the speaker's setup history in the four words the desktop
// app needs:
//
//	"active" the firmware raised setup and the evidence is still fresh
//	"recent" it did so within the last three quarters of an hour
//	"stale"  it did so on this agent run, but longer ago than that
//	"no"     never observed on this agent run
//
// "stale" is its own word on purpose. It used to fold into "no", so a consumer
// reading only the state string could not tell a speaker that has NEVER done
// this from one that did it an hour ago - which for a late install re-check is
// exactly the difference that matters.
//
// episodes counts them, lastSec is the age of the newest observation (0 when
// there is none).
func (s *Server) BoxSetup() (state string, episodes, lastSec int) {
	s.boxSetup.mu.Lock()
	defer s.boxSetup.mu.Unlock()
	episodes = s.boxSetup.episodes
	if episodes == 0 || s.boxSetup.lastAt.IsZero() {
		return "no", 0, 0
	}
	age := time.Since(s.boxSetup.lastAt)
	lastSec = int(age.Seconds())
	switch {
	case age < boxSetupActiveWindow:
		return "active", episodes, lastSec
	case age < boxSetupRecentWindow:
		return "recent", episodes, lastSec
	}
	return "stale", episodes, lastSec
}

// boxSetupDebug is the box_setup_episodes bundle section: the counters plus
// the two ages that say whether STR was fighting the firmware or watching it.
func (s *Server) boxSetupDebug() map[string]any {
	state, episodes, lastSec := s.BoxSetup()
	s.boxSetup.mu.Lock()
	defer s.boxSetup.mu.Unlock()
	out := map[string]any{
		"state":    state,
		"episodes": episodes,
		"clears":   s.boxSetup.clears,
	}
	if !s.boxSetup.lastAt.IsZero() {
		out["lastSec"] = lastSec
	}
	if !s.boxSetup.lastClear.IsZero() {
		out["lastClearSec"] = int(time.Since(s.boxSetup.lastClear).Seconds())
	}
	if !s.boxSetup.firstAt.IsZero() {
		out["episodeAgeSec"] = int(time.Since(s.boxSetup.firstAt).Seconds())
	}
	return out
}
