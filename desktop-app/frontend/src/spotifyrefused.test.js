// The short notice that Spotify refuses the speaker's account on (STR) (#1671).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  isKeyRefusedError, refusalFor, refusalFromReply, refusalHelp, refusalHTML, refusalNoticeFor, refusalSentence,
  refusalText, strSpotifyLocation,
} from './spotifyrefused.js';
import { accountRow } from './views/spotifyui2model.js';
import { srcOf } from './test/source.js';

const here = dirname(fileURLToPath(import.meta.url));
const LANGS = ['ar', 'de', 'en', 'es', 'fr', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant'];
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
    expect(refusalFromReply({ accountRefused: true, accountRefusedName: ' Alex ' })).toEqual({ name: 'Alex', free: false });
    expect(refusalFromReply({ accountRefused: true })).toEqual({ name: '', free: false });
  });

  it('falls back to the short-lived flag of an older agent, without a name', () => {
    expect(refusalFromReply({ audioKeyRefused: true })).toEqual({ name: '', free: false });
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

describe('refusalHelp and the whole message', () => {
  it('a Premium or unknown account gets Spotify\'s rule and the decisive test', () => {
    expect(refusalHelp({ name: '', free: false }, t)).toEqual({ why: 'spotify.refusedWhy', tryText: 'spotify.refusedTry' });
    expect(refusalSentence({ name: 'Alex', free: false }, t))
      .toBe('spotify.accountRefusedNamed|{"account":"Alex"} spotify.refusedWhy spotify.refusedTry');
  });

  it('a free account gets Premium as the cure, separately', () => {
    expect(refusalText({ name: '', free: true }, t)).toBe('spotify.accountRefusedFree');
    expect(refusalText({ name: 'Alex', free: true }, t)).toBe('spotify.accountRefusedFreeNamed|{"account":"Alex"}');
    expect(refusalHelp({ name: '', free: true }, t)).toEqual({ why: 'spotify.refusedFreeWhy', tryText: 'spotify.refusedFreeTry' });
    expect(refusalSentence(null, t)).toBe('');
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

  it('reads the agent\'s key-refused answer, and nothing else', () => {
    const hint = 'spotify play refused: Spotify refused the audio key for several tracks in a row, so the speaker\'s Spotify engine stopped';
    expect(isKeyRefusedError(hint)).toBe(true);
    expect(isKeyRefusedError(new Error(hint))).toBe(true);
    expect(isKeyRefusedError('connection refused')).toBe(false);
    expect(isKeyRefusedError('')).toBe(false);
  });
});

describe('the words, in every language', () => {
  const keys = [
    'spotify.accountRefused', 'spotify.accountRefusedNamed', 'spotify.accountRefusedFree', 'spotify.accountRefusedFreeNamed',
    'spotify.refusedMore', 'spotify.refusedWhy', 'spotify.refusedTry', 'spotify.refusedFreeWhy', 'spotify.refusedFreeTry',
    'play.errSpotifyRefused',
  ];
  it('has every key in all 13 bundles', () => {
    for (const l of LANGS) {
      const b = bundle(l);
      for (const k of keys) expect(b[k], `${l} ${k}`).toBeTruthy();
      expect(b['spotify.accountRefusedFreeNamed'], l).toContain('{{account}}');
      expect(b['spotify.accountRefusedNamed'], l).toContain('{{account}}');
      // The test and the cure point at the (STR) entry.
      expect(b['spotify.refusedTry'], l).toContain('(STR)');
      expect(b['spotify.refusedFreeTry'], l).toContain('(STR)');
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
    // A free account is never promised the speaker's own entry.
    for (const k of ['spotify.accountRefusedFree', 'spotify.accountRefusedFreeNamed', 'spotify.refusedFreeWhy', 'spotify.refusedFreeTry']) {
      expect(en[k], k).not.toContain('without (STR)');
    }
  });
});

describe('a key Spotify refused the audio keys for', () => {
  const friendlyPlayError = new Function('t', 'deps', 'escalationForCount', 'noteNotReady', 'libraryServerOffName', 'isKeyRefusedError',
    `${srcOf('friendlyPlayError')}; return friendlyPlayError;`)(
    (k) => k, { stereoStaleForError: () => null }, () => 'play.errNotReady', () => 1, () => null, isKeyRefusedError,
  );
  const hint = 'spotify play refused: Spotify refused the audio key for several tracks in a row, so the speaker\'s Spotify engine stopped';

  it('labels the tile as Spotify refusing the account, not the speaker', () => {
    expect(friendlyPlayError(hint, '')).toBe('play.errSpotifyRefused');
    expect(friendlyPlayError('connection refused', '')).toBe('play.errSpeakerRefused');
    // The free-account code keeps its own label.
    expect(friendlyPlayError('spotify-premium-required', '')).toBe('play.errSpotifyPremium');
  });

  it('shows why and the test as a toast', () => {
    const src = srcOf('play');
    expect(src).toContain('if (isKeyRefusedError(errStr)');
    expect(src).toContain('showToast(refusalSentence({ name: \'\', free: !!state.spotifyPremiumRequired }, t))');
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
    expect(r.refusal).toEqual({ name: '', free: false });
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
