// #1526: the live-title poll ended silently on every rediscovery. It compared
// state.currentBox by object, and discovery replaces that object with a fresh
// record for the same speaker, so the line froze on the last title it had.
// Run for real against a mocked StreamTitleInfo.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

const api = vi.hoisted(() => ({
  StreamTitleInfo: vi.fn(),
  StreamBitrate: vi.fn(),
  SpotifyBitrate: vi.fn(),
}));
vi.mock('../../api.js', () => api);
vi.mock('./nowplaying.js', () => ({ renderNowPlayingBar: () => {} }));
vi.mock('./presettiles.js', () => ({ renderPresets: () => {} }));
vi.mock('./presets.js', () => ({
  decodeProxyUrl: (s) => s,
  isMeasurableStreamPreset: () => false,
  setPresetIfUnchanged: () => {},
}));

const A = { host: '192.0.2.10', port: 8888, deviceID: 'AAA', kind: 'STR' };

let state;
let live;

beforeEach(async () => {
  vi.useFakeTimers();
  vi.resetModules();
  api.StreamTitleInfo.mockReset();
  ({ state } = await import('../../state.js'));
  state.currentBox = A;
  state.nowLocation = 'http://192.0.2.10:8888/stream/1';
  state.nowPlayState = 'PLAY_STATE';
  state.nowTitle = '';
  state.nowTitleSwapped = false;
  live = await import('./live.js');
});

afterEach(() => {
  vi.useRealTimers();
});

describe('scheduleLiveTitle', () => {
  it('keeps polling when discovery swaps in a fresh record for the same speaker', async () => {
    api.StreamTitleInfo.mockResolvedValue({ title: 'First - Song', swapped: false });
    live.scheduleLiveTitle();
    await vi.advanceTimersByTimeAsync(1200);
    expect(state.nowTitle).toBe('First - Song');

    // Rediscovery: same deviceID, new object, here even a new address.
    state.currentBox = { ...A, host: '192.0.2.20' };
    api.StreamTitleInfo.mockResolvedValue({ title: '', swapped: false });
    await vi.advanceTimersByTimeAsync(12000);
    expect(state.nowTitle).toBe('');
    expect(api.StreamTitleInfo).toHaveBeenLastCalledWith('192.0.2.20', 8888);
  });

  it('stops for a different speaker', async () => {
    api.StreamTitleInfo.mockResolvedValue({ title: 'First - Song' });
    live.scheduleLiveTitle();
    await vi.advanceTimersByTimeAsync(1200);
    state.currentBox = { ...A, deviceID: 'BBB', host: '192.0.2.11' };
    const calls = api.StreamTitleInfo.mock.calls.length;
    await vi.advanceTimersByTimeAsync(30000);
    expect(api.StreamTitleInfo.mock.calls.length).toBe(calls);
  });

  it('stops on a stop and does not bring a late title back', async () => {
    let answer;
    api.StreamTitleInfo.mockImplementation(() => new Promise((r) => { answer = r; }));
    live.scheduleLiveTitle();
    await vi.advanceTimersByTimeAsync(1200);
    // The speaker stops while the request is out; refreshStatus empties the title.
    state.nowPlayState = 'STOP_STATE';
    answer({ title: 'Ruby My Dear' });
    await vi.advanceTimersByTimeAsync(0);
    expect(state.nowTitle).toBe('');
    const calls = api.StreamTitleInfo.mock.calls.length;
    await vi.advanceTimersByTimeAsync(30000);
    expect(api.StreamTitleInfo.mock.calls.length).toBe(calls);

    // The next play starts it again.
    state.nowPlayState = 'PLAY_STATE';
    api.StreamTitleInfo.mockResolvedValue({ title: 'Next - Song' });
    live.scheduleLiveTitle();
    await vi.advanceTimersByTimeAsync(1200);
    expect(state.nowTitle).toBe('Next - Song');
  });
});
