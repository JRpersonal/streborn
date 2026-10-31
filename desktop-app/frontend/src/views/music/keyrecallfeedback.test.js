// The recall of a speaker's own key and the key Transfer (phase 5): with the
// UI 2.0 preview's report hook the outcome lands in the key or under the
// Transfer button, named with the speakers; without it the current layout's
// toasts and the error dialog are exactly what they were.
import { describe, it, expect, vi, beforeEach } from 'vitest';

const api = vi.hoisted(() => ({
  BoxPresets: vi.fn(),
  BoxSnapshot: vi.fn(),
  CopyPresetsAcrossBoxes: vi.fn(),
  GetAppFlag: vi.fn(),
  GetPresets: vi.fn(),
  RadioSearch: vi.fn(),
  RecallBoxPreset: vi.fn(),
  SetAppFlag: vi.fn(),
  SetPreset: vi.fn(),
}));
const ui = vi.hoisted(() => ({ showToast: vi.fn(), showError: vi.fn(), confirmWarn: vi.fn() }));

vi.mock('../../api.js', () => api);
vi.mock('../../utils.js', async (importOriginal) => ({
  ...(await importOriginal()),
  showToast: ui.showToast,
  showError: ui.showError,
  confirmWarn: ui.confirmWarn,
}));
vi.mock('./presettiles.js', () => ({ renderPresets: vi.fn() }));
vi.mock('./ui2radio.js', () => ({ transferReport: () => null }));

let presets;
let state;
const KITCHEN = { host: '192.0.2.1', port: 8888, friendlyName: 'Kitchen' };
const OFFICE = { host: '192.0.2.2', port: 8888, friendlyName: 'Office' };
const BEDROOM = { host: '192.0.2.3', port: 8888, friendlyName: 'Bedroom' };

beforeEach(async () => {
  vi.resetModules();
  Object.values(api).forEach((f) => f.mockReset());
  Object.values(ui).forEach((f) => f.mockReset());
  ({ state } = await import('../../state.js'));
  (await import('../../i18n/index.js')).setLocale('en');
  state.currentBox = KITCHEN;
  state.boxes = [KITCHEN, OFFICE, BEDROOM];
  state.presets = [];
  presets = await import('./presets.js');
});

describe('recalling a speaker\'s own key', () => {
  it('without a report: the error dialog, as before', async () => {
    api.RecallBoxPreset.mockRejectedValue('status 503: busy');
    await presets.recallBoxPreset(5);
    expect(ui.showError).toHaveBeenCalledWith('Could not start this speaker preset: status 503: busy');
  });

  it('with a report: starting, then the refusal in the key, named', async () => {
    api.RecallBoxPreset.mockRejectedValue('status 503: busy');
    const report = vi.fn();
    await presets.recallBoxPreset(5, report);
    expect(ui.showError).not.toHaveBeenCalled();
    expect(report).toHaveBeenNthCalledWith(1, 'Starting key 5 on Kitchen...', 'busy');
    expect(report).toHaveBeenLastCalledWith(expect.stringMatching(/^Key 5 did not start on Kitchen\. /), 'error');
  });

  it('with a report: a recall the speaker took clears the line', async () => {
    api.RecallBoxPreset.mockResolvedValue();
    const report = vi.fn();
    await presets.recallBoxPreset(5, report);
    expect(report).toHaveBeenLastCalledWith(null);
  });
});

describe('Transfer', () => {
  const run = (val, report = null) => presets.runPresetTransfer({
    box: KITCHEN, targets: [OFFICE, BEDROOM], val, twoRows: false, sourcePresets: [], report,
  });

  it('without a report: the progress and done toasts, as before', async () => {
    api.CopyPresetsAcrossBoxes.mockResolvedValue(6);
    await run(`${OFFICE.host}|8888`);
    expect(ui.showToast).toHaveBeenNthCalledWith(1, expect.any(String), 0);
    expect(ui.showToast).toHaveBeenLastCalledWith('Copied 6 presets to Office.');
  });

  it('without a report: a partial copy in the error dialog, as before', async () => {
    api.CopyPresetsAcrossBoxes.mockResolvedValueOnce(6).mockRejectedValueOnce('timeout');
    await run('__ALL__');
    expect(ui.showError).toHaveBeenCalledTimes(1);
  });

  it('with a report: busy, then the targets\' answer under the button, no toast', async () => {
    api.CopyPresetsAcrossBoxes.mockResolvedValue(6);
    const report = vi.fn();
    await run(`${OFFICE.host}|8888`, report);
    expect(ui.showToast).not.toHaveBeenCalled();
    expect(report).toHaveBeenNthCalledWith(1, 'Copying the keys of Kitchen to Office...', 'busy');
    expect(report).toHaveBeenLastCalledWith('Office took 6 keys of Kitchen.', 'ok');
  });

  it('with a report: a partial copy names who did not answer', async () => {
    api.CopyPresetsAcrossBoxes.mockResolvedValueOnce(6).mockRejectedValueOnce('timeout');
    const report = vi.fn();
    await run('__ALL__', report);
    expect(ui.showError).not.toHaveBeenCalled();
    expect(report).toHaveBeenLastCalledWith('1 of 2 speakers took the keys of Kitchen. No answer from Bedroom.', 'warn');
  });

  it('with a report: a refusal under the button, not in the dialog', async () => {
    api.CopyPresetsAcrossBoxes.mockRejectedValue('status 500: disk full');
    const report = vi.fn();
    await run(`${OFFICE.host}|8888`, report);
    expect(ui.showError).not.toHaveBeenCalled();
    expect(report).toHaveBeenLastCalledWith(expect.stringMatching(/^The keys of Kitchen did not reach Office\. /), 'error');
  });
});
