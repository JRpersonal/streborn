// shell2/nav.js: the navigation of the UI 2.0 preview, as data.
//
// Every one of today's eight tabs has a fixed place here (docs/UI-2.0.md,
// "Navigation, current and new"), so nothing disappears when the preview is
// on. Each entry switches to the SAME view the tab does; only the frame
// around it is new.
//
// Since 2026-11-02 the entries are a row of tabs above the page (markup.js,
// #ui2Tabs) and the sidebar belongs to the speakers alone: stacked under the
// speaker list the entries needed a second scroll box and were already cut
// off in the default window. The places to listen from come first, then
// Speakers; Wishes and Help and info follow small and muted at the far end of
// the row.
//
// Phase 3 folds three tabs into one entry, Speakers: Speaker settings is
// Speakers > one speaker, Multi-room is Speakers > Groups (who plays along
// right now stays in the sidebar speaker list) and Set up speaker is
// Speakers > Add speaker. The entry stands for all four views of the area
// (area: speakers/routes.js) and is marked current on each of them.
//
// Phase 5 adds Help and info after Wishes: the one place for help, wishes,
// community, support and about (shell2/help/help.js). It is a view of the
// preview only; the classic layout keeps its footer, its donate rail and its
// Wishes tab.

import { SPEAKERS_AREA_VIEWS } from './speakers/routes.js';

export const NAV = Object.freeze({
  main: Object.freeze([
    Object.freeze({ view: 'box', labelKey: 'ui2.nav.radio' }),
    Object.freeze({ view: 'library', labelKey: 'nav.library' }),
    Object.freeze({ view: 'recent', labelKey: 'nav.recent' }),
    Object.freeze({ view: 'spotify', labelKey: 'nav.spotify' }),
    // The badge counts the speakers due an update.
    Object.freeze({ view: 'speakers', labelKey: 'ui2.nav.speakers', badgeId: 'ui2SpeakersBadge', area: SPEAKERS_AREA_VIEWS }),
  ]),
  // Small and muted at the far end of the row: Wishes and Help and info stay
  // in sight without competing with the places people listen from.
  small: Object.freeze([
    Object.freeze({ view: 'wishes', labelKey: 'nav.wishes', small: true, id: 'ui2NavWishes' }),
    Object.freeze({ view: 'help', labelKey: 'ui2.nav.help', small: true, id: 'ui2NavHelp' }),
  ]),
});

// navItems lists every entry in tab order, start to end.
export function navItems() {
  return [...NAV.main, ...NAV.small];
}

// navEntryFor is the entry that stands for a view: its own, or the entry
// whose area holds it (settings, multiroom and setup are Speakers).
export function navEntryFor(view) {
  return navItems().find((i) => i.view === view || (i.area || []).includes(view)) || null;
}

// navLabelKeys lists every i18n key the tab row renders.
export function navLabelKeys() {
  return navItems().map((i) => i.labelKey);
}

// navStep is where Left / Right (Up / Down kept as aliases) move from the
// entry at index at in a row of n, Home and End to the ends; -1 for any other
// key. The row reads from the right in a right-to-left language, so there
// Left is the next entry.
export function navStep(key, at, n, rtl = false) {
  if (!(n > 0) || at < 0) return -1;
  const fwd = rtl ? 'ArrowLeft' : 'ArrowRight';
  const back = rtl ? 'ArrowRight' : 'ArrowLeft';
  if (key === fwd || key === 'ArrowDown') return Math.min(n - 1, at + 1);
  if (key === back || key === 'ArrowUp') return Math.max(0, at - 1);
  if (key === 'Home') return 0;
  if (key === 'End') return n - 1;
  return -1;
}
