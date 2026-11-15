package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func spotifyKey(slot int, name, uri string) Preset {
	return Preset{Slot: slot, Name: name, Type: "spotify", URI: uri}
}

func nativeKey(slot int, name, source, location, account string) Preset {
	item, _ := json.Marshal(map[string]string{"source": source, "location": location, "sourceAccount": account})
	return Preset{Slot: slot, Name: name, Type: "native", Native: item}
}

// actions is the plan as "slot:action" lines, the easiest shape to compare.
func actions(steps []KeyCopyStep) map[int]string {
	out := map[int]string{}
	for _, s := range steps {
		out[s.Slot] = s.Action
	}
	return out
}

func writtenSlots(write []Preset) []int {
	out := []int{}
	for _, p := range write {
		out = append(out, p.Slot)
	}
	return out
}

// TestPlanKeyCopy pins every outcome the review shows, one target at a time.
func TestPlanKeyCopy(t *testing.T) {
	src := []Preset{radio(1, "A"), radio(2, "B"), radio(3, "C"), radio(4, "D"), radio(5, "E"), radio(8, "Row2")}
	dst := []Preset{
		radio(2, "B"),     // key 2 is already exactly the same
		radio(3, "Other"), // key 3 holds another station
		radio(6, "D"),     // D sits on key 6, which the copy does not rewrite
		{Slot: 5, Name: "E renamed", Type: "radio", StreamURL: "http://example.com/E"},
	}
	plan := planKeyCopy(src, dst, []int{1, 2, 3, 4, 5, 8}, 6)
	want := map[int]string{
		1: keyStepCopy, 2: keyStepSame, 3: keyStepReplace, 4: keyStepOnOtherKey,
		5: keyStepCopy, // the same station under another name: the key is renamed, nothing replaced
		8: keyStepNotKept,
	}
	if got := actions(plan.steps); !reflect.DeepEqual(got, want) {
		t.Errorf("plan = %v, want %v", got, want)
	}
	for _, s := range plan.steps {
		switch s.Slot {
		case 3:
			if s.Replaces != "Other" || s.Name != "C" {
				t.Errorf("key 3 = %+v, want C replacing Other", s)
			}
		case 4:
			if s.OnSlot != 6 {
				t.Errorf("key 4 = %+v, want on key 6", s)
			}
		case 8:
			if s.Name != "Row2" {
				t.Errorf("key 8 = %+v, want its station named", s)
			}
		}
	}
	if got := writtenSlots(plan.write); !reflect.DeepEqual(got, []int{1, 3, 5}) {
		t.Errorf("written = %v, want [1 3 5]: same, colliding and unkept keys stay out", got)
	}
	for i := 1; i < len(plan.steps); i++ {
		if plan.steps[i-1].Slot >= plan.steps[i].Slot {
			t.Fatalf("steps not in slot order: %+v", plan.steps)
		}
	}
}

// TestPlanKeyCopySwapIsNoCollision: two chosen keys that swap places are both
// rewritten, so neither collides (#882).
func TestPlanKeyCopySwapIsNoCollision(t *testing.T) {
	plan := planKeyCopy([]Preset{radio(1, "A"), radio(2, "B")}, []Preset{radio(1, "B"), radio(2, "A")}, []int{1, 2}, 6)
	if got := actions(plan.steps); !reflect.DeepEqual(got, map[int]string{1: keyStepReplace, 2: keyStepReplace}) {
		t.Errorf("plan = %v, want both keys replaced", got)
	}
}

// TestPlanKeyCopyChainedCollision: key 2 collides with key 5 and stays out, so
// the target's key 2 is not rewritten and keeps A, which key 1 would then put
// on a second key. Key 1 stays out too, as the agent's refusals would decide.
func TestPlanKeyCopyChainedCollision(t *testing.T) {
	src := []Preset{radio(1, "A"), radio(2, "B")}
	dst := []Preset{radio(2, "A"), radio(5, "B")}
	plan := planKeyCopy(src, dst, []int{1, 2}, 6)
	if got := actions(plan.steps); !reflect.DeepEqual(got, map[int]string{1: keyStepOnOtherKey, 2: keyStepOnOtherKey}) {
		t.Errorf("plan = %v, want both left out", got)
	}
	if len(plan.write) != 0 {
		t.Errorf("written = %v, want nothing", writtenSlots(plan.write))
	}
	for _, s := range plan.steps {
		if (s.Slot == 1 && s.OnSlot != 2) || (s.Slot == 2 && s.OnSlot != 5) {
			t.Errorf("step %+v names the wrong key", s)
		}
	}
}

// TestPlanKeyCopyStationIdentity: Spotify keys match by URI and Pandora or
// iHeartRadio keys by service, location and account, as on the agent.
func TestPlanKeyCopyStationIdentity(t *testing.T) {
	src := []Preset{
		spotifyKey(1, "Mix", "spotify:playlist:1"),
		nativeKey(2, "Pandora Jazz", "PANDORA", "loc-1", "me"),
		nativeKey(3, "iHeart", "IHEARTRADIO", "loc-2", ""),
	}
	dst := []Preset{
		spotifyKey(4, "Mix (old)", "spotify:playlist:1"),
		nativeKey(5, "Jazz", "pandora", "loc-1", "ME"),
		nativeKey(6, "Heart", "IHEART", "loc-2", "someone"),
	}
	plan := planKeyCopy(src, dst, []int{1, 2, 3}, 6)
	want := map[int]string{1: keyStepOnOtherKey, 2: keyStepOnOtherKey, 3: keyStepOnOtherKey}
	if got := actions(plan.steps); !reflect.DeepEqual(got, want) {
		t.Errorf("plan = %v, want %v", got, want)
	}
	other := []Preset{nativeKey(5, "Jazz", "PANDORA", "loc-1", "someone-else")}
	if got := actions(planKeyCopy(src, other, []int{2}, 6).steps); got[2] != keyStepCopy {
		t.Errorf("another account's station = %v, want copy", got)
	}
}

// TestPreviewWritesNothingAndTheCopyDoesWhatItShowed is the promise of the
// review: the preview touches nothing, and the copy made right after it does
// exactly what it showed, key by key.
func TestPreviewWritesNothingAndTheCopyDoesWhatItShowed(t *testing.T) {
	src := newFakeBox(t, "127.0.0.2", []Preset{radio(1, "A"), radio(2, "B"), radio(3, "C"), radio(4, "D"), radio(8, "Row2")})
	dst := newKeyBox(t, "127.0.0.3", []Preset{radio(2, "B"), radio(3, "Other"), radio(6, "D")})
	installProbe(t, func() bool { return true })
	a := copyApp(t)
	slots := []int{1, 2, 3, 4, 8}

	prev, err := a.PreviewPresetKeyCopy(src.host, src.port(t), dst.host, dst.port(t), slots)
	if err != nil {
		t.Fatalf("preview failed: %v", err)
	}
	if dst.bulkPuts != 0 || dst.slotPuts != 0 || dst.syncs != 0 {
		t.Fatalf("the preview wrote to the target (bulk %d, slot %d, sync %d)", dst.bulkPuts, dst.slotPuts, dst.syncs)
	}
	want := map[int]string{1: keyStepCopy, 2: keyStepSame, 3: keyStepReplace, 4: keyStepOnOtherKey, 8: keyStepNotKept}
	if got := actions(prev.Steps); !reflect.DeepEqual(got, want) {
		t.Fatalf("preview = %v, want %v", got, want)
	}

	res, err := a.CopyPresetKeysAcrossBoxes(src.host, src.port(t), dst.host, dst.port(t), slots)
	if err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	var copied, same, notKept []int
	skipped := []KeyCopySkip{}
	for _, s := range prev.Steps {
		switch s.Action {
		case keyStepCopy, keyStepReplace:
			copied = append(copied, s.Slot)
		case keyStepSame:
			same = append(same, s.Slot)
		case keyStepOnOtherKey:
			skipped = append(skipped, KeyCopySkip{Slot: s.Slot, Name: s.Name, OnSlot: s.OnSlot})
		case keyStepNotKept:
			notKept = append(notKept, s.Slot)
		}
	}
	if !reflect.DeepEqual(res.Copied, copied) || !reflect.DeepEqual(res.Same, same) ||
		!reflect.DeepEqual(res.NotKept, notKept) || !reflect.DeepEqual(res.Skipped, skipped) {
		t.Errorf("copy = %+v, the preview showed copied %v same %v skipped %+v notKept %v", res, copied, same, skipped, notKept)
	}
	if dst.bulkPuts != 1 {
		t.Errorf("bulk writes = %d, want one with no refusal: the plan already left the collision out", dst.bulkPuts)
	}
	wantStore := map[int]string{1: "A", 2: "B", 3: "C", 6: "D"}
	if got := dst.names(); !reflect.DeepEqual(got, wantStore) {
		t.Errorf("target holds %v, want %v", got, wantStore)
	}
}

// TestCopyKeysLeavesAnIdenticalKeyUnwritten: a key the target already holds
// exactly like this is not written again; with nothing else chosen, the copy
// sends nothing at all.
func TestCopyKeysLeavesAnIdenticalKeyUnwritten(t *testing.T) {
	src := newFakeBox(t, "127.0.0.2", []Preset{radio(1, "A"), radio(2, "B")})
	dst := newKeyBox(t, "127.0.0.3", []Preset{radio(1, "A"), radio(2, "B")})
	installProbe(t, func() bool { return true })
	a := copyApp(t)
	res, err := a.CopyPresetKeysAcrossBoxes(src.host, src.port(t), dst.host, dst.port(t), []int{1, 2})
	if err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if !reflect.DeepEqual(res.Same, []int{1, 2}) || len(res.Copied) != 0 {
		t.Errorf("result = %+v, want both keys reported as the same", res)
	}
	if dst.bulkPuts != 0 || dst.slotPuts != 0 {
		t.Errorf("writes = bulk %d, slot %d, want none", dst.bulkPuts, dst.slotPuts)
	}
}

// TestPreviewSaysWhenTheTargetCannotBeRead: the review names the speaker it
// could not check instead of guessing.
func TestPreviewSaysWhenTheTargetCannotBeRead(t *testing.T) {
	src := newFakeBox(t, "127.0.0.2", []Preset{radio(1, "A")})
	dst := newKeyBox(t, "127.0.0.3", nil)
	dst.noList = true
	a := copyApp(t)
	if _, err := a.PreviewPresetKeyCopy(src.host, src.port(t), dst.host, dst.port(t), []int{1}); err == nil {
		t.Fatal("a target whose keys cannot be read gave a preview")
	}
	if _, err := a.PreviewPresetKeyCopy(src.host, src.port(t), src.host, src.port(t), []int{1}); err == nil {
		t.Fatal("a preview onto the source itself was accepted")
	}
	if _, err := a.PreviewPresetKeyCopy(src.host, src.port(t), dst.host, dst.port(t), nil); err == nil {
		t.Fatal("an empty key choice was accepted")
	}
}
