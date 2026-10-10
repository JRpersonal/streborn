// #1190: a preset key holding a music-library album or folder was never shown
// as selected while it played.
//
// Such a key (type "queue") stores no stream URL: the box plays one media-server
// track URL after another, so none of the location-based matches in
// renderPresets can name the key. The agent already says which key started the
// queue: GET /api/queue reports card "queue:slot:<n>" while it plays.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { queueSlotActive, queueSlotCard } from './utils.js';

const main = readFileSync(new URL('./main.js', import.meta.url), 'utf8');

const key = (slot, type = 'queue') => ({ slot, type, stream_url: '' });

describe('queueSlotActive', () => {
  const q = { active: true, card: 'queue:slot:4', pos: 2 };

  it('lights the key whose slot the queue card names', () => {
    expect(queueSlotActive(key(4), q)).toBe(true);
    expect(queueSlotActive(key(3), q)).toBe(false);
  });

  it('needs a running queue', () => {
    expect(queueSlotActive(key(4), { ...q, active: false })).toBe(false);
    expect(queueSlotActive(key(4), null)).toBe(false);
    expect(queueSlotActive(key(4), { active: true })).toBe(false);
  });

  it('ignores a folder started from the library or Recently played', () => {
    // Same queue, but the card names a media-server folder, not a key.
    expect(queueSlotActive(key(4), { active: true, card: 'queue:uuid:abc:64$1' })).toBe(false);
  });

  it('only applies to library keys', () => {
    expect(queueSlotActive(key(4, 'radio'), q)).toBe(false);
    expect(queueSlotActive(null, q)).toBe(false);
  });

  it('does not let slot 1 match a card for slot 12', () => {
    expect(queueSlotActive(key(1), { active: true, card: 'queue:slot:12' })).toBe(false);
  });
});

describe('queueSlotCard', () => {
  it('returns the card only while it names a key', () => {
    expect(queueSlotCard({ active: true, card: 'queue:slot:2' })).toBe('queue:slot:2');
    expect(queueSlotCard({ active: false, card: 'queue:slot:2' })).toBe('');
    expect(queueSlotCard({ active: true, card: 'queue:uuid:x:1' })).toBe('');
    expect(queueSlotCard(undefined)).toBe('');
  });
});

describe('the preset grid uses it', () => {
  it('as one of the ways a key counts as playing', () => {
    // Outside the location gate, so the optimistic card of a just-clicked
    // folder key (empty location) lights it at once (#978).
    const at = main.indexOf('const queueLit = ');
    expect(at).toBeGreaterThan(-1);
    expect(main.slice(at, at + 200)).toContain('queueSlotActive(p, state.queue)');
    expect(main.slice(at, at + 400)).toContain('const baseActive = p && (queueLit ||');
  });

  it('and repaints the keys when the queue card changes', () => {
    // The card arrives with the queue read, not the status poll, so without the
    // repaint the highlight waited for an unrelated status change.
    const fn = main.slice(main.indexOf('async function refreshQueue()'), main.indexOf('function renderQueueControls'));
    expect(fn).toContain('queueSlotCard(state.queue) !== before');
    expect(fn).toContain('renderPresets()');
  });
});
