import { describe, it, expect } from 'vitest';
import { NAV, navEntryFor, navItems, navLabelKeys, navStep } from './nav.js';
import en from '../i18n/bundles/en.json';

describe('UI 2.0 tab row', () => {
  it('gives every view a place, in the mapping order', () => {
    // docs/UI-2.0.md: nothing disappears, every tab has a fixed new place.
    expect(navItems().map((i) => i.view)).toEqual(['box', 'library', 'recent', 'spotify', 'speakers', 'wishes', 'help']);
    const classicTabs = ['box', 'library', 'recent', 'spotify', 'settings', 'multiroom', 'setup', 'wishes'];
    for (const v of classicTabs) expect(navEntryFor(v), v).not.toBeNull();
  });

  it('folds Speaker settings, Multi-room and Set up speaker into Speakers', () => {
    for (const v of ['speakers', 'settings', 'multiroom', 'setup']) expect(navEntryFor(v).view, v).toBe('speakers');
    expect(navEntryFor('speakers').area).toEqual(['speakers', 'settings', 'multiroom', 'setup']);
    expect(navEntryFor('speakers').badgeId).toBe('ui2SpeakersBadge');
    expect(navItems().some((i) => i.interim)).toBe(false);
  });

  it('keeps Wishes and Help and info small at the far end, Help last', () => {
    expect(NAV.small.map((i) => i.view)).toEqual(['wishes', 'help']);
    expect(NAV.small.map((i) => i.id)).toEqual(['ui2NavWishes', 'ui2NavHelp']);
    expect(NAV.small.every((i) => i.small)).toBe(true);
    expect(NAV.small[1].labelKey).toBe('ui2.nav.help');
    for (const v of ['wishes', 'help']) expect(NAV.main.some((i) => i.view === v), v).toBe(false);
    expect(navEntryFor('help').view).toBe('help');
  });

  it('has no group headings any more', () => {
    expect(NAV.groups).toBeUndefined();
    expect(navLabelKeys()).not.toContain('ui2.nav.listen');
    expect(navLabelKeys()).not.toContain('ui2.nav.home');
  });

  it('has an English string for every label', () => {
    for (const k of navLabelKeys()) expect(en[k], k).toBeTruthy();
  });

  it('moves along the row with Left and Right, Up and Down as aliases', () => {
    expect(navStep('ArrowRight', 0, 7)).toBe(1);
    expect(navStep('ArrowDown', 0, 7)).toBe(1);
    expect(navStep('ArrowLeft', 3, 7)).toBe(2);
    expect(navStep('ArrowUp', 3, 7)).toBe(2);
    // Stops at the ends.
    expect(navStep('ArrowRight', 6, 7)).toBe(6);
    expect(navStep('ArrowLeft', 0, 7)).toBe(0);
    expect(navStep('Home', 4, 7)).toBe(0);
    expect(navStep('End', 1, 7)).toBe(6);
    // Anything else is left alone.
    expect(navStep('Enter', 1, 7)).toBe(-1);
    expect(navStep('ArrowRight', -1, 7)).toBe(-1);
    expect(navStep('ArrowRight', 0, 0)).toBe(-1);
  });

  it('reads from the right in a right-to-left language', () => {
    expect(navStep('ArrowLeft', 2, 7, true)).toBe(3);
    expect(navStep('ArrowRight', 2, 7, true)).toBe(1);
    expect(navStep('ArrowDown', 2, 7, true)).toBe(3);
  });
});
