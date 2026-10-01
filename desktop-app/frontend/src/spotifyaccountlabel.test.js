import { describe, it, expect } from 'vitest';
import { spotifyAccountLabel, looksOpaque } from './spotifyaccountlabel.js';

// The two real ids from the 2026-09-30 report, which the app printed in full on
// the preset tiles and which the reporter then pasted into a mail.
const PREMIUM_ID = 'aci7jtog89nhio3gbk3absjgj';
const FREE_ID = '31ususfrcgy4ynphm3h5yvubycui';

describe('looksOpaque', () => {
  it('recognises Spotify canonical user ids', () => {
    expect(looksOpaque(PREMIUM_ID)).toBe(true);
    expect(looksOpaque(FREE_ID)).toBe(true);
  });

  it('leaves names people chose alone', () => {
    // Capitals, separators and short handles are all names, not ids.
    expect(looksOpaque('SpotifyConnectUserName')).toBe(false);
    expect(looksOpaque('eileen.wilson')).toBe(false);
    expect(looksOpaque('eileen_w')).toBe(false);
    expect(looksOpaque('jens-r')).toBe(false);
    expect(looksOpaque('eileen310')).toBe(false);
    expect(looksOpaque('a@b.com')).toBe(false);
  });

  it('says no to nothing', () => {
    expect(looksOpaque('')).toBe(false);
    expect(looksOpaque(null)).toBe(false);
    expect(looksOpaque(undefined)).toBe(false);
  });
});

describe('spotifyAccountLabel', () => {
  it('never puts a whole canonical id on screen', () => {
    for (const id of [PREMIUM_ID, FREE_ID]) {
      const label = spotifyAccountLabel(id);
      expect(label).not.toContain(id);
      expect(id).not.toContain(label.replace('…', '') + 'x');
      expect(label.length).toBeLessThanOrEqual(5);
    }
  });

  it('still tells two accounts apart, which is what the line is for', () => {
    expect(spotifyAccountLabel(PREMIUM_ID)).not.toBe(spotifyAccountLabel(FREE_ID));
  });

  it('shows a real username in full', () => {
    expect(spotifyAccountLabel('SpotifyConnectUserName')).toBe('SpotifyConnectUserName');
    expect(spotifyAccountLabel('eileen.wilson')).toBe('eileen.wilson');
  });

  it('is empty when there is no account, so the tile prints no line', () => {
    expect(spotifyAccountLabel('')).toBe('');
    expect(spotifyAccountLabel(null)).toBe('');
    expect(spotifyAccountLabel(undefined)).toBe('');
    expect(spotifyAccountLabel('   ')).toBe('');
  });

  it('trims, so a padded value does not defeat the check', () => {
    expect(spotifyAccountLabel('  ' + PREMIUM_ID + '  ')).toBe(spotifyAccountLabel(PREMIUM_ID));
  });

  it('keeps the tail stable, so the same account always reads the same', () => {
    expect(spotifyAccountLabel(PREMIUM_ID)).toBe(spotifyAccountLabel(PREMIUM_ID));
    expect(spotifyAccountLabel(PREMIUM_ID)).toBe('…' + PREMIUM_ID.slice(-4));
  });
});
