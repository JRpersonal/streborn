// #1033: a folder played as a queue lost the artist and album its Library rows
// showed. The agent's GET /api/queue now names both per item; the queue line
// shows them for the song the queue is on, in the Library row's own shape.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { queueTrackMeta } from './utils.js';

const main = readFileSync(new URL('./main.js', import.meta.url), 'utf8');
const library = readFileSync(new URL('./views/library.js', import.meta.url), 'utf8');

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
  it('the queue line shows it', () => {
    const fn = main.slice(main.indexOf('function renderQueueControls'), main.indexOf('function resetNowPlaying'));
    expect(fn).toContain('queueTrackMeta(q)');
  });

  it('both folder plays send the album along with the artist', () => {
    const play = library.slice(library.indexOf('async function libraryPlayFolder'), library.indexOf('function librarySaveFolderAsPreset'));
    const save = library.slice(library.indexOf('function librarySaveFolderAsPreset'));
    for (const fn of [play, save]) {
      expect(fn).toContain("artist: it.artist || ''");
      expect(fn).toContain("album: it.album || ''");
    }
  });
});
