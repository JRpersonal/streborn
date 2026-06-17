// state.js — central application state plus localStorage helpers.
//
// Imported from practically every module; always import as `{ state }`
// so that mutations land on the single shared instance rather than a
// copy.

export const state = {
  view: 'box',
  boxes: [],
  currentBox: null,
  settingsBox: null,   // The box whose settings are currently being edited
  presets: [],
  boxPresets: [],      // the box's OWN presets (incl. foreign sources like Deezer)
  boxSnapshot: null,   // agent's pre-takeover snapshot (warns about lost cloud sources like Deezer)
  searchResults: [],
  drives: [],
  selectedDrive: null,
  // Set right after a successful prepare+eject. While set,
  // refreshDrives() hides that path from the list until it either
  // disappears (physical pull) or comes back with a valid FAT32
  // mount. Prevents the wizard re-rendering the ejected stick as
  // "unknown format". Cleared by the "search again" button.
  justEjectedPath: null,
  appInfo: null,
  nowLocation: '',     // current stream URL from now_playing
  nowName: '',         // current itemName from now_playing
  nowPlayState: '',    // current PlayState
  nowIcon: '',         // last known station logo (favicon)
  nowUUID: '',         // last radio-browser UUID
  optimisticUntil: 0,  // timestamp until which refreshStatus trusts our optimistic state over the box
  presetErrors: {},    // slot → last error message (rendered red)
  searchOrder: 'votes',
  // searchCountry is the user's pinned country filter on the Music
  // tab. We persist the last pick across app restarts so the choice
  // is preserved (issue #86) and we do NOT bias new installs to a
  // single country: empty means "all countries" until the stick
  // region pull (loadStickRegion) sets a sensible default OR the
  // user manually picks one.
  searchCountry: loadSearchCountry(),
  searchLang: '',
  searchTag: '',       // active genre chip
  searchOnlyOK: true,
  searchOnlyBose: true,
  searchOffset: 0,
  searchLastMode: 'top', // "top" or "search" — for "load more"
  searchLastQuery: '',
  tags: [],            // cache of top tags for chips
  languages: [],       // cache of languages
  // Pending names: box ID -> { name, until } — after a local rename
  // we override every mDNS entry with this for up to 90 s. Long enough
  // until the stick re-announces its TXT record.
  pendingNames: {},
  // Music tab volume slider busy flag + grace period — prevents the
  // 2 s auto refresh in refreshStatus from yanking the slider thumb
  // out from under the user while they are dragging. Initialized
  // here so every module starts from a consistent value; UI code in
  // views/playback.js flips it at runtime.
  musicVolBusy: false,
  musicVolUntil: 0,
  // showMoreGenres: user clicked "more" on the genre chip row
  showMoreGenres: false,
  // otaInProgress is set true at the start of doBoxUpdate (in
  // main.js) and reset when the OTA verify poll exits. The SSH
  // recommendation banner reads it: while an OTA is in flight, the
  // box reboots through a window where SSH is open but the user
  // must NOT reboot manually (it would interrupt the agent restart
  // and may leave the box in a half-flashed state). Without this
  // flag the banner appears with its prominent "Reboot now" button
  // exactly during the OTA reboot window, tempting users into the
  // exactly wrong action.
  otaInProgress: false,
  // setupTarget is the speaker the USB-stick prep flow is currently
  // bound to. Set by the user via the target picker at the top of
  // the Setup view; cleared on view-switch only when discoverBoxes
  // confirms the previously selected box is no longer reachable
  // (otherwise a brief mDNS gap would keep yanking the choice from
  // the user). Shape:
  //   { kind: 'str' | 'stock' | 'factory-reset', box: BoxInfo | null }
  // box is null only for kind === 'factory-reset'. Without an
  // explicit target the wizard refuses to start so users do not
  // accidentally prepare a stick "into the void" and then have the
  // install step land on the wrong speaker.
  setupTarget: null,
};

// Persistent box selection: deviceID in localStorage, reloaded after
// app restart so the previously controlled box stays focused.
export function loadLastBox() {
  try { return localStorage.getItem('lastBoxDeviceID') || null; } catch { return null; }
}
export function saveLastBox(id) {
  try { localStorage.setItem('lastBoxDeviceID', id || ''); } catch {}
}

// Persistent Music-tab country filter (issue #86). Stored as ISO-3166
// alpha-2; empty string means "all countries". Hoisted above `state`
// so the initial state literal can call loadSearchCountry().
export function loadSearchCountry() {
  try { return localStorage.getItem('searchCountry') || ''; } catch { return ''; }
}
export function saveSearchCountry(cc) {
  try { localStorage.setItem('searchCountry', cc || ''); } catch {}
}

// isRoutableHost returns true if a string looks like a host we could
// reasonably reach over the local network. Filters out:
//   - empty strings
//   - 203.0.113/24 (RFC 5737 TEST-NET-3, the Bose box's USB gadget
//     interface — announced by mDNS but never routable from the LAN)
//   - 0.0.0.0 / 255.255.255.255 and other obviously invalid values
//   - 127/8 loopback
// Hostnames (containing a dot but no all-digit segments, e.g.
// "rhino.local") are passed through.
function isRoutableHost(h) {
  if (!h || typeof h !== 'string') return false;
  if (h.startsWith('203.0.113.')) return false;
  if (h.startsWith('127.')) return false;
  if (h === '0.0.0.0' || h === '255.255.255.255') return false;
  return true;
}

// Persistent cache of the last seen box list. Lets the app render
// the box selector immediately on launch or tab switch without
// waiting 4 seconds for mDNS. discoverBoxes() refreshes in the
// background and overwrites.
//
// Filters poisoned cache entries on read. An older build of the app
// could have written a box with host=203.0.113.x (USB gadget IP)
// before the pickReachableIP filter existed; without filtering here
// the bad entry survives every relaunch and breaks the volume slider
// and preset playback until the next successful discovery overwrite.
export function loadCachedBoxes() {
  try {
    const raw = localStorage.getItem('cachedBoxes');
    if (!raw) return [];
    const arr = JSON.parse(raw);
    if (!Array.isArray(arr)) return [];
    return arr.filter(b => b && isRoutableHost(b.host));
  } catch { return []; }
}
export function saveCachedBoxes(list) {
  try {
    const clean = (list || []).filter(b => b && isRoutableHost(b.host));
    localStorage.setItem('cachedBoxes', JSON.stringify(clean));
  } catch {}
}
