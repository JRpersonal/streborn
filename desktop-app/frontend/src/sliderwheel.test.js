// The wheel and the touchpad on a slider (sliderwheel.js), without a DOM:
// vitest runs in node on purpose, so the wiring is driven with stand-ins.
import { describe, it, expect } from 'vitest';
import {
  WHEEL_END, WHEEL_GESTURE_GAP_MS, WHEEL_MIN_STEP_MS, WHEEL_START, WHEEL_STEP_PX,
  createWheelStepper, isWheelNotch, steppedValue, wheelPixels, wireSliderWheel,
} from './sliderwheel.js';
import { fileSource } from './test/source.js';

// A Chromium mouse notch (WebView2): 100 px, wheelDelta 120.
const notch = (dir) => ({ deltaMode: 0, deltaY: dir > 0 ? -100 : 100, deltaX: 0, wheelDeltaY: dir > 0 ? 120 : -120, wheelDeltaX: 0 });
// A touchpad sample: a few pixels, no notch.
const pad = (dy, dx = 0) => ({ deltaMode: 0, deltaY: dy, deltaX: dx, wheelDeltaY: Math.round(-dy * 1.2), wheelDeltaX: Math.round(-dx * 1.2) });

describe('wheelPixels', () => {
  it('reads up and right as more, down and left as less', () => {
    expect(wheelPixels({ deltaY: -10 })).toBe(10);
    expect(wheelPixels({ deltaY: 10 })).toBe(-10);
    expect(wheelPixels({ deltaX: 10 })).toBe(10);
    expect(wheelPixels({ deltaX: -10 })).toBe(-10);
  });

  it('follows the axis that moved further', () => {
    expect(wheelPixels({ deltaY: -3, deltaX: 12 })).toBe(12);
    expect(wheelPixels({ deltaY: -12, deltaX: 3 })).toBe(12);
  });

  it('keeps the wheel direction under Shift, where it arrives on the x axis', () => {
    // Shift + wheel up arrives as deltaX < 0: still "more".
    expect(wheelPixels({ deltaX: -100, shiftKey: true })).toBe(100);
  });

  it('scales line and page deltas', () => {
    expect(wheelPixels({ deltaMode: 1, deltaY: -3 })).toBeGreaterThanOrEqual(3 * WHEEL_STEP_PX);
    expect(wheelPixels({ deltaMode: 2, deltaY: 1 })).toBeLessThan(-WHEEL_STEP_PX);
  });

  it('is zero for no movement', () => {
    expect(wheelPixels({})).toBe(0);
    expect(wheelPixels(null)).toBe(0);
  });
});

describe('isWheelNotch', () => {
  it('tells a mouse notch from a touchpad', () => {
    expect(isWheelNotch(notch(1))).toBe(true);
    expect(isWheelNotch({ deltaMode: 1, deltaY: 3 })).toBe(true);
    expect(isWheelNotch(pad(4))).toBe(false);
    expect(isWheelNotch({ deltaY: 100 })).toBe(false);
  });
});

describe('createWheelStepper', () => {
  it('moves one step per mouse notch, however many pixels it is worth', () => {
    const s = createWheelStepper();
    expect(s.feed(notch(1), 0)).toBe(1);
    expect(s.feed(notch(-1), 100)).toBe(-1);
    expect(s.feed({ ...notch(1), deltaY: -300 }, 200)).toBe(1);
  });

  it('adds small touchpad deltas up to whole steps', () => {
    const s = createWheelStepper();
    let total = 0;
    let t = 0;
    for (let i = 0; i < 10; i++) { total += s.feed(pad(-8), t); t += 50; }
    // 80 px up is two steps of 40, none lost and none invented.
    expect(total).toBe(2);
  });

  it('works on the horizontal axis', () => {
    const s = createWheelStepper();
    let total = 0;
    for (let i = 0; i < 5; i++) total += s.feed(pad(0, -16), i * 50);
    expect(total).toBe(-2);
  });

  it('starts over when the direction turns', () => {
    const s = createWheelStepper();
    expect(s.feed(pad(-30), 0)).toBe(0);
    // 30 px up were pending; 30 px down must not cancel them into nothing and
    // then need 70 more: a reversal is one fresh step of travel.
    expect(s.feed(pad(30), 50)).toBe(0);
    expect(s.feed(pad(10), 100)).toBe(-1);
  });

  it('caps the rate, so a free-spinning wheel cannot blast the level', () => {
    const s = createWheelStepper();
    let total = 0;
    // Fifty notches within 100 ms.
    for (let i = 0; i < 50; i++) total += s.feed(notch(1), i * 2);
    expect(total).toBeLessThanOrEqual(Math.ceil(100 / WHEEL_MIN_STEP_MS) + 1);
    expect(total).toBeGreaterThan(0);
  });
});

describe('steppedValue', () => {
  it('steps, clamps to min and max, and stays on the step grid', () => {
    expect(steppedValue({ value: '50', min: '0', max: '100', step: '1' }, 2)).toBe(52);
    expect(steppedValue({ value: '99', min: '0', max: '100', step: '1' }, 5)).toBe(100);
    expect(steppedValue({ value: '1', min: '0', max: '100', step: '1' }, -5)).toBe(0);
    expect(steppedValue({ value: '-7', min: '-7', max: '7', step: '1' }, -1)).toBe(-7);
    expect(steppedValue({ value: '0', min: '-7', max: '7', step: '1' }, 1)).toBe(1);
    expect(steppedValue({ value: '0.3', min: '0', max: '1', step: '0.1' }, 1)).toBe(0.4);
    expect(steppedValue({ value: '6', min: '0', max: '10', step: '3' }, 2)).toBe(9);
  });

  it('uses the HTML defaults where an attribute is missing', () => {
    expect(steppedValue({ value: '10' }, 1)).toBe(11);
    expect(steppedValue({ value: '100' }, 1)).toBe(100);
  });
});

// A stand-in world: a root with a delegated listener, sliders inside it,
// a clock and timers the test drives.
function world() {
  const handlers = {};
  const timers = new Map();
  let nextId = 1;
  let clock = 1_000;
  let active = null;
  const root = {
    addEventListener(type, fn, opts) { handlers[type] = { fn, opts }; },
    removeEventListener(type) { delete handlers[type]; },
    contains: (el) => el.inRoot !== false,
  };
  const slider = (attrs = {}) => {
    const el = {
      min: '0', max: '100', step: '1', value: '50', disabled: false, ...attrs,
      events: [],
      dispatchEvent(ev) {
        el.events.push(ev.type);
        if (ev.type === 'input' && el.oninput) el.oninput();
        if (ev.type === 'change' && el.onchange) el.onchange();
      },
    };
    el.closest = (sel) => (sel === 'input[type="range"]' ? el : null);
    return el;
  };
  const page = { closest: () => null };
  const wheel = (target, ev, advance = 30) => {
    clock += advance;
    let prevented = false;
    handlers.wheel.fn({ target, ...ev, preventDefault() { prevented = true; } });
    return prevented;
  };
  const flush = () => {
    clock += WHEEL_GESTURE_GAP_MS + 1;
    for (const [id, fn] of [...timers]) { timers.delete(id); fn(); }
  };
  wireSliderWheel(root, {
    now: () => clock,
    setTimer: (fn) => { const id = nextId++; timers.set(id, fn); return id; },
    clearTimer: (id) => timers.delete(id),
    activeElement: () => active,
    makeEvent: (type) => ({ type }),
  });
  return { handlers, slider, page, wheel, flush, focus: (el) => { active = el; }, idle: (ms) => { clock += ms; } };
}

describe('wireSliderWheel', () => {
  it('listens non-passively, so it can keep the page from scrolling', () => {
    const w = world();
    expect(w.handlers.wheel.opts).toEqual({ passive: false });
  });

  it('moves a slider under the pointer like a drag: start, input per step, change and end on release', () => {
    const w = world();
    const s = w.slider();
    expect(w.wheel(s, notch(1))).toBe(true);
    expect(w.wheel(s, notch(1), 60)).toBe(true);
    expect(s.value).toBe('52');
    expect(s.events).toEqual([WHEEL_START, 'input', 'input']);
    w.flush();
    expect(s.events).toEqual([WHEEL_START, 'input', 'input', 'change', WHEEL_END]);
  });

  it('sends the start before the value moves, so a baseline is the old level', () => {
    const w = world();
    const s = w.slider({ value: '30' });
    let seen = null;
    const orig = s.dispatchEvent;
    s.dispatchEvent = (ev) => { if (ev.type === WHEEL_START) seen = s.value; orig(ev); };
    w.wheel(s, notch(1));
    expect(seen).toBe('30');
    expect(s.value).toBe('31');
  });

  it('runs the slider\'s own input handler, which may clamp further (the volume ceiling)', () => {
    const w = world();
    const s = w.slider({ value: '49' });
    // As settings/basics.js holdUnderCeiling does with a ceiling of 50.
    s.oninput = () => { if (parseInt(s.value, 10) > 50) s.value = '50'; };
    w.wheel(s, notch(1));
    w.wheel(s, notch(1), 60);
    w.wheel(s, notch(1), 60);
    expect(s.value).toBe('50');
  });

  it('keeps the page still at the slider\'s end and sends nothing more', () => {
    const w = world();
    const s = w.slider({ value: '100' });
    expect(w.wheel(s, notch(1))).toBe(true);
    expect(s.value).toBe('100');
    expect(s.events).toEqual([]);
    w.flush();
    // Nothing moved: no change and no end either.
    expect(s.events).toEqual([]);
  });

  it('lets a page scroll go on when the page slides a slider under the pointer', () => {
    const w = world();
    const s = w.slider();
    expect(w.wheel(w.page, notch(-1))).toBe(false);
    expect(w.wheel(s, notch(-1), 50)).toBe(false);
    expect(w.wheel(s, notch(-1), 50)).toBe(false);
    expect(s.value).toBe('50');
    // A pause, then a new gesture that begins on the slider: that one is meant.
    w.idle(WHEEL_GESTURE_GAP_MS + 1);
    expect(w.wheel(s, notch(-1))).toBe(true);
    expect(s.value).toBe('49');
  });

  it('always lets a focused slider take the wheel', () => {
    const w = world();
    const s = w.slider();
    w.focus(s);
    w.wheel(w.page, notch(1));
    expect(w.wheel(s, notch(1), 50)).toBe(true);
    expect(s.value).toBe('51');
  });

  it('leaves disabled sliders and sliders outside the root to the page', () => {
    const w = world();
    const off = w.slider({ disabled: true });
    const out = w.slider({ inRoot: false });
    expect(w.wheel(off, notch(1), 1000)).toBe(false);
    expect(w.wheel(out, notch(1), 1000)).toBe(false);
    expect(off.value).toBe('50');
    expect(out.value).toBe('50');
  });

  it('releases the first slider when the gesture moves to another', () => {
    const w = world();
    const a = w.slider();
    const b = w.slider();
    w.focus(b);
    w.wheel(a, notch(1), 1000);
    w.wheel(b, notch(1), 60);
    expect(a.events).toEqual([WHEEL_START, 'input', 'change', WHEEL_END]);
    expect(b.events).toEqual([WHEEL_START, 'input']);
  });

  it('turns a touchpad swipe into whole steps on a slider with its own range (balance)', () => {
    const w = world();
    const bal = w.slider({ min: '-7', max: '7', value: '0' });
    for (let i = 0; i < 6; i++) w.wheel(bal, pad(0, 20), 50);
    // 120 px to the right is three steps.
    expect(bal.value).toBe('3');
    w.flush();
    expect(bal.events.filter((e) => e === 'change')).toHaveLength(1);
  });
});

describe('who uses it', () => {
  it('the UI 2.0 shell wires every slider in its frame', () => {
    const mount = fileSource('shell2/mount.js');
    expect(mount).toContain("import { wireSliderWheel } from '../sliderwheel.js';");
    expect(mount).toContain('wireSliderWheel(frame)');
  });

  it('the classic Music slider keeps its own wheel and leaves UI 2.0 to the shell', () => {
    const vol = fileSource('views/music/volume.js');
    const at = vol.indexOf("musicVolEl.addEventListener('wheel'");
    expect(at).toBeGreaterThan(0);
    expect(vol.slice(at, at + 400)).toContain('if (state.ui2Shell) return;');
    expect(vol).toContain('musicVolEl.addEventListener(WHEEL_START, beginBusy)');
    expect(vol).toContain('musicVolEl.addEventListener(WHEEL_END, endBusy)');
  });

  it('the group sliders hold the poll off and take their baseline as for a drag', () => {
    const group = fileSource('views/music/group.js');
    expect(group).toMatch(/vol\.addEventListener\(WHEEL_START, \(\) => \{\s*groupPanelState\.dragging = true;\s*captureGroupBaseline/);
    expect(group).toContain('el.addEventListener(WHEEL_END, endMemberDrag)');
  });
});
