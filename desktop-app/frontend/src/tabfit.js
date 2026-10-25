// Fitting the top tab bar into the column.
//
// Eight tabs do not fit one row in every case: Lithuanian and Ukrainian labels
// ran past the 1000px minimum window at normal text size, and every language
// did at Large and Extra large, so the page scrolled sideways and the last tabs
// were cut off. The bar now steps down until it fits: first the usual row,
// then the same row with tighter padding, and only if that still overflows a
// row that wraps (style.css, .tabs-compact / .tabs-wrap). Where the old row
// fitted, nothing changes.
//
// No DOM is touched at import time, so the module stays import-safe in tests.

export const TAB_FIT_CLASSES = ['tabs-compact', 'tabs-wrap'];

const overflows = (bar) => bar.scrollWidth > bar.clientWidth;

// fitTabs resets the bar to its roomiest layout and adds one step at a time
// while the labels still overflow. All three states are measured in the same
// task, so nothing in between is ever painted.
export function fitTabs(bar) {
  if (!bar || !bar.classList) return '';
  bar.classList.remove(...TAB_FIT_CLASSES);
  for (const cls of TAB_FIT_CLASSES) {
    if (!overflows(bar)) break;
    bar.classList.add(cls);
  }
  return TAB_FIT_CLASSES.filter((c) => bar.classList.contains(c)).join(' ');
}

// watchTabs fits the bar now and again whenever the bar or one of its tabs
// changes size: a window resize, a text-size switch, a late web font, the
// Multi-Room count or a new-feature dot. The refit runs in the next animation
// frame rather than inside the observer callback: changing the bar's height
// there would end the frame with an undelivered observation, which the browser
// reports as a "ResizeObserver loop" error and the app's error banner would
// show it. The refit lands on the same state, so the observer settles after one
// extra round.
export function watchTabs(bar, win = globalThis) {
  if (!bar) return;
  fitTabs(bar);
  if (typeof win.ResizeObserver !== 'function') return;
  let queued = false;
  const ro = new win.ResizeObserver(() => {
    if (queued) return;
    queued = true;
    win.requestAnimationFrame(() => { queued = false; fitTabs(bar); });
  });
  ro.observe(bar);
  for (const btn of bar.querySelectorAll('.tab-btn')) ro.observe(btn);
}
