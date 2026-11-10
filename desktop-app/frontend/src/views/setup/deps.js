// views/setup/deps.js: what the setup modules call but must not import.
//
// Same pattern as views/music/deps.js and update/deps.js: a module never
// imports main.js (which would create a cycle), so main.js hands its helpers in
// through initSetupView next to the other init*View calls, and the modules call
// them as deps.<name>. The defaults are no-ops so a module stays importable
// (and vitest can read it) before main.js has run.
//
// One entry does not come from main.js: mountSetupShell stays in views/setup.js
// (the lazy shell mount, see the note there), and that file puts it in here
// itself, because the picker and the stick wizard both call it and neither may
// import the file that imports them.

// Injected main.js helpers (see initSetupView). These stay in main.js because
// they are shared across views; the setup code calls them as deps.<name>.
export let deps = {
  switchView: () => {},
  discoverBoxes: async () => {},
  doBoxUpdate: async () => {},
  getRoomNames: () => [],
  // celebrateProvision(box): fire the community world-map pin invite right after a
  // box is successfully provisioned with STR (the most reliable "alive again"
  // moment). Wired in main.js; no-op default so the view never depends on it.
  celebrateProvision: () => {},
  // speakerPicked(box): focus this speaker across the tabs (it sets
  // state.settingsBox and state.currentBox), so leaving Setup for another tab
  // lands on the speaker the user just pointed at. It was already being called
  // from the target picker but was missing from this list, which made it look
  // optional; the no-op default keeps every call site safe either way.
  speakerPicked: () => {},
  // installHoldsChanged(): a speaker entered or left the update hold
  // (refreshInstallHolds); main.js repaints the speaker list, whose badge and
  // tooltip read the same holds.
  installHoldsChanged: () => {},
  // mountSetupShell(): build the view's static shell on first use. Not from
  // main.js: it stays in views/setup.js, which puts it in here itself (see the
  // note at the top of this file).
  mountSetupShell: () => {},
};
export function initSetupView(d) {
  deps = { ...deps, ...d };
}
