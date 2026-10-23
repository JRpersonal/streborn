// The shared On/Off toggle of Speaker settings, driven without a DOM: the
// element lookup, the i18n lookup and the toasts are fakes handed in as deps.
import { describe, it, expect, vi } from 'vitest';
import { wireToggleSection } from './togglesection.js';

// A stand-in for the three elements a toggle needs: enough of classList for
// the helper, plus the handler slots it assigns.
function fakeEl() {
  const cls = new Set();
  return {
    textContent: '',
    onclick: null,
    classList: {
      toggle: (c, on) => { if (on === undefined) on = !cls.has(c); if (on) cls.add(c); else cls.delete(c); return on; },
      add: (c) => cls.add(c),
      remove: (c) => cls.delete(c),
      contains: (c) => cls.has(c),
    },
  };
}

function harness(overrides = {}) {
  const els = { sec: fakeEl(), on: fakeEl(), off: fakeEl() };
  const deps = {
    $: (id) => els[id] || null,
    t: (k) => `T(${k})`,
    showToast: vi.fn(),
    showError: vi.fn(),
  };
  const opts = {
    sectionId: 'sec', onId: 'on', offId: 'off',
    get: vi.fn(async () => ({ supported: true, enabled: false })),
    stateFn: (r) => ({ show: !!r && r.supported === true, enabled: typeof r?.enabled === 'boolean' ? r.enabled : null }),
    set: vi.fn(async () => ({ ok: true })),
    toastKey: 'saved',
    ...overrides,
  };
  const ctl = wireToggleSection(opts, deps);
  return { els, deps, opts, ctl };
}

const lit = (els) => ({ on: els.on.classList.contains('active'), off: els.off.classList.contains('active') });

describe('wireToggleSection', () => {
  it('paints the speaker state after the read and shows the section', async () => {
    const { els, ctl } = harness();
    await ctl.ready;
    expect(lit(els)).toEqual({ on: false, off: true });
    expect(els.sec.classList.contains('hidden')).toBe(false);
    expect(ctl.state()).toBe(false);
  });

  it('success path: the press moves the highlight, writes, and toasts', async () => {
    const { els, deps, opts, ctl } = harness();
    await ctl.ready;
    await els.on.onclick();
    expect(opts.set).toHaveBeenCalledWith(true);
    expect(lit(els)).toEqual({ on: true, off: false });
    expect(deps.showToast).toHaveBeenCalledWith('T(saved)');
    expect(deps.showError).not.toHaveBeenCalled();
    expect(ctl.state()).toBe(true);
  });

  it('failure path: a refused write repaints the state the press came from', async () => {
    const err = new Error('HTTP 500');
    const { els, deps, ctl } = harness({ set: vi.fn(async () => { throw err; }) });
    await ctl.ready;
    expect(lit(els)).toEqual({ on: false, off: true });
    await els.on.onclick();
    // Not "On", which the speaker never took, but the "Off" it still holds.
    expect(lit(els)).toEqual({ on: false, off: true });
    expect(ctl.state()).toBe(false);
    expect(deps.showError).toHaveBeenCalledWith(err);
    expect(deps.showToast).not.toHaveBeenCalled();
  });

  it('a refused write from an unknown state lights neither button again', async () => {
    const { els, ctl } = harness({
      get: async () => ({ supported: true }),
      set: async () => { throw new Error('no'); },
    });
    await ctl.ready;
    expect(lit(els)).toEqual({ on: false, off: false });
    await els.off.onclick();
    expect(lit(els)).toEqual({ on: false, off: false });
    expect(ctl.state()).toBe(null);
  });

  it('a failed read hides the section', async () => {
    const { els, ctl } = harness({ get: async () => { throw new Error('404'); } });
    await ctl.ready;
    expect(els.sec.classList.contains('hidden')).toBe(true);
  });

  it('a read the speaker answers with "not here" hides the section too', async () => {
    const { els, ctl } = harness({ get: async () => ({ supported: false }) });
    await ctl.ready;
    expect(els.sec.classList.contains('hidden')).toBe(true);
  });

  it('onGetError paints a fallback instead of hiding (resume on power-on defaults to on)', async () => {
    const { els, ctl } = harness({
      get: async () => { throw new Error('old agent'); },
      onGetError: { show: true, enabled: true },
    });
    await ctl.ready;
    expect(els.sec.classList.contains('hidden')).toBe(false);
    expect(lit(els)).toEqual({ on: true, off: false });
  });

  it('a declined confirm leaves everything as it was', async () => {
    const { els, opts, ctl } = harness({ confirm: async () => false });
    await ctl.ready;
    await els.on.onclick();
    expect(opts.set).not.toHaveBeenCalled();
    expect(lit(els)).toEqual({ on: false, off: true });
  });

  it('hooks: onLoaded sees the state, onPaint follows every paint, afterSet runs before the toast', async () => {
    const calls = [];
    const { els, deps, ctl } = harness({
      stateFn: (r) => ({ show: true, enabled: r.enabled, extra: 'x' }),
      onLoaded: (st) => calls.push(['loaded', st.extra]),
      onPaint: (en) => calls.push(['paint', en]),
      afterSet: (r, en) => calls.push(['afterSet', r.ok, en]),
    });
    deps.showToast.mockImplementation(() => calls.push(['toast']));
    await ctl.ready;
    await els.on.onclick();
    expect(calls).toEqual([
      ['paint', false], ['loaded', 'x'],
      ['paint', true], ['afterSet', true, true], ['toast'],
    ]);
  });

  it('toastKey may depend on the direction', async () => {
    const { els, deps, ctl } = harness({ toastKey: (en) => (en ? 'onKey' : 'offKey') });
    await ctl.ready;
    await els.off.onclick();
    expect(deps.showToast).toHaveBeenCalledWith('T(offKey)');
  });

  it('label keys relabel the buttons; without them the markup labels stay', async () => {
    const a = harness();
    expect(a.els.on.textContent).toBe('');
    const b = harness({ onLabelKey: 'common.on', offLabelKey: 'common.off' });
    expect(b.els.on.textContent).toBe('T(common.on)');
    expect(b.els.off.textContent).toBe('T(common.off)');
    await a.ctl.ready;
    await b.ctl.ready;
  });

  it('is a no-op when the section is not in this render', () => {
    const deps = { $: () => null, t: (k) => k, showToast: vi.fn(), showError: vi.fn() };
    expect(wireToggleSection({ sectionId: 'a', onId: 'b', offId: 'c', get: async () => ({}), set: async () => {}, stateFn: () => ({}), toastKey: 'k' }, deps)).toBe(null);
  });
});
