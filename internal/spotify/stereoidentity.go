package spotify

// What a stereo pair looks like in the Spotify app, and why STR got it wrong.
//
// Reported on #976: a pair called "Grace pair (L+R)" shows up in the Spotify
// device list as the pair under its own name when the entry comes from the Bose
// firmware, and as its two halves, "Bose (Grace left) (STR)" and "Bose (Grace
// right) (STR)", when the entries come from STR. The owner then has to guess
// which half to pick, and picking either one is wrong: half a stereo pair is not
// a thing anybody wants to play to.
//
// The cause is one line of history. The engine runs per speaker and takes its
// Connect name straight from that speaker's own friendly name, so nothing on
// that path has ever known a pair exists. Everything needed to know it is
// already in the agent: the zone store says whether this box is in a stereo pair
// and whether it leads it, and the pair's display name lives in the marge group
// record, shared with the Bose app, which is why a rename in one shows up in the
// other.
//
// So while a pair is formed:
//
//   - the MASTER advertises the PAIR's name, and
//   - the FOLLOWER advertises nothing at all.
//
// The follower's engine is idle while paired anyway: the master's engine plays
// and the firmware distributes the audio to the other half. Its Connect entry is
// therefore not a feature that is being removed, it is an entry that could only
// ever have been picked by mistake.
//
// The follower's engine is SUSPENDED rather than reconfigured because the engine
// cannot be asked to stay quiet: zeroconf_enabled exists in go-librespot's
// config, and with credentials.type "zeroconf" the daemon forces it back to true
// before it is ever read (daemon/app.go). Checked in the fork rather than
// assumed.

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// StereoRole is what this speaker is inside a stereo pair.
type StereoRole int

const (
	// StereoNone: not in a stereo pair. A multiroom zone is not a stereo pair
	// and is deliberately not covered: its members stay individually pickable,
	// which is what a multiroom group is for.
	StereoNone StereoRole = iota
	// StereoMaster: in a pair and leading it.
	StereoMaster
	// StereoFollower: in a pair and following.
	StereoFollower
)

// StereoFn reports this speaker's place in a stereo pair and the pair's display
// name. Supplied by the agent, which owns the zone store and the marge group
// record; nil means "no idea", which is treated as no pair.
type StereoFn func() (role StereoRole, pairName string)

// SetStereoFn installs the reporter. Called once at startup.
func (m *Manager) SetStereoFn(fn StereoFn) {
	m.mu.Lock()
	m.stereoFn = fn
	m.mu.Unlock()
}

// stereoIdentity is the current answer, with the nil case folded in.
func (m *Manager) stereoIdentity() (StereoRole, string) {
	m.mu.Lock()
	fn := m.stereoFn
	m.mu.Unlock()
	if fn == nil {
		return StereoNone, ""
	}
	role, name := fn()
	if role == StereoMaster && name == "" {
		// A pair with no name cannot be advertised as one, and inventing a name
		// would put a label in the Spotify app that appears nowhere else. Fall
		// back to this speaker's own name, which is what happened before.
		return StereoNone, ""
	}
	return role, name
}

// suspendedForStereo reports whether this speaker should keep its engine quiet
// because it is the following half of a pair.
func (m *Manager) suspendedForStereo() bool {
	role, _ := m.stereoIdentity()
	return role == StereoFollower
}

// connectName is the name to advertise to Spotify: the pair's name on a stereo
// master, this speaker's own name otherwise.
func (m *Manager) connectName(boxName string) string {
	if role, pair := m.stereoIdentity(); role == StereoMaster {
		return pair
	}
	return boxName
}

// stereoWatchInterval is how often the pair state is re-read. It is an in-memory
// read of state the agent already holds, so it costs the box nothing, and the
// config is rewritten and the engine restarted ONLY on an actual change: a pair
// forms or dissolves a few times in a speaker's life, not every thirty seconds.
//
// That distinction is the whole reason this is a separate watcher rather than a
// branch inside watchDeviceName, which is disabled precisely because it restarted
// the engine on transient answers and churned the box.
const stereoWatchInterval = 20 * time.Second

// watchStereoIdentity notices a pair forming or dissolving and makes the engine
// match: the master's advert is renamed to the pair, the follower's is taken
// down, and both come back when the pair is gone.
func (m *Manager) watchStereoIdentity(ctx context.Context) {
	t := time.NewTicker(stereoWatchInterval)
	defer t.Stop()
	last, lastName := m.stereoIdentity()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		role, name := m.stereoIdentity()
		if role == last && name == lastName {
			continue
		}
		// Never mid-track. A rename is a restart, and a pair that forms while
		// somebody is listening must not cut the music off; the next tick picks
		// it up, and a pair is not formed in a hurry.
		m.mu.Lock()
		streaming := m.sink != nil
		restart := m.runCancel
		m.mu.Unlock()
		if streaming {
			continue
		}
		last, lastName = role, name
		switch role {
		case StereoFollower:
			m.logger.Info("spotify: this speaker is the following half of a stereo pair, taking its own Connect entry down",
				"pair", name)
		case StereoMaster:
			m.logger.Info("spotify: advertising the stereo pair to Spotify under its own name instead of this speaker's",
				"pair", name)
		default:
			m.logger.Info("spotify: no longer in a stereo pair, advertising this speaker again")
		}
		if role != StereoFollower {
			if err := m.rewriteConfigForStereo(ctx); err != nil {
				m.logger.Warn("spotify: could not rewrite the config for the stereo change", "err", err)
				continue
			}
		}
		// Either way the engine has to come down: to pick up the new name, or to
		// stop advertising at all. The supervisor decides whether to start it
		// again, which is where suspendedForStereo is read.
		if restart != nil {
			restart()
		}
	}
}

// rewriteConfigForStereo writes config.yml with the name the current role calls
// for.
func (m *Manager) rewriteConfigForStereo(ctx context.Context) error {
	name, vol := m.boxNameAndVolume(ctx)
	if name == "" {
		m.mu.Lock()
		name = m.name
		m.mu.Unlock()
	}
	return os.WriteFile(filepath.Join(m.configDir, "config.yml"),
		[]byte(m.configYAML(name, vol)), 0o644)
}

// waitWhileStereoFollower parks the supervisor while this speaker is the
// following half of a pair. It reports false only when the agent is shutting
// down, so the caller can tell "carry on" from "give up".
//
// It logs once on the way in and once on the way out, never on the poll: this
// runs on every speaker in every household, and a line every few seconds is
// NAND wear for nothing.
func (m *Manager) waitWhileStereoFollower(ctx context.Context) bool {
	if !m.suspendedForStereo() {
		return true
	}
	_, pair := m.stereoIdentity()
	m.logger.Info("spotify: engine idle while this speaker follows a stereo pair", "pair", pair)
	t := time.NewTicker(stereoWatchInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		}
		if !m.suspendedForStereo() {
			m.logger.Info("spotify: no longer following a stereo pair, starting the engine again")
			return true
		}
	}
}
