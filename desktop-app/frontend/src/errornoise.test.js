import { describe, it, expect } from 'vitest';

// The predicate from main.js's global error reporter, kept in step with it by
// this test. It decides what is Wails plumbing noise and must therefore stay
// out of the red overlay, versus what is a real fault the user has to see.
//
// It lives here rather than being imported because main.js runs its error
// bootstrap as a side effect at module load, before any of the app's Wails
// bindings exist; importing it into a test environment starts the whole app.
// The rule that matters is the SHAPE of what is suppressed, and that is what
// this pins: over-suppressing here would hide a real crash from the one banner
// that is supposed to show it.
const isFrameworkNoise = (detail) => {
  const d = String(detail || '');
  return /Callback\s+'[^']*'\s+not registered/i.test(d) ||
         (/wails\/runtime\.js/i.test(d) && /not registered/i.test(d));
};

describe('global error overlay: what it stays quiet about', () => {
  it('suppresses the Wails callback-registry error behind the report (#676, #597)', () => {
    // Verbatim from the reporter's app.log.
    expect(isFrameworkNoise(
      "Error: Callback 'main.App.CheckAppUpdate-3018697214' not registered!!! @ wails://wails/wails/runtime.js:2",
    )).toBe(true);
    expect(isFrameworkNoise(
      "Error: Callback 'main.App.DiscoverBoxes-983242426' not registered!!! @ wails://wails/wails/runtime.js:2",
    )).toBe(true);
  });

  it('suppresses it whatever the call id and whatever the bound method', () => {
    for (const m of ['main.App.BoxSettings-1', 'main.App.GetPresets-999999999', 'main.App.Status-42']) {
      expect(isFrameworkNoise(`Error: Callback '${m}' not registered!!!`)).toBe(true);
    }
  });

  it('still shows a real application error', () => {
    const real = [
      "TypeError: Cannot read properties of null (reading 'host') @ .../main.js:2481",
      'ReferenceError: renderBoxSelect is not defined @ .../main.js:2290',
      'Error: box unreachable: context deadline exceeded',
      "Error: registered a preset that was not registered",
      '',
      null,
      undefined,
    ];
    for (const d of real) expect(isFrameworkNoise(d)).toBe(false);
  });

  it('does not suppress an unrelated error merely because it mentions wails', () => {
    expect(isFrameworkNoise('Error: wails://wails/runtime.js failed to load')).toBe(false);
    expect(isFrameworkNoise("TypeError: undefined is not a function @ wails://wails/wails/runtime.js:2")).toBe(false);
  });
});
