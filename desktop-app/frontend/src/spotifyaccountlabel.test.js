import { describe, it, expect } from 'vitest';
import { spotifyAccountName, looksOpaque } from './spotifyaccountlabel.js';

// Synthetic ids, shaped like the real thing and belonging to nobody.
//
// They used to be two REAL account ids, copied out of a reporter's diagnostic
// into this file by the very commit that was written to keep that identifier off
// the screen (6793ed39, 2026-10-01). So the fix published the thing it was
// protecting, in a public repository, where it is harder to take back than the
// screenshot ever was. Found 2026-10-02.
//
// A test about the SHAPE of an identifier never needs a real one. The first is
// 25 characters, the second 28 and starting with digits, which is the pattern
// Spotify's canonical ids come in and all this code reasons about.
const OPAQUE_ID = 'qm4xvp7z2bnkd6rt1ys8hgwj3';
const OPAQUE_ID_LONG = '48qpzlmxtreb9vkd2yhsn6wc3gfu';

describe('looksOpaque', () => {
  it('recognises Spotify canonical user ids', () => {
    expect(looksOpaque(OPAQUE_ID)).toBe(true);
    expect(looksOpaque(OPAQUE_ID_LONG)).toBe(true);
  });

  it('leaves names people chose alone', () => {
    // Capitals, separators and short handles are all names, not ids.
    expect(looksOpaque('SpotifyConnectUserName')).toBe(false);
    expect(looksOpaque('eileen.wilson')).toBe(false);
    expect(looksOpaque('eileen_w')).toBe(false);
    expect(looksOpaque('jens-r')).toBe(false);
    expect(looksOpaque('bob')).toBe(false);
    expect(looksOpaque('')).toBe(false);
  });
});

describe('spotifyAccountName', () => {
  it('prints the remembered display name for an account', () => {
    expect(spotifyAccountName(OPAQUE_ID, { [OPAQUE_ID]: 'Eileen' })).toBe('Eileen');
  });

  it('prints NOTHING for an account it knows no name for', () => {
    // The whole point. A four-character tail of an opaque id is not a name
    // either, which is what the reporter said when the first attempt shipped
    // one, and the full id is a personal identifier that resolves to a public
    // profile page.
    expect(spotifyAccountName(OPAQUE_ID, {})).toBe('');
    expect(spotifyAccountName(OPAQUE_ID, null)).toBe('');
    expect(spotifyAccountName(OPAQUE_ID_LONG, { somebodyElse: 'Eileen' })).toBe('');
  });

  it('never leaks any part of the id', () => {
    for (const names of [{}, null, undefined, { other: 'x' }]) {
      const out = spotifyAccountName(OPAQUE_ID, names);
      expect(out).toBe('');
      expect(OPAQUE_ID.includes(out) && out.length > 0).toBe(false);
    }
  });

  it('still shows an old-style username, which is a name somebody chose', () => {
    // This is the case the line was added for, and it is still worth showing
    // when no display name has been remembered yet.
    expect(spotifyAccountName('eileen.wilson', {})).toBe('eileen.wilson');
    expect(spotifyAccountName('eileen.wilson', { 'eileen.wilson': 'Eileen W' })).toBe('Eileen W');
  });

  it('shows nothing at all when there is no account', () => {
    expect(spotifyAccountName('', { x: 'y' })).toBe('');
    expect(spotifyAccountName(null, {})).toBe('');
    expect(spotifyAccountName(undefined, {})).toBe('');
  });
});
