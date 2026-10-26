package main

// Whether this speaker currently follows a group or stereo pair, for the
// preset reconcile (#1077). A follower's firmware refuses every AddPreset
// while it is in GroupSlave, so writing presets into it is wasted NAND writes
// and, worse, a log that claims keys were healed when nothing was stored.

import (
	"strings"
	"sync/atomic"

	"github.com/JRpersonal/streborn/internal/webui"
)

// groupFollowerFn is wired in main (setGroupFollowerFn): the stereo-pair
// record STR keeps in marge first (a pair formed through STR leaves the zone
// store empty), the speaker's own zone second. Unset (tests, early start)
// means "not a follower". Atomic because the reconcile loop starts before the
// webui server that answers it exists.
var groupFollowerFn atomic.Pointer[func() (bool, string)]

func setGroupFollowerFn(fn func() (bool, string)) {
	if fn == nil {
		groupFollowerFn.Store(nil)
		return
	}
	groupFollowerFn.Store(&fn)
}

// presetWriteGroupFollower reports whether the preset writes must be skipped
// because this speaker follows a group, and why.
func presetWriteGroupFollower() (bool, string) {
	fn := groupFollowerFn.Load()
	if fn == nil {
		return false, ""
	}
	return (*fn)()
}

// stereoFollowerFrom builds the play paths' pair-role answer from the pair
// record (master, name, ok), this speaker's own deviceID, whether the record
// lists that id in one of its roles, and the master's address from the record.
// Only a decided follower is reported; an undecided role (no record, own id not
// known yet, or an own id the pair does not list) plays as before.
//
// selfInPair is the guard against a wrong own id. The agent's id starts as a
// guess from a network card's MAC and is only corrected once the speaker's
// /info answers; on an ST10 the guess is known to differ from the real id. A
// master holding that guess would otherwise read "master != self" and refuse
// every play as its own follower, with a link back to itself.
func stereoFollowerFrom(master, name string, pairOK bool, self string, selfInPair bool, masterIP string) webui.StereoFollower {
	if !selfInPair {
		return webui.StereoFollower{}
	}
	if decided, follower := pairRole(master, self, pairOK); !decided || !follower {
		return webui.StereoFollower{}
	}
	return webui.StereoFollower{Follower: true, MasterID: master, MasterIP: masterIP, PairName: name, SelfID: strings.TrimSpace(self)}
}

// pairRole reads the stereo-pair record: decided is false when it says
// nothing (no pair, or this speaker's own id is not known yet), in which case
// the caller asks the zone instead of guessing.
func pairRole(master, self string, pairOK bool) (decided, follower bool) {
	if !pairOK || strings.TrimSpace(master) == "" || strings.TrimSpace(self) == "" {
		return false, false
	}
	return true, !strings.EqualFold(master, self)
}
