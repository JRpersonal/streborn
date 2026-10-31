// "Playing on Kitchen" means Kitchen plays what was sent (UI 2.0 preview,
// Recently played; views/library.js playConfirmed). A recall the speaker
// refused or ignored leaves the old item playing on, and that PLAY_STATE must
// not read as a confirmation. Without expect, waitForPlayState keeps its old
// meaning for the classic callers: any PLAY_STATE counts.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

const status = vi.fn();
vi.mock('./api.js', () => ({
  ProbeTrackDelivery: vi.fn(),
  ListMediaServers: vi.fn(),
  LibraryRefusedServers: vi.fn(),
  BrowseLibrary: vi.fn(),
  AddMediaServerByURL: vi.fn(),
  RemoveManualMediaServer: vi.fn(),
  PlayURL: vi.fn(),
  StartQueue: vi.fn(),
  SaveLibraryPreset: vi.fn(),
  SaveFolderPreset: vi.fn(),
  Status: (...a) => status(...a),
  EnableBoxMediaServer: vi.fn(),
  boxFetch: vi.fn(),
}));

const { playConfirmed, statusSig, waitForPlayState } = await import('./views/library.js');
const { state } = await import('./state.js');

const xml = ({ ps = 'PLAY_STATE', src = 'INTERNET_RADIO', loc = 'http://example.com/a', name = 'Station A' } = {}) =>
  `<nowPlaying deviceID="AABBCCDDEEFF" source="${src}"><ContentItem source="${src}" location="${loc}">`
  + `<itemName>${name}</itemName></ContentItem><playStatus>${ps}</playStatus></nowPlaying>`;

const A = xml();
const B = xml({ src: 'SPOTIFY', loc: 'spotify:playlist:demo', name: 'Morning &amp; Coffee' });

describe('statusSig', () => {
  it('is the source, the location and the item name, entities read', () => {
    expect(statusSig(A)).toBe('INTERNET_RADIO|http://example.com/a|Station A');
    expect(statusSig(B)).toBe('SPOTIFY|spotify:playlist:demo|Morning & Coffee');
    expect(statusSig('')).toBe('||');
  });
});

describe('playConfirmed', () => {
  it('the item that played before the press, unchanged, is not a confirmation', () => {
    expect(playConfirmed(A, { before: statusSig(A), url: 'spotify:playlist:other', name: 'Other' })).toBe(false);
  });

  it('a new item is', () => {
    expect(playConfirmed(B, { before: statusSig(A) })).toBe(true);
  });

  it('the item that was sent is, also when it already played before', () => {
    expect(playConfirmed(A, { before: statusSig(A), url: 'http://example.com/a' })).toBe(true);
    expect(playConfirmed(B, { before: statusSig(B), name: 'Morning & Coffee' })).toBe(true);
  });

  it('a pass through another state since the press is', () => {
    expect(playConfirmed(A, { before: statusSig(A) }, true)).toBe(true);
  });

  it('nothing known from before and nothing matching is not', () => {
    expect(playConfirmed(A, { before: '', name: 'Other' })).toBe(false);
  });
});

describe('waitForPlayState', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    status.mockReset();
    state.currentBox = { host: '192.0.2.10', port: 8888 };
  });
  afterEach(() => {
    vi.useRealTimers();
    state.currentBox = null;
  });

  const box = { host: '192.0.2.10', port: 8888 };

  it('without expect, any PLAY_STATE is playing (the classic callers)', async () => {
    status.mockResolvedValue(A);
    const p = waitForPlayState(box, Date.now() + 26000);
    await vi.advanceTimersByTimeAsync(2000);
    await expect(p).resolves.toBe('playing');
  });

  it('with expect, the old item playing on runs into the timeout', async () => {
    status.mockResolvedValue(A);
    const p = waitForPlayState(box, Date.now() + 7000, { before: statusSig(A), url: 'spotify:playlist:demo', name: 'Other' });
    await vi.advanceTimersByTimeAsync(8000);
    await expect(p).resolves.toBe('timeout');
  });

  it('with expect, the new item is playing', async () => {
    status.mockResolvedValueOnce(A).mockResolvedValue(B);
    const p = waitForPlayState(box, Date.now() + 26000, { before: statusSig(A) });
    await vi.advanceTimersByTimeAsync(4000);
    await expect(p).resolves.toBe('playing');
  });

  it('with expect, a stop and a start of the same item is playing', async () => {
    status.mockResolvedValueOnce(xml({ ps: 'BUFFERING_STATE' })).mockResolvedValue(A);
    const p = waitForPlayState(box, Date.now() + 26000, { before: statusSig(A) });
    await vi.advanceTimersByTimeAsync(4000);
    await expect(p).resolves.toBe('playing');
  });
});
