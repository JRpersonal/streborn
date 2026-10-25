// wirePopover with stand-in elements: vitest runs without a DOM on purpose
// (vitest.config.js), and the behaviour pinned here is only event wiring.
import { describe, it, expect, beforeAll, afterAll } from 'vitest';
import { wirePopover } from './popover.js';

function fakeEl(name, children = []) {
  const handlers = {};
  const attrs = {};
  const el = {
    name,
    hidden: false,
    id: '',
    children,
    addEventListener(type, fn) { (handlers[type] = handlers[type] || []).push(fn); },
    fire(type, ev = {}) { (handlers[type] || []).forEach((fn) => fn({ preventDefault() {}, stopPropagation() {}, ...ev })); },
    setAttribute(k, v) { attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(attrs, k) ? attrs[k] : null; },
    hasAttribute(k) { return Object.prototype.hasOwnProperty.call(attrs, k); },
    contains(other) { return other === el || children.some((c) => c.contains(other)); },
    closest() { return null; },
    querySelector() { return null; },
    querySelectorAll() { return []; },
    focus() {},
    classList: { contains: () => false },
  };
  return el;
}

let savedDocument;
beforeAll(() => {
  savedDocument = globalThis.document;
  globalThis.document = { addEventListener() {}, activeElement: null };
});
afterAll(() => {
  if (savedDocument === undefined) delete globalThis.document;
  else globalThis.document = savedDocument;
});

describe('popover focus', () => {
  const setup = () => {
    const row = fakeEl('row');
    const panel = fakeEl('panel', [row]);
    panel.hidden = true;
    const trigger = fakeEl('trigger');
    const outside = fakeEl('nav item');
    const pop = wirePopover(trigger, panel);
    pop.open();
    return { pop, panel, trigger, row, outside };
  };

  it('closes when keyboard focus tabs out of the panel', () => {
    const { pop, panel, row, outside } = setup();
    expect(pop.isOpen()).toBe(true);
    panel.fire('focusout', { target: row, relatedTarget: outside });
    expect(pop.isOpen()).toBe(false);
    expect(panel.hidden).toBe(true);
  });

  it('closes when focus leaves the trigger for something outside', () => {
    const { pop, trigger, outside } = setup();
    trigger.fire('focusout', { target: trigger, relatedTarget: outside });
    expect(pop.isOpen()).toBe(false);
  });

  it('stays open while focus moves inside, back to the trigger, or nowhere', () => {
    const { pop, panel, trigger, row } = setup();
    panel.fire('focusout', { target: row, relatedTarget: row });
    panel.fire('focusout', { target: row, relatedTarget: trigger });
    trigger.fire('focusout', { target: trigger, relatedTarget: row });
    // A repaint replacing the focused row, or the window losing focus.
    panel.fire('focusout', { target: row, relatedTarget: null });
    expect(pop.isOpen()).toBe(true);
  });
});
