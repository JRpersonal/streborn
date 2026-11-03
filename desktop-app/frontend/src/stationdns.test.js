// A router whose name server fails, or Pi-hole / AdGuard blocking a station
// host (#1084 #1043 #823): the speaker cannot look up the station's address.
// The agent reports it as reason "dns"; these pin where the app says so.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import {
  STATION_DNS, isRadioPreset, isStationDnsFailure, sameStation, watchStationDns,
} from './stationdns.js';
import { allSource } from './test/source.js';

const here = dirname(fileURLToPath(import.meta.url));
const bundleDir = join(here, 'i18n', 'bundles');
const langs = ['ar', 'de', 'en', 'es', 'fr', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant'];
const station = 'http://stream.example.org/live.mp3';

// A fake clock: sleep advances it, so a watch runs without real timers.
function clock() {
  let t = 0;
  return { now: () => t, sleep: async (ms) => { t += ms; } };
}

describe('station lookup failure', () => {
  it('matches the started station by URL or host', () => {
    expect(sameStation(station, station)).toBe(true);
    expect(sameStation('http://stream.example.org/healed.mp3', station)).toBe(true);
    expect(sameStation('http://other.example.org/live.mp3', station)).toBe(false);
    expect(sameStation('', station)).toBe(false);
    expect(sameStation(station, '')).toBe(false);
  });

  it('reads only a dns reason for the started station', () => {
    expect(isStationDnsFailure({ error: true, reason: STATION_DNS, url: station }, station)).toBe(true);
    expect(isStationDnsFailure({ error: true, reason: 'unreachable', url: station }, station)).toBe(false);
    expect(isStationDnsFailure({ error: true, reason: STATION_DNS, url: 'http://other.example.org/' }, station)).toBe(false);
    expect(isStationDnsFailure({ error: false }, station)).toBe(false);
    expect(isStationDnsFailure(null, station)).toBe(false);
  });

  it('watches only keys whose host is looked up by name', () => {
    expect(isRadioPreset({ type: 'radio', stream_url: station })).toBe(true);
    expect(isRadioPreset({ stream_url: 'https://stream.example.org/x' })).toBe(true);
    expect(isRadioPreset({ type: 'spotify', stream_url: station })).toBe(false);
    expect(isRadioPreset({ type: 'queue', stream_url: station })).toBe(false);
    expect(isRadioPreset({ type: 'native', stream_url: station })).toBe(false);
    expect(isRadioPreset({ stream_url: 'http://192.0.2.5:8200/MediaItems/1.mp3' })).toBe(false);
    expect(isRadioPreset({ stream_url: 'http://[2001:db8::1]/x.mp3' })).toBe(false);
    expect(isRadioPreset({ stream_url: '' })).toBe(false);
    expect(isRadioPreset(null)).toBe(false);
  });
});

describe('watchStationDns', () => {
  it('returns the failure once the lookup failed', async () => {
    const c = clock();
    const answers = [{ error: false }, { error: true, reason: 'dns', url: station }];
    const fail = await watchStationDns({ fetchStatus: async () => answers.shift() || { error: false }, url: station, ...c });
    expect(fail).toMatchObject({ reason: 'dns' });
  });

  it('stops at another failure of the same station', async () => {
    const c = clock();
    let calls = 0;
    const fail = await watchStationDns({
      fetchStatus: async () => { calls++; return { error: true, reason: 'gone', status: 404, url: station }; },
      url: station, ...c,
    });
    expect(fail).toBeNull();
    expect(calls).toBe(1);
  });

  it('ignores a failure of a different station and ends with the window', async () => {
    const c = clock();
    let calls = 0;
    const fail = await watchStationDns({
      fetchStatus: async () => { calls++; return { error: true, reason: 'dns', url: 'http://other.example.org/' }; },
      url: station, windowMs: 4000, intervalMs: 1000, ...c,
    });
    expect(fail).toBeNull();
    expect(calls).toBe(4);
  });

  it('stops when the user moved on or the speaker does not answer', async () => {
    let c = clock();
    let calls = 0;
    expect(await watchStationDns({
      fetchStatus: async () => { calls++; return { error: true, reason: 'dns', url: station }; },
      url: station, stillCurrent: () => false, ...c,
    })).toBeNull();
    expect(calls).toBe(0);
    c = clock();
    expect(await watchStationDns({
      fetchStatus: async () => { throw new Error('timeout'); }, url: station, ...c,
    })).toBeNull();
    expect(await watchStationDns({ fetchStatus: null, url: station, ...c })).toBeNull();
  });
});

describe('station lookup wiring', () => {
  const src = allSource();
  it('says it on a Find stations play and after a key press', () => {
    expect(src).toContain("case STATION_DNS:   return t('search.streamDns');");
    expect(src).toContain('watchPresetDns(state.currentBox, p, gen)');
    expect(src).toContain("showToast(t('search.streamDns'))");
  });

  it('has the hint in every bundle, naming DNS and the ad-blockers', () => {
    const en = JSON.parse(readFileSync(join(bundleDir, 'en.json'), 'utf8'));
    for (const lang of langs) {
      const b = JSON.parse(readFileSync(join(bundleDir, `${lang}.json`), 'utf8'));
      for (const k of ['search.streamDns', 'speaker.noInternetHint']) {
        expect(b[k], `${lang} ${k}`).toBeTruthy();
        expect(b[k], `${lang} ${k} names DNS`).toContain('DNS');
        expect(b[k], `${lang} ${k} names Pi-hole`).toContain('Pi-hole');
        expect(b[k], `${lang} ${k} names AdGuard`).toContain('AdGuard');
        // No dash as punctuation (writing rule).
        expect(b[k], `${lang} ${k} dash`).not.toMatch(/[–—]| - /);
        if (lang !== 'en') expect(b[k], `${lang} ${k} untranslated`).not.toBe(en[k]);
      }
    }
    const de = JSON.parse(readFileSync(join(bundleDir, 'de.json'), 'utf8'));
    expect(de['speaker.noInternetHint']).toContain('prüf');
  });
});
