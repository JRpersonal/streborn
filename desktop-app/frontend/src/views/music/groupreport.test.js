// The group edit's optional report callback (UI 2.0 phase 5): the sidebar's
// "+" and "-" get the result in the row instead of a toast across the window.
// Without the callback, the classic toasts and the error dialog say exactly
// what they said before. Run for real against mocked speaker calls, like
// group.test.js.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

const api = vi.hoisted(() => ({
  FormZone: vi.fn(),
  DissolveZone: vi.fn(),
  WakeBox: vi.fn(),
  Stop: vi.fn(),
  SetBoxVolume: vi.fn(),
  BoxSettings: vi.fn(),
}));
const ui = vi.hoisted(() => ({ showToast: vi.fn(), showError: vi.fn() }));

vi.mock('../../api.js', () => api);
vi.mock('../../shell2/mount.js', () => ({ renderShellSpeakers: () => {}, syncShellGroupPanel: () => {} }));
vi.mock('../../utils.js', () => ({
  $: () => null,
  escapeAttr: (s) => String(s),
  escapeHtml: (s) => String(s),
  getBoxLabel: (b) => (b && (b.friendlyName || b.host)) || '',
  showError: ui.showError,
  showToast: ui.showToast,
}));

import en from '../../i18n/bundles/en.json';

const box = (n, id, name) => ({ host: `192.0.2.${n}`, port: 8888, deviceID: id, kind: 'STR', friendlyName: name });
const A = box(10, 'AAA', 'Living room');
const B = box(11, 'BBB', 'Kitchen');
const C = box(12, 'CCC', 'Office');

let group;
let state;

beforeEach(async () => {
  vi.useFakeTimers();
  vi.resetModules();
  globalThis.document = { querySelectorAll: () => [], getElementById: () => null };
  Object.values(api).forEach((f) => f.mockReset());
  api.FormZone.mockResolvedValue({ ok: true });
  api.DissolveZone.mockResolvedValue({ ok: true });
  api.WakeBox.mockResolvedValue({});
  api.Stop.mockResolvedValue({});
  api.SetBoxVolume.mockResolvedValue({});
  api.BoxSettings.mockResolvedValue({ volume: { actual: 20, target: 20 } });
  ui.showToast.mockReset();
  ui.showError.mockReset();
  ({ state } = await import('../../state.js'));
  (await import('../../i18n/index.js')).setLocale('en');
  state.boxes = [A, B, C];
  state.zoneLive = { AAA: {}, BBB: {}, CCC: {} };
  state.currentBox = A;
  group = await import('./group.js');
});

afterEach(async () => {
  await vi.runAllTimersAsync();
  vi.useRealTimers();
  delete globalThis.document;
});

const added = (name) => en['group.addedToast'].replace('{{name}}', name);

describe('the group edit without a report callback', () => {
  it('says an add with the classic toast', async () => {
    group.toggleGroupMember(B.host, B.port);
    await vi.advanceTimersByTimeAsync(700);
    expect(ui.showToast.mock.calls).toEqual([[added('Kitchen')]]);
    expect(ui.showError).not.toHaveBeenCalled();
  });

  it('says a failure with the classic dialog', async () => {
    api.FormZone.mockRejectedValue(new Error('speaker busy'));
    group.toggleGroupMember(B.host, B.port);
    await vi.advanceTimersByTimeAsync(700);
    expect(ui.showError.mock.calls).toEqual([['Error: speaker busy']]);
    expect(ui.showToast).not.toHaveBeenCalled();
  });

  it('says a refusal with the classic dialog', async () => {
    api.FormZone.mockResolvedValue({ ok: false });
    group.toggleGroupMember(B.host, B.port);
    await vi.advanceTimersByTimeAsync(700);
    expect(ui.showError.mock.calls).toEqual([[en['multiroom.formedNone']]]);
  });
});

describe('the group edit with a report callback', () => {
  it('reports the add with the speaker named, the hosts that moved and no toast', async () => {
    const report = vi.fn();
    group.toggleGroupMember(B.host, B.port, A.host, report);
    await vi.advanceTimersByTimeAsync(700);
    expect(ui.showToast).not.toHaveBeenCalled();
    expect(report).toHaveBeenCalledTimes(1);
    const r = report.mock.calls[0][0];
    expect(r).toMatchObject({ tone: 'ok', text: added('Kitchen'), hosts: [B.host], master: A.host, added: [B.host], removed: [] });
    expect(r.text).toContain('Kitchen');
    expect(r.ports).toEqual({ [B.host]: B.port });
  });

  it('reports a failure in place of the dialog, with what to repeat', async () => {
    api.FormZone.mockRejectedValue(new Error('speaker busy'));
    const report = vi.fn();
    group.toggleGroupMember(C.host, C.port, A.host, report);
    await vi.advanceTimersByTimeAsync(700);
    expect(ui.showError).not.toHaveBeenCalled();
    expect(report.mock.calls[0][0]).toMatchObject({ tone: 'error', text: 'Error: speaker busy', hosts: [C.host], master: A.host });
  });

  it('reports a removal as out', async () => {
    state.zoneLive = {
      AAA: { master: 'AAA', members: [{ deviceID: 'AAA', ip: A.host }, { deviceID: 'BBB', ip: B.host }, { deviceID: 'CCC', ip: C.host }] },
      BBB: { master: 'AAA' },
      CCC: { master: 'AAA' },
    };
    const report = vi.fn();
    group.toggleGroupMember(B.host, B.port, A.host, report);
    await vi.advanceTimersByTimeAsync(700);
    expect(report.mock.calls[0][0]).toMatchObject({ tone: 'ok', removed: [B.host], added: [] });
    expect(ui.showToast).not.toHaveBeenCalled();
  });

  it('a click without the callback in another batch keeps its toast', async () => {
    const report = vi.fn();
    group.toggleGroupMember(B.host, B.port, A.host, report);
    await vi.advanceTimersByTimeAsync(700);
    group.toggleGroupMember(C.host, C.port);
    await vi.advanceTimersByTimeAsync(700);
    expect(report).toHaveBeenCalledTimes(1);
    expect(ui.showToast.mock.calls).toEqual([[added('Office')]]);
  });
});
