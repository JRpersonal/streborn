// The short notice that Spotify refuses the speaker's account on (STR) (#1671).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  refusalFor, refusalFromReply, refusalHelp, refusalHTML, refusalNoticeFor, refusalSupportFor, refusalText,
  SPOTIFY_SUPPORT_URL, strSpotifyLocation,
} from './spotifyrefused.js';
import { COPIED_MS, refusalSupportAction } from './spotifyrefusedsupport.js';
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
    // Spotify's rule, hedged about who it hits, never a firm cutoff.
    expect(en['spotify.refusedWhy']).toContain('usually newer accounts');
    expect(en['spotify.refusedWhy']).toContain('even with Premium');
    expect(en['spotify.refusedWhy']).not.toMatch(/20\d\d/);
    expect(en['spotify.refusedTry']).toContain('another, older Spotify account');
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

describe('Spotify support, only when it is certainly Spotify\'s block', () => {
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

  it('puts the line and both buttons inside the folded help, and only then', () => {
    const premium = refusalHTML(from({ accountRefused: true, product: 'premium' }), t);
    expect(premium).toContain('<p class="spotify-refused-support">spotify.refusedSupport</p>');
    expect(premium).toContain('<button type="button" class="btn btn-mini" data-str-refusal="open">spotify.refusedSupportOpen</button>');
    expect(premium).toContain('<button type="button" class="btn btn-mini" data-str-refusal="copy">spotify.refusedSupportCopy</button>');
    // Inside the <details>, after the why and the test.
    expect(premium.indexOf('spotify-refused-try')).toBeLessThan(premium.indexOf('spotify-refused-support'));
    expect(premium.endsWith('</p></details>')).toBe(true);
    // No inline handler: the clicks are wired by delegation.
    expect(premium).not.toMatch(/onclick/i);
    // Deterministic, so the poll's rebuild-on-change never snaps it shut.
    expect(refusalHTML(from({ accountRefused: true, product: 'premium' }), t)).toBe(premium);
    for (const np of [
      { accountRefused: true, product: 'free' }, { accountRefused: true, product: 'open' }, { accountRefused: true },
      { accountRefused: true, premiumRequired: true }, { audioKeyRefused: true, product: 'premium' },
    ]) {
      const html = refusalHTML(from(np), t);
      expect(html, JSON.stringify(np)).not.toContain('data-str-refusal');
      expect(html, JSON.stringify(np)).not.toContain('spotify.refusedSupport');
    }
  });

  it('points at Spotify\'s contact page, not an address', () => {
    expect(SPOTIFY_SUPPORT_URL).toBe('https://support.spotify.com/article/contact-us/');
  });
});

describe('Spotify support buttons', () => {
  const fakeButton = (action) => {
    const attrs = { 'data-str-refusal': action };
    return { textContent: 'label', getAttribute: (k) => attrs[k] ?? null };
  };
  const deps = () => {
    const d = { opened: [], copied: [], timers: [] };
    d.open = (u) => d.opened.push(u);
    d.copy = (s) => { d.copied.push(s); return Promise.resolve(true); };
    d.later = (fn, ms) => d.timers.push({ fn, ms });
    d.t = t;
    return d;
  };
  const tick = () => new Promise((r) => setTimeout(r, 0));

  it('opens the support page', () => {
    const d = deps();
    expect(refusalSupportAction(fakeButton('open'), d)).toBe('open');
    expect(d.opened).toEqual([SPOTIFY_SUPPORT_URL]);
    expect(d.copied).toEqual([]);
  });

  it('copies the message and says so on the button for a moment', async () => {
    const d = deps();
    const btn = fakeButton('copy');
    expect(refusalSupportAction(btn, d)).toBe('copy');
    await tick();
    expect(d.copied).toEqual(['spotify.refusedSupportMessage']);
    expect(btn.textContent).toBe('spotify.refusedSupportCopied');
    expect(d.timers).toHaveLength(1);
    expect(d.timers[0].ms).toBe(COPIED_MS);
    d.timers[0].fn();
    expect(btn.textContent).toBe('spotify.refusedSupportCopy');
  });

  it('claims nothing when the clipboard refused', async () => {
    const d = deps();
    const btn = fakeButton('copy');
    d.copy = () => Promise.resolve(false);
    refusalSupportAction(btn, d);
    await tick();
    expect(btn.textContent).toBe('label');
    d.copy = () => Promise.reject(new Error('no clipboard'));
    refusalSupportAction(btn, d);
    await tick();
    expect(btn.textContent).toBe('label');
    expect(d.timers).toHaveLength(0);
  });

  it('ignores anything else', () => {
    expect(refusalSupportAction(fakeButton('other'), deps())).toBe('');
    expect(refusalSupportAction(null, deps())).toBe('');
  });

  it('is wired once, by delegation, from both places the notice is drawn', () => {
    expect(srcOf('renderNowPlayingBar')).toContain('installRefusalSupport()');
    expect(srcOf('paintAccounts')).toContain('installRefusalSupport()');
    expect(srcOf('installRefusalSupport')).toContain("closest('[data-str-refusal]')");
  });
});

describe('the Spotify support words, in every language', () => {
  const keys = [
    'spotify.refusedSupport', 'spotify.refusedSupportOpen', 'spotify.refusedSupportCopy',
    'spotify.refusedSupportCopied', 'spotify.refusedSupportMessage',
  ];

  it('has every key, non-empty, in all 14 bundles', () => {
    for (const l of LANGS) {
      const b = bundle(l);
      for (const k of keys) {
        expect(typeof b[k], `${l} ${k}`).toBe('string');
        expect(b[k].trim(), `${l} ${k}`).not.toBe('');
        expect(b[k], `${l} ${k}`).not.toMatch(/[–—]| - /);
        // Spotify has a contact page, not a published address.
        expect(b[k], `${l} ${k}`).not.toMatch(/e-?mail|@/i);
      }
      const msg = b['spotify.refusedSupportMessage'];
      // The facts Spotify support needs, in every language.
      for (const fact of ['Premium', 'Bose', 'SoundTouch', 'go-librespot', '2026', '1']) {
        expect(msg, `${l} message`).toContain(fact);
      }
      expect(msg.split('\n\n').length, `${l} message paragraphs`).toBeGreaterThanOrEqual(4);
    }
  });

  it('says it in English and German as agreed', () => {
    const en = bundle('en');
    expect(en['spotify.refusedSupport']).toBe('Only Spotify can lift this. Tell Spotify support, and the more people report it, the likelier they act.');
    expect(en['spotify.refusedSupportOpen']).toBe('Contact Spotify support');
    expect(en['spotify.refusedSupportCopy']).toBe('Copy a message for Spotify');
    expect(en['spotify.refusedSupportCopied']).toBe('Copied');
    expect(en['spotify.refusedSupportMessage']).toMatch(/^Hello Spotify Support,\n\nmy Spotify Premium account/);
    expect(en['spotify.refusedSupportMessage']).toContain('(error code 1)');
    const de = bundle('de');
    expect(de['spotify.refusedSupport']).toBe('Aufheben kann das nur Spotify. Sag es dem Spotify-Support, je mehr Leute es melden, desto eher handelt Spotify.');
    expect(de['spotify.refusedSupportOpen']).toBe('Spotify-Support kontaktieren');
    expect(de['spotify.refusedSupportCopy']).toBe('Nachricht für Spotify kopieren');
    expect(de['spotify.refusedSupportCopied']).toBe('Kopiert');
    expect(de['spotify.refusedSupportMessage']).toMatch(/^Hallo Spotify-Support,\n\nmein Spotify-Premium-Konto/);
  });
});
