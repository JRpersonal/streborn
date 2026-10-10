// recallstaging.go: the gate that keeps a cold recall's paused load silent
// without ever losing the real start of the track.
//
// A cold recall loads the new context with "paused": true, waits for it, sets
// shuffle and repeat, and only then sends /player/resume. What go-librespot
// emits in between differs:
//
//   - The #1077 case (ST10, v1.0.10): the paused load still emits the track's
//     BOS and 13-29 KB of audio (0.5-1.6 s), and the resume starts a fresh
//     logical stream at granule 0. The recall's skip cut was consumed by the
//     first boundary, so the listener heard the track start, break off and
//     start again.
//   - The cold case (Portable, v1.0.10 engine, playback stopped before the
//     recall): the load is logged "paused: false", its BOS and the track
//     itself flow before the resume, and the resume brings NO fresh BOS. The
//     first version of this gate dropped everything until a BOS that never
//     came and lost 1.5-1.8 MB of the track's start.
//
// So the gate holds instead of dropping. From the newest BOS it sees, the
// pages are kept in the drain (stagingHoldMax at most; past that, before the
// resume, the drain stops reading and the engine stops producing). A BOS
// after the resume means the held pages were a preamble: they are dropped and
// the fresh BOS takes the normal path. No BOS within stagingResumeGrace after
// the resume, or a held stream that hits the cap after it, means the held
// pages ARE the start: they go out in order through the normal path, BOS
// first, which meets the armed skip cut there like any boundary. Pages before
// any BOS are the old track and take the normal path (the skip cut handles
// them). A failed play or resume and a user stop open the gate; stagingMaxHold
// bounds a recall that never reaches its resume.

package spotify

import "time"

const (
	// stagingMaxHold bounds the whole paused staging phase: the play POST
	// (10.8 s observed on a slow ST10), the context wait (5 s), a replay from
	// the top (5 s) and the shuffle pick (5 s), with room to spare.
	stagingMaxHold = 45 * time.Second
	// stagingResumeGrace is how long after /player/resume the gate waits for
	// a fresh BOS before it treats the held pages as the real start. The
	// field preamble's BOS and the resume's BOS were 0.9 s apart, waits
	// included.
	stagingResumeGrace = 1500 * time.Millisecond
	// stagingHoldMax caps the held pages. A preamble is under 30 KB; a held
	// stream this large is the track playing for real.
	stagingHoldMax = 1 << 20
	// stagingWaitStep is the drain's poll step while it holds a full buffer
	// and waits for the resume.
	stagingWaitStep = 50 * time.Millisecond
)

// stagingVerdict is what the drain does with one page.
type stagingVerdict int

const (
	// stagingNone: no gate, or a fresh BOS after the resume just opened it.
	// Held pages are dropped before a BOS and delivered before anything else.
	stagingNone stagingVerdict = iota
	// stagingHold: keep the page back (a BOS first drops what was held).
	stagingHold
	// stagingWait: the held pages hit the cap before the resume; stop reading
	// and ask again shortly.
	stagingWait
	// stagingRelease: the held pages are the real start; deliver them, then
	// this page. The gate is open.
	stagingRelease
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
// time has come. bos says whether the page starts a logical stream, held is
// how many bytes the drain currently holds back.
func (m *Manager) stagingVerdictFor(bos bool, held int) stagingVerdict {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.staging {
		return stagingNone
	}
	now := time.Now()
	resumed := !m.stagingResumedAt.IsZero()
	hardBound := now.After(m.stagingUntil)
	if bos {
		if resumed || hardBound {
			m.staging = false
			return stagingNone
		}
		return stagingHold
	}
	if held == 0 {
		// No BOS seen yet: the old track still draining. The skip cut deals
		// with it on the normal path.
		if hardBound {
			m.staging = false
		}
		return stagingNone
	}
	if hardBound || (resumed && (now.Sub(m.stagingResumedAt) > stagingResumeGrace || held >= stagingHoldMax)) {
		m.staging = false
		return stagingRelease
	}
	if held >= stagingHoldMax {
		return stagingWait
	}
	return stagingHold
}
