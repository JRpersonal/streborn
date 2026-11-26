package webui

// One infrared remote, several speakers in reach of it.
//
// The SoundTouch remote is plain infrared, and a permanent group's speakers
// often stand in one open room. A press aimed at the main speaker then also
// lands on a member, and the member acts on it as if it were meant for it.
// Field case 2026-11-26 (Wohnzimmer ST30 main speaker, Küche ST10 member):
// PRESET_2 on the remote reached both speakers within 0.9 s, the member's
// hold-to-save read its half of the press as a hold, and only the source it
// happened to be on (Bluetooth) kept it from saving over its own key 2.
//
// The rule (Jens, 2026-11-26): when a member of a permanent group gets a
// remote key at the same moment as its main speaker, the press can only have
// come from the same remote, and the main speaker wins. The main speaker
// re-forms the group on its own start (formDefaultGroupOnPlay, which takes a
// member playing something of its own along on a play kick), and the member
// does nothing of its own with the press: no hold-to-save, no firmware store
// kept, no STR-driven play.
//
// How the member learns of the main speaker's press: the main speaker tells
// it. On every remote press of a key the members would act on, the main
// speaker posts the key to each stored member's agent (one request per member
// per human press, nothing repeats). The member keeps only the newest notice,
// in memory, stamped with its own clock on arrival, so the two speakers'
// clocks never have to agree. The member compares its own press against that
// stamp when it is about to act.
//
// Why this direction and not the member asking: a member does not reliably
// know its group. The group document lives on the main speaker, and the
// member in the field case had dropped out of the firmware zone 41 s before
// the press (its Bluetooth switch took it out). The main speaker always knows
// its members. The member's decisions that matter (hold-to-save, a dead key's
// play, the firmware's own store) are made seconds after the press, long
// after the notice arrived. The one that is not (STR's push for a preset key
// the firmware activates at once) checks too, and when the notice comes too
// late for it, the main speaker's re-form still takes the member over, as
// the firmware's own reaction to the press cannot be prevented anyway.

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
)

// GroupPressWindow is how far apart a member's remote press and its main
// speaker's may lie and still be one press of one remote. Both speakers
// decode the same infrared burst; what separates the two readings is how each
// chassis reports a key (the key trace at once, the sm2 analytics line up to
// about a second later: 0.85 s in the field case) and the notice's own trip.
const GroupPressWindow = 2 * time.Second

// masterKeyNote is the newest remote press a main speaker reported here.
type masterKeyNote struct {
	at     time.Time // arrival, on this speaker's clock
	key    string
	master string // the main speaker's deviceID, as it named itself
	from   string // the address the notice came from
}

// groupKeyNoticeBody is what the main speaker posts to /api/group/master-key.
type groupKeyNoticeBody struct {
	Key    string `json:"key"`
	Master string `json:"master"`
}

// groupKeyNoticeClient bounds one notice: a member that does not answer at
// once is not worth holding the press up for, and the post runs off the key
// reader anyway.
var groupKeyNoticeClient = netutil.GuardedClient(2 * time.Second)

// RemoteKeyReachesMembers reports whether a remote press of key is one the
// members of a permanent group act on by themselves, and therefore one the
// main speaker tells them about: the preset keys (hold-to-save, a dead key's
// play, STR's push for a key) and the track keys (the radio-key step).
func RemoteKeyReachesMembers(key string) bool {
	switch key {
	case "PRESET_1", "PRESET_2", "PRESET_3", "PRESET_4", "PRESET_5", "PRESET_6",
		"NEXT_TRACK", "PREV_TRACK":
		return true
	}
	return false
}

// NotifyMembersOfRemoteKey tells every member of this speaker's permanent
// group that the remote just pressed key here. Only the main speaker of a
// stored permanent group sends; everyone else returns at once. Blocking (one
// round of parallel posts, two seconds at most): the caller runs it off the
// key reader's goroutine.
func (s *Server) NotifyMembersOfRemoteKey(key string) {
	if s.zones == nil || !RemoteKeyReachesMembers(key) {
		return
	}
	z, ok := s.zones.Get()
	if !ok || !z.Permanent || z.Stereo || len(z.Slaves) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if self := s.rejoinSelfDeviceID(ctx); self == "" || !strings.EqualFold(self, z.Master) {
		return // a member's copy of the document: only the main speaker overrides
	}
	body, err := json.Marshal(groupKeyNoticeBody{Key: key, Master: z.Master})
	if err != nil {
		return
	}
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		delivered int
	)
	for _, m := range z.Slaves {
		if m.IP == "" {
			continue
		}
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			if s.sendGroupKeyNotice(ctx, ip, body) {
				mu.Lock()
				delivered++
				mu.Unlock()
			}
		}(m.IP)
	}
	wg.Wait()
	s.logger.Info("group: told the members about a remote key press on the main speaker, so a member that caught it too leaves it to this one",
		"key", key, "members", len(z.Slaves), "delivered", delivered)
}

// sendGroupKeyNotice posts one notice to a member's agent, on either agent
// port (the chassis decides which one answers).
func (s *Server) sendGroupKeyNotice(ctx context.Context, ip string, body []byte) bool {
	if s.groupKeyNoticeFn != nil {
		return s.groupKeyNoticeFn(ctx, ip, body)
	}
	if !isLANPeer(ip) {
		return false
	}
	for _, port := range []string{"17008", "8888"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"http://"+net.JoinHostPort(ip, port)+"/api/group/master-key", strings.NewReader(string(body)))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := groupKeyNoticeClient.Do(req)
		if err != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return true
		}
	}
	return false
}

// handleGroupMasterKey receives a main speaker's notice of a remote press
// (POST /api/group/master-key). LAN only. It stores the notice and nothing
// else: what it means is decided by MasterRemoteKeyNear when this speaker is
// about to act on a press of its own.
func (s *Server) handleGroupMasterKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isLocalLAN(r.RemoteAddr) {
		http.Error(w, "only allowed from LAN", http.StatusForbidden)
		return
	}
	var in groupKeyNoticeBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in); err != nil || strings.TrimSpace(in.Key) == "" {
		http.Error(w, "name the key", http.StatusBadRequest)
		return
	}
	from, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		from = r.RemoteAddr
	}
	s.noteMasterKey(masterKeyNote{at: time.Now(), key: strings.TrimSpace(in.Key), master: strings.TrimSpace(in.Master), from: from})
	s.logger.Info("group: the main speaker reported a remote key press", "key", in.Key, "master", in.Master, "from", from)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) noteMasterKey(n masterKeyNote) {
	s.masterKeyMu.Lock()
	s.masterKey = n
	s.masterKeyMu.Unlock()
}

// MasterRemoteKeyNear reports whether the main speaker of this speaker's
// permanent group reported a remote press within window of at (this speaker's
// own press). delta is the main speaker's notice minus at: negative when the
// notice came first. key and from describe the notice for the log.
func (s *Server) MasterRemoteKeyNear(at time.Time, window time.Duration) (delta time.Duration, key, from string, ok bool) {
	s.masterKeyMu.Lock()
	n := s.masterKey
	s.masterKeyMu.Unlock()
	if n.at.IsZero() || at.IsZero() {
		return 0, "", "", false
	}
	delta = n.at.Sub(at)
	if delta > window || delta < -window {
		return delta, n.key, n.from, false
	}
	return delta, n.key, n.from, true
}
