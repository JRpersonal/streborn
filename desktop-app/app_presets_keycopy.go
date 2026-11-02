package main

// Copying CHOSEN preset keys to another speaker (#1551). The whole-set copy in
// app_presets.go carries every key; this one carries only the keys the user
// ticked and leaves every other key on the target exactly as it is.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
}

// CopyPresetKeysAcrossBoxes copies only the given keys (slots 1..12) of the
// source speaker onto the same keys of the target, and leaves every other key
// of the target alone.
//
// The duplicate guard works as it does for a save: a station that already sits
// on ANOTHER key of the target is not copied (nothing is ever deleted to make
// room, #836), and the result names that key so the app can say so. Unlike the
// whole-set copy, such a collision does not stop the other chosen keys: they
// are copied, and the colliding one is reported. The error is reserved for a
// target that took nothing at all (unreadable source, a target that never
// answered, a refusal that is not a collision).
func (a *App) CopyPresetKeysAcrossBoxes(srcHost string, srcPort int, dstHost string, dstPort int, slots []int) (KeyCopyResult, error) {
	res := KeyCopyResult{Copied: []int{}, Skipped: []KeyCopySkip{}, NotKept: []int{}}
	if srcHost == "" || dstHost == "" {
		return res, fmt.Errorf("source and target host are required")
	}
	if srcHost == dstHost {
		return res, fmt.Errorf("source and target are the same speaker")
	}
	if len(slots) == 0 {
		return res, fmt.Errorf("no key chosen to copy")
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
	set, notKept := chosenKeys(presets, slots, maxSlot)
	res.NotKept = notKept
	if len(set) == 0 {
		a.logger.Info("copy keys: nothing to copy to this target",
			"src", srcHost, "dst", dstHost, "chosen", slots, "notKept", notKept)
		return res, nil
	}
	copied, skipped, err := a.putChosenKeys(dstHost, dstPort, set)
	res.Copied = append(res.Copied, copied...)
	res.Skipped = append(res.Skipped, skipped...)
	a.logger.Info("copy keys: done", "src", srcHost, "dst", dstHost,
		"chosen", slots, "copied", res.Copied, "skipped", len(res.Skipped), "notKept", res.NotKept, "err", err)
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
