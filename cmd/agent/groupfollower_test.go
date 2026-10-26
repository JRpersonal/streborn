package main

import (
	"reflect"
	"testing"
)

func TestPairRole(t *testing.T) {
	cases := []struct {
		name                 string
		master, self         string
		ok                   bool
		wantDecided, wantFol bool
	}{
		{"no pair", "", "", false, false, false},
		{"pair record without a master", "", "self-id", true, false, false},
		{"own id unknown: ask the zone", "master-id", "", true, false, false},
		{"this speaker leads", "AABBCCDDEEFF", "aabbccddeeff", true, true, false},
		{"this speaker follows", "AABBCCDDEEFF", "112233445566", true, true, true},
	}
	for _, c := range cases {
		d, f := pairRole(c.master, c.self, c.ok)
		if d != c.wantDecided || f != c.wantFol {
			t.Errorf("%s: got decided=%v follower=%v, want %v %v", c.name, d, f, c.wantDecided, c.wantFol)
		}
	}
}

func TestStereoFollowerFrom(t *testing.T) {
	if got := stereoFollowerFrom("", "", false, "112233445566", ""); got.Follower {
		t.Error("no pair record: reported a follower")
	}
	if got := stereoFollowerFrom("AABBCCDDEEFF", "Pair", true, "", "192.0.2.10"); got.Follower {
		t.Error("own id unknown: reported a follower instead of leaving the play alone")
	}
	if got := stereoFollowerFrom("AABBCCDDEEFF", "Pair", true, "aabbccddeeff", "192.0.2.10"); got.Follower {
		t.Error("the master was told it follows its own pair")
	}
	got := stereoFollowerFrom("AABBCCDDEEFF", "Pair", true, "112233445566", "192.0.2.10")
	if !got.Follower || got.MasterID != "AABBCCDDEEFF" || got.MasterIP != "192.0.2.10" || got.PairName != "Pair" {
		t.Errorf("follower = %+v, want the master's id, address and the pair name", got)
	}
}

func TestPresetWriteGroupFollowerNilIsStandalone(t *testing.T) {
	old := groupFollowerFn.Load()
	t.Cleanup(func() { groupFollowerFn.Store(old) })
	setGroupFollowerFn(nil)
	if f, _ := presetWriteGroupFollower(); f {
		t.Fatal("an unwired check must not hold the preset writes")
	}
	setGroupFollowerFn(func() (bool, string) { return true, "stereo pair follower" })
	if f, why := presetWriteGroupFollower(); !f || why == "" {
		t.Fatal("a follower must hold the preset writes and say why")
	}
}

// #1077: a forced pass rewrote six slots the follower already held, every
// write was refused, and the readback still found all six. Only slots that
// were missing before may be called healed.
func TestSplitHealedFromRewritten(t *testing.T) {
	before := map[int]string{1: "a", 2: "b", 4: "d"}
	healed, rewritten := splitHealedFromRewritten([]int{1, 2, 3, 4, 5}, before)
	if !reflect.DeepEqual(healed, []int{3, 5}) {
		t.Errorf("healed = %v, want [3 5]", healed)
	}
	if !reflect.DeepEqual(rewritten, []int{1, 2, 4}) {
		t.Errorf("rewritten = %v, want [1 2 4]", rewritten)
	}
	if h, r := splitHealedFromRewritten(nil, before); h != nil || r != nil {
		t.Errorf("nothing landed must report nothing, got %v %v", h, r)
	}
}
