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

const nameOf = (el) => String(el.textContent || '').trim();

// mountTitleExpand wires the behaviour once for the whole window: one click
// listener, and one observer that re-opens remembered titles on rows drawn
// again. The observer does nothing while no title is open.
export function mountTitleExpand(doc = document) {
  const remembered = openTitles();

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
  });

  if (typeof MutationObserver === 'undefined' || !doc.body) return;
  new MutationObserver((muts) => {
    if (!remembered.size) return;
    for (const m of muts) {
      for (const node of m.addedNodes) {
        if (node.nodeType !== 1) continue;
        const found = [...node.querySelectorAll(TITLE_SELECTOR)];
        if (node.matches(TITLE_SELECTOR)) found.push(node);
        for (const el of found) {
          if (remembered.has(nameOf(el)) && !el.closest(CONTROL_SELECTOR)) el.classList.add(OPEN_CLASS);
        }
      }
    }
  }).observe(doc.body, { childList: true, subtree: true });
}
