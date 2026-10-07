// #1190: a click on the rename (tag) glyph of a preset key played the key
// instead of opening the rename.
//
// The icons are inline SVGs, so the click target is the <svg> or one of its
// <path>s, not the .ren button. isKeyChrome used to look only at the target's
// own classList, so a click on the drawing went straight through to the play
// handler. It now asks for the nearest .ren/.del ancestor.
import { describe, it, expect } from 'vitest';
import { isKeyChrome } from './utils.js';

// A minimal element: its own classes plus a parent chain, with the closest()
// a browser gives for a plain ".a, .b" class selector list.
function el(classes, parent = null) {
  const node = {
    parent,
    classList: { contains: (c) => classes.includes(c) },
    closest(sel) {
      const wanted = sel.split(',').map(s => s.trim().replace(/^\./, ''));
      for (let n = node; n; n = n.parent) {
        if (wanted.some(w => n.classList.contains(w))) return n;
      }
      return null;
    },
  };
  return node;
}

describe('isKeyChrome', () => {
  const tile = el(['preset']);

  it('catches a click on the svg path inside the rename button', () => {
    const ren = el(['ren'], tile);
    const svg = el([], ren);
    const path = el([], svg);
    expect(isKeyChrome(path)).toBe(true);
    expect(isKeyChrome(svg)).toBe(true);
    expect(isKeyChrome(ren)).toBe(true);
  });

  it('catches the clear button the same way', () => {
    const del = el(['del'], tile);
    expect(isKeyChrome(el([], el([], del)))).toBe(true);
  });

  it('lets a click on the key itself through', () => {
    expect(isKeyChrome(tile)).toBe(false);
    expect(isKeyChrome(el(['preset-logo'], tile))).toBe(false);
  });

  it('says no to targets without closest (text nodes, nothing)', () => {
    expect(isKeyChrome(null)).toBe(false);
    expect(isKeyChrome({})).toBe(false);
  });
});
