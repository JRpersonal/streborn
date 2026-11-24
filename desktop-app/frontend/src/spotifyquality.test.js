import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { qualityOptions, qualityStatusText, qualityReasonKey, qualityUnits, QUALITY_I18N_KEYS } from './spotifyquality.js';
import { srcOf } from './test/source.js';

// The speaker defaults to 320 kbps on Premium and 160 otherwise; the row where
// the setting lives has to say which bitrate is active and why.

const bundle = (l) =>
  JSON.parse(readFileSync(new URL(`./i18n/bundles/${l}.json`, import.meta.url), 'utf8'));
const LANGS = ['en', 'de', 'nl', 'fr', 'es', 'pl', 'tr', 'uk', 'lt', 'lv', 'ja', 'zh-Hant', 'ar'];

// A t() that renders the English bundle, so the expectations read like the UI.
const en = bundle('en');
const t = (k, vars = {}) => (en[k] || k).replace(/\{\{(\w+)\}\}/g, (_, v) => String(vars[v]));

describe('Spotify streaming quality row', () => {
  it('names the Premium default', () => {
    const st = { ok: true, bitrate: 320, autoSupported: true, choice: 0, reason: 'premium-default' };
    expect(qualityStatusText(st, t)).toBe('Streaming quality: 320 kbps (Premium default)');
  });

  it('names an explicit choice and the unknown-plan default', () => {
    expect(qualityStatusText({ ok: true, bitrate: 160, reason: 'chosen' }, t))
      .toBe('Streaming quality: 160 kbps (chosen for this speaker)');
    expect(qualityStatusText({ ok: true, bitrate: 160, reason: 'plan-unknown' }, t))
      .toBe('Streaming quality: 160 kbps (default, Spotify plan not known yet)');
  });

  it('falls back to the plain line for an older agent without a reason', () => {
    expect(qualityStatusText({ ok: true, bitrate: 160 }, t)).toBe('Streaming quality: 160 kbps');
    expect(qualityReasonKey('something-new')).toBe('');
  });

  it('offers Automatic and selects the stored choice on a current agent', () => {
    const auto = qualityOptions({ ok: true, bitrate: 320, autoSupported: true, choice: 0 }, t);
    expect(auto.map(o => o.value)).toEqual([0, 160, 320]);
    expect(auto.find(o => o.selected).value).toBe(0);
    expect(auto[0].label).toBe('Automatic');
    const chosen = qualityOptions({ ok: true, bitrate: 160, autoSupported: true, choice: 160 }, t);
    expect(chosen.find(o => o.selected).value).toBe(160);
  });

  it('keeps the original two options on an older agent', () => {
    const old = qualityOptions({ ok: true, bitrate: 320 }, t);
    expect(old.map(o => o.value)).toEqual([160, 320]);
    expect(old.find(o => o.selected).value).toBe(320);
  });

  it('renders the status line in the speaker row', () => {
    expect(srcOf('refreshQualityList')).toContain('qualityStatusText');
  });

  it('has every key in every language', () => {
    for (const l of LANGS) {
      const b = bundle(l);
      for (const k of QUALITY_I18N_KEYS) expect(b[k], `${k} missing in ${l}`).toBeTruthy();
      expect(b['spotify.qualityActive'], `placeholders in ${l}`).toMatch(/\{\{kbps\}\}.*\{\{reason\}\}/);
    }
  });
});

describe('quality rows for a stereo pair (#2100)', () => {
  const left = { host: '192.0.2.10', name: 'Grace left' };
  const right = { host: '192.0.2.11', name: 'Grace right' };
  const solo = { host: '192.0.2.12', name: 'Bose 1' };
  const pairFor = (b) => (b === left || b === right ? { masterHost: left.host, name: 'Kitchen pair' } : null);

  it('shows a pair as one row on its master, named after the pair', () => {
    const rows = qualityUnits([left, right, solo], pairFor);
    expect(rows.map((r) => [r.box.host, r.name])).toEqual([
      [left.host, 'Kitchen pair'],
      [solo.host, 'Bose 1'],
    ]);
  });

  it('keeps both halves when the master is not in the list', () => {
    const rows = qualityUnits([right, solo], pairFor);
    expect(rows.map((r) => r.name)).toEqual(['Grace right', 'Bose 1']);
  });

  it('falls back to the speaker name when the pair has none', () => {
    const rows = qualityUnits([left, right], () => ({ masterHost: left.host, name: '' }));
    expect(rows.map((r) => r.name)).toEqual(['Grace left']);
  });

  it('lists every speaker when nobody is paired', () => {
    expect(qualityUnits([left, right, solo], () => null)).toHaveLength(3);
    expect(qualityUnits(null)).toEqual([]);
  });
});
