// The Recently-played "now playing" badge belongs to the CURRENT speaker only.
//
// Field evidence (#710, 2026-08-24, three-ST10 bundle): speaker A (the app's
// current box) played "Exclusively Rush"; the user scoped the Recently-played
// view to speaker B, whose history also contains a Rush card from an earlier
// session. state.nowName describes speaker A, but cardIsPlaying compared every
// card against it regardless of the card's box, so speaker B's Rush card wore
// the green badge while B was actually playing "101 SMOOTH JAZZ".
import { describe, it, expect, beforeEach } from 'vitest';
import { state } from './state.js';
import { cardIsPlaying } from './views/recent.js';

const boxA = { deviceID: 'DEV-A', host: '192.0.2.10', port: 8888 };
const boxB = { deviceID: 'DEV-B', host: '192.0.2.11', port: 8888 };

function radioCard(boxKey, name) {
  return { boxKey, source: 'radio', name, url: 'http://example.com/' + name };
}

describe('cardIsPlaying box scoping', () => {
  beforeEach(() => {
    state.currentBox = boxA;
    state.nowName = 'Exclusively Rush';
    state.nowLocation = '';
    state.nowSpotifySlot = null;
  });

  it('matches a card on the current speaker by now-playing name', () => {
    expect(cardIsPlaying(radioCard('DEV-A', 'Exclusively Rush'))).toBe(true);
  });

  it('never matches a card from another speaker (the #710 wrong badge)', () => {
    expect(cardIsPlaying(radioCard('DEV-B', 'Exclusively Rush'))).toBe(false);
  });

  it('matches nothing when no speaker is current', () => {
    state.currentBox = null;
    expect(cardIsPlaying(radioCard('DEV-A', 'Exclusively Rush'))).toBe(false);
  });

  it('keys host:port speakers without a deviceID consistently', () => {
    state.currentBox = { host: '192.0.2.11', port: 8888 };
    state.nowName = 'X';
    expect(cardIsPlaying(radioCard('192.0.2.11:8888', 'X'))).toBe(true);
    expect(cardIsPlaying(radioCard('192.0.2.10:8888', 'X'))).toBe(false);
  });

  it('badges only the newest of several same-station cards (#761)', () => {
    // A station played several times sits in history as several same-name
    // cards; cardIsPlaying (name-matched) is true for every one of them, so the
    // render must pick a single card. It uses the first match, and cards are
    // newest-first, so exactly one card, the running session, lights up.
    const cards = [
      radioCard('DEV-A', 'Exclusively Rush'), // newest
      radioCard('DEV-A', 'KLove'),
      radioCard('DEV-A', 'Exclusively Rush'),
      radioCard('DEV-A', 'Exclusively Rush'), // oldest
    ];
    expect(cards.map(cardIsPlaying).filter(Boolean).length).toBe(3);
    const playingIdx = cards.findIndex(cardIsPlaying);
    expect(playingIdx).toBe(0);
  });
});
