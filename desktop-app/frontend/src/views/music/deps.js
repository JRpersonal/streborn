// views/music/deps.js: the main.js-local helpers the Music modules call.
//
// Same pattern as views/library.js and views/recent.js: a view never imports
// main.js (which would create a cycle), so main.js hands these in through
// initMusicView next to the other init*View calls, and the modules call them
// as deps.<name>. The defaults are no-ops so a module stays importable (and
// vitest can read it) before main.js has run.

export let deps = {
  action() {},
  checkAppUpdate() {},
  discoverBoxes() {},
  doRefilter() {},
  queueAction() {},
  refreshStatus() {},
  renderNowPlayingBar() {},
  updateFilterIndicators() {},
};

export function initMusicView(d) {
  deps = { ...deps, ...d };
}
