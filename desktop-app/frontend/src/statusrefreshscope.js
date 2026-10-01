// How much a status poll is allowed to do, by which tab is open.
//
// The status bar at the top, with the track title and the progress bar, hangs
// off boxControls, which is shown whenever a speaker is selected. So it is on
// screen in every tab. The status poll, however, used to return immediately
// unless the box tab was the open one, and the bar then sat frozen: no play
// state, no track position, and the progress bar hid itself because its last
// reading was never refreshed.
//
// That is where it hurt most. Single tracks get started from the Library and
// the Recently-played tabs, and the user stays there to pick the next one, so
// the bar was dead exactly when it was being watched. The phone remote has one
// screen and therefore never skipped a tick, which is why it showed a progress
// bar for the same track and the desktop did not (#845).
//
// The other half of the poll stays gated. The queue transport, the preset tiles
// and the volume slider belong to controls that are not on screen outside the
// box tab, and every one of these ticks is a request to a speaker whose NAND
// and CPU nobody can replace.

// statusTickScope says what one poll may refresh.
//
//   bar      the status line and the track position, on screen in every tab
//   controls the queue, the preset tiles and the volume slider
export function statusTickScope({ hasBox, view }) {
  if (!hasBox) return { bar: false, controls: false };
  return { bar: true, controls: view === 'box' };
}
