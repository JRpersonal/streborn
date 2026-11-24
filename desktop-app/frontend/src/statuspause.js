// statuspause.js: whether the status poll of the chosen speaker may skip a round.
//
// The poll (main.js, statusPollLoop) asks the chosen speaker for its status,
// volume, queue and track position every 15 s even when nothing is playing,
// and the track position read goes straight into the firmware's UPnP renderer.
// With the window minimised nobody sees any of it, yet an app left open over
// night kept asking all night, and every one of those reads is a chance to
// keep the speaker out of deep standby (#119 family). So the poll sleeps while
// the window is hidden or minimised and catches up the moment it is shown
// again (main.js refreshes on visibilitychange and focus).
//
// Two signals, because neither is enough alone: the page's own visibility,
// and the Wails window state, since a minimised WebView2 window does not
// always report the page as hidden.

import { WindowIsMinimised } from './api.js';

// statusPollPaused is the decision on its own, for the tests.
export function statusPollPaused({ visibility, minimised }) {
  return visibility === 'hidden' || minimised === true;
}

// windowHidden reads both signals. A runtime that cannot answer (the browser
// harness, an older Wails) counts as shown, so the poll keeps running there.
export async function windowHidden() {
  let minimised = false;
  try {
    minimised = (await WindowIsMinimised()) === true;
  } catch {
    minimised = false;
  }
  const visibility = typeof document !== 'undefined' ? document.visibilityState : 'visible';
  return statusPollPaused({ visibility, minimised });
}
