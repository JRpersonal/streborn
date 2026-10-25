// shell2/popover.js: the one popover behaviour of UI 2.0 (room switcher,
// More, Output).
//
// Keyboard first: Enter or Space on the trigger opens it and puts focus on the
// current row (or the first control); Escape closes it and gives focus back to
// the trigger; a click anywhere outside, or focus moving outside, closes it.
// Arrow keys move between rows only where the popover is a LIST (the room
// switcher). In More and
// Output the arrows belong to the controls inside, a volume slider above all,
// and a text field or a select never loses its own arrow keys anywhere.
//
// Runtime only: it touches the DOM when called, never at import.

const FOCUSABLE = 'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

function visible(el) {
  return !!el && !el.closest('[hidden]') && !el.closest('.hidden');
}

export function wirePopover(trigger, panel, opts = {}) {
  if (!trigger || !panel) return null;
  const roving = !!opts.roving;
  trigger.setAttribute('aria-expanded', 'false');
  if (panel.id) trigger.setAttribute('aria-controls', panel.id);

  const rows = () => [...panel.querySelectorAll('.u-row')].filter(visible);
  const isOpen = () => !panel.hidden;

  const focusInitial = () => {
    const current = rows().find((r) => r.getAttribute('aria-pressed') === 'true' || r.hasAttribute('aria-current'));
    const first = current || rows()[0]
      || [...panel.querySelectorAll(FOCUSABLE)].filter(visible).find((el) => !el.classList.contains('u-pop-close'))
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
      return;
    }
    if (!roving) return;
    const tag = (e.target && e.target.tagName) || '';
    if (tag === 'INPUT' || tag === 'SELECT' || tag === 'TEXTAREA') return;
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp' && e.key !== 'Home' && e.key !== 'End') return;
    const list = rows();
    if (!list.length) return;
    e.preventDefault();
    const at = list.indexOf(document.activeElement);
    let next = 0;
    if (e.key === 'End') next = list.length - 1;
    else if (e.key === 'ArrowDown') next = at < 0 ? 0 : Math.min(list.length - 1, at + 1);
    else if (e.key === 'ArrowUp') next = at < 0 ? list.length - 1 : Math.max(0, at - 1);
    list[next].focus();
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
