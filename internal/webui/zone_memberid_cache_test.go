package webui

import "testing"

// A member speaker that is waking or busy right after an OTA does not answer
// its firmware /info in time. Zone forming used to fall back to the deviceID
// the caller supplied, which on a two-chip chassis is the wlan0 MAC rather than
// the SoundTouch ID /setZone keys on, so a member was enrolled that never
// joined. These tests pin the cache that carries the firmware's own answer
// across such a moment (#544, and a 7-speaker fleet on the same day where the
// correction fired for one speaker and then not for the same speaker 25s
// later).
func TestMemberDeviceIDCache(t *testing.T) {
	t.Run("a firmware answer is remembered per IP", func(t *testing.T) {
		s := &Server{}
		s.rememberMemberDeviceID("192.0.2.26", "SOUNDTOUCHID26")
		s.rememberMemberDeviceID("192.0.2.31", "SOUNDTOUCHID31")

		got, ok := s.cachedMemberDeviceID("192.0.2.26")
		if !ok || got != "SOUNDTOUCHID26" {
			t.Errorf("got (%q, %v), want (\"SOUNDTOUCHID26\", true)", got, ok)
		}
		got, ok = s.cachedMemberDeviceID("192.0.2.31")
		if !ok || got != "SOUNDTOUCHID31" {
			t.Errorf("got (%q, %v), want (\"SOUNDTOUCHID31\", true)", got, ok)
		}
	})

	t.Run("a speaker never seen answering reports no entry", func(t *testing.T) {
		s := &Server{}
		if got, ok := s.cachedMemberDeviceID("192.0.2.99"); ok {
			t.Errorf("got (%q, true), want no entry", got)
		}
	})

	t.Run("a later answer replaces the earlier one", func(t *testing.T) {
		s := &Server{}
		s.rememberMemberDeviceID("192.0.2.26", "OLDID")
		s.rememberMemberDeviceID("192.0.2.26", "NEWID")
		if got, _ := s.cachedMemberDeviceID("192.0.2.26"); got != "NEWID" {
			t.Errorf("got %q, want NEWID", got)
		}
	})

	// The cache exists to hold FIRMWARE answers only. An empty id would make a
	// later lookup hand out nothing while claiming a hit, and an empty IP has
	// no member to belong to.
	t.Run("empty values are not stored", func(t *testing.T) {
		s := &Server{}
		s.rememberMemberDeviceID("192.0.2.26", "")
		s.rememberMemberDeviceID("", "SOUNDTOUCHID")
		if _, ok := s.cachedMemberDeviceID("192.0.2.26"); ok {
			t.Error("an empty deviceID was cached")
		}
		if _, ok := s.cachedMemberDeviceID(""); ok {
			t.Error("an entry under an empty IP was cached")
		}
	})
}
