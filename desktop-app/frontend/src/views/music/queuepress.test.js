// play() on a music-library key (#1767): pressed on a sleeping speaker, the
// press takes about 8 s while the firmware resumes the station it played last.
// The optimistic card must hold until the press returns and a little after,
// never for the 6 s every other key gets.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

const api = vi.hoisted(() => ({ PlaySlot: vi.fn(), boxFetch: vi.fn() }));
vi.mock('../../api.js', () => api);
vi.mock('./presettiles.js', () => ({ renderPresets: vi.fn() }));
vi.mock('./nowplaying.js', () => ({ renderNowPlayingBar: vi.fn() }));
vi.mock('./status.js', () => ({ refreshStatus: vi.fn() }));
vi.mock('./live.js', () => ({ scheduleLiveBitrate: vi.fn(), scheduleLiveTitle: vi.fn() }));
vi.mock('../settings.js', () => ({ throttledSetVolume: vi.fn() }));
vi.mock('./volume.js', () => ({ currentPairTarget: () => null, musicVolEl: null, musicVolValEl: null, pairSetVolume: vi.fn() }));

let state;
let play;
const BOX = { host: '192.0.2.1', port: 8888, friendlyName: 'Kitchen' };

beforeEach(async () => {
  vi.resetModules();
  vi.useFakeTimers();
  vi.setSystemTime(new Date('2026-11-08T10:00:00Z'));
  api.PlaySlot.mockReset();
  ({ state } = await import('../../state.js'));
  (await import('../../i18n/index.js')).setLocale('en');
  ({ play } = await import('./play.js'));
  state.currentBox = BOX;
  state.boxes = [BOX];
  state.presetErrors = {};
  state.nowPlayState = '';
  state.nowSource = 'STANDBY';
  state.nowLocation = '';
  state.presets = [
    { slot: 3, type: 'queue', name: 'Blues album', stream_url: '' },
    { slot: 1, type: 'radio', name: 'Radio One', stream_url: 'http://radio.example/one' },
  ];
});

afterEach(() => {
  vi.useRealTimers();
});

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

describe('a library key pressed on a sleeping speaker', () => {
  it('holds the optimistic card while the press is in flight, then 2 s more', async () => {
    const d = deferred();
    api.PlaySlot.mockReturnValue(d.promise);
    const done = play(3);
    expect(state.optimisticQueueSlot).toBe(3);
    expect(state.optimisticQueueInFlight).toBeTruthy();
    // 8 s later, still waking: the card is still the app's.
    vi.advanceTimersByTime(8000);
    expect(Date.now() < state.optimisticUntil).toBe(true);
    d.resolve({});
    await done;
    expect(state.optimisticQueueInFlight).toBe(0);
    expect(state.optimisticUntil).toBe(Date.now() + 2000);
  });

  it('a failed press lets the card go', async () => {
    api.PlaySlot.mockRejectedValue('status 503: box_not_ready');
    await play(3);
    expect(state.optimisticQueueInFlight).toBe(0);
    expect(state.optimisticUntil).toBe(0);
  });

  it('a radio key keeps its 6 s window and no in-flight hold', async () => {
    const d = deferred();
    api.PlaySlot.mockReturnValue(d.promise);
    const done = play(1);
    expect(state.optimisticQueueInFlight).toBe(0);
    expect(state.optimisticUntil).toBe(Date.now() + 6000);
    d.resolve({});
    await done;
    expect(state.optimisticUntil).toBe(Date.now() + 6000);
  });

  it('a later press owns the state: the earlier return does not re-arm it', async () => {
    const first = deferred();
    const second = deferred();
    api.PlaySlot.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    const a = play(3);
    const b = play(1);
    first.resolve({});
    await a;
    expect(state.optimisticQueueSlot).toBe(null);
    expect(state.optimisticUntil).toBe(Date.now() + 6000);
    second.resolve({});
    await b;
  });
});

// #1763: a Pandora key the speaker's Pandora client cannot start right now.
describe('a native-service key the speaker cannot start', () => {
  const refused = (detail) => `status 502: {"code":"native-select-refused","detail":${JSON.stringify(detail)},"error":"The speaker could not start this station","name":"Jazz","slot":1}`;

  it('says Pandora is not answering, not "speaker refused"', async () => {
    state.presets = [{ slot: 1, type: 'native', name: 'Jazz', native: { source: 'PANDORA', location: '111' } }];
    api.PlaySlot.mockRejectedValue(refused('select: <error value="1008" name="GENERAL_PANDORA_ERROR" severity="Unknown">'));
    await play(1);
    expect(state.presetErrors[1]).toBe("Pandora isn't answering on this speaker right now. Try again later.");
  });

  it('the agent refusing up front because the speaker lists the service UNAVAILABLE', async () => {
    state.presets = [{ slot: 1, type: 'native', name: 'Jazz', native: { source: 'PANDORA', location: '111' } }];
    api.PlaySlot.mockRejectedValue('status 503: {"code":"native-service-unavailable","source":"PANDORA","slot":1}');
    await play(1);
    expect(state.presetErrors[1]).toBe("Pandora isn't answering on this speaker right now. Try again later.");
  });

  it('any other refusal of a native item keeps the plain label', async () => {
    state.presets = [{ slot: 1, type: 'native', name: 'Jazz', native: { source: 'IHEART', location: '222' } }];
    api.PlaySlot.mockRejectedValue(refused('select: <error value="1005" name="INVALID_SOURCE">'));
    await play(1);
    expect(state.presetErrors[1]).toBe('speaker refused');
  });
});

describe('nativeServiceDown', () => {
  it('names the label for each refusal', async () => {
    const { nativeServiceDown } = await import('./play.js');
    expect(nativeServiceDown('{"code":"native-service-unavailable","source":"IHEART"}')).toBe('play.errNativeServiceUnavailable');
    expect(nativeServiceDown('{"code":"native-service-unavailable"}')).toBe('play.errPandoraUnavailable');
    // the agent's real body (playback.go): the service in "source", the key's name beside it
    expect(nativeServiceDown('status 503: {"code":"native-service-unavailable","detail":"the speaker lists this service as unavailable (PANDORA)","error":"The speaker\'s own login for this service is not working","name":"iHeart hits","slot":2,"source":"PANDORA"}')).toBe('play.errPandoraUnavailable');
    expect(nativeServiceDown('{"code":"native-select-refused","detail":"GENERAL_PANDORA_ERROR"}')).toBe('play.errPandoraUnavailable');
    expect(nativeServiceDown('{"code":"native-select-refused","detail":"error 1008"}')).toBe('play.errPandoraUnavailable');
    expect(nativeServiceDown('{"code":"native-select-refused","detail":"10080"}')).toBe('');
    expect(nativeServiceDown('status 503: box_not_ready')).toBe('');
    expect(nativeServiceDown('')).toBe('');
  });
});
