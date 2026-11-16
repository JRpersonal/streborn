// #1033: a folder played as a queue lost the artist and album its Library rows
// showed. The agent's GET /api/queue now names both per item; the queue line
// shows them for the song the queue is on, in the Library row's own shape.
import { describe, it, expect } from 'vitest';
import { queueTrackMeta } from './utils.js';
import { srcOf } from './test/source.js';

const q = {
  active: true,
  pos: 1,
  items: [
    { title: 'More Than a Feeling', artist: 'Boston', album: 'Boston' },
    { title: 'Amanda', artist: 'Boston', album: 'Third Stage' },
    { title: 'Untitled' },
  ],
};

describe('queueTrackMeta', () => {
  it('names the artist and album of the current song', () => {
    expect(queueTrackMeta(q)).toBe('Boston — Third Stage');
  });

  it('shows what is known when one of them is missing', () => {
    expect(queueTrackMeta({ ...q, items: [{ title: 'x', artist: 'Boston' }], pos: 0 })).toBe('Boston');
    expect(queueTrackMeta({ ...q, items: [{ title: 'x', album: 'Third Stage' }], pos: 0 })).toBe('Third Stage');
  });

  it('is empty without a running queue or metadata', () => {
    expect(queueTrackMeta({ ...q, pos: 2 })).toBe('');
    expect(queueTrackMeta({ ...q, active: false })).toBe('');
    expect(queueTrackMeta({ ...q, pos: -1 })).toBe('');
    expect(queueTrackMeta({ active: true, pos: 0 })).toBe('');
    expect(queueTrackMeta(null)).toBe('');
  });
});

describe('wiring', () => {
  // Since #1874 the now-playing line carries artist and album, so the queue
  // line under it names the position only; both together repeated the names
  // right below each other (#1922).
  it('the now-playing line shows it, the queue line does not repeat it', () => {
    expect(srcOf('renderQueueControls')).not.toContain('queueTrackMeta');
    expect(srcOf('renderQueueControls')).toContain("t('queue.trackOf'");
  });

  it('both folder plays send the album along with the artist', () => {
    const play = srcOf('libraryPlayFolder');
    const save = srcOf('librarySaveFolderAsPreset');
    for (const fn of [play, save]) {
      expect(fn).toContain("artist: it.artist || ''");
      expect(fn).toContain("album: it.album || ''");
    }
  });
});
