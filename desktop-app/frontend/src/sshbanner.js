// Whether the "SSH is open on this speaker" banner belongs on screen.
//
// The banner is ONE element at the top of the window and its text says "this
// speaker", so every verdict it shows is a statement about whichever speaker is
// selected right now. That is what made it lie on 2026-10-02: it reported SSH
// open on a speaker whose agent answered running:false, persistent:false,
// boseMarker:false, with port 22 refusing connections.
//
// Three ways the old code left a verdict standing that it could no longer
// support, all of them the same mistake: a path that returns without writing
// the banner's visibility.
//
//   - the answer came back non-ok, and it returned
//   - the speaker could not be reached at all, and the catch swallowed it
//   - the speaker was switched while the request was in flight, so the PREVIOUS
//     speaker's answer arrived and was painted under the new speaker's name
//
// A banner that cannot be confirmed must come down. Not knowing whether SSH is
// open is not the same as knowing it is, and the one that costs the user
// something is the false alarm: it asks them to reboot a speaker for no reason.

// sshBannerShow is the whole decision, so it can be exercised without a DOM.
//
// reachable false means the speaker did not answer, or answered something that
// was not a status: nothing is known, so nothing is claimed.
export function sshBannerShow({ reachable, status, dismissed }) {
  if (!reachable) return false;
  if (!status || !status.sshOpen) return false;
  // Deliberately kept open across reboots via a NAND marker. The banner's whole
  // point is "pull the stick and reboot to close it", which does not apply and
  // cannot be acted on here (#381/#385); Speaker Settings says the right thing.
  if (status.sshPersistent) return false;
  // Dismissed per speaker: a reminder somebody has read and understood should
  // not reappear on every app start.
  return !dismissed;
}
