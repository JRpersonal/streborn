import { describe, it, expect } from 'vitest';
import {
  optimisticQueue, queueRecallConfirmed, queueReplyMerge, queueSlotActive, queueSlotCard, queueTrackMeta,
} from './utils.js';

// #978: a library folder key (type=queue, no stream URL) lit about ten seconds
// after the click although the agent started the queue within a second.
describe('optimistic library key recall', () => {
  const folderKey = { slot: 3, type: 'queue', stream_url: '' };
  const oldQueue = { active: true, card: 'queue:slot:1', items: [{ artist: 'A', album: 'B' }], pos: 0, shuffle: true };

  it('names the clicked key at once, without the old song', () => {
    const q = optimisticQueue(oldQueue, 3);
    expect(queueSlotCard(q)).toBe('queue:slot:3');
    expect(queueSlotActive(folderKey, q)).toBe(true);
    expect(queueSlotActive({ slot: 1, type: 'queue' }, q)).toBe(false);
    expect(queueTrackMeta(q)).toBe('');
    expect(q.shuffle).toBe(true);
    expect(queueSlotCard(optimisticQueue(null, 2))).toBe('queue:slot:2');
  });

  it('ends the optimistic window once the speaker reports a new location', () => {
    const q = optimisticQueue(oldQueue, 3);
    const prev = 'http://192.0.2.1:8888/stream/1';
    expect(queueRecallConfirmed(3, q, 'http://192.0.2.10:8200/MediaItems/7.mp3', prev)).toBe(true);
    // the old stream, or nothing yet: keep waiting
    expect(queueRecallConfirmed(3, q, prev, prev)).toBe(false);
    expect(queueRecallConfirmed(3, q, '', prev)).toBe(false);
    // the queue names another key, or no click pending
    expect(queueRecallConfirmed(3, optimisticQueue(null, 4), 'http://x/y.mp3', prev)).toBe(false);
    expect(queueRecallConfirmed(null, q, 'http://x/y.mp3', prev)).toBe(false);
  });

  it('keeps the optimistic card against a reply that predates the queue start', () => {
    const cur = optimisticQueue(oldQueue, 3);
    expect(queueReplyMerge(cur, oldQueue, 3)).toBe(cur);
    expect(queueReplyMerge(cur, null, 3)).toBe(cur);
    const fresh = { active: true, card: 'queue:slot:3', items: [], pos: 0 };
    expect(queueReplyMerge(cur, fresh, 3)).toBe(fresh);
    // nothing pending: the reply always wins
    expect(queueReplyMerge(cur, oldQueue, null)).toBe(oldQueue);
    expect(queueReplyMerge(cur, null, null)).toBe(null);
  });
});

// #1525: the card of a queue key 6 started kept naming key 6 after the key was
// given another folder, and the tile stayed lit for the old one. An agent that
// checks the key sends slot_checked and names the key as card_slot only while
// it still holds the tracks that play.
describe('queueSlotActive with a checked card', () => {
  const key = (slot) => ({ slot, type: 'queue', stream_url: '' });

  it('does not light a key on a stale card', () => {
    const q = { active: true, card: 'queue:slot:6', slot_checked: true, items: [], pos: 0 };
    expect(queueSlotActive(key(6), q)).toBe(false);
  });

  it('lights the card key while it holds the folder, and the saved key next to it', () => {
    const q = { active: true, card: 'queue:slot:6', card_slot: 6, slot: 2, slot_checked: true };
    expect(queueSlotActive(key(6), q)).toBe(true);
    expect(queueSlotActive(key(2), q)).toBe(true);
    expect(queueSlotActive(key(3), q)).toBe(false);
    expect(queueSlotActive(key(6), { ...q, active: false })).toBe(false);
  });

  it('lights the key that holds the old folder when the card went stale', () => {
    const q = { active: true, card: 'queue:slot:6', slot: 2, slot_checked: true };
    expect(queueSlotActive(key(2), q)).toBe(true);
    expect(queueSlotActive(key(6), q)).toBe(false);
  });

  it('keeps the card match for an older agent', () => {
    expect(queueSlotActive(key(6), { active: true, card: 'queue:slot:6' })).toBe(true);
  });

  it('lights a clicked key at once, and only that key, while another folder plays', () => {
    const prev = { active: true, card: 'queue:slot:6', card_slot: 6, slot: 2, slot_checked: true };
    const q = optimisticQueue(prev, 4);
    expect(queueSlotActive(key(4), q)).toBe(true);
    expect(queueSlotActive(key(6), q)).toBe(false);
    expect(queueSlotActive(key(2), q)).toBe(false);
    const stale = optimisticQueue({ active: true, card: 'queue:slot:6', slot_checked: true }, 4);
    expect(queueSlotActive(key(4), stale)).toBe(true);
  });
});
