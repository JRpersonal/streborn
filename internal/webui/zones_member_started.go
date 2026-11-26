package webui

// A member's agent reporting in after it started.
//
// The main speaker re-forms a permanent group whose zone ended with a member's
// restart (reformAfterMemberReboot), but only for a member whose agent is
// freshly started, and it can only tell that from the member's agent. Right
// after an update the member's firmware answers first and its agent a few
// seconds later. The main speaker's tick then landed in exactly that gap
// (field case 2026-11-26, 11:24:18: the member's firmware said STANDBY, its
// agent did not answer, its listeners came up two seconds later), read the
// silence as "not just restarted", and the group stayed apart until the next
// five-minute tick.
//
// The fix is an event, not a retry loop: every agent, once its web server is
// up, tells the speakers it knows that it just started (one post per known
// speaker, once per start). A main speaker that stores the sender as a member
// of its permanent group runs one tick-style round of the group paths right
// then, through the same reconcile goroutine as the tick, so the member is
// read again the moment its agent can answer. Everyone else ignores it.

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/netutil"
	"github.com/JRpersonal/streborn/internal/zones"
)

// memberStartedBody is what a starting agent posts to its peers.
type memberStartedBody struct {
	DeviceID string `json:"deviceID,omitempty"`
}

var memberStartedClient = netutil.GuardedClient(3 * time.Second)

// memberStartedRepeatGap is how long after one start notice from an address a
// second one asks for no further round (noteMemberStarted). An agent start
// takes far longer than this, so a real second start is never lost.
const memberStartedRepeatGap = 30 * time.Second

// AnnounceAgentStarted tells every speaker in the peer roster that this agent
// just started. Runs once per agent start, off the start-up path; blocking for
// one round of parallel posts.
func (s *Server) AnnounceAgentStarted() {
	if s.cachedPeersFn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	body, err := json.Marshal(memberStartedBody{DeviceID: s.rejoinSelfDeviceID(ctx)})
	if err != nil {
		return
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		told    int
		masters []string
	)
	seen := map[string]bool{}
	for _, p := range s.cachedPeersFn() {
		// Restored roster entries start dimmed until the first sweep, so
		// reachability is not a filter here: the post itself is the probe.
		if p.IP == "" || seen[p.IP] {
			continue
		}
		seen[p.IP] = true
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			member, ok := s.sendMemberStarted(ctx, ip, body)
			if !ok {
				return
			}
			mu.Lock()
			told++
			if member {
				masters = append(masters, ip)
			}
			mu.Unlock()
		}(p.IP)
	}
	wg.Wait()
	s.logger.Info("agent start reported to the other speakers, so a group's main speaker can take this one back at once",
		"peers", len(seen), "answered", told, "mainSpeakers", strings.Join(masters, ","))
}

// sendMemberStarted posts the start notice to one peer's agent on either agent
// port. member reports that the peer named this speaker a member of its group.
func (s *Server) sendMemberStarted(ctx context.Context, ip string, body []byte) (member, ok bool) {
	if s.memberStartedNoticeFn != nil {
		return s.memberStartedNoticeFn(ctx, ip, body)
	}
	if !isLANPeer(ip) {
		return false, false
	}
	for _, port := range []string{"17008", "8888"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"http://"+net.JoinHostPort(ip, port)+"/api/group/member-started", strings.NewReader(string(body)))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := memberStartedClient.Do(req)
		if err != nil {
			continue
		}
		var out struct {
			Member bool `json:"member"`
		}
		derr := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&out)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			continue // an older agent without the endpoint answers 404
		}
		return derr == nil && out.Member, true
	}
	return false, false
}

// storedMemberAt reports whether the stored permanent group names the speaker
// at ip (or with deviceID) as a member.
func storedMemberAt(z zones.Zone, ip, deviceID string) bool {
	for _, m := range z.Slaves {
		if ip != "" && strings.TrimSpace(m.IP) == ip {
			return true
		}
		if deviceID != "" && strings.EqualFold(strings.TrimSpace(m.DeviceID), deviceID) {
			return true
		}
	}
	return false
}

// handleGroupMemberStarted receives a peer's start notice
// (POST /api/group/member-started). LAN only. When the sender is a member of
// this speaker's stored permanent group, one round of the group paths is
// asked for; the round itself decides whether anything is to be done (a
// recent zone loss, the main speaker awake, the member freshly started).
func (s *Server) handleGroupMemberStarted(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	var in memberStartedBody
	_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in)
	from, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		from = r.RemoteAddr
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "member": s.noteMemberStarted(from, strings.TrimSpace(in.DeviceID))})
}

// noteMemberStarted is the decision behind the handler: it reports whether the
// sender is a member of the stored permanent group and, when it is, asks the
// reconcile goroutine for one round.
func (s *Server) noteMemberStarted(ip, deviceID string) bool {
	if s.zones == nil {
		return false
	}
	z, ok := s.zones.Get()
	if !ok || !z.Permanent || z.Stereo || !storedMemberAt(z, ip, deviceID) {
		return false
	}
	// One round per member start. An agent sends its notice once per start,
	// so a second one from the same address within the gap is a repeat (or
	// somebody on the LAN posting in a loop), and the round it would ask for
	// has just run.
	now := time.Now()
	s.zoneLossMu.Lock()
	if s.memberStartedAt == nil {
		s.memberStartedAt = map[string]time.Time{}
	}
	last := s.memberStartedAt[ip]
	repeat := !last.IsZero() && now.Sub(last) < memberStartedRepeatGap
	if !repeat {
		s.memberStartedAt[ip] = now
	}
	s.zoneLossMu.Unlock()
	if repeat {
		s.logger.Info("default group: a member's start notice repeated within moments, the round already ran",
			"ip", ip, "sinceSec", int(now.Sub(last).Seconds()))
		return true
	}
	queued := false
	if s.memberStartedKick != nil {
		select {
		case s.memberStartedKick <- struct{}{}:
			queued = true
		default: // a round is already queued and has not started yet
		}
	}
	s.logger.Info("default group: a member's agent reported in after starting, checking the group once",
		"ip", ip, "deviceID", deviceID, "queued", queued)
	return true
}

// memberUptimeOrStartNotice is the member's agent uptime in seconds, or -1
// when it is unknown. A member whose agent does not answer the read but sent
// its start notice a moment ago counts as started at that notice: the notice
// can only come from a running agent that just started, and waiting for it
// again waits for something that already happened. Field case 2026-11-26,
// 15:40:50: the member reported in, the round it asked for read no uptime 8 s
// later (the agent was still busy with its own start), filed the member as
// "still starting" and left it out until a later round 16 s on.
func (s *Server) memberUptimeOrStartNotice(ctx context.Context, ip string) int {
	if up := s.rejoinReadAgentUptime(ctx, ip); up >= 0 {
		return up
	}
	s.zoneLossMu.Lock()
	at := s.memberStartedAt[ip]
	s.zoneLossMu.Unlock()
	if at.IsZero() {
		return -1
	}
	age := time.Since(at)
	if age < 0 || age > rejoinFreshBootWindow {
		return -1
	}
	s.logger.Info("default group: member's agent did not answer, but it reported its start moments ago, counting it as just restarted",
		"ip", ip, "noticeAgeSec", int(age.Seconds()))
	return int(age.Seconds())
}
