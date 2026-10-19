// Recently played lists each station once, at its newest listening (Bernard's
// wish, 2026-10-19). The agent keeps one session per station per speaker; the
// app does the same across speakers and for a speaker whose agent still sends
// the older history, and a delete removes the station from every speaker the
// card stands for.
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { state } from './state.js';
import { groupRecentCards, deleteRecentStation, cardIsPlaying } from './views/recent.js';

const A = { deviceID: 'DEV-A', host: '192.0.2.10', port: 8888 };
const B = { deviceID: 'DEV-B', host: '192.0.2.11', port: 8888 };

function row(box, cardKey, track, ts, extra = {}) {
  return { _box: box, _boxKey: box.deviceID, _boxName: box.deviceID === 'DEV-A' ? 'Kitchen' : 'Bedroom', source: 'radio', cardKey, cardName: cardKey.toUpperCase(), track, ts, ...extra };
}

// newestFirst mirrors loadRecentCards: every box's rows merged, newest first.
function newestFirst(rows) {
  return rows.slice().sort((a, b) => (a.ts < b.ts ? 1 : a.ts > b.ts ? -1 : 0));
}

describe('one card per station', () => {
  it('collapses an older session of the same station on one speaker (older agent)', () => {
    const cards = groupRecentCards(newestFirst([
      row(A, 'swr3', 'Epic', 't1'),
      row(A, 'swr3', 'Yellow', 't2'),
      row(A, '1live', 'Other', 't3'),
      row(A, 'swr3', 'Reborn', 't4'),
    ]));
    expect(cards.map((c) => c.cardKey)).toEqual(['swr3', '1live']);
    // Only the newest session's tracks: the older Epic/Yellow are gone.
    expect(cards[0].tracks.map((t) => t.track)).toEqual(['Reborn']);
    expect(cards[0].ts).toBe('t4');
  });

  it('does not split a session when another speaker played in between', () => {
    const cards = groupRecentCards(newestFirst([
      row(A, 'swr3', 'Epic', 't1'),
      row(B, 'dlf', 'News', 't2'),
      row(A, 'swr3', 'Yellow', 't3'),
    ]));
    expect(cards.map((c) => c.cardKey)).toEqual(['swr3', 'dlf']);
    expect(cards[0].tracks.map((t) => t.track)).toEqual(['Yellow', 'Epic']);
  });

  it('shows a station heard on two speakers once, owned by the newer one', () => {
    const cards = groupRecentCards(newestFirst([
      row(B, 'swr3', 'Old', 't1'),
      row(A, 'swr3', 'New', 't2'),
    ]));
    expect(cards).toHaveLength(1);
    expect(cards[0].boxKey).toBe('DEV-A');
    expect(cards[0].tracks.map((t) => t.track)).toEqual(['New']);
    expect(cards[0].members.map((m) => [m.boxKey, m.ts])).toEqual([['DEV-A', 't2'], ['DEV-B', 't1']]);
  });
});

describe('deleting a card', () => {
  it('removes the station from every speaker it stands for', async () => {
    const [card] = groupRecentCards(newestFirst([
      row(B, 'swr3', 'Old', 't1'),
      row(A, 'swr3', 'New', 't2'),
    ]));
    const del = vi.fn().mockResolvedValue(undefined);
    await deleteRecentStation(card, del, null);
    expect(del.mock.calls).toEqual([
      ['192.0.2.10', 8888, 'swr3', 't2'],
      ['192.0.2.11', 8888, 'swr3', 't1'],
    ]);
  });

  it('still tries the other speakers when one fails, then reports it', async () => {
    const [card] = groupRecentCards(newestFirst([
      row(B, 'swr3', 'Old', 't1'),
      row(A, 'swr3', 'New', 't2'),
    ]));
    const del = vi.fn().mockRejectedValueOnce(new Error('offline')).mockResolvedValue(undefined);
    await expect(deleteRecentStation(card, del, null)).rejects.toThrow('offline');
    expect(del).toHaveBeenCalledTimes(2);
  });
});

describe('now-playing mark on a shared card', () => {
  beforeEach(() => {
    state.nowName = 'SWR3';
    state.nowLocation = '';
    state.queue = null;
  });

  it('lights up when the current speaker is any of the card speakers', () => {
    const [card] = groupRecentCards(newestFirst([
      row(B, 'swr3', 'Old', 't1'),
      row(A, 'swr3', 'New', 't2'),
    ]));
    state.currentBox = B;
    expect(cardIsPlaying(card)).toBe(true);
  });
});
