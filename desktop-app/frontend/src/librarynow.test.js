// Discussion #1874: a folder of the music library played on a SoundTouch 20.
// The player line showed the bare title ("Hobo  playing"): artist and album
// were only on the queue line, which the UI 2.0 player folds away under "More
// controls", and they flashed up for half a second when a track started (the
// lone file of the click before) and vanished again. After a skip the line
// kept the previous title for 1 to 10 s, because it printed the speaker's own
// now-playing, which lags. In the folder list "Playing on X" stayed on the
// track that had been clicked while the folder played on, and after another
// tab and back no row was marked at all: the marker was the one-off answer to
// a click, not the track that plays.
import { describe, it, expect } from 'vitest';
import { libraryRowStatus, nowLine, playingLibraryURLs, queueNowItem, queueNowURL } from './librarynow.js';
import { beginLibraryNowPlaying } from './utils.js';
import { srcOf } from './test/source.js';

const A = 'http://192.0.2.10:8200/MediaItems/1.mp3';
const B = 'http://192.0.2.10:8200/MediaItems/2.mp3';

const folder = {
  active: true,
  pos: 1,
  url: B,
  items: [
    { title: 'From Shark To Haggis', artist: 'Beggars Opera', album: 'Pathfinder' },
    { title: 'Hobo', artist: 'Beggars Opera', album: 'Pathfinder' },
  ],
};

const t = (key, vars) => `${key}:${vars ? vars.name : ''}`;

describe('queueNowItem', () => {
  it('is the track the agent handed the speaker, even while the speaker still names the one before', () => {
    // The speaker lags: its location is still track A, the queue is on B.
    expect(queueNowItem(folder, { location: A })).toBe(folder.items[1]);
  });

  it('is the queue track for an HTTPS server wrapped in the stream proxy', () => {
    const raw = 'http://127.0.0.1:8888/stream/raw?u=aHR0cHM6Ly9leGFtcGxlLm9yZy8xLmZsYWM';
    expect(queueNowItem(folder, { location: raw })).toBe(folder.items[1]);
  });

  it('is nothing while the speaker plays something else', () => {
    for (const location of [
      'http://127.0.0.1:8888/stream/3',
      'http://127.0.0.1:8888/spotify/stream?slot=2',
      'http://127.0.0.1:8888/station?data=eyJ9',
      '/playback/container/c3BvdGlmeTpwbGF5bGlzdA==',
    ]) {
      expect(queueNowItem(folder, { location }), location).toBe(null);
    }
  });

  it('is nothing right after the app started something itself', () => {
    expect(queueNowItem(folder, { location: A, optimistic: true })).toBe(null);
  });

  it('is nothing without a running queue or a position', () => {
    expect(queueNowItem(null)).toBe(null);
    expect(queueNowItem({ ...folder, active: false })).toBe(null);
    expect(queueNowItem({ ...folder, pos: -1 })).toBe(null);
    expect(queueNowItem({ ...folder, pos: 9 })).toBe(null);
    expect(queueNowItem({ active: true, pos: 0 })).toBe(null);
  });
});

describe('queueNowURL', () => {
  it('reads the agent field, empty from an older agent', () => {
    expect(queueNowURL(folder)).toBe(B);
    expect(queueNowURL({ ...folder, url: undefined })).toBe('');
    expect(queueNowURL(null)).toBe('');
  });
});

describe('nowLine', () => {
  it('puts artist and album after the title, the shape of a single track', () => {
    expect(nowLine('Hobo', 'Beggars Opera — Pathfinder')).toBe('Hobo · Beggars Opera — Pathfinder');
    expect(nowLine('Hobo', '')).toBe('Hobo');
    expect(nowLine('', 'Beggars Opera')).toBe('Beggars Opera');
    expect(nowLine('', '')).toBe('');
  });
});

describe('playingLibraryURLs', () => {
  it('names the queue track the agent reports, not the lagging location', () => {
    expect(playingLibraryURLs({ queue: folder, location: A, decodedLocation: A, playState: 'PLAY_STATE' })).toEqual([B]);
  });

  it('falls back to the speaker location for an agent without the field', () => {
    const old = { ...folder, url: undefined };
    expect(playingLibraryURLs({ queue: old, location: A, decodedLocation: A, playState: 'PLAY_STATE' })).toEqual([A]);
  });

  it('names a single track by the location, and the file inside a proxy wrapper', () => {
    const raw = 'http://127.0.0.1:8888/stream/raw?u=x';
    expect(playingLibraryURLs({ location: A, decodedLocation: A, playState: 'PLAY_STATE' })).toEqual([A]);
    expect(playingLibraryURLs({ location: raw, decodedLocation: B, playState: 'BUFFERING_STATE' })).toEqual([raw, B]);
  });

  it('is empty while nothing plays', () => {
    for (const playState of ['PAUSE_STATE', 'STOP_STATE', '']) {
      expect(playingLibraryURLs({ queue: folder, location: A, decodedLocation: A, playState }), playState).toEqual([]);
    }
  });
});

describe('libraryRowStatus', () => {
  it('marks the row that plays, on the selected speaker', () => {
    expect(libraryRowStatus(null, true, 'Kitchen', t)).toEqual({ tone: 'ok', text: 'ui2.library.playingOn:Kitchen' });
    expect(libraryRowStatus(null, false, 'Kitchen', t)).toBe(null);
  });

  it('keeps the answer to a press first: a click still starting, a key just saved', () => {
    const busy = { tone: 'busy', text: 'Starting on Kitchen' };
    const saved = { tone: 'ok', text: 'On key 3 of Kitchen' };
    expect(libraryRowStatus(busy, true, 'Kitchen', t)).toBe(busy);
    expect(libraryRowStatus(saved, true, 'Kitchen', t)).toBe(saved);
  });

  it('a failure stays until the row plays after all', () => {
    const err = { tone: 'error', text: 'did not start' };
    expect(libraryRowStatus(err, false, 'Kitchen', t)).toBe(err);
    expect(libraryRowStatus(err, true, 'Kitchen', t).tone).toBe('ok');
  });
});

describe('beginLibraryNowPlaying', () => {
  it('drops the queue reply of what the play replaces', () => {
    const st = { queue: folder, spotifyNowGen: 0 };
    beginLibraryNowPlaying(st, { url: '', name: 'Pathfinder' }, 1000);
    expect(st.queue).toBe(null);
  });
});

describe('wiring', () => {
  it('the player line names a running folder track from the queue, with artist and album', () => {
    const bar = srcOf('renderNowPlayingBar');
    expect(bar).toContain('queueNowItem(state.queue, { location: loc, optimistic })');
    expect(bar).toContain('displayName = nowLine(queueItem.title || name, queueTrackMeta(state.queue));');
    // Spotify and a station with a live title still come first.
    expect(bar.indexOf('} else if (queueItem) {')).toBeGreaterThan(bar.indexOf('proxiedRadioPlaying(loc) && state.nowTitle'));
  });

  it('the player line hands the Library what plays', () => {
    const bar = srcOf('renderNowPlayingBar');
    expect(bar).toContain('deps.libraryNowPlaying(playingLibraryURLs({');
  });

  it('a queue reply and a skip repaint the line at once', () => {
    expect(srcOf('refreshQueue')).toContain('renderNowPlayingBar();');
    expect(srcOf('queueAction')).toMatch(/await fn\(box\.host, box\.port\);[\s\S]*refreshQueue\(\);/);
    expect(srcOf('action')).toContain("if (kind === 'next' || kind === 'prev') refreshQueue();");
  });

  it('the Library rows draw the live marker and follow it in place', () => {
    expect(srcOf('ui2FolderRowsHTML')).toContain('status: rowStatusFor(statusKey(it)),');
    expect(srcOf('ui2SearchHTML')).toContain('status: rowStatusFor(statusKey(it)),');
    expect(srcOf('rowStatusFor')).toContain('libraryRowStatus(stored,');
    expect(srcOf('libraryNowPlaying')).toContain('paintLibStatus(key)');
  });

  it('a click no longer leaves a fixed "Playing on" behind on its row', () => {
    const fn = srcOf('libraryPlay');
    expect(fn).not.toContain("t('ui2.library.playingOn'");
    expect(fn).toContain('if (!r || r.ok) setLibStatus(key, null);');
  });
});
