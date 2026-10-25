// shell2/nav.js: the sidebar of the UI 2.0 preview, as data.
//
// Every one of today's eight tabs has a fixed place here (docs/UI-2.0.md,
// "Navigation, current and new"), so nothing disappears when the preview is
// on. Each entry switches to the SAME view the tab does; only the frame
// around it is new.
//
// Phase 1 keeps two entries flat that will later live one level down: Groups
// and Add speaker belong under Speakers (phases 3 and 4). Until then they use
// the tab's own words, so the preview adds no wording that is about to change
// again. Help and info arrives with phase 5.

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
        Object.freeze({ view: 'settings', labelKey: 'nav.speakerSettings', badgeId: 'ui2SpeakersBadge' }),
        // Interim, flat until phase 3 (Speakers > Groups).
        Object.freeze({ view: 'multiroom', labelKey: 'nav.multiroom', badgeId: 'ui2GroupsBadge', interim: true }),
        // Interim, flat until phase 4 (Speakers > Add speaker).
        Object.freeze({ view: 'setup', labelKey: 'nav.setupStick', interim: true }),
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

// navLabelKeys lists every i18n key the sidebar renders.
export function navLabelKeys() {
  return [...NAV.groups.map((g) => g.labelKey), ...navItems().map((i) => i.labelKey)];
}
