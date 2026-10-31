import { describe, it, expect } from 'vitest';
import { NAV, navEntryFor, navItems, navLabelKeys } from './nav.js';
import en from '../i18n/bundles/en.json';

describe('UI 2.0 sidebar', () => {
  it('gives every view a place, in the mapping order', () => {
    // docs/UI-2.0.md: nothing disappears, every tab has a fixed new place.
    expect(navItems().map((i) => i.view)).toEqual(['box', 'library', 'recent', 'spotify', 'speakers', 'wishes']);
    const classicTabs = ['box', 'library', 'recent', 'spotify', 'settings', 'multiroom', 'setup', 'wishes'];
    for (const v of classicTabs) expect(navEntryFor(v), v).not.toBeNull();
  });

  it('folds Speaker settings, Multi-room and Set up speaker into Speakers', () => {
    for (const v of ['speakers', 'settings', 'multiroom', 'setup']) expect(navEntryFor(v).view, v).toBe('speakers');
    expect(navEntryFor('speakers').area).toEqual(['speakers', 'settings', 'multiroom', 'setup']);
    expect(navEntryFor('speakers').badgeId).toBe('ui2SpeakersBadge');
    expect(navItems().some((i) => i.interim)).toBe(false);
  });

  it('keeps Wishes small at the bottom', () => {
    expect(NAV.bottom.map((i) => i.view)).toEqual(['wishes']);
    expect(NAV.bottom[0].small).toBe(true);
    expect(NAV.groups.flatMap((g) => g.items).some((i) => i.view === 'wishes')).toBe(false);
  });

  it('has an English string for every label', () => {
    for (const k of navLabelKeys()) expect(en[k], k).toBeTruthy();
  });
});
