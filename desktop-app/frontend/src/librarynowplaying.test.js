// #1394: after a radio station, a single track clicked in the Library left the
// station's song on the now-playing line for 5 to 10 s, while "Play folder"
// cleared it at once. The line prints the song only while the location is a
// proxied radio stream, and the location only moved on the next status poll;
// a station play moves it itself, a library play did not. The same report asked
// for the track's artist and album, which a folder's queue line shows and a
// single track never did.
import { describe, it, expect, vi } from 'vitest';
import { beginLibraryNowPlaying, libraryTrackMeta } from './utils.js';
import { fileSource, srcOf } from './test/source.js';

const trackURL = 'http://192.0.2.10:8200/MediaItems/42.flac';

function afterRadio() {
  return {
    nowPlayState: 'PLAY_STATE',
    nowLocation: 'http://192.0.2.1:8888/stream/3',
    nowName: 'Some Station',
    nowTitle: 'Rush - Freewill',
    nowBitrate: 128,
    nowIcon: 'http://logo.example/x.png',
    nowUUID: 'abc',
    spotifyNow: { song: 'x' },
    spotifyRecall: { uri: 'y' },
    spotifyNowGen: 3,
    nowBoxSpotify: { track: 'z' },
    optimisticUntil: 0,
  };
}

describe('beginLibraryNowPlaying', () => {
  it('drops the station song and points the line at the track at once', () => {
    const st = afterRadio();
    beginLibraryNowPlaying(st, { url: trackURL, name: 'Amanda', art: 'http://art/1.jpg' }, 1000);
    expect(st.nowTitle).toBe('');
    expect(st.nowBitrate).toBe(0);
    expect(st.nowLocation).toBe(trackURL);
    expect(st.nowName).toBe('Amanda');
    expect(st.nowIcon).toBe('http://art/1.jpg');
    expect(st.nowPlayState).toBe('BUFFERING_STATE');
    // The same six-second window a preset recall opens.
    expect(st.optimisticUntil).toBe(7000);
  });

  it('forgets a cached Spotify song so it is not printed over the track', () => {
    const st = afterRadio();
    beginLibraryNowPlaying(st, { url: trackURL, name: 'Amanda' }, 1000);
    expect(st.spotifyNow).toBe(null);
    expect(st.spotifyRecall).toBe(null);
    expect(st.nowBoxSpotify).toBe(null);
    expect(st.spotifyNowGen).toBe(4);
  });

  it('opens no optimistic window for a shuffled folder without a known first track', () => {
    const st = afterRadio();
    beginLibraryNowPlaying(st, { url: '', name: 'Third Stage' }, 1000);
    expect(st.nowTitle).toBe('');
    expect(st.nowLocation).toBe('');
    expect(st.nowName).toBe('Third Stage');
    expect(st.optimisticUntil).toBe(0);
  });
});

describe('libraryTrackMeta', () => {
  const rec = { url: trackURL, name: 'Amanda', artist: 'Boston', album: 'Third Stage' };

  it('names artist and album in the shape the queue line uses, while the track plays', () => {
    expect(libraryTrackMeta(rec, trackURL, trackURL)).toBe('Boston — Third Stage');
    // Through the agent relay the speaker reports the wrapped URL.
    const relay = 'http://192.0.2.1:8888/stream/raw?u=' + Buffer.from(trackURL).toString('base64');
    expect(libraryTrackMeta(rec, relay, trackURL)).toBe('Boston — Third Stage');
  });

  it('shows what is known when one of them is missing', () => {
    expect(libraryTrackMeta({ ...rec, album: '' }, trackURL, trackURL)).toBe('Boston');
    expect(libraryTrackMeta({ ...rec, artist: '' }, trackURL, trackURL)).toBe('Third Stage');
  });

  it('is empty once the speaker plays something else, and without a record', () => {
    expect(libraryTrackMeta(rec, 'http://192.0.2.1:8888/stream/3', 'http://192.0.2.1:8888/stream/3')).toBe('');
    expect(libraryTrackMeta(null, trackURL, trackURL)).toBe('');
    expect(libraryTrackMeta({ url: trackURL, name: 'x' }, trackURL, trackURL)).toBe('');
  });
});

describe('wiring', () => {
  // Every library play path moves the line itself and asks the speaker again
  // soon, as a station play does, instead of waiting for the slow poll.
  it('a Library click, a folder play and a Recently-played replay move the line at once', () => {
    for (const name of ['libraryPlay', 'libraryPlayFolder', 'replayLibraryTrack']) {
      const fn = srcOf(name);
      expect(fn, name).toContain('beginLibraryNowPlaying(state');
      expect(fn, name).toContain('deps.renderNowPlayingBar();');
      expect(fn, name).toContain('setTimeout(deps.refreshStatus, 1500);');
    }
  });

  it('a Library click remembers the artist and album of the track', () => {
    const fn = srcOf('libraryPlay');
    expect(fn).toContain("artist: item.artist || ''");
    expect(fn).toContain("album: item.album || ''");
  });

  it('the bar prints them next to the title', () => {
    const bar = srcOf('renderNowPlayingBar');
    expect(bar).toContain('libraryTrackMeta(state.lastLibraryPlay, loc, decodeProxyUrl(loc))');
    expect(bar).toMatch(/displayName = name \? `\$\{name\} · \$\{libMeta\}` : libMeta;/);
  });

  // The real replay, cut out of the view (it imports the Wails runtime), run
  // against a fake speaker: the line moves and the poll is asked again at
  // 1.5 s, but only when the card's speaker is the selected one.
  it('a Recently-played replay moves the line for the selected speaker only', async () => {
    vi.useFakeTimers();
    try {
      const box = { host: '192.0.2.10', port: 17008 };
      const run = async (currentBox) => {
        const st = { ...afterRadio(), currentBox };
        const deps = { renderNowPlayingBar: vi.fn(), refreshStatus: vi.fn() };
        const replay = new Function('PlayURL', 'state', 'beginLibraryNowPlaying', 'deps',
          `${srcOf('replayLibraryTrack')}\nreturn replayLibraryTrack;`)(async () => {}, st, beginLibraryNowPlaying, deps);
        await replay({ url: trackURL, name: 'Amanda', mime: 'audio/flac', durationSec: 245 }, box, 'art.png');
        return { st, deps };
      };

      const same = await run(box);
      expect(same.st.nowTitle).toBe('');
      expect(same.st.nowLocation).toBe(trackURL);
      expect(same.deps.renderNowPlayingBar).toHaveBeenCalledTimes(1);
      expect(same.deps.refreshStatus).not.toHaveBeenCalled();
      vi.advanceTimersByTime(1500);
      expect(same.deps.refreshStatus).toHaveBeenCalledTimes(1);

      const other = await run({ host: '192.0.2.11', port: 17008 });
      expect(other.st.nowTitle).toBe('Rush - Freewill');
      expect(other.deps.renderNowPlayingBar).not.toHaveBeenCalled();
      vi.advanceTimersByTime(1500);
      expect(other.deps.refreshStatus).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  it('main.js hands the bar and the poll to both views', () => {
    const main = fileSource('main.js');
    const lib = main.slice(main.indexOf('initLibraryView({'), main.indexOf('\n', main.indexOf('initLibraryView({')));
    const rec = main.slice(main.indexOf('initRecentView({'), main.indexOf('\n', main.indexOf('initRecentView({')));
    for (const call of [lib, rec]) {
      expect(call).toContain('renderNowPlayingBar');
      expect(call).toContain('refreshStatus');
    }
  });
});
