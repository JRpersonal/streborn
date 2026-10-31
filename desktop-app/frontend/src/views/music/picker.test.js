// The shared slot picker hands the keyboard back to the control that opened
// it (UI 2.0 preview). The opener can be repainted away while the picker is
// open (a status poll, the Recently played refresh), so it is found again by
// the row or card it stood in.
import { describe, it, expect } from 'vitest';
import { openerMark, openerTarget } from './picker.js';

function fakeNode({ classes = [], dataset = {}, host = null, connected = true } = {}) {
  return {
    classList: classes,
    dataset,
    isConnected: connected,
    closest: () => host,
  };
}

function fakeHost(attr, val, children) {
  return {
    dataset: { [attr]: val },
    hasAttribute: (a) => a === `data-${attr}`,
    querySelector: (sel) => children.find((c) => {
      const act = sel.match(/^\[data-act="([^"]+)"\]$/);
      if (act) return c.dataset.act === act[1];
      return c.classList.includes(sel.replace(/^\./, ''));
    }) || null,
  };
}

describe('where the picker gives the keyboard back', () => {
  it('returns to the opener while it is still on the page', () => {
    const host = fakeHost('key', 's1', []);
    const btn = fakeNode({ classes: ['u-btn', 'ui2-st-key'], dataset: { act: 'key' }, host });
    const mark = openerMark(btn);
    expect(openerTarget(mark, { querySelectorAll: () => [] })).toBe(btn);
  });

  it('finds the same Radio row button after the list was redrawn', () => {
    const old = fakeNode({ classes: ['u-btn', 'ui2-st-key'], dataset: { act: 'key' }, host: fakeHost('key', 's1', []) });
    const mark = openerMark(old);
    expect(mark.host).toEqual({ attr: 'key', val: 's1', act: 'key', cls: 'ui2-st-key' });
    old.isConnected = false;
    const fresh = fakeNode({ classes: ['u-btn', 'ui2-st-key'], dataset: { act: 'key' } });
    const other = fakeHost('key', 's2', [fakeNode({ dataset: { act: 'key' } })]);
    const same = fakeHost('key', 's1', [fresh]);
    const doc = { querySelectorAll: (sel) => (sel === '[data-key]' ? [other, same] : []) };
    expect(openerTarget(mark, doc)).toBe(fresh);
  });

  it('finds a Recently played card button by its own class, not the shell one', () => {
    const old = fakeNode({ classes: ['u-btn', 'u-btn--icon', 'rc-pick'], host: fakeHost('ck', 'radio:x', []) });
    const mark = openerMark(old);
    expect(mark.host.cls).toBe('rc-pick');
    old.isConnected = false;
    const fresh = fakeNode({ classes: ['u-btn', 'u-btn--icon', 'rc-pick'] });
    const doc = { querySelectorAll: (sel) => (sel === '[data-ck]' ? [fakeHost('ck', 'radio:x', [fresh])] : []) };
    expect(openerTarget(mark, doc)).toBe(fresh);
  });

  it('gives up quietly when nothing stands for the opener any more', () => {
    const old = fakeNode({ classes: ['btn'], connected: false });
    expect(openerTarget(openerMark(old), { querySelectorAll: () => [] })).toBeNull();
    expect(openerTarget(null)).toBeNull();
    expect(openerMark(null)).toBeNull();
  });
});
