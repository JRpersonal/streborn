// The Multi-Room tab's "show on the remote key map" button dropped the user on
// the settings page with everything still folded up (reported 2026-09-10:
// "schickt den user nur auf die einstellungsseite aber nicht direkt in das
// untermenue wo man die tasten belegen kann").
//
// The cause is not the section, it is what renderSettingsGroups does to it
// afterwards: every section is moved into a collapsible GROUP, and the two that
// hold these targets, Advanced and Info, start closed. Opening the section's own
// <details> therefore reveals nothing.
//
// The view is read as source, the same way the bass-control tests read it: it
// cannot be imported without a DOM and the Wails runtime.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

const src = readFileSync(new URL('./views/settings.js', import.meta.url), 'utf8');

describe('settings deep links', () => {
  it('has one helper that opens the ancestors, not just the target', () => {
    const fn = src.slice(src.indexOf('function revealInSettings'), src.indexOf('\n}', src.indexOf('function revealInSettings')));
    expect(fn, 'revealInSettings must exist').toContain('revealInSettings');
    // It has to walk UP. A single element.open cannot reveal a target whose
    // group is closed.
    expect(fn).toContain('parentElement');
    expect(fn).toContain("'DETAILS'");
    expect(fn).toContain('scrollIntoView');
  });

  // Both deep links must go through it. A bare scrollIntoView is the bug.
  it('every deep link into a settings section goes through the helper', () => {
    const scrolls = [...src.matchAll(/scrollIntoView/g)];
    expect(scrolls.length, 'expected the helper plus the how-to nudge').toBeGreaterThan(0);
    // The key-map link and the firmware banner are the two that live inside a
    // collapsible group; neither may scroll on its own.
    // Anchor on the CONSUMPTION site, not on the `let pendingKeyMapOpen = false`
    // declaration further up the file.
    const at = src.indexOf('if (pendingKeyMapOpen)');
    expect(at, 'the pending-intent block must still be findable').toBeGreaterThan(-1);
    const keyMap = src.slice(at, at + 400);
    expect(keyMap).toContain('revealInSettings');
    expect(keyMap).not.toContain('scrollIntoView');

    const fwBtn = src.slice(src.indexOf("$('fwUpdateBtn')"), src.indexOf("$('fwUpdateBtn')") + 300);
    expect(fwBtn).toContain('revealInSettings');
    expect(fwBtn).not.toContain('scrollIntoView');
  });

  // The user came to look at the remote, so the scroll lands on the key map
  // rather than on the section heading a screen above it.
  it('the key-map link targets the remote itself', () => {
    expect(src).toContain('id="webhookKeyMap"');
    expect(src).toContain("revealInSettings($('webhookKeyMap')");
  });

  // The reason the helper is needed at all: expert sections are grouped into
  // Advanced, and that group is created closed.
  it('the advanced group really does start closed', () => {
    const groups = src.slice(src.indexOf('const GROUPS'), src.indexOf('];', src.indexOf('const GROUPS')));
    expect(groups).toMatch(/key:\s*'advanced'[^}]*open:\s*false/);
  });
});
