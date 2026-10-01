import { describe, it, expect } from 'vitest';
import { statusTickScope } from './statusrefreshscope.js';

describe('statusTickScope', () => {
  it('keeps the bar alive in the tabs single tracks are started from', () => {
    // The defect: these returned nothing at all, so the progress bar hid itself
    // while the user sat on exactly these screens picking the next track.
    for (const view of ['library', 'recent', 'multiroom', 'settings', 'setup']) {
      expect(statusTickScope({ hasBox: true, view }).bar).toBe(true);
    }
  });

  it('does not refresh off-screen controls outside the box tab', () => {
    // Every tick is a request to a speaker whose flash nobody can replace.
    for (const view of ['library', 'recent', 'multiroom', 'settings']) {
      expect(statusTickScope({ hasBox: true, view }).controls).toBe(false);
    }
  });

  it('refreshes everything in the box tab', () => {
    expect(statusTickScope({ hasBox: true, view: 'box' })).toEqual({ bar: true, controls: true });
  });

  it('does nothing at all without a speaker', () => {
    expect(statusTickScope({ hasBox: false, view: 'box' })).toEqual({ bar: false, controls: false });
    expect(statusTickScope({ hasBox: false, view: 'library' })).toEqual({ bar: false, controls: false });
  });
});
