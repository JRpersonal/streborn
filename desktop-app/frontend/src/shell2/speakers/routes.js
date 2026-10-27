// shell2/speakers/routes.js: the routes of the UI 2.0 Speakers area, as data
// (phase 3, #1287, docs/UI-2.0.md "Navigation, current and new").
//
// The Speakers area is one sidebar entry with four places under it. Three of
// them ARE the views the classic tabs open, so their lifecycle (the settings
// load and reconnect loop, the Multi-room live poll, the setup drive scan)
// stays exactly where it is and works the same in both layouts; only the
// list is a view of its own, and it exists only in the preview:
//
//   route     view (state.view)   what it shows
//   list      speakers            every speaker with its state (list.js)
//   speaker   settings            one speaker, five tabs (speaker.js)
//   groups    multiroom           stereo pairs and fixed groups (groups.js)
//   add       setup               the existing setup view (the wizard is phase 4)
//
// Who plays along right now is NOT a route: that stays in the sidebar speaker
// list ("+" and "-", phase 1). There is no output popover and no room switcher.
//
// Pure: no DOM, so the nav data (nav.js) and vitest can import it.

export const SPEAKER_ROUTES = Object.freeze(['list', 'speaker', 'groups', 'add']);

export const ROUTE_VIEW = Object.freeze({
  list: 'speakers',
  speaker: 'settings',
  groups: 'multiroom',
  add: 'setup',
});

// Every view the Speakers entry of the sidebar stands for, the list first.
export const SPEAKERS_AREA_VIEWS = Object.freeze(SPEAKER_ROUTES.map((r) => ROUTE_VIEW[r]));

// The five tabs of one speaker, in the order they are shown.
export const SPEAKER_TABS = Object.freeze(['overview', 'sound', 'network', 'keys', 'maintenance']);

export const SPEAKER_TAB_LABEL_KEYS = Object.freeze({
  overview: 'ui2.speakers.tab.overview',
  sound: 'ui2.speakers.tab.sound',
  network: 'ui2.speakers.tab.network',
  keys: 'ui2.speakers.tab.keys',
  maintenance: 'ui2.speakers.tab.maintenance',
});

// The places of the area's own navigation row, in order. "speaker" has no
// entry of its own: a speaker is opened from the list (or follows the sidebar).
export const SUBNAV = Object.freeze([
  Object.freeze({ route: 'list', labelKey: 'ui2.speakers.all' }),
  Object.freeze({ route: 'groups', labelKey: 'ui2.speakers.groups', badgeId: 'ui2GroupsBadge' }),
  Object.freeze({ route: 'add', labelKey: 'ui2.speakers.add' }),
]);

// routeOfView is the Speakers route a view belongs to, or '' outside the area.
export function routeOfView(view) {
  for (const r of SPEAKER_ROUTES) if (ROUTE_VIEW[r] === view) return r;
  return '';
}

export function inSpeakersArea(view) {
  return routeOfView(view) !== '';
}

// normalizeTab keeps a tab name to one of the five, the first by default.
export function normalizeTab(tab) {
  return SPEAKER_TABS.includes(tab) ? tab : SPEAKER_TABS[0];
}

// tabIndexFor is where a key press moves the focus in a tab row of n tabs
// (WAI-ARIA tabs pattern: the arrows wrap, Home and End jump), or -1 for a key
// the row does not handle. rtl swaps the two arrows.
export function tabIndexFor(key, at, n, rtl = false) {
  if (n <= 0) return -1;
  const next = rtl ? 'ArrowLeft' : 'ArrowRight';
  const prev = rtl ? 'ArrowRight' : 'ArrowLeft';
  if (key === next) return (at + 1) % n;
  if (key === prev) return (at - 1 + n) % n;
  if (key === 'Home') return 0;
  if (key === 'End') return n - 1;
  return -1;
}
