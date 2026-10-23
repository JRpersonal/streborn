// A music-library track replayed from Recently played showed no progress bar,
// while the same track started from the Library did (#845). The speaker only
// reports a total when PlayURL hands it the length, and the replay always sent
// 0. The box now keeps the length on the history entry; these tests pin that
// the card carries it and the replay passes it on.
import { describe, it, expect, vi } from 'vitest';
import { srcOf } from './test/source.js';

// The real functions, cut out of the view (it imports the Wails runtime).
const BOX = { host: '192.0.2.10', port: 17008 };

describe('replaying a Recently-played library track', () => {
  // No selected speaker: the now-playing move the replay also does (#1394,
  // pinned in librarynowplaying.test.js) stays out of these calls.
  // eslint-disable-next-line no-new-func
  const makeReplay = (PlayURL) => new Function('PlayURL', 'state', 'beginLibraryNowPlaying', 'deps',
    `${srcOf('replayLibraryTrack')}\nreturn replayLibraryTrack;`)(PlayURL, { currentBox: null }, () => {}, {});

  it('hands the stored length to the speaker', async () => {
    const play = vi.fn().mockResolvedValue(undefined);
    await makeReplay(play)({ url: 'http://nas/a.flac', name: 'Song', mime: 'audio/flac', durationSec: 245 }, BOX, 'art.png');
    expect(play).toHaveBeenCalledWith('192.0.2.10', 17008, 'http://nas/a.flac', 'Song', 'art.png', '', 'audio/flac', '', '', 245);
  });

  it('still plays a card recorded before the length was kept', async () => {
    const play = vi.fn().mockResolvedValue(undefined);
    await makeReplay(play)({ url: 'http://nas/a.mp3', name: 'Old', mime: 'audio/mpeg' }, BOX, '');
    expect(play).toHaveBeenCalledWith('192.0.2.10', 17008, 'http://nas/a.mp3', 'Old', '', '', 'audio/mpeg', '', '', 0);
  });
});

describe('grouping history into cards', () => {
  // eslint-disable-next-line no-new-func
  const group = new Function(`${srcOf('groupRecentCards')}\nreturn groupRecentCards;`)();

  it('keeps the length on the card', () => {
    const cards = group([
      { _boxKey: 'b', cardKey: 'http://nas/a.flac', source: 'upnp', cardURL: 'http://nas/a.flac', mime: 'audio/flac', durationSec: 245, ts: '2' },
    ]);
    expect(cards[0].durationSec).toBe(245);
  });

  it('fills the length from an older row of the same card', () => {
    const cards = group([
      { _boxKey: 'b', cardKey: 'k', source: 'upnp', cardURL: 'http://nas/a.flac', track: 'B', ts: '3' },
      { _boxKey: 'b', cardKey: 'k', source: 'upnp', cardURL: 'http://nas/a.flac', track: 'A', durationSec: 180, ts: '2' },
    ]);
    expect(cards).toHaveLength(1);
    expect(cards[0].durationSec).toBe(180);
  });
});
