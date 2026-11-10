import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { FakeDocument } from './speakers/fakedom.testutil.js';
import { liveRegion } from './live.js';
import { fileSource } from '../test/source.js';

let doc;
beforeEach(() => {
  doc = new FakeDocument();
  globalThis.document = doc;
});
afterEach(() => { delete globalThis.document; });

describe('liveRegion', () => {
  it('creates nothing until it is asked for', () => {
    const live = liveRegion('ui2TestLive');
    expect(live.attached()).toBe(false);
    expect(doc.getElementById('ui2TestLive')).toBeNull();
  });

  it('creates one empty polite status region on <body>', () => {
    const live = liveRegion('ui2TestLive');
    const node = live.ensure();
    expect(node.parentNode).toBe(doc.body);
    expect(node.id).toBe('ui2TestLive');
    expect(node.className).toBe('u-sr-only');
    expect(node.getAttribute('role')).toBe('status');
    expect(node.getAttribute('aria-live')).toBe('polite');
    expect(node.textContent).toBe('');
    expect(live.attached()).toBe(true);
  });

  it('hands the same node back while it is on the page', () => {
    const live = liveRegion('ui2TestLive');
    const node = live.ensure();
    node.textContent = 'Done.';
    expect(live.ensure()).toBe(node);
    expect(doc.body.children.length).toBe(1);
    expect(live.ensure().textContent).toBe('Done.');
  });

  it('makes a new one after the node left the page', () => {
    const live = liveRegion('ui2TestLive');
    const first = live.ensure();
    first.remove();
    expect(live.attached()).toBe(false);
    const second = live.ensure();
    expect(second).not.toBe(first);
    expect(second.parentNode).toBe(doc.body);
    expect(doc.body.children.length).toBe(1);
  });

  it('keeps two regions apart', () => {
    const a = liveRegion('ui2ALive');
    const b = liveRegion('ui2BLive');
    expect(a.ensure()).not.toBe(b.ensure());
    expect(doc.getElementById('ui2ALive')).toBe(a.ensure());
    expect(doc.getElementById('ui2BLive')).toBe(b.ensure());
  });
});

describe('the pages that speak through it', () => {
  it('keep their own region ids', () => {
    expect(fileSource('shell2/speakers/list.js')).toContain("liveRegion('ui2SplLive')");
    expect(fileSource('shell2/speakers/groups.js')).toContain("liveRegion('ui2GrpLive')");
  });

  it('do not build a region of their own any more', () => {
    for (const rel of ['shell2/speakers/list.js', 'shell2/speakers/groups.js']) {
      expect(fileSource(rel)).not.toContain("setAttribute('aria-live'");
    }
  });
});
