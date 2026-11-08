import { describe, it, expect } from 'vitest';
import {
  optimisticQueue, queueRecallConfirmed, queueReplyMerge, queueSlotActive, queueSlotCard, queueTrackLocation,
  queueTrackMeta,
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
    // The agent's queue for key 3, with its tracks.
    const q = { active: true, card: 'queue:slot:3', card_slot: 3, items: [{ title: 'T1' }], pos: 0 };
    const prev = 'http://192.0.2.1:8888/stream/1';
    const upnp = { source: 'UPNP' };
    expect(queueRecallConfirmed(3, q, 'http://192.0.2.10:8200/MediaItems/7.mp3', prev, upnp)).toBe(true);
    // a library file the agent wraps is a queue track too
    expect(queueRecallConfirmed(3, q, 'http://127.0.0.1:8888/stream/raw?u=aHR0cHM6Ly94', prev, upnp)).toBe(true);
    // the old stream, or nothing yet: keep waiting
    expect(queueRecallConfirmed(3, q, prev, prev, upnp)).toBe(false);
    expect(queueRecallConfirmed(3, q, '', prev, upnp)).toBe(false);
    // the queue names another key, or no click pending
    expect(queueRecallConfirmed(3, { ...q, card: 'queue:slot:4', card_slot: 4 }, 'http://x/y.mp3', prev, upnp)).toBe(false);
    expect(queueRecallConfirmed(null, q, 'http://x/y.mp3', prev, upnp)).toBe(false);
    // only the app's optimistic card so far, no tracks from the agent
    expect(queueRecallConfirmed(3, optimisticQueue(oldQueue, 3), 'http://x/y.mp3', prev, upnp)).toBe(false);
  });

  // #1767: key 3 pressed on a sleeping speaker. Before the click the speaker
  // reported nothing; while it wakes the firmware resumes the station it played
  // last for a few seconds, then the agent's queue takes over on UPNP.
  it('does not take the station a waking speaker resumes for the queue', () => {
    const q = { active: true, card: 'queue:slot:3', card_slot: 3, items: [{ title: 'T1' }], pos: 0 };
    const file = 'http://192.0.2.10:8200/MediaItems/7.mp3';
    // the press has not returned yet: nothing counts
    expect(queueRecallConfirmed(3, q, file, '', { source: 'UPNP', inFlight: true })).toBe(false);
    // the firmware's own resume: a radio key's proxy slot, a native station,
    // a radio source, Spotify
    expect(queueRecallConfirmed(3, q, 'http://127.0.0.1:8888/stream/2', '', { source: 'UPNP' })).toBe(false);
    expect(queueRecallConfirmed(3, q, 'http://127.0.0.1:8888/stream/2', '', { source: 'LOCAL_INTERNET_RADIO' })).toBe(false);
    expect(queueRecallConfirmed(3, q, file, '', { source: 'TUNEIN' })).toBe(false);
    expect(queueRecallConfirmed(3, q, file, '', {})).toBe(false);
    expect(queueRecallConfirmed(3, q, 'http://127.0.0.1:8888/spotify/stream-3.ogg', '', { source: 'UPNP' })).toBe(false);
    // the queue's first track, after the press returned
    expect(queueRecallConfirmed(3, q, file, '', { source: 'UPNP' })).toBe(true);
  });

  it('knows which locations a queue track can have', () => {
    expect(queueTrackLocation('http://192.0.2.10:8200/MediaItems/7.mp3')).toBe(true);
    expect(queueTrackLocation('http://127.0.0.1:8888/stream/raw?u=aHR0cHM6Ly94')).toBe(true);
    expect(queueTrackLocation('http://127.0.0.1:8888/stream/4')).toBe(false);
    expect(queueTrackLocation('http://127.0.0.1:8888/stream/4?x=1')).toBe(false);
    expect(queueTrackLocation('http://127.0.0.1:8888/spotify/stream-1.ogg')).toBe(false);
    expect(queueTrackLocation('')).toBe(false);
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

  // #1525: key 6 clicked while the old folder of key 6 still plays. The agent's
  // reply still carries the stale card, but its checked keys do not name key 6:
  // taking it darkened the clicked key until the new queue started.
  it('keeps the optimistic state against a stale checked card naming the clicked key', () => {
    const playing = { active: true, card: 'queue:slot:6', slot_checked: true, card_slot: 0, slot: 0, items: [], pos: 0 };
    const cur = optimisticQueue(playing, 6);
    const stale = { card: 'queue:slot:6', slot_checked: true, slot: 0 };
    expect(queueReplyMerge(cur, stale, 6)).toBe(cur);
    expect(queueReplyMerge(cur, { ...stale, card_slot: 0 }, 6)).toBe(cur);
    // the new queue started: the agent names key 6 again
    const started = { ...playing, card_slot: 6 };
    expect(queueReplyMerge(cur, started, 6)).toBe(started);
    const saved = { ...playing, slot: 6 };
    expect(queueReplyMerge(cur, saved, 6)).toBe(saved);
    // nothing pending: the reply always wins
    expect(queueReplyMerge(cur, stale, null)).toBe(stale);
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
