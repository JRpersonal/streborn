// recallstaging.go: the gate that keeps a cold recall's paused load silent.
//
// A cold recall loads the new context with "paused": true, waits for it, sets
// shuffle and repeat, and only then sends /player/resume. go-librespot does
// not stay silent during that paused load: it emits the new track's BOS and
// the first 13-29 KB of audio (0.5-1.6 s), and the resume then starts a fresh
// logical stream at granule 0. The recall's skip cut was consumed by the
// FIRST of those two boundaries, so the preamble reached the box and the
// listener heard the new track start, break off and start again (#1077,
// ST10, v1.0.10: two BOS per press, a bad-checksum mid-page handoff between).
//
// The gate closes before the paused /player/play and drops every page the
// engine emits, BOS included, without letting a BOS consume the skip cut.
// It opens on the first BOS after /player/resume was sent; that BOS then
// takes the normal path (the skip cut drops the old tail, the pacing
// re-anchors). Every way out is bounded: a failed play or resume opens it at
// once, a resume whose BOS never comes opens it after stagingResumeGrace, and
// stagingMaxHold caps a recall that never reaches its resume at all, so a
// broken recall can never leave the stream permanently mute.

package spotify

import "time"

const (
	// stagingMaxHold bounds the whole paused staging phase: the play POST
	// (10.8 s observed on a slow ST10), the context wait (5 s), a replay from
	// the top (5 s) and the shuffle pick (5 s), with room to spare.
	stagingMaxHold = 45 * time.Second
	// stagingResumeGrace is how long after /player/resume the gate waits for
	// the resume's BOS. The field gap was 0.9 s.
	stagingResumeGrace = 4 * time.Second
)

// stagingVerdict is what the drain does with one page while the gate is
// considered.
type stagingVerdict int

const (
	// stagingNone: no gate, or the gate just opened on this BOS. The page
	// takes the normal path.
	stagingNone stagingVerdict = iota
	// stagingDrop: the page belongs to the paused load and never reaches the
	// box.
	stagingDrop
	// stagingExpired: the gate timed out on a non-BOS page. The stream goes
	// on without a fresh BOS, so the drain re-sends the current track's
	// headers in front of it.
	stagingExpired
)

// beginRecallStaging closes the gate for the recall that is about to send
// its paused /player/play and returns that recall's generation. A newer
// recall simply takes the gate over.
func (m *Manager) beginRecallStaging() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stagingGen++
	m.staging = true
	m.stagingUntil = time.Now().Add(stagingMaxHold)
	m.stagingResumedAt = time.Time{}
	return m.stagingGen
}

// markRecallStagingResumed records that recall gen is sending /player/resume,
// so the next BOS is the real track start. Called BEFORE the POST: the BOS
// can arrive before the HTTP reply does.
func (m *Manager) markRecallStagingResumed(gen uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.staging && m.stagingGen == gen {
		m.stagingResumedAt = time.Now()
	}
}

// endRecallStaging opens the gate for recall gen (a failed play or resume:
// no further audio of that recall is coming to be gated). A gate a newer
// recall owns is left alone.
func (m *Manager) endRecallStaging(gen uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stagingGen == gen {
		m.staging = false
	}
}

// clearRecallStaging opens the gate whoever owns it (a user stop).
func (m *Manager) clearRecallStaging() {
	m.mu.Lock()
	m.staging = false
	m.mu.Unlock()
}

// recallStaging reports whether the gate is closed (tests, diagnostics).
func (m *Manager) recallStaging() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.staging
}

// stagingVerdictFor decides one page's fate and opens the gate when its
// time has come. bos says whether the page starts a logical stream.
func (m *Manager) stagingVerdictFor(bos bool) stagingVerdict {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.staging {
		return stagingNone
	}
	now := time.Now()
	resumed := !m.stagingResumedAt.IsZero()
	expired := now.After(m.stagingUntil) || (resumed && now.Sub(m.stagingResumedAt) > stagingResumeGrace)
	switch {
	case resumed && bos:
		m.staging = false
		return stagingNone
	case expired:
		m.staging = false
		if bos {
			return stagingNone
		}
		return stagingExpired
	default:
		return stagingDrop
	}
}
