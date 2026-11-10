// views/setup/shared.js: the two small things more than one setup module
// reads: whether this computer is a Mac (the Wi-Fi password is not read from
// the keychain there), and the update hold on the speaker the network-install
// card is for. Extracted from views/setup.js.

import { state } from '../../state.js';
import { holdFor } from '../../installhold.js';

// macOS gates the System keychain behind an admin prompt for every
// `security find-generic-password -ga`. Auto-firing the password
// fetch at app startup pops the prompt every single launch (#88).
// We still want auto-fill on Windows (netsh -> no prompt for
// user-saved profiles) and Linux (nmcli -> no prompt). On macOS,
// the SSID gets auto-selected but the password field stays empty
// until the user clicks the explicit fill-from-keychain button.
// Re-derived locally (the same pure check main.js uses) so the view does not
// need an injected helper for it.
export const isMacOS = typeof navigator !== 'undefined' && /Mac OS X|Macintosh/.test(navigator.userAgent);

// currentHeroHold is the hold on the speaker the network-install card is for,
// or null.
export function currentHeroHold() {
  const sel = state.setupTarget;
  return sel && sel.kind === 'stock' && sel.box ? holdFor(state.installHolds, sel.box.host) : null;
}
