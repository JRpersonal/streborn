// The short notice that Spotify refuses the speaker's account on (STR) (#1671).
import { describe, it, expect } from 'vitest';
import { existsSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import * as refused from './spotifyrefused.js';
import {
  refusalFor, refusalFromReply, refusalHelp, refusalHTML, refusalNoticeFor, refusalSupportFor, refusalText,
  strSpotifyLocation,
} from './spotifyrefused.js';
import { accountRow } from './views/spotifyui2model.js';
import { srcOf } from './test/source.js';

const here = dirname(fileURLToPath(import.meta.url));
const LANGS = ['ar', 'de', 'en', 'es', 'fr', 'it', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant'];
const bundle = (l) => JSON.parse(readFileSync(join(here, 'i18n', 'bundles', `${l}.json`), 'utf8'));

const t = (key, p) => (p ? `${key}|${JSON.stringify(p)}` : key);
const BOX = { host: '192.0.2.10', port: 8888 };
const STR_LOC = 'http://192.0.2.10:8888/spotify/stream.ogg';

describe('refusalFromReply', () => {
  it('says nothing for a speaker Spotify does not refuse', () => {
    expect(refusalFromReply(null)).toBe(null);
    expect(refusalFromReply({ accountRefused: false, audioKeyRefused: false })).toBe(null);
  });

  it('takes the sticky flag and the account name', () => {
    expect(refusalFromReply({ accountRefused: true, accountRefusedName: ' Alex ' })).toEqual({ name: 'Alex', free: false, premium: false, sticky: true });
    expect(refusalFromReply({ accountRefused: true })).toEqual({ name: '', free: false, premium: false, sticky: true });
    expect(refusalFromReply({ accountRefused: true, product: 'PREMIUM' })).toEqual({ name: '', free: false, premium: true, sticky: true });
  });

  it('falls back to the short-lived flag of an older agent, without a name', () => {
    expect(refusalFromReply({ audioKeyRefused: true })).toEqual({ name: '', free: false, premium: false, sticky: false });
  });

  it('tells a free account from a Premium or unknown one', () => {
    expect(refusalFromReply({ accountRefused: true, product: 'free' }).free).toBe(true);
    expect(refusalFromReply({ accountRefused: true, product: 'open' }).free).toBe(true);
    expect(refusalFromReply({ accountRefused: true, premiumRequired: true }).free).toBe(true);
    expect(refusalFromReply({ accountRefused: true, product: 'premium' }).free).toBe(false);
    // The explicit plan wins over a stale flag.
    expect(refusalFromReply({ accountRefused: true, product: 'premium', premiumRequired: true }).free).toBe(false);
    // Unknown is not free: it gets the Premium-refusal words and the test.
    expect(refusalFromReply({ accountRefused: true, premiumRequired: false }).free).toBe(false);
  });
});

describe('refusalHelp', () => {
  it('a Premium or unknown account gets Spotify\'s rule and the decisive test', () => {
    expect(refusalHelp({ name: '', free: false }, t)).toEqual({ why: 'spotify.refusedWhy', tryText: 'spotify.refusedTry' });
    expect(refusalHelp(null, t)).toEqual({ why: '', tryText: '' });
  });

  it('a free account gets Premium as the cure, separately', () => {
    expect(refusalText({ name: '', free: true }, t)).toBe('spotify.accountRefusedFree');
    expect(refusalText({ name: 'Alex', free: true }, t)).toBe('spotify.accountRefusedFreeNamed|{"account":"Alex"}');
    expect(refusalHelp({ name: '', free: true }, t)).toEqual({ why: 'spotify.refusedFreeWhy', tryText: 'spotify.refusedFreeTry' });
  });

  it('folds the why and the test under the notice, escaped', () => {
    const html = refusalHTML({ name: '<b>Alex</b>', free: false }, t);
    expect(html).toContain('<span class="spotify-refused-lead">spotify.accountRefusedNamed|{&quot;account&quot;:&quot;&lt;b&gt;Alex&lt;/b&gt;&quot;}</span>');
    expect(html).toContain('<details class="spotify-refused-more"><summary>spotify.refusedMore</summary>');
    expect(html).toContain('<p class="spotify-refused-why">spotify.refusedWhy</p>');
    expect(html).toContain('<p class="spotify-refused-try">spotify.refusedTry</p>');
    expect(refusalHTML({ name: 'Alex', free: true }, t, { lead: 'own lead' })).toContain('<span class="spotify-refused-lead">own lead</span>');
    expect(refusalHTML(null, t)).toBe('');
  });
});

describe('the words, in every language', () => {
  const keys = [
    'spotify.accountRefused', 'spotify.accountRefusedNamed', 'spotify.accountRefusedFree', 'spotify.accountRefusedFreeNamed',
    'spotify.refusedMore', 'spotify.refusedWhy', 'spotify.refusedTry', 'spotify.refusedFreeWhy', 'spotify.refusedFreeTry',
  ];
  it('has every key in all 14 bundles', () => {
    for (const l of LANGS) {
      const b = bundle(l);
      for (const k of keys) expect(b[k], `${l} ${k}`).toBeTruthy();
      expect(b['spotify.accountRefusedFreeNamed'], l).toContain('{{account}}');
      expect(b['spotify.accountRefusedNamed'], l).toContain('{{account}}');
      // The test and the cure point at the (STR) entry.
      expect(b['spotify.refusedTry'], l).toContain('(STR)');
      expect(b['spotify.refusedFreeTry'], l).toContain('(STR)');
      // Presets keep the account they were saved under and switch back to it,
      // so playing from another Premium account only helps once the Spotify
      // presets are saved again: that step follows the (STR) entry.
      expect(b['spotify.refusedFreeTry'], l).toMatch(/\(STR\).*Spotify/);
      expect(b['spotify.refusedWhy'], l).toContain('Premium');
      expect(b['spotify.refusedFreeWhy'], l).toContain('Premium');
      for (const k of keys) expect(b[k], `${l} ${k}`).not.toMatch(/[–—]| - /);
    }
  });

  it('says it in English the way the evidence supports', () => {
    const en = bundle('en');
    // Spotify's rule, with no claim about which accounts it hits: account
    // age is not a reliable sign (old accounts were refused too).
    expect(en['spotify.refusedWhy']).toBe("Spotify refuses some accounts on players like (STR), even with Premium. This is Spotify's rule, not a fault of your speaker.");
    expect(en['spotify.refusedWhy']).not.toMatch(/20\d\d|newer|older/);
    expect(en['spotify.refusedTry']).toBe('To check, play once from another Spotify account on the entry ending in (STR). If that one plays, Spotify refuses only this account.');
    expect(en['spotify.refusedFreeTry']).toContain('then save your Spotify presets again.');
    // A free account is never promised the speaker's own entry.
    for (const k of ['spotify.accountRefusedFree', 'spotify.accountRefusedFreeNamed', 'spotify.refusedFreeWhy', 'spotify.refusedFreeTry']) {
      expect(en[k], k).not.toContain('without (STR)');
    }
  });
});

describe('refusalText', () => {
  it('names the account when the speaker knows it', () => {
    expect(refusalText({ name: 'Alex' }, t)).toBe('spotify.accountRefusedNamed|{"account":"Alex"}');
    expect(refusalText({ name: '' }, t)).toBe('spotify.accountRefused');
    expect(refusalText(null, t)).toBe('');
  });
});

describe('refusalNoticeFor', () => {
  const base = { currentBox: BOX, nowLocation: STR_LOC, nowBoxSpotify: null, spotifyRefusal: { ...BOX, name: '' } };

  it('shows while the selected speaker plays from STR Spotify', () => {
    expect(refusalNoticeFor(base, t)).toBe('spotify.accountRefused');
  });

  it('stays away from radio and the speaker\'s own Spotify entry', () => {
    expect(refusalNoticeFor({ ...base, nowLocation: 'http://192.0.2.10:8888/stream/2' }, t)).toBe('');
    expect(refusalNoticeFor({ ...base, nowBoxSpotify: { track: 'x' } }, t)).toBe('');
    expect(strSpotifyLocation('/playback/container/abc', null)).toBe(true);
  });

  it('never carries one speaker\'s answer over to another', () => {
    expect(refusalNoticeFor({ ...base, currentBox: { host: '192.0.2.11', port: 8888 } }, t)).toBe('');
    expect(refusalNoticeFor({ ...base, spotifyRefusal: null }, t)).toBe('');
    expect(refusalFor({ ...base, currentBox: { host: '192.0.2.11', port: 8888 } })).toBe(null);
  });

  it('draws the notice with its why, and only rebuilds when the words change', () => {
    const src = srcOf('renderNowPlayingBar');
    expect(src).toContain('const refusal = refusalFor(state);');
    expect(src).toContain('refusalHTML(refusal, t)');
    expect(src).toContain('refusedRow.dataset.sig !== refusedHTML');
  });
});

describe('UI 2.0 accounts row', () => {
  it('marks the speaker whose logged-in account Spotify refuses', () => {
    const r = accountRow({ host: BOX.host, port: BOX.port, name: 'Kitchen' }, { account: 'id1', accountRefused: true, canRecall: true }, {});
    expect(r.refused).toBe(true);
    expect(r.refusal).toEqual({ name: '', free: false, premium: false, sticky: true });
    const free = accountRow({ host: BOX.host, port: BOX.port, name: 'Kitchen' }, { account: 'id1', accountRefused: true, canRecall: true, product: 'free' }, {});
    expect(free.refusal.free).toBe(true);
    const paint = srcOf('paintAccounts');
    expect(paint).toContain("refusalHTML(r.refusal, t, { lead: t(r.refusal.free ? 'spotify.accountRefusedFree' : 'spotify.accountRefused') })");
    const ok = accountRow({ host: BOX.host, port: BOX.port, name: 'Kitchen' }, { account: 'id1', canRecall: true }, {});
    expect(ok.refused).toBe(false);
  });
});

describe('status poll', () => {
  const src = readFileSync(new URL('./views/music/status.js', import.meta.url), 'utf8');
  it('feeds the notice instead of a one-shot toast', () => {
    expect(src).toContain('refusalFromReply(np)');
    expect(src).not.toContain("t('spotify.audioKeyRefused')");
  });
});

describe('Spotify\'s rule, only when it is certainly Spotify\'s block', () => {
  const from = (np) => refusalFromReply(np);

  it('holds only for the sticky per-account record on a Premium plan', () => {
    expect(refusalSupportFor(from({ accountRefused: true, product: 'premium' }))).toBe(true);
    expect(refusalSupportFor(from({ accountRefused: true, product: 'Premium', accountRefusedName: 'Alex' }))).toBe(true);
  });

  it('never for a free, open or unknown plan, nor on premiumRequired', () => {
    expect(refusalSupportFor(from({ accountRefused: true, product: 'free' }))).toBe(false);
    expect(refusalSupportFor(from({ accountRefused: true, product: 'open' }))).toBe(false);
    expect(refusalSupportFor(from({ accountRefused: true }))).toBe(false);
    expect(refusalSupportFor(from({ accountRefused: true, product: '' }))).toBe(false);
    expect(refusalSupportFor(from({ accountRefused: true, product: 'family' }))).toBe(false);
    expect(refusalSupportFor(from({ accountRefused: true, premiumRequired: true }))).toBe(false);
  });

  it('never on an older agent\'s ten-minute flag alone', () => {
    expect(refusalSupportFor(from({ audioKeyRefused: true, product: 'premium' }))).toBe(false);
    expect(refusalSupportFor(from({ audioKeyRefused: true, accountRefused: false, product: 'premium' }))).toBe(false);
    expect(refusalSupportFor(null)).toBe(false);
  });

  it('carries over into the per-speaker refusal the now-playing area reads', () => {
    const ref = { host: BOX.host, port: BOX.port, ...from({ accountRefused: true, product: 'premium' }) };
    const st = { currentBox: BOX, nowLocation: STR_LOC, nowBoxSpotify: null, spotifyRefusal: ref };
    expect(refusalSupportFor(refusalFor(st))).toBe(true);
  });

  it('puts the one line inside the folded help, and only then', () => {
    const premium = refusalHTML(from({ accountRefused: true, product: 'premium' }), t);
    expect(premium).toContain('<p class="spotify-refused-support">spotify.refusedSupport</p>');
    // Inside the <details>, after the why and the test, and nothing after it.
    expect(premium.indexOf('spotify-refused-try')).toBeLessThan(premium.indexOf('spotify-refused-support'));
    expect(premium.endsWith('<p class="spotify-refused-support">spotify.refusedSupport</p></details>')).toBe(true);
    // Deterministic, so the poll's rebuild-on-change never snaps it shut.
    expect(refusalHTML(from({ accountRefused: true, product: 'premium' }), t)).toBe(premium);
    for (const np of [
      { accountRefused: true, product: 'free' }, { accountRefused: true, product: 'open' }, { accountRefused: true },
      { accountRefused: true, premiumRequired: true }, { audioKeyRefused: true, product: 'premium' },
    ]) {
      const html = refusalHTML(from(np), t);
      expect(html, JSON.stringify(np)).not.toContain('spotify.refusedSupport');
    }
  });

  it('sends nobody to Spotify support: no buttons, no contact page, no message (#1077)', () => {
    const premium = refusalHTML(from({ accountRefused: true, product: 'premium' }), t);
    expect(premium).not.toContain('<button');
    expect(premium).not.toContain('data-str-refusal');
    expect(premium).not.toContain('spotify-refused-actions');
    expect(premium).not.toMatch(/refusedSupport(Open|Copy|Copied|Message)/);
    expect(refused.SPOTIFY_SUPPORT_URL).toBeUndefined();
    expect(existsSync(join(here, 'spotifyrefusedsupport.js'))).toBe(false);
    expect(srcOf('renderNowPlayingBar')).not.toContain('installRefusalSupport');
    expect(srcOf('paintAccounts')).not.toContain('installRefusalSupport');
    expect(readFileSync(join(here, 'style.css'), 'utf8')).not.toContain('spotify-refused-actions');
  });
});

describe('Spotify\'s rule, in every language', () => {
  const removed = [
    'spotify.refusedSupportOpen', 'spotify.refusedSupportCopy',
    'spotify.refusedSupportCopied', 'spotify.refusedSupportMessage',
  ];

  it('has the line in all 14 bundles, and none of the support keys', () => {
    for (const l of LANGS) {
      const b = bundle(l);
      const line = b['spotify.refusedSupport'];
      expect(typeof line, `${l} spotify.refusedSupport`).toBe('string');
      expect(line.trim(), l).not.toBe('');
      expect(line, l).not.toMatch(/[–—]| - /);
      expect(line, l).toContain('STR');
      // Spotify's rule, not a support step.
      expect(line, l).not.toMatch(/e-?mail|@|support|https?:/i);
      for (const k of removed) expect(b[k], `${l} ${k}`).toBeUndefined();
    }
  });

  it('says it in English and German as agreed', () => {
    expect(bundle('en')['spotify.refusedSupport']).toBe('Spotify has said it no longer enables accounts that have not played on players like this within the last year. Nothing in STR or your speaker can change that.');
    expect(bundle('de')['spotify.refusedSupport']).toBe('Spotify hat erklärt, dass es Konten, die im letzten Jahr nicht auf solchen Playern gespielt haben, nicht mehr freischaltet. Daran können weder STR noch dein Lautsprecher etwas ändern.');
  });
});
