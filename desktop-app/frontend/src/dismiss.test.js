// A dismissed notice must come back, but not on the very next version: a click
// on "not now" for 0.9.33 should not mean the 0.9.34 notice appears the next
// day. It covers the dismissed version and the one after, and returns on the
// version after that (Jens, 2026-08-04).
import { describe, it, expect, beforeEach } from 'vitest';
import { dismissNotice, noticeDismissed, clearNoticeDismissal } from './utils.js';

describe('dismissible notices', () => {
  beforeEach(() => { clearNoticeDismissal('app'); clearNoticeDismissal('speaker'); });

  it('shows a notice that was never dismissed', () => {
    expect(noticeDismissed('app', '0.9.33')).toBe(false);
  });

  it('hides the dismissed version itself, however often it is offered', () => {
    dismissNotice('app', '0.9.33');
    expect(noticeDismissed('app', '0.9.33')).toBe(true);
    expect(noticeDismissed('app', '0.9.33')).toBe(true);
  });

  it('still hides the next version, then shows the one after', () => {
    dismissNotice('app', '0.9.33');
    expect(noticeDismissed('app', '0.9.34')).toBe(true);
    expect(noticeDismissed('app', '0.9.35')).toBe(false);
  });

  it('counts distinct versions, not repeated offers of the same one', () => {
    dismissNotice('app', '0.9.33');
    for (let i = 0; i < 20; i++) expect(noticeDismissed('app', '0.9.34')).toBe(true);
    expect(noticeDismissed('app', '0.9.35')).toBe(false);
  });

  it('works across a minor bump, where version arithmetic would not', () => {
    dismissNotice('app', '0.9.34');
    expect(noticeDismissed('app', '0.10.0')).toBe(true);
    expect(noticeDismissed('app', '0.10.1')).toBe(false);
  });

  it('keeps notices of different kinds apart', () => {
    dismissNotice('app', '0.9.33');
    expect(noticeDismissed('speaker', '0.9.33')).toBe(false);
  });

  it('forgets a dismissal on request', () => {
    dismissNotice('app', '0.9.33');
    clearNoticeDismissal('app');
    expect(noticeDismissed('app', '0.9.33')).toBe(false);
  });

  it('ignores an empty version instead of hiding everything', () => {
    dismissNotice('app', '');
    expect(noticeDismissed('app', '0.9.33')).toBe(false);
  });
});
