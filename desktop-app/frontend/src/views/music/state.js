// views/music/state.js: the few mutable values more than one Music module
// writes. An imported binding is read-only, so a flag one module sets and
// another resets cannot be a plain exported `let`; it lives here instead.
// Everything else the modules share is read-only (functions, Maps, consts)
// and is imported directly; the app-wide state stays in src/state.js.

export const mstate = {
  // When the preset list was last re-read from the speaker (refreshPresetsIfChanged
  // in presets.js). refreshQueue zeroes it when the agent reports a new preset
  // revision, so the next status tick re-reads at once instead of waiting out
  // PRESET_RECHECK_MS.
  _lastPresetCheck: 0,
};
