// views/music/deps.js: the main.js-local helpers the Music modules call.
//
// Same pattern as views/library.js and views/recent.js: a view never imports
// main.js (which would create a cycle), so main.js hands these in through
// initMusicView next to the other init*View calls, and the modules call them
// as deps.<name>. The defaults are no-ops so a module stays importable (and
// vitest can read it) before main.js has run.

export let deps = {
  // The refresh button also re-asks whether a newer app exists (wiring.js).
  checkAppUpdate() {},
  // The status poll re-checks the SSH reminder banner on every tick (status.js).
  checkSshBanner() {},
  // The refresh button, and the status poll when a speaker has stopped
  // answering (wiring.js, status.js).
  discoverBoxes() {},
  // The world map invite rides the status poll (status.js).
  maybeInviteWorldMap() {},
  // The group panel re-reads the live zones after a member toggle (group.js).
  refreshMusicZones() {},
  // The group panel repaints the speaker picker's group frames (group.js).
  renderBoxSelect() {},
  // The "speaker has no internet" prompt hands the speaker to the settings
  // tab before switching there (search.js).
  speakerPickedInTab() {},
  // play() names a stale stereo pair in its error text (play.js).
  stereoStaleForError() {},
  // The "speaker has no internet" prompt opens the settings tab (search.js).
  switchView() {},
};

export function initMusicView(d) {
  deps = { ...deps, ...d };
}
