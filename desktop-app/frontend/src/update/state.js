// update/state.js: the two re-entrancy latches of the speaker update. Each is
// set by one runner and read by the other (and by the failure report, which
// asks whether an Update all run is showing), and an imported binding is
// read-only, so they cannot be plain exported `let`s; they live here instead
// (same reason as views/music/state.js). The app-wide OTA gate stays in
// src/state.js (state.otaInProgress).

export const ustate = {
  // Re-entrancy latch for the single-speaker update, set BEFORE the first await.
  //
  // state.otaInProgress is only raised after the stick gate and the Wi-Fi
  // preflight, two network round-trips further down, so a second click inside
  // that window sailed straight past the check at the top. Both runs then reached
  // the backend, the second was correctly refused, and the REFUSED one ran the
  // shared teardown and put a failure report on screen for the update that was
  // running fine (Sascha, SoundTouch 30, 2026-09-14). The whole-house path
  // learned this in August, see updateAllBusy; this one had not.
  boxUpdateBusy: false,
  // Re-entrancy latch of Update all; the reasoning is on updateAllBoxes
  // (all.js).
  updateAllBusy: false,
};
