package main

// Copying CHOSEN preset keys to another speaker (#1551). The whole-set copy in
// app_presets.go carries every key; this one carries only the keys the user
// ticked and leaves every other key on the target exactly as it is.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// KeyCopySkip is one chosen key that did not land on the target.
type KeyCopySkip struct {
	// Slot is the key on the source (and the key it would have taken on the
	// target): 1..6, or 7..12 for the keys pressed twice.
	Slot int    `json:"slot"`
	Name string `json:"name"`
	// OnSlot is the target's key that already holds this station (the
	// duplicate guard, #836), 0 when the key was refused for another reason.
	OnSlot int `json:"onSlot,omitempty"`
	// Reason is the target's answer when OnSlot is 0.
	Reason string `json:"reason,omitempty"`
}

// KeyCopyResult says, for one target speaker, what happened to each chosen key.
type KeyCopyResult struct {
	Copied  []int         `json:"copied"`
	Skipped []KeyCopySkip `json:"skipped"`
	// NotKept are chosen keys pressed twice (7..12) that the target cannot
	// hold, because its agent keeps one row of keys only.
	NotKept []int `json:"notKept"`
	// Same are chosen keys the target already held exactly like the source,
	// so they were left as they were instead of being written again.
	Same []int `json:"same"`
}

// What a copy of chosen keys does with one key on one target. The preview
// shows these before anything is written; the copy acts on the very same plan
// (planKeyCopy), so what the user was shown is what happens.
const (
	// keyStepCopy: the target's key is empty, or holds the same station with
	// other details (a renamed key): the source key goes onto it.
	keyStepCopy = "copy"
	// keyStepReplace: the target's key holds a different station, which the
	// copy replaces.
	keyStepReplace = "replace"
	// keyStepSame: the target's key is already exactly the source key. It is
	// left out of the write, which spares the speaker a NAND write and a
	// hardware key registration for nothing.
	keyStepSame = "same"
	// keyStepOnOtherKey: the station already sits on another key of the
	// target that the copy does not rewrite (the duplicate guard, #836).
	keyStepOnOtherKey = "onOtherKey"
	// keyStepNotKept: a key pressed twice (7..12) on a target that keeps one
	// row of keys only.
	keyStepNotKept = "notKept"
)

// KeyCopyStep is what a copy would do with one chosen key on one target.
type KeyCopyStep struct {
	// Slot is the key on the source and on the target: 1..6, or 7..12 for
	// the keys pressed twice.
	Slot int `json:"slot"`
	// Name is the source key's station.
	Name string `json:"name"`
	// Action is one of copy, replace, same, onOtherKey, notKept.
	Action string `json:"action"`
	// Replaces is the station the target holds on that key now (replace).
	Replaces string `json:"replaces,omitempty"`
	// OnSlot is the target's key that already holds the station (onOtherKey).
	OnSlot int `json:"onSlot,omitempty"`
}

// KeyCopyPreview is the plan of a copy of chosen keys onto one target, in
// slot order, read without writing anything.
type KeyCopyPreview struct {
	Steps []KeyCopyStep `json:"steps"`
}

// keyCopyPlan is the decision a copy of chosen keys acts on: one step per
// chosen key and the presets that are actually written.
type keyCopyPlan struct {
	steps []KeyCopyStep
	write []Preset
}

// validateKeyCopy is the argument check the preview and the copy share.
func validateKeyCopy(srcHost, dstHost string, slots []int) error {
	if srcHost == "" || dstHost == "" {
		return fmt.Errorf("source and target host are required")
	}
	if srcHost == dstHost {
		return fmt.Errorf("source and target are the same speaker")
	}
	if len(slots) == 0 {
		return fmt.Errorf("no key chosen to copy")
	}
	return nil
}

// PreviewPresetKeyCopy says, per chosen key, what CopyPresetKeysAcrossBoxes
// with the same arguments would do on the target, and writes nothing. It reads
// both speakers' keys and the target's row count, and runs the same plan the
// copy runs. The target's agent stays the final judge when the copy is made
// (a key changed in between, a station it refuses to keep); the copy's own
// result reports that.
//
// It does not wait for a target that is still starting up, as the copy does:
// the user is looking at the review, and "not answering" is the honest answer
// for that speaker there.
func (a *App) PreviewPresetKeyCopy(srcHost string, srcPort int, dstHost string, dstPort int, slots []int) (KeyCopyPreview, error) {
	out := KeyCopyPreview{Steps: []KeyCopyStep{}}
	if err := validateKeyCopy(srcHost, dstHost, slots); err != nil {
		return out, err
	}
	src, err := a.GetPresets(srcHost, srcPort)
	if err != nil {
		return out, fmt.Errorf("read source presets: %w", err)
	}
	dst, err := a.GetPresets(dstHost, dstPort)
	if err != nil {
		return out, fmt.Errorf("read target presets: %w", err)
	}
	plan := planKeyCopy(src, dst, slots, a.boxPresetMaxSlot(dstHost, dstPort))
	out.Steps = append(out.Steps, plan.steps...)
	a.logger.Info("copy keys: preview", "src", srcHost, "dst", dstHost, "chosen", slots,
		"steps", len(plan.steps), "writes", len(plan.write))
	return out, nil
}

// planKeyCopy decides what a copy of the chosen slots of src does on a target
// holding dst that keeps keys up to maxSlot. Pure, so the preview and the copy
// share it and it is tested without a speaker.
//
// The duplicate rule is the agent's bulk rule (#882): a station collides with
// a target key the write does not rewrite, and keys that swap places do not
// collide with each other. A key left out (same, or a collision) is not
// rewritten either, so it can make another chosen key collide in turn; the
// loop runs until nothing new collides. Collisions only grow as keys drop
// out, so the result does not depend on the order, and it is the set the
// agent's own one-at-a-time refusals arrive at.
func planKeyCopy(src, dst []Preset, slots []int, maxSlot int) keyCopyPlan {
	set, notKept := chosenKeys(src, slots, maxSlot)
	onTarget := make(map[int]Preset, len(dst))
	var targetKeys []Preset
	for _, p := range dst {
		if p.Slot < 1 || p.Name == "" {
			continue
		}
		onTarget[p.Slot] = p
		targetKeys = append(targetKeys, p)
	}
	sort.Slice(targetKeys, func(i, j int) bool { return targetKeys[i].Slot < targetKeys[j].Slot })

	stepOf := make(map[int]KeyCopyStep, len(set)+len(notKept))
	srcName := make(map[int]string, len(src))
	for _, p := range src {
		srcName[p.Slot] = p.Name
	}
	for _, s := range notKept {
		stepOf[s] = KeyCopyStep{Slot: s, Name: srcName[s], Action: keyStepNotKept}
	}
	pending := make([]Preset, 0, len(set))
	for _, p := range set {
		if cur, ok := onTarget[p.Slot]; ok && sameKeyContent(p, cur) {
			stepOf[p.Slot] = KeyCopyStep{Slot: p.Slot, Name: p.Name, Action: keyStepSame}
			continue
		}
		pending = append(pending, p)
	}
	for {
		rewritten := make(map[int]bool, len(pending))
		for _, p := range pending {
			rewritten[p.Slot] = true
		}
		kept := pending[:0:0]
		for _, p := range pending {
			if on := collidingKey(p, targetKeys, rewritten); on > 0 {
				stepOf[p.Slot] = KeyCopyStep{Slot: p.Slot, Name: p.Name, Action: keyStepOnOtherKey, OnSlot: on}
				continue
			}
			kept = append(kept, p)
		}
		if len(kept) == len(pending) {
			break
		}
		pending = kept
	}
	for _, p := range pending {
		step := KeyCopyStep{Slot: p.Slot, Name: p.Name, Action: keyStepCopy}
		if cur, ok := onTarget[p.Slot]; ok && !sameKeyStation(p, cur) {
			step.Action = keyStepReplace
			step.Replaces = cur.Name
		}
		stepOf[p.Slot] = step
	}
	plan := keyCopyPlan{steps: make([]KeyCopyStep, 0, len(stepOf)), write: pending}
	for _, st := range stepOf {
		plan.steps = append(plan.steps, st)
	}
	sort.Slice(plan.steps, func(i, j int) bool { return plan.steps[i].Slot < plan.steps[j].Slot })
	return plan
}

// collidingKey is the first target key (by slot) that already holds p's
// station and is not rewritten, 0 when there is none.
func collidingKey(p Preset, targetKeys []Preset, rewritten map[int]bool) int {
	for _, other := range targetKeys {
		if other.Slot == p.Slot || rewritten[other.Slot] {
			continue
		}
		if sameKeyStation(p, other) {
			return other.Slot
		}
	}
	return 0
}

// sameKeyStation is the agent's "same station" test of its duplicate guard
// (internal/webui samePresetStation), which the desktop module cannot import:
// a native key by its service, location and account, a Spotify key by its
// URI, every other key by its stream address.
func sameKeyStation(p, other Preset) bool {
	switch p.Type {
	case "native":
		return other.Type == "native" && sameNativeRef(p.Native, other.Native)
	case "spotify":
		return p.URI != "" && other.URI == p.URI
	}
	return p.StreamURL != "" && other.StreamURL == p.StreamURL
}

// sameNativeRef mirrors internal/presets SameNativeItem on the raw items.
func sameNativeRef(a, b json.RawMessage) bool {
	type ref struct {
		Source        string `json:"source"`
		SourceAccount string `json:"sourceAccount"`
		Location      string `json:"location"`
	}
	var x, y ref
	if len(a) == 0 || len(b) == 0 || json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	norm := func(s string) string {
		s = strings.ToUpper(strings.TrimSpace(s))
		if s == "IHEARTRADIO" {
			return "IHEART"
		}
		return s
	}
	if x.Location == "" || norm(x.Source) != norm(y.Source) || x.Location != y.Location {
		return false
	}
	if x.SourceAccount == "" || y.SourceAccount == "" {
		return true
	}
	return strings.EqualFold(x.SourceAccount, y.SourceAccount)
}

// sameKeyContent says whether the target's key is already exactly the source
// key: every field the copy would carry, compared as the JSON it would send.
// A difference this misses only costs a write of the same data; it never
// leaves a key out that differs.
func sameKeyContent(p, cur Preset) bool {
	a, err1 := json.Marshal(p)
	b, err2 := json.Marshal(cur)
	return err1 == nil && err2 == nil && bytes.Equal(a, b)
}

// result is what the plan already decided, as the copy reports it: the keys
// left as they were, the collisions and the keys the target cannot hold.
func (p keyCopyPlan) result() KeyCopyResult {
	res := KeyCopyResult{Copied: []int{}, Skipped: []KeyCopySkip{}, NotKept: []int{}, Same: []int{}}
	for _, st := range p.steps {
		switch st.Action {
		case keyStepSame:
			res.Same = append(res.Same, st.Slot)
		case keyStepOnOtherKey:
			res.Skipped = append(res.Skipped, KeyCopySkip{Slot: st.Slot, Name: st.Name, OnSlot: st.OnSlot})
		case keyStepNotKept:
			res.NotKept = append(res.NotKept, st.Slot)
		}
	}
	return res
}

// CopyPresetKeysAcrossBoxes copies only the given keys (slots 1..12) of the
// source speaker onto the same keys of the target, and leaves every other key
// of the target alone.
//
// The duplicate guard works as it does for a save: a station that already sits
// on ANOTHER key of the target is not copied (nothing is ever deleted to make
// room, #836), and the result names that key so the app can say so. Unlike the
// whole-set copy, such a collision does not stop the other chosen keys: they
// are copied, and the colliding one is reported. A key the target already
// holds exactly like this is left as it is (Same). The error is reserved for a
// target that took nothing at all (unreadable source, a target that never
// answered, a refusal that is not a collision).
//
// What goes where is decided by planKeyCopy, the same plan PreviewPresetKeyCopy
// shows before the copy. The target's agent still judges every write, so a
// collision the plan could not see (the target's keys changed in between, or
// they could not be read) is still caught and reported.
func (a *App) CopyPresetKeysAcrossBoxes(srcHost string, srcPort int, dstHost string, dstPort int, slots []int) (KeyCopyResult, error) {
	res := KeyCopyResult{Copied: []int{}, Skipped: []KeyCopySkip{}, NotKept: []int{}, Same: []int{}}
	if err := validateKeyCopy(srcHost, dstHost, slots); err != nil {
		return res, err
	}
	presets, err := a.GetPresets(srcHost, srcPort)
	if err != nil {
		return res, fmt.Errorf("read source presets: %w", err)
	}
	// Same reason as the whole-set copy: a target that has just rebooted after
	// an update answers pings before its agent listens.
	if err := a.waitCopyTargetReady(dstHost, dstPort); err != nil {
		return res, err
	}
	maxSlot := a.boxPresetMaxSlot(dstHost, dstPort)
	// Without the target's keys the plan simply sees an empty target: every
	// chosen key is sent and the agent's own guard sorts out collisions, as
	// the copy did before it had a plan.
	dstPresets, derr := a.GetPresets(dstHost, dstPort)
	if derr != nil {
		a.logger.Info("copy keys: could not read the target's keys, its agent judges every key",
			"dst", dstHost, "err", derr)
		dstPresets = nil
	}
	plan := planKeyCopy(presets, dstPresets, slots, maxSlot)
	res = plan.result()
	set := plan.write
	if len(set) == 0 {
		a.logger.Info("copy keys: nothing to write to this target",
			"src", srcHost, "dst", dstHost, "chosen", slots, "same", res.Same,
			"skipped", len(res.Skipped), "notKept", res.NotKept)
		return res, nil
	}
	copied, skipped, err := a.putChosenKeys(dstHost, dstPort, set)
	res.Copied = append(res.Copied, copied...)
	res.Skipped = append(res.Skipped, skipped...)
	a.logger.Info("copy keys: done", "src", srcHost, "dst", dstHost,
		"chosen", slots, "copied", res.Copied, "same", res.Same, "skipped", len(res.Skipped), "notKept", res.NotKept, "err", err)
	if err != nil && len(res.Copied) == 0 {
		return res, err
	}
	// The target's agent writes each accepted key to the speaker's own hardware
	// key itself (bulk and per-slot alike), so no extra full key sync here: it
	// would rewrite the six keys the user chose to leave alone.
	if hasSpotifyPreset(presetsOnSlots(set, res.Copied)) {
		a.transferSpotifyCredential(srcHost, srcPort, dstHost, dstPort)
	}
	return res, nil
}

// chosenKeys picks the named source presets on the chosen slots, in slot
// order. A chosen key pressed twice that the target cannot hold (above
// maxSlot) goes to notKept; an empty or unknown slot is dropped.
func chosenKeys(presets []Preset, slots []int, maxSlot int) (set []Preset, notKept []int) {
	want := make(map[int]bool, len(slots))
	for _, s := range slots {
		want[s] = true
	}
	notKept = []int{}
	for _, p := range presetsUpTo(presets, 2*presetRowSize) {
		if !want[p.Slot] {
			continue
		}
		if p.Slot > maxSlot {
			notKept = append(notKept, p.Slot)
			continue
		}
		set = append(set, p)
	}
	return set, notKept
}

// presetsOnSlots returns the presets of set whose slot is in slots.
func presetsOnSlots(set []Preset, slots []int) []Preset {
	in := make(map[int]bool, len(slots))
	for _, s := range slots {
		in[s] = true
	}
	var out []Preset
	for _, p := range set {
		if in[p.Slot] {
			out = append(out, p)
		}
	}
	return out
}

// putChosenKeys writes set onto the target and sorts each key into copied or
// skipped.
//
// It uses the bulk endpoint, whose duplicate rule judges the FINISHED result
// (#882), so two chosen keys that swap places on the target do not collide
// with each other. A refusal there is all-or-nothing: when it names the key
// that collided, that key is set aside and the rest is sent again. That loop
// is bounded by the number of chosen keys (at most twelve), and a refused
// request writes nothing on the speaker. An agent without the bulk endpoint,
// or one whose refusal does not say which key collided, gets the per-slot
// writes, where every key carries its own answer.
func (a *App) putChosenKeys(host string, port int, set []Preset) (copied []int, skipped []KeyCopySkip, err error) {
	pending := append([]Preset(nil), set...)
	for len(pending) > 0 {
		supported, berr := a.putPresetsBulk(host, port, pending)
		if berr != nil && isTransportNotReady(berr) {
			if werr := a.waitCopyTargetReady(host, port); werr == nil {
				supported, berr = a.putPresetsBulk(host, port, pending)
			}
		}
		if !supported {
			if berr != nil {
				return copied, skipped, berr
			}
			a.logger.Info("copy keys: target agent has no bulk preset endpoint, copying key by key", "dst", host)
			c, s := a.putKeysPerSlot(host, port, pending)
			return append(copied, c...), append(skipped, s...), nil
		}
		if berr == nil {
			for _, p := range pending {
				copied = append(copied, p.Slot)
			}
			return copied, skipped, nil
		}
		var conflict *presetConflictError
		if !errors.As(berr, &conflict) {
			// A real refusal (an unplayable preset, a server error): report it
			// for every key still pending. Nothing of the pending set landed.
			for _, p := range pending {
				skipped = append(skipped, KeyCopySkip{Slot: p.Slot, Name: p.Name, Reason: berr.Error()})
			}
			return copied, skipped, berr
		}
		i := conflictIndex(pending, conflict)
		if i < 0 {
			a.logger.Info("copy keys: the target refused a key without naming which, copying key by key",
				"dst", host, "station", conflict.Name, "onKey", conflict.Slot)
			c, s := a.putKeysPerSlot(host, port, pending)
			return append(copied, c...), append(skipped, s...), nil
		}
		p := pending[i]
		a.logger.Info("copy keys: the station is already on another key of the target, leaving this key out",
			"dst", host, "slot", p.Slot, "name", p.Name, "onKey", conflict.Slot)
		skipped = append(skipped, KeyCopySkip{Slot: p.Slot, Name: p.Name, OnSlot: conflict.Slot})
		pending = append(pending[:i:i], pending[i+1:]...)
	}
	return copied, skipped, nil
}

// conflictIndex finds the pending key a bulk refusal is about: the key the
// agent names (payloadSlot), else the one key whose name matches the station
// the refusal names, else -1. With a single key pending it can only be that
// one.
func conflictIndex(pending []Preset, c *presetConflictError) int {
	if c.PayloadSlot > 0 {
		for i, p := range pending {
			if p.Slot == c.PayloadSlot {
				return i
			}
		}
	}
	if len(pending) == 1 {
		return 0
	}
	match := -1
	for i, p := range pending {
		if strings.EqualFold(strings.TrimSpace(p.Name), strings.TrimSpace(c.Name)) {
			if match >= 0 {
				return -1 // two candidates: do not guess
			}
			match = i
		}
	}
	return match
}

// putKeysPerSlot writes each key on its own, for an agent without the bulk
// endpoint. Every key gets its own answer: copied, already on another key, or
// refused with the target's reason.
func (a *App) putKeysPerSlot(host string, port int, set []Preset) (copied []int, skipped []KeyCopySkip) {
	for _, p := range set {
		err := a.putPresetSlot(host, port, p)
		if err != nil && isTransportNotReady(err) {
			if werr := a.waitCopyTargetReady(host, port); werr == nil {
				err = a.putPresetSlot(host, port, p)
			}
		}
		var conflict *presetConflictError
		switch {
		case err == nil:
			copied = append(copied, p.Slot)
		case errors.As(err, &conflict):
			skipped = append(skipped, KeyCopySkip{Slot: p.Slot, Name: p.Name, OnSlot: conflict.Slot})
		default:
			a.logger.Warn("copy keys: key refused by the target", "dst", host, "slot", p.Slot, "type", p.Type, "err", err)
			skipped = append(skipped, KeyCopySkip{Slot: p.Slot, Name: p.Name, Reason: err.Error()})
		}
	}
	return copied, skipped
}

// putPresetSlot is PUT /api/presets/<slot> with the preset verbatim, turning
// the duplicate guard's 409 into a presetConflictError.
func (a *App) putPresetSlot(host string, port int, p Preset) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	resp, err := a.boxDo(host, port, http.MethodPut, fmt.Sprintf("%s/%d", presetAPIPath, p.Slot), "application/json", string(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		if c := parsePresetConflict(body); c != nil {
			return c
		}
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	if resp.StatusCode >= 400 {
		return readHTTPError(resp)
	}
	return nil
}
