// A dismissed notice covers exactly the version it was dismissed at. Clicking
// "0.9.33 is out" away is an answer about 0.9.33; 0.9.34 is new news and must
// appear. The earlier rule swallowed the next version too, so a single click
// silenced two releases and the notice looked like it never came back (Jens,
// 2026-08-12).
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

  it('shows the very next version again', () => {
    dismissNotice('app', '0.9.33');
    expect(noticeDismissed('app', '0.9.34')).toBe(false);
  });

  it('lets every following version be dismissed on its own', () => {
    dismissNotice('app', '0.9.33');
    expect(noticeDismissed('app', '0.9.34')).toBe(false);
    dismissNotice('app', '0.9.34');
    expect(noticeDismissed('app', '0.9.34')).toBe(true);
    expect(noticeDismissed('app', '0.9.35')).toBe(false);
  });

  it('works across a minor bump, where version arithmetic would not', () => {
    dismissNotice('app', '0.9.34');
    expect(noticeDismissed('app', '0.10.0')).toBe(false);
  });

  it('does not resurrect a dismissal after a newer version was offered', () => {
    dismissNotice('app', '0.9.33');
    expect(noticeDismissed('app', '0.9.34')).toBe(false);
    // The record is gone, so even the originally dismissed version shows again
    // rather than staying silently hidden forever.
    expect(noticeDismissed('app', '0.9.33')).toBe(false);
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
