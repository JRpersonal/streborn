// banners/deps.js: the main.js-local helpers the banner modules call.
//
// Same pattern as views/music/deps.js and update/deps.js: a module never
// imports main.js (which would create a cycle), so main.js hands these in
// through initBanners next to the init*View calls, and the modules call them
// as deps.<name>. The defaults are no-ops so a module stays importable (and
// vitest can read it) before main.js has run.

export let deps = {
  // The speaker update card lists the speakers that are behind this app
  // (speakerupdate.js).
  boxNeedsUpdate() { return false; },
  // A restored cloud address, a removed rival mod: the speaker list is read
  // again (speaker.js, attention.js).
  discoverBoxes() {},
  // The speaker update card stays away while the app itself is out of date
  // (speakerupdate.js).
  speakerUpdateCardMuted() { return false; },
  // "Save Wi-Fi" and the single-speaker update open Speaker settings
  // (speaker.js, attention.js, speakerupdate.js).
  switchView() {},
};

export function initBanners(d) {
  deps = { ...deps, ...d };
}
