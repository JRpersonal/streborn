// A hold-to-save in the UI 2.0 preview reports in the held key's own line
// (phase 5): named with the key and the speaker, done only after the preset
// list read back from the speaker holds it. Without a report hook the current
// layout's toasts and dialogs are exactly what they were.
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
// What the speaker's preset list holds after the save (loadPresets reads it).
const readBack = vi.hoisted(() => ({ list: [] }));

vi.mock('../../api.js', () => api);
vi.mock('./status.js', () => ({ refreshStatus: async () => {} }));
vi.mock('./presets.js', () => ({
  // The state module of this test's module registry (vi.resetModules).
  loadPresets: async () => { (await import('../../state.js')).state.presets = readBack.list.slice(); },
  decodeProxyUrl: (u) => u,
}));
vi.mock('../../utils.js', async (importOriginal) => ({
  ...(await importOriginal()),
  showToast: ui.showToast,
  showError: ui.showError,
  confirmWarn: ui.confirmWarn,
}));

const station = 'http://radio.example/jazz.mp3';
let save;
let state;

beforeEach(async () => {
  vi.resetModules();
  Object.values(api).forEach((f) => f.mockReset());
  Object.values(ui).forEach((f) => f.mockReset());
  api.RadioVote.mockResolvedValue();
  api.SetPreset.mockResolvedValue();
  readBack.list = [];
  ({ state } = await import('../../state.js'));
  (await import('../../i18n/index.js')).setLocale('en');
  state.currentBox = { host: '192.0.2.1', port: 8888, friendlyName: 'Kitchen' };
  state.boxes = [state.currentBox];
  state.presets = [];
  state.queue = null;
  state.lastLibraryPlay = null;
  state.lastAppPlay = null;
  state.nowSource = 'LOCAL_INTERNET_RADIO';
  state.nowName = 'Jazz Lounge';
  state.nowLocation = station;
  state.nowIcon = '';
  state.nowUUID = '';
  save = await import('./save.js');
});

describe('a hold-to-save without a report hook (the current layout)', () => {
  it('toasts the save, exactly as before', async () => {
    api.SavePlayingPreset.mockResolvedValue({ kind: 'stream', name: 'Jazz Lounge' });
    await save.saveCurrentToSlot(3);
    expect(ui.showToast).toHaveBeenCalledWith('Saved to key 3: Jazz Lounge');
    expect(ui.showError).not.toHaveBeenCalled();
  });

  it('shows a refusal in the error dialog, exactly as before', async () => {
    api.SavePlayingPreset.mockRejectedValue('status 500: disk full');
    await save.saveCurrentToSlot(3);
    expect(ui.showError).toHaveBeenCalledWith('Save failed: status 500: disk full');
    expect(ui.showToast).not.toHaveBeenCalled();
  });

  it('toasts "nothing plays", exactly as before', async () => {
    state.nowLocation = '';
    await save.saveCurrentToSlot(2);
    expect(ui.showToast).toHaveBeenCalledTimes(1);
    expect(ui.showError).not.toHaveBeenCalled();
  });
});

describe('a hold-to-save with the preview\'s report hook', () => {
  it('says done only after the read-back holds the key, naming key and speaker', async () => {
    api.SavePlayingPreset.mockResolvedValue({ kind: 'stream', name: 'Jazz Lounge' });
    readBack.list = [{ slot: 3, name: 'Jazz Lounge' }];
    const report = vi.fn();
    await save.saveCurrentToSlot(3, report);
    expect(ui.showToast).not.toHaveBeenCalled();
    expect(ui.showError).not.toHaveBeenCalled();
    expect(report).toHaveBeenNthCalledWith(1, 'Putting it on key 3 of Kitchen...', 'busy');
    expect(report).toHaveBeenLastCalledWith('On key 3 of Kitchen. The speaker confirmed it.', 'ok');
  });

  it('does not call it done when the read-back does not hold it', async () => {
    api.SavePlayingPreset.mockResolvedValue({ kind: 'stream', name: 'Jazz Lounge' });
    readBack.list = [{ slot: 3, name: 'Rock FM' }];
    const report = vi.fn();
    await save.saveCurrentToSlot(3, report);
    expect(report).toHaveBeenLastCalledWith('Kitchen did not confirm key 3. Check the key and try again.', 'warn');
    expect(ui.showToast).not.toHaveBeenCalled();
  });

  it('puts a refusal into the key, not into the dialog', async () => {
    api.SavePlayingPreset.mockRejectedValue('status 500: disk full');
    const report = vi.fn();
    await save.saveCurrentToSlot(3, report);
    expect(ui.showError).not.toHaveBeenCalled();
    const [text, kind] = report.mock.calls[report.mock.calls.length - 1];
    expect(kind).toBe('error');
    expect(text).toMatch(/^Key 3 of Kitchen was not changed: /);
  });

  it('says in the key that nothing plays', async () => {
    state.nowLocation = '';
    const report = vi.fn();
    await save.saveCurrentToSlot(2, report);
    expect(ui.showToast).not.toHaveBeenCalled();
    const [text, kind] = report.mock.calls[report.mock.calls.length - 1];
    expect(kind).toBe('warn');
    expect(text).toMatch(/^Key 2 of Kitchen was not changed: /);
  });

  it('keeps the move offer for a station already on another key', async () => {
    api.SavePlayingPreset.mockRejectedValue('status 409: {"error":"already-on-slot","slot":5,"name":"Jazz Lounge"}');
    ui.confirmWarn.mockResolvedValue(false);
    const report = vi.fn();
    await save.saveCurrentToSlot(3, report);
    expect(ui.showError).not.toHaveBeenCalled();
    expect(report).toHaveBeenLastCalledWith(null);
  });
});
