// The group edit path (toggleGroupMember -> runPendingGroupEdits ->
// runGroupMemberToggle), run for real against mocked speaker calls: a batch
// edits the group it was clicked in, whatever the selection does afterwards.
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

const box = (n, id) => ({ host: `192.0.2.${n}`, port: 8888, deviceID: id, kind: 'STR', friendlyName: id });
const A = box(10, 'AAA');
const B = box(11, 'BBB');
const C = box(12, 'CCC');
const D = box(13, 'DDD');

// Fresh modules per test: the batch, its timer and the shared state are
// module-level.
let group;
let state;

beforeEach(async () => {
  vi.useFakeTimers();
  vi.resetModules();
  // The pending marks paint chips; the node environment has no document.
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
  state.boxes = [A, B, C, D];
  state.zoneLive = { AAA: {}, BBB: {}, CCC: {}, DDD: {} };
  state.currentBox = A;
  group = await import('./group.js');
});

afterEach(async () => {
  await vi.runAllTimersAsync();
  vi.useRealTimers();
  delete globalThis.document;
});

const formed = () => api.FormZone.mock.calls.map(([host, , req]) => [host, req.slaves.map((s) => s.ip)]);

describe('group edits', () => {
  it('edits the group the click was made in, even after another speaker is selected', async () => {
    group.toggleGroupMember(B.host, B.port);
    state.currentBox = C;
    await vi.advanceTimersByTimeAsync(700);
    expect(formed()).toEqual([[A.host, [B.host]]]);
  });

  it('sends a waiting batch at once when the next click is for another group', async () => {
    group.toggleGroupMember(B.host, B.port);
    state.currentBox = C;
    group.toggleGroupMember(D.host, D.port);
    // A's batch left without waiting for the coalesce window.
    await vi.advanceTimersByTimeAsync(10);
    expect(formed()).toEqual([[A.host, [B.host]]]);
    await vi.advanceTimersByTimeAsync(700);
    expect(formed()).toEqual([[A.host, [B.host]], [C.host, [D.host]]]);
  });

  it('cancels a change of mind inside one batch', async () => {
    group.toggleGroupMember(B.host, B.port);
    group.toggleGroupMember(D.host, D.port);
    group.toggleGroupMember(B.host, B.port);
    await vi.advanceTimersByTimeAsync(700);
    expect(formed()).toEqual([[A.host, [D.host]]]);
  });

  it('drops the batch and says so when its master follows another speaker by now', async () => {
    group.toggleGroupMember(B.host, B.port);
    state.zoneLive = { ...state.zoneLive, AAA: { master: 'CCC' } };
    await vi.advanceTimersByTimeAsync(700);
    expect(api.FormZone).not.toHaveBeenCalled();
    expect(api.DissolveZone).not.toHaveBeenCalled();
    expect(ui.showToast).toHaveBeenCalledWith(en['group.editStale']);
  });

  it('never sends the master as its own follower', async () => {
    group.toggleGroupMember(A.host, A.port);
    group.toggleGroupMember(B.host, B.port);
    await vi.advanceTimersByTimeAsync(700);
    expect(formed()).toEqual([[A.host, [B.host]]]);
  });

  it('drops a waiting batch quietly when its master is purged', async () => {
    group.toggleGroupMember(B.host, B.port);
    group.purgeGroupStateForSpeaker(A.host);
    state.boxes = [B, C, D];
    await vi.advanceTimersByTimeAsync(700);
    expect(api.FormZone).not.toHaveBeenCalled();
    expect(ui.showToast).not.toHaveBeenCalled();
    expect(group.groupEditPendingHosts().size).toBe(0);
  });
});
