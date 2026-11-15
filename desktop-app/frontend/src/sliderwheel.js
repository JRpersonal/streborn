// sliderwheel.js: the mouse wheel and the touchpad on a range slider.
//
// The Music view's volume (views/music/volume.js) has stepped by one per wheel
// notch since it was asked for, but only there and only along the vertical
// axis. UI 2.0 has more sliders (the player volume, the group volume, every
// speaker's level under Levels, volume, balance and bass on a speaker's Sound
// tab), and a touchpad sends a stream of small deltas in both directions where
// a mouse sends one big one per notch. This module turns either into whole
// slider steps and hands them to the slider the way a drag does: the value
// moves, an `input` event per step runs the slider's own throttled and clamped
// write path (the volume ceiling included), and one `change` event at the end
// of the gesture stands for the release. So no slider gets a second write path
// and nothing here knows what a slider controls.
//
// Pure helpers plus one wiring function; nothing touches `document` at import
// time (vitest runs without a DOM, and the module must stay import-safe).

// A touchpad moves this many pixels for one slider step. About a finger's
// width: a deliberate swipe across the pad moves a volume by ten or so, a
// resting finger's jitter moves nothing.
export const WHEEL_STEP_PX = 40;
// DOM_DELTA_LINE / DOM_DELTA_PAGE deltas, in the pixels above. Only their sign
// and size relative to the step matter: a line or page delta is a notch anyway.
const LINE_PX = 40;
const PAGE_PX = 400;
// Wheel events this close together belong to one gesture. It is also how long
// after the last event the gesture counts as released (the `change` event).
export const WHEEL_GESTURE_GAP_MS = 400;
// At most one step per this many ms. A free-spinning wheel or a hard flick can
// send dozens of notches in a blink, and the classic slider's comment says why
// that must not become a volume blast: it moves one at a time, quickly.
export const WHEEL_MIN_STEP_MS = 40;

// The two events a slider can listen for to treat a wheel gesture like a drag:
// start comes before the first value change (so a baseline can be taken from
// the level the gesture started at), end after the last one. Neither bubbles.
export const WHEEL_START = 'str-wheelstart';
export const WHEEL_END = 'str-wheelend';

function scaleOf(e) {
  if (e.deltaMode === 1) return LINE_PX;
  if (e.deltaMode === 2) return PAGE_PX;
  return 1;
}

// wheelPixels is a wheel event's movement in pixels toward MORE: up and right
// raise the slider, down and left lower it, whichever axis moved further.
// Shift with a plain mouse wheel arrives as a horizontal delta in the opposite
// sense (wheel up is deltaX < 0); it keeps the wheel's own direction.
export function wheelPixels(e) {
  if (!e) return 0;
  const s = scaleOf(e);
  const dy = -(Number(e.deltaY) || 0) * s;
  let dx = (Number(e.deltaX) || 0) * s;
  if (e.shiftKey && !dy && dx) dx = -dx;
  const px = Math.abs(dy) >= Math.abs(dx) ? dy : dx;
  return px || 0;
}

// isWheelNotch tells a mouse wheel's notch from a touchpad's stream. A line or
// page delta is always a notch (Firefox, WebKitGTK); in pixel mode the legacy
// wheelDelta is a multiple of 120 per notch in Chromium (WebView2) and WebKit,
// and anything else for a touchpad. One notch is one step, as on the classic
// slider, however many pixels the platform says it is worth.
export function isWheelNotch(e) {
  if (!e) return false;
  if (e.deltaMode === 1 || e.deltaMode === 2) return true;
  const wy = Number(e.wheelDeltaY) || 0;
  const wx = Number(e.wheelDeltaX) || 0;
  const w = Math.abs(wy) >= Math.abs(wx) ? wy : wx;
  return w !== 0 && w % 120 === 0;
}

// createWheelStepper accumulates one gesture's deltas into whole steps:
// feed(event, now) returns the signed number of steps to move now (0 while a
// touchpad is still short of a step). A change of direction starts over, so
// a step back is one step's travel, not the remainder of the way forward.
export function createWheelStepper({ stepPx = WHEEL_STEP_PX, minStepMs = WHEEL_MIN_STEP_MS } = {}) {
  let acc = 0;
  let lastStepAt = -Infinity;
  return {
    feed(e, now) {
      const px = wheelPixels(e);
      if (!px) return 0;
      if (acc && Math.sign(acc) !== Math.sign(px)) acc = 0;
      let steps;
      if (isWheelNotch(e)) {
        acc = 0;
        steps = Math.sign(px);
      } else {
        acc += px;
        steps = Math.trunc(acc / stepPx);
        acc -= steps * stepPx;
      }
      if (!steps) return 0;
      // The rate cap drops what comes too fast rather than queueing it: a
      // queue would keep the level moving after the hand has stopped.
      const allowed = Math.floor((now - lastStepAt) / minStepMs);
      const n = Math.min(Math.abs(steps), allowed);
      if (n <= 0) return 0;
      lastStepAt = now;
      return Math.sign(steps) * n;
    },
  };
}

function num(v, fallback) {
  const n = parseFloat(v);
  return Number.isFinite(n) ? n : fallback;
}

// steppedValue is the slider's value moved by steps of its own step size,
// clamped to its own min and max and kept on its step grid, the way a native
// range input rounds a dragged value. Defaults are the HTML ones (0, 100, 1).
export function steppedValue(el, steps) {
  const min = num(el.min, 0);
  const max = Math.max(min, num(el.max, 100));
  const rawStep = num(el.step, 1);
  const step = rawStep > 0 ? rawStep : 1;
  const cur = Math.min(max, Math.max(min, num(el.value, min)));
  const onGrid = min + Math.round((cur - min) / step) * step;
  const next = onGrid + steps * step;
  const clamped = Math.min(max, Math.max(min, next));
  // Back onto the grid from the clamped end, and rid of float noise (0.1 steps).
  const snapped = min + Math.floor((clamped - min) / step + 1e-9) * step;
  return Math.round(snapped * 1e6) / 1e6;
}

function rangeFrom(target, root) {
  let el = target && typeof target.closest === 'function' ? target.closest('input[type="range"]') : null;
  if (!el || el.disabled) return null;
  if (root && typeof root.contains === 'function' && !root.contains(el)) return null;
  return el;
}

function defaultEvent(type) {
  if (type === WHEEL_START || type === WHEEL_END) return new CustomEvent(type, { bubbles: false });
  return new Event(type, { bubbles: true });
}

// wireSliderWheel makes the wheel and the touchpad move every enabled range
// slider inside root, now and later (one delegated listener, so sliders that a
// re-render creates are covered without wiring each).
//
// Which wheel turns a slider and which scrolls the page: a slider with focus
// always takes the wheel. Otherwise a gesture that BEGINS with the pointer on
// a slider adjusts it, while a page scroll that is still going (a wheel event
// elsewhere less than a gesture gap ago) carries on scrolling when the page
// slides a slider under the pointer. Scrolling the page past the player or a
// Levels row must never change a level by accident; that is the accident the
// classic slider guards against by asking for a click first.
//
// While a slider takes the wheel the page does not scroll, also at its ends,
// so reaching 100 does not suddenly move the page instead.
//
// opts (all optional, for the tests): now, setTimer, clearTimer,
// activeElement (a function), makeEvent(type).
export function wireSliderWheel(root, opts = {}) {
  if (!root || typeof root.addEventListener !== 'function') return () => {};
  const now = opts.now || (() => Date.now());
  const setTimer = opts.setTimer || ((fn, ms) => setTimeout(fn, ms));
  const clearTimer = opts.clearTimer || ((id) => clearTimeout(id));
  const activeElement = opts.activeElement || (() => (typeof document !== 'undefined' ? document.activeElement : null));
  const makeEvent = opts.makeEvent || defaultEvent;

  let lastAt = -Infinity;
  let lastSlider = null;
  let gesture = null;

  const finish = () => {
    const g = gesture;
    gesture = null;
    if (!g) return;
    if (g.timer !== null) clearTimer(g.timer);
    // The release: a slider that writes on change (balance) writes now, and
    // the one that took a baseline at the start lets the poll back in.
    if (g.moved) g.el.dispatchEvent(makeEvent('change'));
    if (g.started) g.el.dispatchEvent(makeEvent(WHEEL_END));
  };

  const adjust = (el, e, t) => {
    if (!gesture || gesture.el !== el) {
      finish();
      gesture = { el, stepper: createWheelStepper(), started: false, moved: false, timer: null };
    }
    const g = gesture;
    const steps = g.stepper.feed(e, t);
    if (steps) {
      const cur = num(el.value, NaN);
      const next = steppedValue(el, steps);
      if (next !== cur) {
        if (!g.started) {
          g.started = true;
          el.dispatchEvent(makeEvent(WHEEL_START));
        }
        el.value = String(next);
        g.moved = true;
        el.dispatchEvent(makeEvent('input'));
      }
    }
    if (g.timer !== null) clearTimer(g.timer);
    g.timer = setTimer(() => { if (gesture === g) { g.timer = null; finish(); } }, WHEEL_GESTURE_GAP_MS);
  };

  const onWheel = (e) => {
    const t = now();
    const el = rangeFrom(e.target, root);
    const continuing = t - lastAt < WHEEL_GESTURE_GAP_MS;
    const prev = lastSlider;
    lastAt = t;
    if (!el) { lastSlider = null; return; }
    if (activeElement() !== el && continuing && prev !== el) {
      // A page scroll that the page itself slid this slider under.
      lastSlider = null;
      return;
    }
    lastSlider = el;
    if (typeof e.preventDefault === 'function') e.preventDefault();
    adjust(el, e, t);
  };

  // passive: false, or preventDefault is ignored and the page scrolls along.
  root.addEventListener('wheel', onWheel, { passive: false });
  return () => {
    finish();
    if (typeof root.removeEventListener === 'function') root.removeEventListener('wheel', onWheel, { passive: false });
  };
}
