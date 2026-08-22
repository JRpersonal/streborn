package webui

// A speaker that JOINS a group starts at the group's volume.
//
// Until now nothing set it at all: the joiner kept whatever level it happened
// to have, and since every add path wakes it first and the firmware brings a
// speaker out of standby at its own default (~30, see cmd/agent/wshandler.go),
// adding a speaker to a quiet evening group regularly made one speaker shout.
//
// Three things this deliberately does NOT do, each one paid for before:
//
//   - It touches ONLY the members that are actually joining. The group slider
//     is relative on purpose (#401): writing one absolute number to every
//     member destroys the per-speaker balance a user dialled in, and one added
//     speaker must not flatten the whole room.
//   - It does not re-assert on a timer. #548 removed exactly that kind of
//     blind restore after it was caught pushing a level DOWN over a user's own
//     correction: a control that cannot tell whose intent it is enforcing
//     should not enforce one. The single follow-up write here stands down the
//     moment it sees a level it did not write.
//   - It does not write through the member's :8090 when the member runs STR.
//     A raw Bose volume PUT landing during a play-start kills the play (a
//     documented firmware flaw, and the reason internal/spotify/volume.go
//     prefers the agent). The write goes to the member's own agent, which
//     serializes it behind that box's command lock.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/JRpersonal/streborn/internal/boxapi"
	"github.com/JRpersonal/streborn/internal/zones"
)

// Test seams. boxapi pins :8090, which a httptest server can never serve, so
// the live calls sit behind vars the tests replace. Same pattern as
// dissolvestragglers.go.
var (
	readMemberVolumeFn = func(ctx context.Context, host string) (boxapi.Volume, error) {
		return boxapi.New(host).GetVolume(ctx)
	}
	setMemberVolumeFn = setJoinerVolume
)

// joinVolumeMu serializes appliers. Two overlapping group edits (the chips
// coalesce, and a second edit can arrive while the first applier is still in
// its settle) could otherwise deliver an old level after a newer one, which is
// the hazard spotify's serial volume worker exists to prevent.
var joinVolumeMu sync.Mutex

// newlyJoinedMembers returns the requested slaves that were not already in the
// group, and whether the answer can be trusted at all.
//
// The "before" picture is the UNCONDITIONAL union of the firmware's live member
// list and STR's own stored document. Unconditional matters twice over: a live
// read that failed would otherwise make every existing member look new and hand
// it the master's level, and a healthy MIRROR group legitimately reports an
// empty /getZone because mirror mode never drives the firmware's zone at all.
//
// trustworthy is false when there is no usable before-picture whatsoever - the
// live read failed AND nothing is on record. That happens on chassis whose
// /getZone does not answer and on a group formed by the Bose app, and the only
// safe move there is to touch nobody: an empty before-picture is not the same
// statement as "this group had no members".
func newlyJoinedMembers(want []boxapi.ZoneMember, live boxapi.Zone, liveErr error, prev zones.Zone, hadPrev bool) (added []boxapi.ZoneMember, trustworthy bool) {
	if liveErr != nil && !hadPrev {
		return nil, false
	}
	// Match on IP first, deviceID second, both case-insensitive: the same key
	// order zoneDiff uses, because a two-chip chassis (Portable/taigan, BCO
	// ST20) is listed under its SCM deviceID while discovery supplies its
	// wlan0 MAC, so deviceID alone reports a present member as new.
	seenIP := make(map[string]bool, len(live.Members)+len(prev.Slaves))
	seenID := make(map[string]bool, len(live.Members)+len(prev.Slaves))
	note := func(ip, id string) {
		if ip = strings.TrimSpace(ip); ip != "" {
			seenIP[strings.ToLower(ip)] = true
		}
		if id = strings.TrimSpace(id); id != "" {
			seenID[strings.ToLower(id)] = true
		}
	}
	for _, m := range live.Members {
		note(m.IP, m.DeviceID)
	}
	for _, m := range prev.Slaves {
		note(m.IP, m.DeviceID)
	}
	for _, m := range want {
		if seenIP[strings.ToLower(strings.TrimSpace(m.IP))] {
			continue
		}
		if seenID[strings.ToLower(strings.TrimSpace(m.DeviceID))] {
			continue
		}
		added = append(added, m)
	}
	return added, true
}

// dropMembers removes members whose deviceID appears in ids. Used to subtract
// verifyFollowersJoined's "missing" list: a speaker that never confirmed the
// join must not get the group's volume, or a speaker sitting in another room in
// no group at all suddenly jumps to the living room's level.
func dropMembers(members []boxapi.ZoneMember, ids []string) []boxapi.ZoneMember {
	if len(ids) == 0 || len(members) == 0 {
		return members
	}
	skip := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			skip[strings.ToLower(id)] = true
		}
	}
	out := members[:0:0]
	for _, m := range members {
		if skip[strings.ToLower(strings.TrimSpace(m.DeviceID))] {
			continue
		}
		out = append(out, m)
	}
	return out
}

// setJoinerVolume writes one member's volume, preferring that member's STR
// agent over its Bose port for the reason in the file header. Copied in shape
// from internal/spotify/volume.go setFollowerVolume, which solved the same
// problem for the same firmware flaw.
func setJoinerVolume(ctx context.Context, ip string, pct int) error {
	body := fmt.Sprintf(`{"value":%d}`, pct)
	var lastErr error
	for _, port := range []string{"17008", "8888"} {
		rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		req, err := http.NewRequestWithContext(rctx, http.MethodPut,
			"http://"+ip+":"+port+"/api/box/volume", strings.NewReader(body))
		if err != nil {
			cancel()
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode < 300 {
			return nil
		}
		lastErr = fmt.Errorf("agent volume PUT %s: status %d", port, resp.StatusCode)
	}
	// No reachable STR agent on the member (a stock speaker, or one still
	// starting): drive the Bose port directly rather than give up.
	if err := boxapi.New(ip).SetVolume(ctx, pct); err != nil {
		return fmt.Errorf("%w (agent fallback: %v)", err, lastErr)
	}
	return nil
}

// matchNewMembersToMasterVolume gives every freshly joined member the group's
// current level. Detached by its callers (it settles for a couple of seconds)
// and bounded: one write, one confirming read, at most one correction.
func (s *Server) matchNewMembersToMasterVolume(members []boxapi.ZoneMember, seq uint64) {
	if len(members) == 0 || s.boxHost == "" {
		return
	}
	joinVolumeMu.Lock()
	defer joinVolumeMu.Unlock()
	// A newer group edit has taken over while we waited for the lock. Its own
	// applier knows the current membership; ours would deliver a stale level.
	if s.zoneFormSeq.Load() != seq {
		s.logger.Info("zone: skipping the join-volume match, a newer group edit is in flight")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 20*time.Second)
	defer cancel()

	v, err := readMemberVolumeFn(ctx, s.boxHost)
	// Target over Actual: a master that is muted, or mid-ramp, reports Actual 0
	// while Target still carries the level the user set.
	want := v.Target
	if want <= 0 {
		want = v.Actual
	}
	if err != nil || want <= 0 {
		// Load-bearing, not defensive padding. Pushing 0 into the joiner
		// produces a speaker that is provably in the group and provably
		// silent, which is the hardest failure class there is to diagnose
		// from a bundle - and from the sofa it is indistinguishable from
		// "the speaker never joined".
		s.logger.Info("zone: not matching the join volume, the group leader did not report a usable level",
			"err", err, "target", v.Target, "actual", v.Actual)
		return
	}

	type target struct{ ip, id string }
	list := make([]target, 0, len(members))
	for _, m := range members {
		ip := strings.TrimSpace(m.IP)
		if ip == "" || !isLANPeer(ip) {
			continue
		}
		list = append(list, target{ip: ip, id: m.DeviceID})
	}
	if len(list) == 0 {
		return
	}

	var wg sync.WaitGroup
	for _, tg := range list {
		wg.Add(1)
		go func(tg target) {
			defer wg.Done()
			if err := setMemberVolumeFn(ctx, tg.ip, want); err != nil {
				s.logger.Warn("zone: could not set a joining member's volume", "ip", tg.ip, "err", err)
			}
		}(tg)
	}
	wg.Wait()

	// One confirming pass, and only for the case it exists for: the firmware
	// resets a speaker's level to its own default as it finishes coming out of
	// standby, and every add path wakes the member seconds before the join, so
	// a single write can legitimately land BEFORE the wake default does.
	//
	// It corrects nothing else. A level that moved to some third value in this
	// window is somebody's hand on a slider, on the phone, or on the speaker,
	// and #548 is explicit that a control which cannot tell whose intent it is
	// enforcing must not enforce one. So: re-write only a member that came back
	// at neither our value nor anything a person would have chosen on purpose,
	// i.e. one that reverted to a level it had before we wrote.
	time.Sleep(2500 * time.Millisecond)
	if s.zoneFormSeq.Load() != seq {
		return
	}
	corrected := 0
	for _, tg := range list {
		got, rerr := readMemberVolumeFn(ctx, tg.ip)
		if rerr != nil {
			continue
		}
		have := got.Target
		if have <= 0 {
			have = got.Actual
		}
		if have == want || have <= 0 {
			continue
		}
		// Only the wake-default revert is corrected. Anything else stays.
		if !looksLikeWakeDefault(have) {
			s.logger.Info("zone: leaving a joining member's volume alone, it was changed after we set it",
				"ip", tg.ip, "set", want, "now", have)
			continue
		}
		if err := setMemberVolumeFn(ctx, tg.ip, want); err != nil {
			s.logger.Warn("zone: could not re-apply a joining member's volume", "ip", tg.ip, "err", err)
			continue
		}
		corrected++
	}
	s.logger.Info("zone: new members start at the group volume",
		"volume", want, "members", len(list), "corrected", corrected)
}

// looksLikeWakeDefault reports whether a level is the firmware's own
// post-standby default rather than a human choice. The boxes come back at
// about 30 (cmd/agent/wshandler.go documents the observed value); the band
// keeps the correction narrow so a user who deliberately dialled a joining
// speaker to 28 in the two-second window keeps their 28.
func looksLikeWakeDefault(v int) bool { return v >= 29 && v <= 31 }
