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
