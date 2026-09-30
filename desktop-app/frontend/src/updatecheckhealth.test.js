import { describe, it, expect, beforeEach } from 'vitest';
import {
  noteCheckFailed, noteCheckSucceeded, shouldSayChecksAreFailing,
  updateCheckFailureRun, SPEAK_AFTER,
} from './updatecheckhealth.js';

// The suite runs in node, which has no localStorage.
const store = new Map();
globalThis.localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => store.set(k, String(v)),
  removeItem: (k) => store.delete(k),
  clear: () => store.clear(),
};

beforeEach(() => store.clear());

// A machine where the request always fails looks exactly like "you are up to
// date", forever. One user sat on a June build until the end of September, 142
// releases behind, with nothing on screen ever about the app itself.
describe('the update-check failure run', () => {
  it('says nothing about a bad afternoon', () => {
    for (let n = 1; n < SPEAK_AFTER; n++) {
      expect(shouldSayChecksAreFailing(noteCheckFailed())).toBe(false);
    }
  });

  it('speaks once the run is long enough to rule out a flaky router', () => {
    let spoke = 0;
    for (let n = 0; n < SPEAK_AFTER; n++) {
      if (shouldSayChecksAreFailing(noteCheckFailed())) spoke++;
    }
    expect(spoke).toBe(1);
  });

  it('speaks exactly ONCE, not on every startup afterwards', () => {
    let spoke = 0;
    for (let n = 0; n < 40; n++) {
      if (shouldSayChecksAreFailing(noteCheckFailed())) spoke++;
    }
    expect(spoke, 'a notice on every start is wallpaper, and wallpaper is ignored').toBe(1);
  });

  it('starts over as soon as one check gets through', () => {
    noteCheckFailed();
    noteCheckFailed();
    noteCheckSucceeded();
    expect(updateCheckFailureRun()).toBe(0);
    expect(shouldSayChecksAreFailing(noteCheckFailed())).toBe(false);
  });

  it('survives a stored value that is not a number', () => {
    for (const junk of ['', 'x', '-4', 'NaN', '{}']) {
      store.clear();
      store.set('str.updateCheckFailures', junk);
      expect(noteCheckFailed()).toBe(1);
    }
  });

  it('stays silent rather than crying wolf when storage is unavailable', () => {
    const real = globalThis.localStorage;
    globalThis.localStorage = {
      getItem() { throw new Error('denied'); },
      setItem() { throw new Error('denied'); },
      removeItem() { throw new Error('denied'); },
    };
    try {
      // Without storage the count cannot survive a restart, so the notice must
      // simply never fire. Silence is the correct failure here, not a warning
      // on every single start.
      let spoke = 0;
      for (let n = 0; n < 20; n++) {
        if (shouldSayChecksAreFailing(noteCheckFailed())) spoke++;
      }
      expect(spoke).toBe(0);
      expect(() => noteCheckSucceeded()).not.toThrow();
    } finally {
      globalThis.localStorage = real;
    }
  });
});
