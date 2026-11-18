package spotify

import "time"

// /spotify/info during an account switch.
//
// A recall of a key another household account saved first swaps that account's
// login in and restarts the engine (switchAccountFrom), which takes about eight
// seconds. Until the new engine process is up, everything the reply says about
// refusals still describes the account being LEFT: premiumRequired and
// audioKeyRefused are that engine process's state, and the sticky
// accountRefused is looked up for the account last seen logged in. The apps
// turn those fields into the "free account" notice, so a Premium account's key
// that was about to play had that notice over it for the length of the switch
// (discussion #1947: the engine sat on a lapsed free account).
//
// No new state is kept for this. Two things the manager already tracks say a
// switch is under way:
//
//   - recallRestartAt, the restart a cross-account recall just made
//     (recallRestartedRecently, the window ServeOgg uses for the same recall).
//   - A recall in flight (recallUntil, or the display gate of recalldisplay.go)
//     while the logged-in account gates as free. Both recall gates refuse a key
//     that would play under a free account before anything is marked as a
//     recall (PremiumRequiredFor), so a recall that IS in flight over a free
//     account can only be a key of another account on its way to the switch.
//     This covers the seconds between the press and the restart, and an engine
//     that takes longer to come back than the restart window.
//
// A recall on the account that stays logged in changes nothing here: its
// refusal notice is about the account that plays, and stays up.

// refusalOfDepartingAccount reports whether the refusal fields of a
// /spotify/info reply would describe an account a recall is switching away
// from. displayPending is recallDisplayPendingLocked's answer, premiumRequired
// is PremiumRequired's; both are passed in because the reply needs them anyway.
func (m *Manager) refusalOfDepartingAccount(now time.Time, displayPending, premiumRequired bool) bool {
	if m.recallRestartedRecently() {
		return true
	}
	if !premiumRequired {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return displayPending || now.Before(m.recallUntil)
}
