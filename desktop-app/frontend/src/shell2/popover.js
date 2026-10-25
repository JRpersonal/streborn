// shell2/popover.js: the one popover behaviour of UI 2.0 (More, Levels).
//
// Keyboard first: Enter or Space on the trigger opens it and puts focus on the
// first control; Escape closes it and gives focus back to the trigger; a click
// anywhere outside, or focus moving outside, closes it. The arrow keys belong
// to the controls inside, a volume slider above all: no popover is a list (the
// speakers live in the sidebar, speakers.js).
//
// Runtime only: it touches the DOM when called, never at import.

const FOCUSABLE = 'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

// visible tells whether el can take focus: not inside [hidden] or .hidden, and
// with a layout box. A control hidden by CSS alone (the group chips in Levels,
// shell2.css) ignores focus(), and picking it left focus on the trigger.
function visible(el) {
  if (!el || el.closest('[hidden]') || el.closest('.hidden')) return false;
  if (typeof el.checkVisibility === 'function') return el.checkVisibility();
  const win = typeof window !== 'undefined' ? window : null;
  if (!win || typeof win.getComputedStyle !== 'function') return true;
  for (let n = el; n && n.nodeType === 1; n = n.parentElement) {
    const cs = win.getComputedStyle(n);
    if (cs.display === 'none' || cs.visibility === 'hidden') return false;
  }
  return true;
}

export function wirePopover(trigger, panel, opts = {}) {
  if (!trigger || !panel) return null;
  trigger.setAttribute('aria-expanded', 'false');
  if (panel.id) trigger.setAttribute('aria-controls', panel.id);

  const isOpen = () => !panel.hidden;

  const focusInitial = () => {
    const first = [...panel.querySelectorAll(FOCUSABLE)].filter(visible).find((el) => !el.classList.contains('u-pop-close'))
      || panel.querySelector('.u-pop-close');
    if (first) first.focus();
  };

  const open = () => {
    if (isOpen()) return;
    panel.hidden = false;
    trigger.setAttribute('aria-expanded', 'true');
    if (typeof opts.onOpen === 'function') { try { opts.onOpen(); } catch (_) { /* best effort */ } }
    focusInitial();
  };
  const close = (returnFocus) => {
    if (!isOpen()) return;
    panel.hidden = true;
    trigger.setAttribute('aria-expanded', 'false');
    if (typeof opts.onClose === 'function') { try { opts.onClose(); } catch (_) { /* best effort */ } }
    if (returnFocus) trigger.focus();
  };

  trigger.addEventListener('click', (e) => {
    e.stopPropagation();
    if (isOpen()) close(false); else open();
  });
  const closeBtn = panel.querySelector('.u-pop-close');
  if (closeBtn) closeBtn.addEventListener('click', () => close(true));

  panel.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      close(true);
    }
  });
  trigger.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && isOpen()) { e.preventDefault(); close(true); }
  });

  // Focus leaving closes it too, so a keyboard user who tabs past the last
  // control never lands on something the still-open panel covers (WCAG 2.4.11,
  // Focus Not Obscured). Only a move to a known element outside both counts: no
  // relatedTarget means the window lost focus or the focused node was replaced
  // by a repaint of the list, and neither is the user leaving.
  const onFocusOut = (e) => {
    if (!isOpen()) return;
    const to = e.relatedTarget;
    if (!to) return;
    if (panel.contains(to) || trigger.contains(to)) return;
    close(false);
  };
  panel.addEventListener('focusout', onFocusOut);
  trigger.addEventListener('focusout', onFocusOut);

  // A click outside closes it. A click whose target is no longer in the page
  // does not count as outside: a control inside the popover that re-renders
  // its own list on click (the group chips do) has already detached the node
  // the click started on by the time the event reaches the document.
  document.addEventListener('click', (e) => {
    if (!isOpen()) return;
    const tgt = e.target;
    if (!tgt || !tgt.isConnected) return;
    if (panel.contains(tgt) || trigger.contains(tgt)) return;
    close(false);
  });

  return { open, close, isOpen };
}
