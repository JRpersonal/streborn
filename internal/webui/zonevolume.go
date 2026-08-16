package webui

// Group volume from the phone.
//
// The desktop app can form a group, and until now that was also the only place
// it could be used: to switch the group on or to change how loud it plays, a
// user had to walk to the computer and start it. Three people asked for this on
// the same day, one of them putting it plainly: he has to boot the laptop just
// to turn the volume down while listening.
//
// Two things make this work from a speaker's own web page. The membership comes
// from the PERSISTED zone rather than the firmware's live one, because the
// firmware's zone endpoint answers on some chassis and not on others (measured
// across a nine-speaker fleet: only the three rhino ST10s ever answered, healthy
// or not), while the persisted zone is STR's own record and is always there. And
// the volume calls go to each member's BOSE port, which stays reachable between
// speakers even on the series-I boxes whose agent ports block each other.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
	"strings"
)

// zoneMemberVolume is one speaker of the group as the phone page sees it.
type zoneMemberVolume struct {
	Name     string `json:"name"`
	IP       string `json:"ip"`
	DeviceID string `json:"deviceID,omitempty"`
	Role     string `json:"role,omitempty"`
	IsSelf   bool   `json:"isSelf"`
	IsMaster bool   `json:"isMaster"`
	// Volume is -1 when the speaker did not answer. The page shows the row
	// greyed rather than hiding it: a member that has gone quiet is exactly
	// what the user wants to see.
	Volume int  `json:"volume"`
	Muted  bool `json:"muted"`
}

// handleZoneVolume serves the group's volume state and changes it.
//
//	GET  -> {"grouped":bool,"stereo":bool,"members":[...],"average":N}
//	POST {"value":N}            -> set every member
//	POST {"ip":"...","value":N} -> set one member
func (s *Server) handleZoneVolume(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.zoneVolumeGet(w, r)
	case http.MethodPost:
		s.zoneVolumeSet(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// groupMembers returns this box plus its persisted group, master first. The
// second return is false when the box is standalone.
func (s *Server) groupMembers() ([]zoneMemberVolume, bool, bool) {
	if s.zones == nil {
		return nil, false, false
	}
	z, ok := s.zones.Get()
	if !ok || len(z.Slaves) == 0 {
		return nil, false, false
	}
	out := []zoneMemberVolume{{
		Name: s.groupSelfName(z), IP: s.boxHost, DeviceID: z.Master,
		IsSelf: true, IsMaster: true, Volume: -1,
	}}
	for _, m := range z.Slaves {
		if m.IP == "" {
			continue // nothing to call without an address
		}
		out = append(out, zoneMemberVolume{
			Name: s.peerName(m), IP: m.IP, DeviceID: m.DeviceID, Role: m.Role, Volume: -1,
		})
	}
	return out, true, z.Stereo
}

// groupSelfName is what this speaker calls itself in the member list. The
// stored zone name is the group's name, not the speaker's, so it is only used
// when nothing better is known.
func (s *Server) groupSelfName(z zones.Zone) string {
	if n := s.remoteDisplayName(); n != "" {
		return n
	}
	if z.Name != "" {
		return z.Name
	}
	return "This speaker"
}

// peerName resolves a member's display name from the peer list when the app has
// seeded one, and falls back to the address. A row labelled 192.168.1.42 is
// still useful; a row labelled nothing is not.
func (s *Server) peerName(m zones.Member) string {
	if s.peersFn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		for _, p := range s.peersFn(ctx) {
			// A peer is identified by its web URL, so compare on the host part
			// of it rather than on a field PeerLink does not carry.
			if u, err := url.Parse(p.URL); err == nil && u.Hostname() == m.IP && p.Name != "" {
				return p.Name
			}
		}
	}
	return m.IP
}

// storedGroupIsLive reports whether the speaker still has the group the stored
// zone document describes.
//
// The document exists so a group survives a reboot or a standby and re-forms
// itself, which is right. But nothing checked it against the speaker, so when a
// group went away without STR writing the file (a re-form that failed, a
// firmware that dropped the zone, a factory reset) the file kept insisting and
// the phone kept drawing a group card for it. Pressing play then sent audio into
// a zone the firmware never had, which from the sofa is indistinguishable from a
// broken speaker.
//
// Seen on three speakers at once, 2026-08-09: the living room reported itself
// leading a group with the bathroom, the portable reported itself leading one
// with the living room, the bathroom reported nothing, and the living room's own
// firmware answered <zone /> to all of it.
//
// Only the DISPLAY asks this. The sleep timer keeps using the stored document
// when it switches a group off, because there the file is the record of what
// STR grouped and switching off one speaker too many is harmless.
//
// A read and nothing else: it runs whenever the phone opens its Speakers tab,
// and a write there would reset the deep-standby countdown.
//
// It takes the answer liveGroupView already got instead of asking again. Two
// reads of the same speaker a fraction of a second apart are two different
// answers, and the second one decided: a speaker whose zone read fine for
// liveGroupView could still have its group thrown away here because the follow
// up read came back empty. One read, one verdict.
func (s *Server) storedGroupIsLive(own ownZoneAnswer) bool {
	live := own.zone
	if !own.read {
		if s.boxHost == "" {
			return true // cannot ask; trust the file rather than hide a real group
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		z, err := fetchZone(ctx, s.boxHost)
		if err != nil {
			return true // unreachable right now is not evidence the group is gone
		}
		live = z
	}
	if live.Master == "" && len(live.Members) == 0 {
		// Only speakers that actually held a group document lost anything. On
		// one that never had one the line described an incident that never
		// happened, once per poll of the phone's Speakers tab, which is exactly
		// the noise that hides the real occurrence.
		if s.hasStoredGroup() {
			s.logger.Info("zone: the stored group is not on the speaker any more, reporting standalone")
		}
		return false
	}
	return true
}

// hasStoredGroup reports whether STR's own zone document describes a group.
func (s *Server) hasStoredGroup() bool {
	if s.zones == nil {
		return false
	}
	z, ok := s.zones.Get()
	return ok && len(z.Slaves) > 0
}

func (s *Server) zoneVolumeGet(w http.ResponseWriter, r *http.Request) {
	gctx, gcancel := context.WithTimeout(r.Context(), 3*time.Second)
	members, grouped, stereo := s.groupView(gctx)
	gcancel()
	_ = stereo // groupView already applied the stereo exception
	if !grouped {
		writeJSON(w, http.StatusOK, map[string]any{"grouped": false})
		return
	}

	// Read every member at once. Serially this is one timeout per silent
	// speaker, and the page polls: a single unplugged member would make the
	// whole card feel broken.
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := range members {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, err := boxapi.New(members[i].IP).GetVolume(ctx)
			if err != nil {
				return // stays -1, the page shows it as not answering
			}
			members[i].Volume = v.Actual
			members[i].Muted = v.Muted
		}(i)
	}
	wg.Wait()

	sum, n, loudest := 0, 0, -1
	for _, m := range members {
		if m.Volume >= 0 {
			sum += m.Volume
			n++
			if m.Volume > loudest {
				loudest = m.Volume
			}
		}
	}
	avg := -1
	if n > 0 {
		avg = sum / n
	}
	sort.SliceStable(members, func(a, b int) bool { return members[a].IsMaster && !members[b].IsMaster })
	// Both figures, because they answer different questions. The average says
	// what the group sits at on balance; the loudest says how loud the room
	// actually is, which is what a group slider should read when the speakers
	// are deliberately at different levels. A slider showing the average of 40
	// and 10 reads 25, and nothing in the room is playing at 25.
	writeJSON(w, http.StatusOK, map[string]any{
		"grouped": true, "stereo": stereo, "members": members,
		"average": avg, "loudest": loudest,
	})
}

// groupView is the ONE answer to "which speakers is this one grouped with".
//
// It has to be one, because a reader and a writer that disagree produce a
// control that draws itself and then refuses every press. That is exactly what
// happened when the read was moved onto the firmware and the write was left on
// the stored document: on a follower the phone drew a full group card with
// live levels, and every volume it sent came back 409 "not in a group",
// including the follower's own (caught by the audit the same day, before
// anybody reported it).
//
// Order: a stereo pair is the firmware's own concept and the stored document
// describes it, so it wins. Otherwise the firmware outranks the document,
// because only the speaker that FORMED the group has one.
func (s *Server) groupView(ctx context.Context) ([]zoneMemberVolume, bool, bool) {
	members, grouped, stereo := s.groupMembers()
	if stereo {
		return members, grouped, true
	}
	live, isFollower, own := s.liveGroupView(ctx)
	if isFollower {
		return live, true, false
	}
	if grouped && !s.storedGroupIsLive(own) {
		return nil, false, false
	}
	return members, grouped, false
}

func (s *Server) zoneVolumeSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IP    string `json:"ip"`
		Value int    `json:"value"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*1024)).Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Value < 0 || req.Value > 100 {
		http.Error(w, "value must be 0..100", http.StatusBadRequest)
		return
	}
	vctx, vcancel := context.WithTimeout(r.Context(), 3*time.Second)
	members, grouped, _ := s.groupView(vctx)
	vcancel()
	if !grouped {
		http.Error(w, "this speaker is not in a group", http.StatusConflict)
		return
	}
	targets := members
	if req.IP != "" {
		targets = nil
		for _, m := range members {
			if m.IP == req.IP {
				targets = append(targets, m)
			}
		}
		if len(targets) == 0 {
			http.Error(w, "that speaker is not in this group", http.StatusBadRequest)
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 8*time.Second)
	defer cancel()
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		failed []string
	)
	for _, m := range targets {
		wg.Add(1)
		go func(m zoneMemberVolume) {
			defer wg.Done()
			if err := boxapi.New(m.IP).SetVolume(ctx, req.Value); err != nil {
				mu.Lock()
				failed = append(failed, m.Name)
				mu.Unlock()
				s.logger.Info("group volume: member did not take the change", "member", m.Name, "ip", m.IP, "err", err)
			}
		}(m)
	}
	wg.Wait()

	// Partial success is reported as partial. Silently reporting ok while one
	// speaker stayed loud is the kind of answer that makes people stop
	// believing the control.
	s.logger.Info("group volume set", "value", req.Value, "targets", len(targets), "failed", len(failed))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": len(failed) == 0, "value": req.Value,
		"changed": len(targets) - len(failed), "failed": failed,
	})
}

// The two firmware reads liveGroupView makes, behind seams. boxapi pins port
// 8090, which a test cannot serve, and the same swap is what the other box
// tests in this package use.
var (
	fetchZone = func(ctx context.Context, host string) (boxapi.Zone, error) {
		return boxapi.New(host).GetZone(ctx)
	}
	fetchDeviceID = func(ctx context.Context, host string) string {
		info, err := boxapi.New(host).GetInfo(ctx)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(info.DeviceID)
	}
)

// ownZoneAnswer is what this speaker's own firmware said about its zone, passed
// from the one place that reads it to the one that would otherwise repeat the
// read. read is false when the speaker was never asked or did not answer, and
// zone is meaningless then.
type ownZoneAnswer struct {
	read bool
	zone boxapi.Zone
}

// liveGroupView reports the group this speaker is actually in, as seen by the
// speaker that leads it, or false when this speaker is not following anybody.
//
// Why it exists. groupMembers reads the STORED zone document, which only the
// speaker that FORMED the group has. That produced three different answers to
// the same question on one fleet (live 2026-08-15, five speakers in one group
// led by the Portable):
//
//	the leader          reported the group correctly
//	a speaker holding a stale document from an earlier session claimed to
//	                    LEAD a group somebody else leads, listing itself as
//	                    master while playing as a follower
//	the other three     reported "not grouped" while playing the group's music
//
// The firmware knows better on every one of them: a follower's own /getZone
// carries the real master's deviceID and, in senderIPAddress, the address to
// reach it at. So the leader is asked for the member list and every speaker
// answers with the same group.
//
// senderIsMaster is deliberately not consulted. It reads "true" on a follower
// on these chassis, which is exactly the trap that made a follower look like a
// leader in the first place; the deviceIDs are unambiguous.
//
// The third return carries this speaker's own zone read on to the caller, so
// the stored-group cross-check can use it instead of asking a second time.
func (s *Server) liveGroupView(ctx context.Context) ([]zoneMemberVolume, bool, ownZoneAnswer) {
	if s.boxHost == "" {
		return nil, false, ownZoneAnswer{}
	}
	live, err := fetchZone(ctx, s.boxHost)
	if err != nil {
		return nil, false, ownZoneAnswer{}
	}
	own := ownZoneAnswer{read: true, zone: live}
	master := strings.TrimSpace(live.Master)
	if master == "" {
		return nil, false, own
	}
	ownID := fetchDeviceID(ctx, s.boxHost)
	if ownID == "" || strings.EqualFold(ownID, master) {
		// We lead it, or the speaker cannot say who it is. Either way the
		// stored document is the better source and the caller keeps it.
		return nil, false, own
	}
	masterIP := strings.TrimSpace(live.SenderIP)
	if masterIP == "" {
		return nil, false, own
	}
	// What this speaker knows first hand: it follows, and whom. Used whenever
	// the leader's own answer cannot be trusted, because a short list beats
	// "not grouped" while the group's music is playing.
	certain := []zoneMemberVolume{
		{Name: s.peerName(zones.Member{IP: masterIP}), IP: masterIP, DeviceID: live.Master, IsMaster: true, Volume: -1},
		{Name: s.groupSelfName(zones.Zone{}), IP: s.boxHost, DeviceID: ownID, IsSelf: true, Volume: -1},
	}

	mz, err := fetchZone(ctx, masterIP)
	if err != nil {
		return certain, true, own // the leader is not answering
	}
	// Both speakers have to name the same leader. When they do not, the list
	// that came back belongs to some other group: the address in senderIPAddress
	// is the one that sent the last zone message, and after a group is handed
	// over or torn down and rebuilt that speaker is in a different group than
	// the one this speaker still thinks it is in. Adopting it puts strangers on
	// the page and takes their volume away from whoever is listening to them.
	if !strings.EqualFold(strings.TrimSpace(mz.Master), master) {
		return certain, true, own
	}

	// The leader lists its followers, not itself, so it is added first.
	out := []zoneMemberVolume{certain[0]}
	// Nothing in the firmware promises the list is free of repeats, and a
	// speaker that appears twice gets two rows on the page and two volume calls
	// from one press of the group slider. The master seeds both sets, because a
	// leader that does include itself would otherwise get a second row that
	// contradicts the first about who leads.
	seenIP := map[string]bool{masterIP: true}
	seenID := map[string]bool{strings.ToUpper(master): true}
	var selfSeen bool
	for _, m := range mz.Members {
		ip := strings.TrimSpace(m.IP)
		if ip == "" {
			continue
		}
		id := strings.ToUpper(strings.TrimSpace(m.DeviceID))
		if seenIP[ip] || (id != "" && seenID[id]) {
			continue
		}
		seenIP[ip] = true
		if id != "" {
			seenID[id] = true
		}
		self := id != "" && strings.EqualFold(id, ownID)
		name := s.peerName(zones.Member{IP: ip})
		if self {
			// Talk to ourselves over the loopback the rest of this file uses,
			// and use the name this speaker calls itself.
			selfSeen = true
			ip = s.boxHost
			name = s.groupSelfName(zones.Zone{})
		}
		out = append(out, zoneMemberVolume{
			Name: name, IP: ip, DeviceID: m.DeviceID, Role: m.Role, IsSelf: self, Volume: -1,
		})
	}
	// This speaker has to be in the group it says it follows. When the leader's
	// list leaves it out, add it rather than throwing the list away.
	//
	// Our OWN firmware named that leader, so the membership is not unreported:
	// one of the two speakers reported it, and it is the one we asked first.
	// Collapsing to the two certain rows instead would turn a five speaker
	// group into two on the phone whenever a leader answers with an empty
	// deviceID for a member, which is exactly what the two-chip chassis here
	// (Portable, ST20 spotty) have done before.
	if !selfSeen {
		out = append(out, zoneMemberVolume{
			Name: s.groupSelfName(zones.Zone{}), IP: s.boxHost, DeviceID: ownID,
			IsSelf: true, Volume: -1,
		})
		s.logger.Info("zone: the leader did not list this speaker, adding it from its own report",
			"master", master, "ownID", ownID)
	}
	return out, true, own
}
