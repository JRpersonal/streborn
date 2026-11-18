// titleexpand.js: tap a cut-off title in a list to read all of it
// (discussion #1811).
//
// Long station, track and card names are cut with "…" so a row keeps its
// height. The full name was only in the tooltip, which a touch screen does not
// have and which nobody finds. A click on the cut-off TEXT now opens that one
// title to its full wrapped length, and a second click closes it again.
//
// What it must not do is get in the way of a row's own action:
//
//   - only the title text reacts, never the row around it, its logo or its
//     buttons, so Play, the key button and the star work as before;
//   - a title inside a control (a button, a link, a label, a key tile, whose
//     whole surface plays the key) is left alone: there the press belongs to
//     the control, and the tooltip keeps the full name;
//   - a title that is not cut off does nothing, so nothing changes for the
//     rows that already fit;
//   - dragging over a title to select and copy it is not a tap.
//
// A title that can be opened says so with a small arrow at its end, down while
// it is closed and up while it is open. The first version drew nothing, and
// nobody could tell that a cut title opens. The arrow is there only while the
// title really is cut off (or open), so a row that fits looks as it always
// did. It is a CSS shape on the title itself (style.css, .title-cut), not an
// element and not a font glyph: the whole title stays the one click target.
//
// There is no new control and no new string, and nothing is added to the
// keyboard's tab order: a keyboard or screen-reader user reads the full name
// as before, it was never shortened in the markup, only on screen.
//
// A list that is drawn again (Recently played follows the music) would close
// an opened title. The opened names are remembered and re-opened on the rows
// that come back, for as long as the window stays open.
//
// The phone remote has the same behaviour in its own page
// (internal/webui/assets/index.html, titlePress); titleexpand.test.js holds
// the two against each other.

// The cut-off titles of the lists: station search results, favourites and
// Pandora stations (.result-name-text), the cards of Recently played and the
// tracks under them (.rc-name, .rc-tr-main), and every two-line name
// (.clamp-2). The UI 2.0 lists wrap their names and never cut them.
export const TITLE_SELECTOR = '.result-name-text, .rc-name, .rc-tr-main, .clamp-2';

// Where a press belongs to something else. .preset is a key tile.
export const CONTROL_SELECTOR = 'button, a, input, select, textarea, label, summary, [role="button"], .preset';

export const OPEN_CLASS = 'title-open';

// On a closed title that is cut off right now: it carries the arrow.
export const CUT_CLASS = 'title-cut';

// How many opened names are remembered; the oldest goes first.
export const MAX_OPEN = 50;

// titlePress decides one click on a title: 'open', 'close' or '' (leave it).
//
//   inControl  the title sits inside a control
//   open       it is opened already
//   cutOff     it does not fit its line(s) right now
//   selecting  the click ended a text selection
export function titlePress({ inControl = false, open = false, cutOff = false, selecting = false } = {}) {
  if (inControl || selecting) return '';
  if (open) return 'close';
  return cutOff ? 'open' : '';
}

// isCutOff says whether an element shows less than it holds: wider than its
// line (the one-line ellipsis) or taller than its box (the two-line clamp).
// One pixel of slack for sub-pixel rounding.
export function isCutOff(el) {
  return el.scrollWidth > el.clientWidth + 1 || el.scrollHeight > el.clientHeight + 1;
}

// openTitles remembers the opened names, the newest last, at most max.
export function openTitles(max = MAX_OPEN) {
  let names = [];
  return {
    add(name) {
      if (!name) return;
      names = names.filter((n) => n !== name);
      names.push(name);
      if (names.length > max) names = names.slice(names.length - max);
    },
    delete(name) { names = names.filter((n) => n !== name); },
    has(name) { return !!name && names.includes(name); },
    get size() { return names.length; },
  };
}

// titleMark says whether a title carries the arrow: an opened one always (it
// points up there), a closed one only while it is cut off, and never a title
// inside a control, which does not react to the press.
export function titleMark({ inControl = false, open = false, cutOff = false } = {}) {
  if (inControl) return false;
  return open || cutOff;
}

// markCutOff puts the arrow on the closed titles among els that are cut off
// and takes it from the ones that fit. The arrow needs room at the end of the
// title, so a title is always measured WITHOUT it: otherwise a title that just
// fits would stay marked for ever once it had been cut. All marks come off
// first, then everything is measured, then the marks go on, so the browser
// lays the page out twice however long the list is, and nothing is painted in
// between.
export function markCutOff(els) {
  const closed = [];
  for (const el of els) {
    if (!el.isConnected || el.classList.contains(OPEN_CLASS) || el.closest(CONTROL_SELECTOR)) continue;
    el.classList.remove(CUT_CLASS);
    closed.push(el);
  }
  const cut = closed.map((el) => isCutOff(el));
  closed.forEach((el, i) => {
    if (titleMark({ cutOff: cut[i] })) el.classList.add(CUT_CLASS);
  });
}

const nameOf = (el) => String(el.textContent || '').trim();

const titlesIn = (node) => {
  const found = [...node.querySelectorAll(TITLE_SELECTOR)];
  if (node.matches(TITLE_SELECTOR)) found.push(node);
  return found;
};

// mountTitleExpand wires the behaviour once for the whole window: one click
// listener, one observer that re-opens remembered titles on rows drawn again,
// and one ResizeObserver that keeps the arrows true.
//
// What is cut off changes with the window's width, the language and the
// content, and a list drawn in a hidden view has no size at all. So every
// title is watched for its size, and measured when it is drawn, when its text
// changes and whenever its box changes. No timer, no polling.
//
// The measuring itself always happens at the start of the next frame, all
// titles that are due in one go. Not inside the observer's own report: the
// arrow's room can change a title's size by a few pixels (a title sized by
// its own text, a row whose other parts give way), and changing an observed
// size in there makes the browser raise "ResizeObserver loop completed with
// undelivered notifications". A row that was just drawn is measured before
// its first paint, so its arrow does not pop in.
export function mountTitleExpand(doc = document) {
  const remembered = openTitles();
  const win = doc.defaultView || globalThis;

  let due = null;
  const measureSoon = (el) => {
    if (!due) {
      due = new Set();
      win.requestAnimationFrame(() => { const els = [...due]; due = null; markCutOff(els); });
    }
    due.add(el);
  };
  let watch = measureSoon;
  let unwatch = () => {};
  if (typeof win.ResizeObserver !== 'undefined') {
    const sizes = new win.ResizeObserver((entries) => entries.forEach((e) => measureSoon(e.target)));
    watch = (el) => { sizes.observe(el, { box: 'border-box' }); measureSoon(el); };
    unwatch = (el) => sizes.unobserve(el);
  } else {
    // An engine without ResizeObserver: everything again when the window
    // changes its size.
    win.addEventListener('resize', () => { if (doc.body) titlesIn(doc.body).forEach(measureSoon); });
  }

  doc.addEventListener('click', (ev) => {
    const target = ev.target;
    const el = target && target.closest ? target.closest(TITLE_SELECTOR) : null;
    if (!el) return;
    const sel = doc.getSelection ? String(doc.getSelection() || '') : '';
    const open = el.classList.contains(OPEN_CLASS);
    const press = titlePress({
      inControl: !!el.closest(CONTROL_SELECTOR),
      open,
      cutOff: !open && isCutOff(el),
      selecting: sel.trim() !== '',
    });
    if (!press) return;
    el.classList.toggle(OPEN_CLASS, press === 'open');
    if (press === 'open') remembered.add(nameOf(el)); else remembered.delete(nameOf(el));
    // Closed again: it is cut off again unless the window grew meanwhile.
    if (press === 'close') markCutOff([el]);
  });

  if (typeof MutationObserver === 'undefined' || !doc.body) return;
  titlesIn(doc.body).forEach(watch);
  new MutationObserver((muts) => {
    for (const m of muts) {
      for (const node of m.removedNodes) {
        if (node.nodeType === 1) titlesIn(node).forEach(unwatch);
      }
      // New text written into a title that stays where it is.
      const changed = m.addedNodes.length && m.target.nodeType === 1 ? m.target.closest(TITLE_SELECTOR) : null;
      if (changed) watch(changed);
      for (const node of m.addedNodes) {
        if (node.nodeType !== 1) continue;
        for (const el of titlesIn(node)) {
          if (el.closest(CONTROL_SELECTOR)) continue;
          if (remembered.has(nameOf(el))) el.classList.add(OPEN_CLASS);
          watch(el);
        }
      }
    }
  }).observe(doc.body, { childList: true, subtree: true });
}
