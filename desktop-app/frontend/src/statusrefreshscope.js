// How much a status poll is allowed to do, by which tab is open.
//
// The status bar with the track title and the progress bar lives in the Music
// tab (#view-box, inside boxControls). With the UI 2.0 preview on it moves into
// the persistent player and is then on screen in every view. The status poll
// used to return immediately unless the box tab was the open one, and the bar
// then sat frozen: no play state, no track position, and the progress bar hid
// itself because its last reading was never refreshed.
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
//   bar      the status line and the track position
//   controls the preset tiles, which only the Music tab shows
//   player   the volume slider and the queue transport: the Music tab's, or
//            the UI 2.0 persistent player's in any other view (not in Setup,
//            and not while a speaker update runs, when nothing should poll a
//            speaker that is about to restart)
export function statusTickScope({ hasBox, view, persistentPlayer = false, otaInProgress = false }) {
  if (!hasBox) return { bar: false, controls: false, player: false };
  return {
    bar: true,
    controls: view === 'box',
    player: playerOnScreen({ view, persistentPlayer, otaInProgress }),
  };
}

// playerOnScreen says whether the volume and transport controls are visible,
// without asking whether a speaker is selected: the volume sync guards that
// itself. With the preview off it is exactly "the Music tab is open".
export function playerOnScreen({ view, persistentPlayer = false, otaInProgress = false }) {
  if (view === 'box') return true;
  return !!persistentPlayer && view !== 'setup' && !otaInProgress;
}
