// Rename, clear and the lost-key notice of a key tile (phase 5): with the UI
// 2.0 preview's report hook the outcome stays in the key, named with key and
// speaker, and done is said only after the speaker's read-back. Without it the
// current layout's toasts and dialogs are exactly what they were.
import { describe, it, expect, vi, beforeEach } from 'vitest';

const api = vi.hoisted(() => ({ DeletePreset: vi.fn(), GetPresets: vi.fn(), RenamePreset: vi.fn() }));
const ui = vi.hoisted(() => ({ showToast: vi.fn(), showError: vi.fn(), confirmWarn: vi.fn() }));
const presets = vi.hoisted(() => ({ loadPresets: vi.fn(), list: [] }));

vi.mock('../../api.js', () => api);
vi.mock('../../utils.js', async (importOriginal) => ({
  ...(await importOriginal()),
  showToast: ui.showToast,
  showError: ui.showError,
  confirmWarn: ui.confirmWarn,
}));
vi.mock('./presets.js', () => ({
  boxSourceLabel: () => '',
  decodeProxyUrl: (u) => u,
  isMeasurableStreamPreset: () => false,
  loadPresets: (...a) => presets.loadPresets(...a),
  lostStrKey: () => false,
  presetStateLabel: () => '',
  recallBoxPreset: vi.fn(),
  setPresetIfUnchanged: vi.fn(),
  updatePresetTransferRow: vi.fn(),
  wirePresetTransferRow: vi.fn(),
}));
vi.mock('./ui2radio.js', () => ({
  keyFocusMark: () => null,
  keyReport: () => null,
  restoreKeyFocus: vi.fn(),
  syncUi2Keys: vi.fn(),
}));
vi.mock('./group.js', () => ({ renderGroupControl: vi.fn() }));
vi.mock('./nowplaying.js', () => ({ renderNowPlayingBar: vi.fn() }));
vi.mock('./play.js', () => ({ play: vi.fn() }));
vi.mock('./save.js', () => ({ saveCurrentToSlot: vi.fn() }));

let tiles;
let state;

beforeEach(async () => {
  vi.resetModules();
  Object.values(api).forEach((f) => f.mockReset());
  Object.values(ui).forEach((f) => f.mockReset());
  presets.loadPresets.mockReset();
  presets.list = [];
  ({ state } = await import('../../state.js'));
  (await import('../../i18n/index.js')).setLocale('en');
  state.currentBox = { host: '192.0.2.1', port: 8888, friendlyName: 'Kitchen' };
  state.boxes = [state.currentBox];
  state.presets = [{ slot: 2, name: 'Old Name' }];
  presets.loadPresets.mockImplementation(async () => { state.presets = presets.list.slice(); });
  tiles = await import('./presettiles.js');
});

describe('rename', () => {
  it('without a report: the same toast and reload as before', async () => {
    await tiles.renameOutcome(2, 'Morning', null);
    expect(ui.showToast).toHaveBeenCalledWith('Key 2 is now called Morning');
    expect(presets.loadPresets).toHaveBeenCalledTimes(1);
    expect(ui.showError).not.toHaveBeenCalled();
  });

  it('without a report: a refusal in the error dialog, as before', async () => {
    await tiles.renameOutcome(2, 'Morning', 'status 500: busy', null);
    expect(ui.showError).toHaveBeenCalledWith('status 500: busy');
    expect(ui.showToast).not.toHaveBeenCalled();
  });

  it('with a report: the new name is done only once the read-back carries it', async () => {
    presets.list = [{ slot: 2, name: 'Morning' }];
    const report = vi.fn();
    await tiles.renameOutcome(2, 'Morning', null, report);
    expect(ui.showToast).not.toHaveBeenCalled();
    expect(report).toHaveBeenCalledWith('Key 2 of Kitchen is now called Morning. The speaker confirmed it.', 'ok');

    presets.list = [{ slot: 2, name: 'Old Name' }];
    const again = vi.fn();
    await tiles.renameOutcome(2, 'Morning', null, again);
    expect(again).toHaveBeenCalledWith('Kitchen did not confirm key 2. Check the key and try again.', 'warn');
  });

  it('with a report: a refusal stays in the key', async () => {
    const report = vi.fn();
    await tiles.renameOutcome(2, 'Morning', 'status 500: busy', report);
    expect(ui.showError).not.toHaveBeenCalled();
    expect(report.mock.calls[0][1]).toBe('error');
    expect(report.mock.calls[0][0]).toMatch(/^Key 2 of Kitchen was not changed: /);
  });
});

describe('clear', () => {
  it('without a report: reload, or the error dialog, as before', async () => {
    api.DeletePreset.mockResolvedValue();
    await tiles.clearPresetKey(2);
    expect(api.DeletePreset).toHaveBeenCalledWith('192.0.2.1', 8888, 2);
    expect(presets.loadPresets).toHaveBeenCalledTimes(1);
    expect(api.GetPresets).not.toHaveBeenCalled();

    api.DeletePreset.mockRejectedValue('status 500: busy');
    await tiles.clearPresetKey(2);
    expect(ui.showError).toHaveBeenCalledWith('status 500: busy');
  });

  it('with a report: "empty" only once the speaker\'s list has nothing on the key', async () => {
    api.DeletePreset.mockResolvedValue();
    api.GetPresets.mockResolvedValue([{ slot: 1, name: 'Other' }]);
    const report = vi.fn();
    await tiles.clearPresetKey(2, report);
    expect(report).toHaveBeenLastCalledWith('Key 2 of Kitchen is empty now. The speaker confirmed it.', 'ok');

    api.GetPresets.mockResolvedValue([{ slot: 2, name: 'Old Name' }]);
    const again = vi.fn();
    await tiles.clearPresetKey(2, again);
    expect(again).toHaveBeenLastCalledWith('Kitchen did not confirm key 2. Check the key and try again.', 'warn');
    expect(ui.showError).not.toHaveBeenCalled();
  });

  it('with a report: a refusal stays in the key', async () => {
    api.DeletePreset.mockRejectedValue('status 500: busy');
    const report = vi.fn();
    await tiles.clearPresetKey(2, report);
    expect(ui.showError).not.toHaveBeenCalled();
    expect(report).toHaveBeenCalledWith(expect.stringMatching(/^Key 2 of Kitchen was not cleared\. /), 'error');
  });
});

describe('a key left over from before a reinstall', () => {
  it('says so in the key, naming key and speaker', () => {
    const report = vi.fn();
    tiles.lostKeyNotice(4, report);
    expect(report).toHaveBeenCalledWith(expect.stringMatching(/^Key 4 of Kitchen is left over/), 'warn');
    expect(ui.showToast).not.toHaveBeenCalled();
  });
});
