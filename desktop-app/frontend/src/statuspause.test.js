// statuspause.test.js: the chosen speaker's status poll sleeps while the window
// cannot be seen, so an app left open over night stops reading the speaker.

import { describe, expect, it, vi } from 'vitest';

vi.mock('./api.js', () => ({ WindowIsMinimised: vi.fn() }));

import { WindowIsMinimised } from './api.js';
import { statusPollPaused, windowHidden } from './statuspause.js';
import { readFileSync } from 'node:fs';

describe('statusPollPaused', () => {
  it('polls while the window is shown', () => {
    expect(statusPollPaused({ visibility: 'visible', minimised: false })).toBe(false);
  });
  it('sleeps while the page is hidden', () => {
    expect(statusPollPaused({ visibility: 'hidden', minimised: false })).toBe(true);
  });
  it('sleeps while the window is minimised even if the page still says visible', () => {
    expect(statusPollPaused({ visibility: 'visible', minimised: true })).toBe(true);
  });
});

describe('windowHidden', () => {
  it('reads the Wails window state', async () => {
    WindowIsMinimised.mockResolvedValueOnce(true);
    expect(await windowHidden()).toBe(true);
  });
  it('counts a runtime that cannot answer as shown', async () => {
    WindowIsMinimised.mockImplementationOnce(() => { throw new Error('no runtime'); });
    expect(await windowHidden()).toBe(false);
  });
});

describe('main.js status poll', () => {
  const src = readFileSync(new URL('./main.js', import.meta.url), 'utf8');
  it('skips the round while the window is hidden and catches up when shown', () => {
    expect(src).toContain('if (await windowHidden()) statusSkipped = true;');
    expect(src).toContain("document.addEventListener('visibilitychange', catchUpStatusWhenShown);");
    expect(src).toContain("window.addEventListener('focus', catchUpStatusWhenShown);");
  });
});
