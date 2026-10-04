package main

import (
	"sync"
	"time"
)

// groupWakeWindow is how long a speaker the app woke out of standby for a group
// counts as "just woken" when the group form is sent. The wake, the readiness
// probe and the form follow each other within seconds; the agent's own episode
// window is 30 s, and this matches it.
const groupWakeWindow = 30 * time.Second

// groupWakeLog remembers which speakers WakeBox brought out of standby, so
// FormZone can tell the master's agent. What such a member plays at form time
// is its own power-on resume, and a master that has nothing to play used to
// take that over and start it in every room (fleet run 2026-10-04: a group
// formed out of idle speakers started playing). The zero value is ready.
type groupWakeLog struct {
	mu   sync.Mutex
	woke map[string]time.Time
}

func (g *groupWakeLog) note(host string, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.woke == nil {
		g.woke = map[string]time.Time{}
	}
	g.woke[host] = now
}

// zoneFormPayload is the body FormZone posts: the spec plus the members woken
// for it. Kept out of ZoneSpec itself, which is a Wails-bound type the frontend
// never fills this field of.
func zoneFormPayload(spec ZoneSpec, woken []string) any {
	return struct {
		ZoneSpec
		WokenFromStandby []string `json:"wokenFromStandby,omitempty"`
	}{spec, woken}
}

// recent returns the hosts among members that were woken within the window
// before now, in the members' order.
func (g *groupWakeLog) recent(members []ZoneMember, now time.Time) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for _, m := range members {
		if at, ok := g.woke[m.IP]; ok && m.IP != "" && now.Sub(at) < groupWakeWindow {
			out = append(out, m.IP)
		}
	}
	return out
}
