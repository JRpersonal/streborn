import { describe, it, expect } from 'vitest';
import { statusTickScope, playerOnScreen } from './statusrefreshscope.js';

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
      expect(statusTickScope({ hasBox: true, view }).player).toBe(false);
    }
  });

  it('refreshes everything in the box tab', () => {
    expect(statusTickScope({ hasBox: true, view: 'box' })).toEqual({ bar: true, controls: true, player: true });
  });

  it('does nothing at all without a speaker', () => {
    expect(statusTickScope({ hasBox: false, view: 'box' })).toEqual({ bar: false, controls: false, player: false });
    expect(statusTickScope({ hasBox: false, view: 'library' })).toEqual({ bar: false, controls: false, player: false });
    expect(statusTickScope({ hasBox: false, view: 'library', persistentPlayer: true }).player).toBe(false);
  });

  it('refreshes the persistent player off the box tab only with the UI 2.0 preview', () => {
    for (const view of ['library', 'recent', 'multiroom', 'settings', 'spotify', 'wishes']) {
      expect(statusTickScope({ hasBox: true, view, persistentPlayer: true }).player).toBe(true);
      expect(statusTickScope({ hasBox: true, view, persistentPlayer: false }).player).toBe(false);
    }
  });

  it('leaves the persistent player alone in Setup and during a speaker update', () => {
    expect(statusTickScope({ hasBox: true, view: 'setup', persistentPlayer: true }).player).toBe(false);
    expect(statusTickScope({ hasBox: true, view: 'library', persistentPlayer: true, otaInProgress: true }).player).toBe(false);
  });

  it('keeps the preset tiles a box-tab matter, preview or not', () => {
    for (const view of ['library', 'recent', 'settings']) {
      expect(statusTickScope({ hasBox: true, view, persistentPlayer: true }).controls).toBe(false);
    }
  });
});

describe('playerOnScreen', () => {
  it('matches the scope without asking for a speaker', () => {
    for (const view of ['box', 'library', 'setup', 'settings']) {
      for (const persistentPlayer of [false, true]) {
        for (const otaInProgress of [false, true]) {
          expect(playerOnScreen({ view, persistentPlayer, otaInProgress }))
            .toBe(statusTickScope({ hasBox: true, view, persistentPlayer, otaInProgress }).player);
        }
      }
    }
  });

  it('is exactly "the Music tab is open" with the preview off', () => {
    expect(playerOnScreen({ view: 'box' })).toBe(true);
    expect(playerOnScreen({ view: 'library' })).toBe(false);
  });
});
