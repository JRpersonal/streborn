// Reading the group: GET /api/box/zone, the answer it gives, the members a
// stored group remembers, and the back-off for a /getGroup that hangs.
// Split out of zones_stereo.go.

package webui

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
)

func (s *Server) handleZoneGet(w http.ResponseWriter, r *http.Request) {
	c := boxapi.New(s.boxHost)
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	z, err := c.GetZone(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// A stereo pair is a firmware GROUP, not a zone, so /getZone says nothing
	// about it: a paired speaker reports {"members":[]} exactly like a
	// standalone one. That left the desktop app unable to see which speakers
	// were paired, so its pair controls just offered the first two candidates
	// and its "undo pair" went to whichever speaker the multiroom master
	// selection happened to point at. A user with three SoundTouch 10s pressed
	// undo twice, both times against a speaker that was not in the pair, and
	// the pair stayed up while the app reported success (field, 2026-08-04).
	//
	// Reported alongside the zone so one poll answers both. Best-effort: a box
	// that does not answer /getGroup simply reports no pair, which is what
	// every caller assumed until now anyway.
	// Embedded, so the zone fields keep their exact previous JSON shape
	// (omitempty and all) and only gain a sibling.
	// A FOLLOWER's own /getZone lists only itself, so a caller asking one
	// speaker saw a group of one while the group had five members. The
	// desktop app reads this endpoint, which is why it showed some speakers
	// and not others depending on which one it happened to ask (live
	// 2026-08-15). The leader has the full list, and a follower carries its
	// address, so it is fetched and reported here. The speaker's OWN master
	// and sender fields are kept, so nothing that relies on them changes.
	if full, ok := s.leaderZone(ctx, z); ok {
		z.Members = full
	}
	out := zoneAnswer{Zone: z}
	if len(z.Members) == 0 {
		out.Remembered = s.rememberedZoneMembers()
	}
	// Live or not: the desktop marks a permanent group's frame with it, so
	// the user can tell a group that will come back by itself from one that
	// ends with the next standby (Jens, 2026-09-06).
	if s.zones != nil {
		if sz, ok := s.zones.Get(); ok && sz.Permanent && !sz.Stereo {
			out.Permanent = true
			if len(z.Members) > 0 && strings.EqualFold(z.Master, sz.Master) {
				out.NotJoined = notJoinedMembers(z.Members, s.rememberedZoneMembers())
			}
		}
	}
	// The pair read gets its own SHORT budget, never the zone's. The firmware's
	// /getGroup HANGS on scm/BCO chassis — no refusal, just silence (12 s and
	// counting on an awake scm ST30, 2026-08-18, while its /getZone answered in
	// 28 ms). Chained on the zone ctx it ate the whole 6 s budget, the desktop
	// app's 6 s client always gave up first, and so no non-ST10 speaker EVER
	// delivered a live zone to the app: both group screens then ran on
	// optimistic data alone (the two-screens-disagree family). After a
	// few consecutive hangs the read is paused for a while — the answer cannot
	// change while the firmware refuses to give one.
	if s.groupReadAllowed() {
		gctx, gcancel := context.WithTimeout(r.Context(), 1500*time.Millisecond)
		g, gerr := c.GetGroup(gctx)
		gcancel()
		s.noteGroupReadResult(gerr)
		if gerr == nil && (g.ID != "" || len(g.Members) > 0) {
			out.Stereo = &g
		}
		if gerr == nil {
			s.noteStereoSeen(out.Stereo)
		}
	}
	// A pair whose other half is gone is the one state that makes a healthy,
	// reachable speaker refuse to play anything at all. Re-asked in the
	// background so the app's warning clears on the next poll once a partner
	// that was only missed answers; the zone read itself never waits for it.
	if ip, id := s.zonePartnerGone(); ip != "" {
		out.PairPartnerGone, out.PairPartnerGoneID = ip, id
	}
	writeJSON(w, http.StatusOK, out)
}

// zoneAnswer is what GET /api/box/zone returns. Named rather than anonymous so
// its JSON shape can be asserted without a live speaker on :8090.
type zoneAnswer struct {
	boxapi.Zone
	Stereo *boxapi.Group `json:"stereo,omitempty"`
	// Remembered is the persisted zone membership (zones.json) when NO zone is
	// live: the follower IPs and names, so a client can offer "play together
	// again" with one tap. The desktop always showed the remembered group; the
	// phone page could not, and a three-speaker household read that as the group
	// being gone (mail report, 2026-08-25). Only the desktop's own store, no
	// probes.
	Remembered []rememberedMember `json:"remembered,omitempty"`
	// Permanent says the remembered group is the user's durable choice (re-formed
	// on the master's next play). The desktop shows such a group as a stored
	// frame when it is not live, so the user can see it exists and remove it;
	// without the flag the group was invisible between plays (2026-09-06).
	Permanent bool `json:"permanent,omitempty"`
	// NotJoined lists the stored members of a live permanent group that are
	// not in it right now (asleep and not woken, unreachable, or grouped under
	// another speaker). Without it the app drew only the live zone, and a
	// member that stayed out read as removed from the group, then came back
	// once the group ended (#1576).
	NotJoined []rememberedMember `json:"notJoined,omitempty"`
	// PairPartnerGone names the other half of a stereo pair that did not answer.
	// While a pair is incomplete the firmware refuses every source activation, so
	// the speaker wakes and is back in standby five seconds later and nothing it
	// plays ever starts. Without this the app can only report that the speaker is
	// not responding, which sends the owner looking at his speaker, his network
	// and his presets (2026-09-29: a partner absent since 11 September, over
	// eighty failed presses).
	PairPartnerGone   string `json:"pairPartnerGone,omitempty"`
	PairPartnerGoneID string `json:"pairPartnerGoneId,omitempty"`
}

// rememberedMember is one follower of the persisted (not currently live) zone.
type rememberedMember struct {
	IP       string `json:"ip"`
	DeviceID string `json:"deviceID,omitempty"`
	Name     string `json:"name,omitempty"`
}

// notJoinedMembers returns the stored members missing from the live member
// list, matched by deviceID and, for a member stored without one, by address.
func notJoinedMembers(live []boxapi.ZoneMember, stored []rememberedMember) []rememberedMember {
	var out []rememberedMember
	for _, m := range stored {
		in := false
		for _, l := range live {
			if (m.DeviceID != "" && strings.EqualFold(m.DeviceID, l.DeviceID)) || (m.IP != "" && m.IP == l.IP) {
				in = true
				break
			}
		}
		if !in {
			out = append(out, m)
		}
	}
	return out
}

// rememberedZoneMembers reads the persisted zone from the store and returns
// its follower IPs, named from the peer roster where a name is known. Empty
// when this box is not the remembered master, or remembers no zone, or the
// remembered pair is a stereo pair (re-forming that is the pair flow's job).
func (s *Server) rememberedZoneMembers() []rememberedMember {
	if s.zones == nil {
		return nil
	}
	z, ok := s.zones.Get()
	if !ok || z.Stereo || len(z.Slaves) == 0 {
		return nil
	}
	names := map[string]string{}
	if s.peersFn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		for _, p := range s.peersFn(ctx) {
			names[p.IP] = p.Name
		}
		cancel()
	}
	out := make([]rememberedMember, 0, len(z.Slaves))
	for _, m := range z.Slaves {
		if m.IP == "" {
			continue
		}
		out = append(out, rememberedMember{IP: m.IP, DeviceID: m.DeviceID, Name: names[m.IP]})
	}
	return out
}

// groupReadAllowed reports whether the stereo /getGroup read should be tried,
// i.e. it is not currently paused after consecutive firmware hangs.
func (s *Server) groupReadAllowed() bool {
	s.groupReadMu.Lock()
	defer s.groupReadMu.Unlock()
	return time.Now().After(s.groupReadSkipUntil)
}

// noteGroupReadResult tracks consecutive /getGroup timeouts and pauses the
// read once the firmware has proven it will not answer. The pause is short
// (5 min): a deep-standby speaker hangs this endpoint too and must get its
// pair read back soon after waking.
func (s *Server) noteGroupReadResult(err error) {
	s.groupReadMu.Lock()
	defer s.groupReadMu.Unlock()
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		s.groupReadFails = 0
		return
	}
	s.groupReadFails++
	if s.groupReadFails < 3 {
		return
	}
	s.groupReadSkipUntil = time.Now().Add(5 * time.Minute)
	s.groupReadFails = 0
	s.logger.Info("zone: the firmware's /getGroup keeps hanging (seen on several chassis, sm2 ST20 included), pausing the stereo-pair read",
		"pauseMin", 5)
}
