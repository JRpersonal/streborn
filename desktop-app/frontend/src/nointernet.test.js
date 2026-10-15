// A router cut five speakers off the internet while the LAN kept working
// (2026-10-14). The app found and controlled every one of them and said
// nothing, so the owner rebooted speakers for two hours. The agent now
// reports internet:"unreachable"; these pin where the app says so.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import {
  boxCannotReachInternet, noInternetBoxes, playFailedForNoInternet, noInternetText,
} from './nointernet.js';

const here = dirname(fileURLToPath(import.meta.url));
const main = readFileSync(join(here, 'main.js'), 'utf8');
const state = readFileSync(join(here, 'state.js'), 'utf8');
const bundleDir = join(here, 'i18n', 'bundles');
const langs = ['ar', 'de', 'en', 'es', 'fr', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant'];
const keys = ['speaker.noInternetChip', 'speaker.noInternetTitle', 'speaker.noInternetHint', 'speaker.noInternetBanner'];

const live = { host: '192.0.2.1', kind: 'str', internet: 'unreachable', internetReason: 'dns' };

describe('no-internet verdict', () => {
  it('flags only a reachable STR speaker with positive evidence', () => {
    expect(boxCannotReachInternet(live)).toBe(true);
    expect(boxCannotReachInternet({ ...live, internet: 'ok' })).toBe(false);
    expect(boxCannotReachInternet({ ...live, internet: 'unknown' })).toBe(false);
    expect(boxCannotReachInternet({ ...live, internet: undefined })).toBe(false); // older agent
    expect(boxCannotReachInternet({ ...live, offline: true })).toBe(false);
    expect(boxCannotReachInternet({ ...live, kind: 'stock' })).toBe(false);
    expect(boxCannotReachInternet(null)).toBe(false);
  });

  it('lists the speakers for the banner', () => {
    const ok = { host: '192.0.2.2', kind: 'str', internet: 'ok' };
    expect(noInternetBoxes([live, ok, null])).toEqual([live]);
    expect(noInternetBoxes(undefined)).toEqual([]);
  });

  it('rewords a failed play only when the internet is the cause', () => {
    expect(playFailedForNoInternet({ reason: 'offline' }, { internet: 'ok' })).toBe(true);
    expect(playFailedForNoInternet({ reason: 'unreachable' }, live)).toBe(true);
    expect(playFailedForNoInternet({ reason: 'unreachable' }, { ...live, internet: 'ok' })).toBe(false);
    // The station answered (403): the internet works, the station is at fault.
    expect(playFailedForNoInternet({ reason: 'blocked', status: 403 }, live)).toBe(false);
    expect(playFailedForNoInternet(null, live)).toBe(false);
  });

  it('joins the title and the router hint', () => {
    const t = (k) => ({ 'speaker.noInternetTitle': 'A.', 'speaker.noInternetHint': 'B?' })[k];
    expect(noInternetText(t)).toBe('A. B?');
  });
});

describe('no-internet wiring', () => {
  it('shows on the tile, in the banner and on a failed play', () => {
    expect(main).toContain("from './nointernet.js'");
    expect(main).toContain('box-nointernet-chip');
    expect(main).toContain('noInternetBoxes(boxes)');
    expect(main).toContain('playFailedForNoInternet(fail, box)');
  });

  it('never survives in the speaker cache', () => {
    expect(state).toContain("'internet'");
    expect(state).toContain("'internetReason'");
  });

  it('has every string in every bundle', () => {
    const en = JSON.parse(readFileSync(join(bundleDir, 'en.json'), 'utf8'));
    expect(en['speaker.noInternetHint']).toContain('check the router');
    for (const lang of langs) {
      const b = JSON.parse(readFileSync(join(bundleDir, `${lang}.json`), 'utf8'));
      for (const k of keys) {
        expect(b[k], `${lang} ${k}`).toBeTruthy();
        if (lang !== 'en' && k !== 'speaker.noInternetChip') expect(b[k], `${lang} ${k} untranslated`).not.toBe(en[k]);
      }
      expect(b['speaker.noInternetBanner']).toContain('{{name}}');
    }
    const de = JSON.parse(readFileSync(join(bundleDir, 'de.json'), 'utf8'));
    expect(de['speaker.noInternetHint']).toContain('prüf');
  });
});
