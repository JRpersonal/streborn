// update/deps.js: the main.js-local helpers the speaker-update modules call.
//
// Same pattern as views/music/deps.js: a module never imports main.js (which
// would create a cycle), so main.js hands these in through initUpdateFlow next
// to the init*View calls, and the modules call them as deps.<name>. The
// defaults are no-ops so a module stays importable (and vitest can read it)
// before main.js has run.

export let deps = {
  // Update all asks of every reachable speaker whether it is behind (all.js).
  boxNeedsUpdate() { return false; },
  // Both runners re-run the per-speaker update check when the OTA gate goes up
  // and when it comes down (single.js, all.js).
  checkBoxUpdate() {},
  // Both runners re-check the SSH reminder banner around the OTA gate, which
  // suppresses it (single.js, all.js).
  checkSshBanner() {},
  // Both runners refresh the speaker list once the run is over (single.js,
  // all.js).
  discoverBoxes() {},
  // The single update shows the upload throughput next to the percentage
  // (single.js).
  fmtRate() { return ''; },
};

export function initUpdateFlow(d) {
  deps = { ...deps, ...d };
}
