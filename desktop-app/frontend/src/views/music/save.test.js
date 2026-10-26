// A long-press save of a station the app did not just start goes through the
// speaker agent's save-what-plays (#1467): the agent's record of the play holds
// the whole logo chain, the homepage and the codec, the speaker's now-playing
// only one logo guess. Run for real against mocked speaker calls.
import { describe, it, expect, vi, beforeEach } from 'vitest';

const api = vi.hoisted(() => ({
  LogSpotifySaveGate: vi.fn(),
  MovePreset: vi.fn(),
  RadioVote: vi.fn(),
  SaveLibraryPreset: vi.fn(),
  SaveNativePreset: vi.fn(),
  SavePlayingPreset: vi.fn(),
  SaveQueuePreset: vi.fn(),
  SaveSpotifyPreset: vi.fn(),
  SetPreset: vi.fn(),
  SpotifyNowPlaying: vi.fn(),
}));
const ui = vi.hoisted(() => ({ showToast: vi.fn(), showError: vi.fn(), confirmWarn: vi.fn() }));

vi.mock('../../api.js', () => api);
vi.mock('./status.js', () => ({ refreshStatus: async () => {} }));
vi.mock('./presets.js', () => ({
  loadPresets: async () => {},
  decodeProxyUrl: (u) => {
    const m = /\/stream\/raw\?u=([^&]+)/.exec(u || '');
    return m ? atob(m[1].replace(/-/g, '+').replace(/_/g, '/')) : u;
  },
}));
vi.mock('../../utils.js', async (importOriginal) => ({
  ...(await importOriginal()),
  showToast: ui.showToast,
  showError: ui.showError,
  confirmWarn: ui.confirmWarn,
}));

const station = 'http://radio.example/jazz.mp3';
const raw = 'http://127.0.0.1:8888/stream/raw?u=' + btoa(station).replace(/=+$/, '');
const orion = '/station?data=' + btoa(JSON.stringify({
  streamUrl: raw, name: 'Jazz Lounge', imageUrl: 'http://127.0.0.1:8888/art?u=x',
})).replace(/=+$/, '');

let save;
let state;

beforeEach(async () => {
  vi.resetModules();
  Object.values(api).forEach((f) => f.mockReset());
  Object.values(ui).forEach((f) => f.mockReset());
  api.RadioVote.mockResolvedValue();
  api.SetPreset.mockResolvedValue();
  ({ state } = await import('../../state.js'));
  (await import('../../i18n/index.js')).setLocale('en');
  state.currentBox = { host: '192.0.2.1', port: 8888 };
  state.presets = [];
  state.queue = null;
  state.lastLibraryPlay = null;
  state.nowSource = 'LOCAL_INTERNET_RADIO';
  state.nowName = 'Jazz Lounge';
  state.nowIcon = '';
  state.nowUUID = '';
  save = await import('./save.js');
});

describe('long-press save of a playing station', () => {
  it('lets the agent save a station the app started minutes ago', async () => {
    state.nowLocation = orion;
    state.lastAppPlay = { url: station, name: 'Jazz Lounge', icon: 'https://img.example/jazz.png', at: Date.now() - 3 * 60 * 1000 };
    api.SavePlayingPreset.mockResolvedValue({ kind: 'stream', name: 'Jazz Lounge' });
    await save.saveCurrentToSlot(1);
    expect(api.SavePlayingPreset).toHaveBeenCalledWith('192.0.2.1', 8888, 1);
    expect(api.SetPreset).not.toHaveBeenCalled();
    expect(ui.showToast).toHaveBeenCalledWith('Saved to key 1: Jazz Lounge');
  });

  it('announces a copy of the key that plays as a copy', async () => {
    state.nowLocation = orion;
    state.lastAppPlay = null;
    api.SavePlayingPreset.mockResolvedValue({ kind: 'copy', name: 'Jazz Lounge' });
    await save.saveCurrentToSlot(4);
    expect(ui.showToast).toHaveBeenCalledWith('Copied to key 4: Jazz Lounge');
  });

  it('lets the agent save a station another client started', async () => {
    state.nowSource = 'UPNP';
    state.nowLocation = raw;
    state.lastAppPlay = null;
    api.SavePlayingPreset.mockResolvedValue({ kind: 'stream', name: 'Jazz Lounge' });
    await save.saveCurrentToSlot(2);
    expect(api.SavePlayingPreset).toHaveBeenCalledWith('192.0.2.1', 8888, 2);
    expect(api.SetPreset).not.toHaveBeenCalled();
  });

  it('keeps a fresh app play on the app record', async () => {
    state.nowLocation = orion;
    state.lastAppPlay = { url: station, name: 'Jazz Lounge', icon: 'https://img.example/jazz.png', homepage: 'https://jazz.example/', codec: 'MP3', at: Date.now() };
    await save.saveCurrentToSlot(3);
    expect(api.SavePlayingPreset).not.toHaveBeenCalled();
    expect(api.SetPreset).toHaveBeenCalledWith('192.0.2.1', 8888, 3, 'Jazz Lounge', station,
      'https://img.example/jazz.png', 0, 'https://jazz.example/', 'MP3');
  });

  it('offers the move when the agent says the station sits on another key', async () => {
    state.nowLocation = orion;
    state.lastAppPlay = null;
    ui.confirmWarn.mockResolvedValue(false);
    api.SavePlayingPreset.mockRejectedValue(new Error('status 409: {"code":"already-on-slot","slot":5,"name":"Jazz Lounge"}'));
    await save.saveCurrentToSlot(1);
    expect(ui.confirmWarn).toHaveBeenCalled();
    expect(api.SetPreset).not.toHaveBeenCalled();
    expect(ui.showError).not.toHaveBeenCalled();
  });

  it('shows a refusal of the agent as an error', async () => {
    state.nowLocation = orion;
    state.lastAppPlay = null;
    api.SavePlayingPreset.mockRejectedValue(new Error('status 422: {"code":"nothing-saveable"}'));
    await save.saveCurrentToSlot(1);
    expect(ui.showError).toHaveBeenCalled();
    expect(api.SetPreset).not.toHaveBeenCalled();
  });

  it('shows a known refusal of the agent in the app language, not its English', async () => {
    state.nowLocation = orion;
    state.lastAppPlay = null;
    (await import('../../i18n/index.js')).setLocale('de');
    api.SavePlayingPreset.mockRejectedValue(new Error('status 422: {"code":"nothing-saveable","error":"What the speaker plays has no stream that can be kept on a key."}'));
    await save.saveCurrentToSlot(1);
    expect(ui.showError).toHaveBeenCalledWith('Was der Lautsprecher gerade spielt, lässt sich nicht auf einer Taste speichern.');
  });

  it('shows an unknown refusal of the agent as a failed save', async () => {
    state.nowLocation = orion;
    state.lastAppPlay = null;
    api.SavePlayingPreset.mockRejectedValue(new Error('status 422: {"code":"constructor"}'));
    await save.saveCurrentToSlot(1);
    expect(ui.showError.mock.calls[0][0]).toMatch(/^Save failed: /);
  });

  it('falls back to its own save on an agent without the call', async () => {
    state.nowLocation = orion;
    state.lastAppPlay = null;
    api.SavePlayingPreset.mockRejectedValue(new Error('status 404: 404 page not found'));
    await save.saveCurrentToSlot(1);
    expect(api.SetPreset).toHaveBeenCalledTimes(1);
    expect(api.SetPreset.mock.calls[0][4]).toBe(station);
  });

  it('falls back to its own save on an agent that answers with its phone page', async () => {
    // An agent older than the call serves its phone page with 200 text/html;
    // the binding reports that as this 404.
    state.nowLocation = orion;
    state.lastAppPlay = null;
    api.SavePlayingPreset.mockRejectedValue(new Error('status 404: save-playing not available on this agent'));
    await save.saveCurrentToSlot(1);
    expect(api.SetPreset).toHaveBeenCalledTimes(1);
    expect(api.SetPreset.mock.calls[0][4]).toBe(station);
    expect(ui.showError).not.toHaveBeenCalled();
  });

  it('falls back to its own save when the agent could not read the now-playing', async () => {
    state.nowLocation = orion;
    state.lastAppPlay = null;
    api.SavePlayingPreset.mockRejectedValue(new Error('status 502: {"code":"nowplaying-unreadable"}'));
    await save.saveCurrentToSlot(1);
    expect(api.SetPreset).toHaveBeenCalledTimes(1);
    expect(ui.showError).not.toHaveBeenCalled();
  });
});
