// The Deezer area's list of the home's Deezer logins (deezercard.js): one
// compact entry per speaker, a button only where an action applies, never a
// login or an account.
import { describe, it, expect, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { setLocale, t } from '../i18n/index.js';
import { deezerHousehold } from '../deezer.js';

vi.mock('../api.js', () => ({
  DeezerStatus: vi.fn(), DeezerRecheck: vi.fn(), UnlinkMusicService: vi.fn(),
  DeezerHousehold: vi.fn(), DeezerCopyLogin: vi.fn(), DeezerUseOlderLogin: vi.fn(),
  BrowserOpenURL: vi.fn(), ClipboardSetText: vi.fn(),
}));
vi.mock('./settings/shared.js', () => ({ confirmFor: vi.fn() }));

const { deezerHomeHTML } = await import('./deezercard.js');

setLocale('en');
const date = () => '24 Nov 2026';

const HOME = {
  speakers: [
    { id: '', name: 'Kitchen', self: true, state: 'works', kept: true, from: 'sources-file', keptAt: '2026-11-24T10:00:00Z', previous: 1, account: 'same' },
    { id: '192.0.2.5:8888', name: 'Bedroom', state: 'no-login', kept: false, previous: 0, account: 'same', canCopy: true },
    { id: '192.0.2.6:17008', name: 'Office', state: 'works', kept: true, from: 'peer', fromName: 'Kitchen', keptAt: '2026-11-25T10:00:00Z', previous: 2, account: 'same' },
    { id: '192.0.2.7:8888', name: 'Garage', state: 'refused', kept: true, from: 'registration', keptAt: '2026-11-20T10:00:00Z', account: 'other' },
    { id: '192.0.2.8:8888', name: 'Porch', state: 'unknown', why: 'unreachable' },
  ],
  canCopy: 1,
};

describe('Deezer logins of the home', () => {
  it('lists every speaker with its state, kept login and account', () => {
    const html = deezerHomeHTML(deezerHousehold(HOME), t, date);
    expect(html).toContain('Deezer logins in your home');
    expect(html).toContain('Kitchen (this speaker)');
    expect(html).toContain('STR keeps its own login (since 24 Nov 2026)');
    expect(html).toContain('1 older login backed up');
    expect(html).toContain('STR keeps a login copied from Kitchen (since 24 Nov 2026)');
    expect(html).toContain('2 older logins backed up');
    expect(html).toContain('Deezer refuses the login');
    expect(html).toContain('Another Deezer account');
    expect(html).toContain('Not reachable from here');
    expect(html).toContain('A login goes to another speaker only when you press Copy.');
  });

  it('offers Copy only on the rows that can take the login, Copy to all only for more than one', () => {
    const html = deezerHomeHTML(deezerHousehold(HOME), t, date);
    expect(html.match(/data-dz-copy=/g)).toHaveLength(1);
    expect(html).toContain('data-dz-copy="192.0.2.5:8888"');
    expect(html).toContain('Copy to Bedroom');
    expect(html).not.toContain('id="deezerCopyAll"');
    expect(html).not.toContain('id="deezerUseOlder"');
    const two = { ...HOME, speakers: HOME.speakers.map((s) => (s.name === 'Garage' ? { ...s, account: 'same', canCopy: true } : s)) };
    expect(deezerHomeHTML(deezerHousehold(two), t, date)).toContain('id="deezerCopyAll"');
  });

  it('offers Use the older login on this speaker only', () => {
    const hh = deezerHousehold({ speakers: [{ name: 'Kitchen', self: true, state: 'refused', kept: true, previous: 1, canUseOlder: true }] });
    const html = deezerHomeHTML(hh, t, date);
    expect(html).toContain('id="deezerUseOlder"');
    expect(html).toContain('Use the older login');
    const peer = deezerHousehold({ speakers: [{ name: 'A', self: true, state: 'works' }, { id: 'x', name: 'B', state: 'refused', canUseOlder: true }] });
    expect(deezerHomeHTML(peer, t, date)).not.toContain('id="deezerUseOlder"');
  });

  it('stays out of the way for a lone speaker with nothing kept, or an older agent', () => {
    expect(deezerHomeHTML(deezerHousehold({ speakers: [{ name: 'A', self: true, state: 'not-set-up' }] }), t, date)).toBe('');
    expect(deezerHomeHTML(deezerHousehold(null), t, date)).toBe('');
  });

  it('escapes the names', () => {
    const hh = deezerHousehold({ speakers: [{ name: '<b>x</b>', self: true, state: 'works', kept: true }, { id: '"y"', name: 'B&B', state: 'no-login', canCopy: true }] });
    const html = deezerHomeHTML(hh, t, date);
    expect(html).not.toContain('<b>x</b>');
    expect(html).toContain('&lt;b&gt;x&lt;/b&gt;');
    expect(html).toContain('B&amp;B');
    expect(html).not.toContain('data-dz-copy=""y""');
  });

  it('has its texts in every language, translated, without dashes', () => {
    const en = JSON.parse(readFileSync(new URL('../i18n/bundles/en.json', import.meta.url), 'utf8'));
    const keys = Object.keys(en).filter((k) => k.startsWith('deezer.home.'));
    expect(keys.length).toBeGreaterThan(30);
    for (const lang of ['ar', 'de', 'es', 'fr', 'it', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant']) {
      const b = JSON.parse(readFileSync(new URL(`../i18n/bundles/${lang}.json`, import.meta.url), 'utf8'));
      for (const k of keys) {
        expect(b[k], `${lang} ${k}`).toBeTruthy();
        expect(b[k], `${lang} ${k} untranslated`).not.toBe(en[k]);
        expect(b[k], `${lang} ${k}`).not.toMatch(/[–—]/);
        for (const p of (en[k].match(/\{\{\w+\}\}/g) || [])) expect(b[k], `${lang} ${k} ${p}`).toContain(p);
      }
    }
  });
});
