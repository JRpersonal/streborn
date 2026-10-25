// helpers/layout.mjs: geometry checks. Inject a11y.KILL_ANIM first, so no box
// is measured mid-transition.
//
// The async ones assert; disjoint and inside are pure and return a verdict.

import { expect } from '@playwright/test';

// The page does not scroll sideways.
export async function noHorizontalOverflow(page) {
  const m = await page.evaluate(() => ({ scroll: document.documentElement.scrollWidth, client: document.documentElement.clientWidth }));
  expect(m.scroll, `page scrolls sideways (${m.scroll} > ${m.client})`).toBeLessThanOrEqual(m.client + 1);
}

// Every element the locator matches fits its own width (no clipped text).
export async function fitsX(locator) {
  const over = await locator.evaluateAll((els) => els
    .filter((e) => e.scrollWidth > e.clientWidth + 1)
    .map((e) => `${e.id || e.className || e.tagName}: ${e.scrollWidth} > ${e.clientWidth}`));
  expect(over, 'elements wider than their box').toEqual([]);
}

const overlaps = (a, b) => a.x < b.x + b.width && b.x < a.x + a.width && a.y < b.y + b.height && b.y < a.y + a.height;

// disjoint returns the index pairs of boxes that intersect; [] means none do.
// Boxes as from locator.boundingBox(); null entries (not rendered) are skipped.
export function disjoint(boxes) {
  const hits = [];
  for (let i = 0; i < boxes.length; i++) {
    for (let j = i + 1; j < boxes.length; j++) {
      if (boxes[i] && boxes[j] && overlaps(boxes[i], boxes[j])) hits.push([i, j]);
    }
  }
  return hits;
}

// inside reports whether box lies fully within the viewport ({ width, height }).
export function inside(viewport, box, tolerance = 0.5) {
  return !!box && box.x >= -tolerance && box.y >= -tolerance
    && box.x + box.width <= viewport.width + tolerance && box.y + box.height <= viewport.height + tolerance;
}

// Every VISIBLE element the locator matches is at least min x min px
// (WCAG 2.5.8 asks 24, the app's own target is 40).
export async function minSize(locator, min = 40) {
  const small = await locator.evaluateAll((els, m) => els
    .filter((e) => e.offsetParent !== null || getComputedStyle(e).position === 'fixed')
    .map((e) => ({ e, r: e.getBoundingClientRect() }))
    .filter(({ r }) => r.width + 0.5 < m || r.height + 0.5 < m)
    .map(({ e, r }) => `${e.id || e.className || e.tagName}: ${Math.round(r.width)}x${Math.round(r.height)}`), min);
  expect(small, `targets smaller than ${min}x${min}`).toEqual([]);
}
