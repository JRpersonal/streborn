// shell2/nav.js: the sidebar of the UI 2.0 preview, as data.
//
// Every one of today's eight tabs has a fixed place here (docs/UI-2.0.md,
// "Navigation, current and new"), so nothing disappears when the preview is
// on. Each entry switches to the SAME view the tab does; only the frame
// around it is new.
//
// Phase 3 folds three tabs into one entry, Speakers: Speaker settings is
// Speakers > one speaker, Multi-room is Speakers > Groups (who plays along
// right now stays in the sidebar speaker list) and Set up speaker is
// Speakers > Add speaker. The entry stands for all four views of the area
// (area: speakers/routes.js) and is marked current on each of them. Help and
// info arrives with phase 5.

import { SPEAKERS_AREA_VIEWS } from './speakers/routes.js';

export const NAV = Object.freeze({
  groups: Object.freeze([
    Object.freeze({
      id: 'listen',
      labelKey: 'ui2.nav.listen',
      items: Object.freeze([
        Object.freeze({ view: 'box', labelKey: 'ui2.nav.radio' }),
        Object.freeze({ view: 'library', labelKey: 'nav.library' }),
        Object.freeze({ view: 'recent', labelKey: 'nav.recent' }),
        Object.freeze({ view: 'spotify', labelKey: 'nav.spotify' }),
      ]),
    }),
    Object.freeze({
      id: 'home',
      labelKey: 'ui2.nav.home',
      items: Object.freeze([
        // The list first; the badge counts the speakers due an update.
        Object.freeze({ view: 'speakers', labelKey: 'ui2.nav.speakers', badgeId: 'ui2SpeakersBadge', area: SPEAKERS_AREA_VIEWS }),
      ]),
    }),
  ]),
  // Small, at the bottom of the sidebar: Wishes stay visible without
  // competing with the places people listen from.
  bottom: Object.freeze([
    Object.freeze({ view: 'wishes', labelKey: 'nav.wishes', small: true, id: 'ui2NavWishes' }),
  ]),
});

// navItems lists every entry in sidebar order, top to bottom.
export function navItems() {
  return [...NAV.groups.flatMap((g) => g.items), ...NAV.bottom];
}

// navEntryFor is the sidebar entry that stands for a view: its own, or the
// entry whose area holds it (settings, multiroom and setup are Speakers).
export function navEntryFor(view) {
  return navItems().find((i) => i.view === view || (i.area || []).includes(view)) || null;
}

// navLabelKeys lists every i18n key the sidebar renders.
export function navLabelKeys() {
  return [...NAV.groups.map((g) => g.labelKey), ...navItems().map((i) => i.labelKey)];
}
