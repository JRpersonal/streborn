// Tests for the pure decision helpers in utils.js.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { savePresetCase, spotifySaveContextURI, spotifyURIFromContainer, libraryPlayMatches, queueSaveFallsThrough, savePlayingFallsThrough, bassControlsDisabled, bassSliderProps } from './utils.js';
import { allSource, srcOf } from './test/source.js';

const FRESH_MS = 2 * 60 * 1000;
const NOW = 1_000_000_000;
const freshPlay = { url: 'http://radio.example.com/stream', at: NOW - 5_000 };
const stalePlay = { url: 'http://radio.example.com/stream', at: NOW - FRESH_MS - 1 };

describe('savePresetCase', () => {
  const orionLoc = '/station?data=native-descriptor-caller-decodes-it';

  it('classifies Spotify locations first, whatever the slot or app record says', () => {
    expect(savePresetCase('http://192.0.2.1:8888/spotify/stream-3.ogg', 3, freshPlay, NOW, FRESH_MS)).toBe('spotify');
    expect(savePresetCase('/playback/container/c3BvdGlmeQ==', null, null, NOW, FRESH_MS)).toBe('spotify');
  });

  // A fresh app play is authoritative for EVERY location shape (#836): the app
  // knows which station the user just picked, even when the box's now-playing
  // still lags on it (native preset switch #530, wake-resume race #252). Saving
  // the box's stale report captured an already-saved station and the dedup then
  // wiped the key it was on, so presets vanished one after another.
  it('prefers the app`s own fresh play record, whatever the box reports', () => {
    expect(savePresetCase('http://192.0.2.1:8888/stream/2', 2, freshPlay, NOW, FRESH_MS)).toBe('app-play'); // proxy slot
    expect(savePresetCase('http://radio.example.com/live.mp3', null, freshPlay, NOW, FRESH_MS)).toBe('app-play'); // non-proxy
    expect(savePresetCase(orionLoc, null, freshPlay, NOW, FRESH_MS)).toBe('app-play'); // native, no slot in location
  });

  it('copies the source preset when a proxy slot plays and the app record is stale or absent', () => {
    expect(savePresetCase('http://192.0.2.1:8888/stream/2', 2, stalePlay, NOW, FRESH_MS)).toBe('copy-slot');
    expect(savePresetCase('http://192.0.2.1:8888/stream/2', 2, null, NOW, FRESH_MS)).toBe('copy-slot');
    expect(savePresetCase('http://192.0.2.1:8888/stream/2', 2, { at: NOW }, NOW, FRESH_MS)).toBe('copy-slot'); // no url
  });

  it('saves the box-reported now-playing when there is no fresh app record and no slot', () => {
    expect(savePresetCase('http://radio.example.com/live.mp3', null, stalePlay, NOW, FRESH_MS)).toBe('direct');
    expect(savePresetCase(orionLoc, null, stalePlay, NOW, FRESH_MS)).toBe('direct');
    expect(savePresetCase('', null, null, NOW, FRESH_MS)).toBe('direct');
  });

  // A music-library folder plays as the speaker's queue, and the speaker only
  // reports the current track. Holding a key then has to store the FOLDER, the
  // preset the Library star button stores (#1030), not that one track.
  const liveQueue = { active: true, items: [{ title: 'Come Together' }, { title: 'Something' }], card: 'queue:uuid:abc:64$1' };
  const trackLoc = 'http://192.0.2.10:9000/disk/01.mp3';
  it('saves the folder while a library queue plays', () => {
    expect(savePresetCase(trackLoc, null, stalePlay, NOW, FRESH_MS, liveQueue)).toBe('queue');
    expect(savePresetCase(trackLoc, null, null, NOW, FRESH_MS, liveQueue)).toBe('queue');
    // A folder recalled from a key reaches the box as that key's proxy: still the folder.
    expect(savePresetCase('http://192.0.2.1:8888/stream/2', 2, null, NOW, FRESH_MS, liveQueue)).toBe('queue');
  });

  it('keeps Spotify and a fresh app play ahead of the queue', () => {
    expect(savePresetCase('http://192.0.2.1:8888/spotify/stream-3.ogg', 3, null, NOW, FRESH_MS, liveQueue)).toBe('spotify');
    expect(savePresetCase('http://radio.example.com/live.mp3', null, freshPlay, NOW, FRESH_MS, liveQueue)).toBe('app-play');
  });

  it('ignores a queue that is stopped, empty or unknown', () => {
    expect(savePresetCase(trackLoc, null, null, NOW, FRESH_MS, { active: false, items: liveQueue.items })).toBe('direct');
    expect(savePresetCase(trackLoc, null, null, NOW, FRESH_MS, { active: true, items: [] })).toBe('direct');
    expect(savePresetCase(trackLoc, null, null, NOW, FRESH_MS, null)).toBe('direct');
    expect(savePresetCase('http://192.0.2.1:8888/stream/2', 2, null, NOW, FRESH_MS, undefined)).toBe('copy-slot');
  });
});

// Holding a key while a single library track plays stored a plain stream
// without the media server, while the Library star stores the server (#1065).
describe('savePresetCase for a single library track', () => {
  const trackLoc = 'http://192.0.2.10:9000/disk/01.mp3';
  const lib = { url: trackLoc, name: 'Come Together', source: 'Synology', at: NOW - 600_000 };
  const relayLoc = 'http://192.0.2.1:8888/stream/raw?u=' + btoa(trackLoc).replace(/\+/g, '-').replace(/\//g, '_');

  it('matches only while the speaker plays the remembered URL', () => {
    expect(libraryPlayMatches(lib, trackLoc, trackLoc)).toBe(true);
    expect(libraryPlayMatches(lib, relayLoc, trackLoc)).toBe(true); // through the agent relay
    expect(libraryPlayMatches(lib, 'http://radio.example.com/live.mp3', 'http://radio.example.com/live.mp3')).toBe(false);
    expect(libraryPlayMatches(null, trackLoc, trackLoc)).toBe(false);
    expect(libraryPlayMatches({ name: 'x' }, trackLoc, trackLoc)).toBe(false);
  });

  it('saves the library track instead of the plain stream', () => {
    expect(savePresetCase(trackLoc, null, null, NOW, FRESH_MS, null, true)).toBe('library');
    expect(savePresetCase(trackLoc, null, stalePlay, NOW, FRESH_MS, null, true)).toBe('library');
    expect(savePresetCase(trackLoc, null, null, NOW, FRESH_MS, null, false)).toBe('direct');
  });

  it('leaves Spotify, a fresh app play and a playing folder ahead of it', () => {
    const liveQueue = { active: true, items: [{ title: 'Come Together' }] };
    expect(savePresetCase('http://192.0.2.1:8888/spotify/stream-3.ogg', 3, null, NOW, FRESH_MS, null, true)).toBe('spotify');
    expect(savePresetCase(trackLoc, null, freshPlay, NOW, FRESH_MS, null, true)).toBe('app-play');
    expect(savePresetCase(trackLoc, null, null, NOW, FRESH_MS, liveQueue, true)).toBe('queue');
    // The folder save then reports no-queue and the caller asks again without it.
    expect(savePresetCase(trackLoc, null, null, NOW, FRESH_MS, null, true)).toBe('library');
  });
});

describe('queueSaveFallsThrough', () => {
  it('falls back when no folder plays, the agent is too old, or the binding is missing', () => {
    expect(queueSaveFallsThrough('status 409: {"code":"no-queue","error":"No music-library folder is playing right now."}')).toBe(true);
    expect(queueSaveFallsThrough(new Error('status 404: 404 page not found'))).toBe(true);
    const missing = new Error('STR_MISSING_BINDING: SaveQueuePreset is not available in this build');
    missing.code = 'STR_MISSING_BINDING';
    expect(queueSaveFallsThrough(missing)).toBe(true);
  });

  it('reports every other failure', () => {
    expect(queueSaveFallsThrough('status 500: preset store write failed')).toBe(false);
    expect(queueSaveFallsThrough(new Error('dial tcp 192.0.2.1:8888: connection refused'))).toBe(false);
  });
});

describe('savePlayingFallsThrough', () => {
  it('falls back only when the agent or this build does not know the call', () => {
    expect(savePlayingFallsThrough(new Error('status 404: 404 page not found'))).toBe(true);
    expect(savePlayingFallsThrough('status 405: method not allowed')).toBe(true);
    const missing = new Error('STR_MISSING_BINDING: SavePlayingPreset is not available in this build');
    missing.code = 'STR_MISSING_BINDING';
    expect(savePlayingFallsThrough(missing)).toBe(true);
    // An agent without the call serves its phone page; the binding turns that into this.
    expect(savePlayingFallsThrough(new Error('status 404: save-playing not available on this agent'))).toBe(true);
  });

  it('falls back when the agent could not read the now-playing for a moment', () => {
    expect(savePlayingFallsThrough(new Error('status 502: {"code":"nowplaying-unreadable","error":"The speaker could not be asked what it is playing. Try again in a moment."}'))).toBe(true);
    expect(savePlayingFallsThrough('status 502: bad gateway')).toBe(true);
    expect(savePlayingFallsThrough('status 5020: odd')).toBe(false);
  });

  it('reports a refusal of the agent as the answer (#1467)', () => {
    expect(savePlayingFallsThrough('status 409: {"code":"already-on-slot","slot":2,"name":"Jazz"}')).toBe(false);
    expect(savePlayingFallsThrough('status 422: {"code":"nothing-saveable"}')).toBe(false);
    expect(savePlayingFallsThrough('status 4040: odd')).toBe(false);
    expect(savePlayingFallsThrough(new Error('dial tcp 192.0.2.1:8888: connection refused'))).toBe(false);
  });
});

// The bass slider and the "default" reset button next to it must read the SAME
// gate. Until 2026-08-23 only the slider was disabled on a speaker that reports
// bassAvailable=false, so the button stayed clickable and posted /bass to a
// device with no bass endpoint (Lifestyle 650, mail with photo 2026-08-22).
// The rendering itself is not testable here on purpose: the settings view
// writes innerHTML and the vitest environment is DOM-free, so the shared helper
// is where the gate gets pinned.
describe('bassControlsDisabled', () => {
  it('enables the controls only when the speaker reports bass capability', () => {
    expect(bassControlsDisabled({ available: true, min: -10, max: 10, default: 0 })).toBe(false);
  });
  it('disables them when the speaker says bass is not available', () => {
    expect(bassControlsDisabled({ available: false, min: 0, max: 0, default: 0 })).toBe(true);
  });
  it('disables them when availability is unknown or the reading is missing', () => {
    expect(bassControlsDisabled({})).toBe(true);
    expect(bassControlsDisabled(undefined)).toBe(true);
    expect(bassControlsDisabled(null)).toBe(true);
  });
  // The defect was never in the helper, it was one control skipping it. There
  // is no DOM to render into here, so the settings view's markup is read as
  // source and both controls are checked for the gate. This is the assertion
  // that fails if the reset button ever loses it again. Matched on the helper
  // NAME rather than a literal `bassControlsDisabled(bass)` call so renaming
  // the argument or hoisting the call into a local does not fail a view that
  // is still correct; what must never come back is a control with no gate.
  it('is applied to the slider AND the reset button in the settings view', () => {
    const src = allSource();
    const slider = src.match(/<input type="range" id="boxBass"[\s\S]*?\/>/);
    const resetBtn = src.match(/<button[^>]*id="boxBassReset"[\s\S]*?>/);
    expect(slider, 'the bass slider markup must still be findable').not.toBeNull();
    expect(resetBtn, 'the bass reset button markup must still be findable').not.toBeNull();
    expect(slider[0]).toContain('bassControlsDisabled');
    expect(resetBtn[0]).toContain('bassControlsDisabled');
  });
  // Greying the row without a word is what put the Lifestyle 650 owner in the
  // inbox: he could see that slider and button were dead, not why. The view
  // must therefore carry a reason string on the disabled branch, and it must
  // not be the how-to-use help, which is instructions for a control that
  // cannot move.
  it('the settings view explains the disabled row instead of showing the usage help', () => {
    const src = srcOf('renderBasicsSections');
    // Anchor on the slider's id, not on the heading key: the heading key is
    // what a section-name lookup elsewhere would use too.
    const start = src.indexOf('id="boxBass"');
    const section = src.slice(start, src.indexOf('settings-section', start + 1));
    expect(start, 'the bass section must still be findable').toBeGreaterThan(-1);
    expect(section).toContain('boxBassReset');
    expect(section).toContain('settingsView.bassUnavailable');
    // The reason replaces the help; both showing at once would tell a user
    // with no bass control how to use one.
    const help = section.indexOf('settingsView.bassHelp');
    const reason = section.indexOf('settingsView.bassUnavailable');
    expect(help, 'the usage help must still exist for speakers that DO have bass').toBeGreaterThan(-1);
    expect(section.slice(Math.min(help, reason), Math.max(help, reason)))
      .toMatch(/[?:]/); // the two sit on opposite sides of one conditional
  });
});

// The bass slider's range, step and position come from one shared mapping.
// The slider is relative to the speaker's default (0 = default); step exists
// for the capability-gated tone-controls route (the Lifestyle 650 case),
// whose write granularity can exceed 1. The agent maps that route's default
// to 0, so relative equals absolute there.
describe('bassSliderProps', () => {
  it('maps the classic reading relative to the default with step 1', () => {
    expect(bassSliderProps({ available: true, min: -9, max: 0, default: 0, actual: -4 }))
      .toEqual({ min: -9, max: 0, step: 1, value: -4 });
    // a non-zero default shifts the whole scale so 0 keeps meaning "default"
    expect(bassSliderProps({ available: true, min: -5, max: 5, default: 2, actual: 4 }))
      .toEqual({ min: -7, max: 3, step: 1, value: 2 });
  });
  it('passes a tone-controls step above 1 through', () => {
    expect(bassSliderProps({ available: true, min: -100, max: 100, default: 0, step: 50, actual: 50 }))
      .toEqual({ min: -100, max: 100, step: 50, value: 50 });
  });
  it('falls back to step 1 when the speaker reports none', () => {
    expect(bassSliderProps({ min: -9, max: 0, step: 0, actual: 0 })).toMatchObject({ step: 1 });
    expect(bassSliderProps({})).toEqual({ min: 0, max: 0, step: 1, value: 0 });
    expect(bassSliderProps(undefined)).toEqual({ min: 0, max: 0, step: 1, value: 0 });
  });
  // Source check in the style of the gate check above: the slider must take
  // all four properties from the helper. Until 2026-08-24 the template
  // hardcoded step="1", which posts values a tone-controls speaker is free
  // to refuse.
  it('the settings view slider takes min/max/step/value from this helper', () => {
    const src = allSource();
    const slider = src.match(/<input type="range" id="boxBass"[\s\S]*?\/>/);
    expect(slider, 'the bass slider markup must still be findable').not.toBeNull();
    expect(slider[0]).not.toContain('step="1"');
    for (const key of ['min', 'max', 'step', 'value']) {
      expect(slider[0]).toContain('${bassProps.' + key + '}');
    }
  });
});

// Every user-facing string the bass row can show must exist in all bundles and
// must not be left in English in a non-English one. The i18n coverage script
// checks presence across the whole bundle set; this pins the one string added
// on 2026-08-23, because a reason line that renders as a raw key or as English
// on a German speaker's screen is worse than no reason line at all.
describe('settingsView.bassUnavailable', () => {
  const bundles = ['en', 'de', 'fr', 'es', 'nl', 'pl', 'tr', 'uk', 'lt', 'lv', 'ar', 'ja', 'zh-Hant'];
  const read = (loc) => JSON.parse(
    readFileSync(new URL(`./i18n/bundles/${loc}.json`, import.meta.url), 'utf8'));
  it('is present in every bundle', () => {
    for (const loc of bundles) {
      expect(read(loc)['settingsView.bassUnavailable'], `${loc}.json`).toBeTruthy();
    }
  });
  it('is translated, not the English text copied', () => {
    const en = read('en')['settingsView.bassUnavailable'];
    for (const loc of bundles.filter((l) => l !== 'en')) {
      expect(read(loc)['settingsView.bassUnavailable'], `${loc}.json`).not.toBe(en);
    }
  });
});

// What a long-press save writes onto a key while Spotify plays (#1077 issue 15).
describe('spotifySaveContextURI', () => {
  const b64url = (str) => Buffer.from(str).toString('base64').replace(/\+/g, '-').replace(/\//g, '_');
  const nativeTrack = 'spotify:track:NATIVE';
  const nativeLoc = `/playback/container/${b64url(nativeTrack)}`;
  const staleEngine = 'spotify:album:STALE_FROM_KEY_6';
  const engineLoc = 'http://192.0.2.1:8888/spotify/stream-6.ogg';

  it('decodes the box container location', () => {
    expect(spotifyURIFromContainer(nativeLoc)).toBe(nativeTrack);
    expect(spotifyURIFromContainer(engineLoc)).toBe('');
    expect(spotifyURIFromContainer('')).toBe('');
  });

  it('takes the speaker`s own Spotify URI, never the engine`s stale context', () => {
    expect(spotifySaveContextURI('SPOTIFY', nativeLoc, staleEngine)).toBe(nativeTrack);
    // Source not yet known: the decodable container still wins.
    expect(spotifySaveContextURI('', nativeLoc, staleEngine)).toBe(nativeTrack);
  });

  it('never guesses from the engine on a native entry it cannot decode', () => {
    expect(spotifySaveContextURI('SPOTIFY', '/playback/container/not-base64!', staleEngine)).toBe('');
  });

  it('takes the engine context while the speaker plays STR`s engine stream', () => {
    expect(spotifySaveContextURI('UPNP', engineLoc, 'spotify:playlist:LIVE')).toBe('spotify:playlist:LIVE');
    expect(spotifySaveContextURI('UPNP', engineLoc, '')).toBe('');
  });

  it('falls back to the engine context without a box location', () => {
    expect(spotifySaveContextURI('UPNP', '', 'spotify:playlist:LIVE')).toBe('spotify:playlist:LIVE');
    expect(spotifySaveContextURI(undefined, undefined, 'spotify:playlist:LIVE')).toBe('spotify:playlist:LIVE');
  });

  it('keeps the #45 fallback: engine context empty, box location carries the URI', () => {
    const loc = `/playback/container/${b64url('spotify:playlist:P45')}`;
    expect(spotifySaveContextURI('UPNP', loc, '')).toBe('spotify:playlist:P45');
  });
});
