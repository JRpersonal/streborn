// views/music/deps.js: the main.js-local helpers the Music modules call.
//
// Same pattern as views/library.js and views/recent.js: a view never imports
// main.js (which would create a cycle), so main.js hands these in through
// initMusicView next to the other init*View calls, and the modules call them
// as deps.<name>. The defaults are no-ops so a module stays importable (and
// vitest can read it) before main.js has run.

export let deps = {
  applyTrackScroll() {},
  // The refresh button also re-asks whether a newer app exists (wiring.js).
  checkAppUpdate() {},
  // The status poll re-checks the SSH reminder banner on every tick (status.js).
  checkSshBanner() {},
  decodeProxyUrl() {},
  // The refresh button, and the status poll when a speaker has stopped
  // answering (wiring.js, status.js).
  discoverBoxes() {},
  doRefilter() {},
  isMeasurableStreamPreset() {},
  // The world map invite rides the status poll (status.js).
  maybeInviteWorldMap() {},
  refreshPresetsIfChanged() {},
  renderPresets() {},
  setPresetIfUnchanged() {},
  updateFilterIndicators() {},
};

export function initMusicView(d) {
  deps = { ...deps, ...d };
}
