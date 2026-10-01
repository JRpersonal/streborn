package spotify

import "time"

// A speaker that is a MEMBER of a multiroom group cannot be driven individually
// from the Spotify app, and until now nothing said so.
//
// Picking such a speaker in Spotify starts the engine on it, and STR then tries
// to point the speaker's own transport at the Spotify stream. The firmware
// refuses, because the speaker is following its group:
//
//	SetAVTransportURI -> 501 "Can't control member of group"
//
// Nothing plays. A few seconds later the Spotify app gives up and moves
// playback to another speaker, and from the listener's side the speaker simply
// refused to be used, with no reason given anywhere they can see. Measured live
// on 2026-10-01: two attempts twelve seconds apart, both refused, then
// "playback was transferred to <other speaker>".
//
// The refusal was already recognisable. webui.IsGroupedRejection exists for
// exactly this fault string and the hardware-preset path has consulted it since
// #528. The Spotify path logged a generic warning and told nobody.
//
// Modelled on AudioKeyRefused, which exists for the same reason: the apps show
// what happened instead of leaving the silence unexplained. Short-lived for the
// same reason too. The speaker becomes usable the moment it leaves the group,
// so a stale notice would be worse than none, and nothing on the speaker needs
// clearing.

// groupedRefusalWindow is how long after a refusal the apps still show it. Long
// enough to switch to the app and look, short enough that it is gone once the
// group is dissolved and the speaker works again.
const groupedRefusalWindow = 5 * time.Minute

// NoteGroupedRefusal records that the speaker refused transport control because
// it is a member of a group. Called by the agent when its auto-switch fails
// with that fault.
func (m *Manager) NoteGroupedRefusal() {
	m.mu.Lock()
	m.lastGroupedRefusalAt = time.Now()
	m.mu.Unlock()
}

// ClearGroupedRefusal forgets the refusal, because the speaker has just been
// driven successfully and is therefore no longer a group member.
func (m *Manager) ClearGroupedRefusal() {
	m.mu.Lock()
	m.lastGroupedRefusalAt = time.Time{}
	m.mu.Unlock()
}

// GroupedRefusal reports whether the speaker has just refused to be driven
// because it is part of a group.
func (m *Manager) GroupedRefusal() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastGroupedRefusalAt.IsZero() {
		return false
	}
	return time.Since(m.lastGroupedRefusalAt) < groupedRefusalWindow
}
