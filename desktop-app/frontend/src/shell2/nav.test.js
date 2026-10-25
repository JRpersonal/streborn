import { describe, it, expect } from 'vitest';
import { NAV, navItems, navLabelKeys } from './nav.js';
import en from '../i18n/bundles/en.json';

describe('UI 2.0 sidebar', () => {
  it('gives each of the eight views exactly one entry, in the mapping order', () => {
    // docs/UI-2.0.md: nothing disappears, every tab has a fixed new place.
    expect(navItems().map((i) => i.view)).toEqual(
      ['box', 'library', 'recent', 'spotify', 'settings', 'multiroom', 'setup', 'wishes']);
  });

  it('keeps Wishes small at the bottom', () => {
    expect(NAV.bottom.map((i) => i.view)).toEqual(['wishes']);
    expect(NAV.bottom[0].small).toBe(true);
    expect(NAV.groups.flatMap((g) => g.items).some((i) => i.view === 'wishes')).toBe(false);
  });

  it('marks the flat Groups and Add speaker entries as interim', () => {
    const byView = Object.fromEntries(navItems().map((i) => [i.view, i]));
    expect(byView.multiroom.interim).toBe(true);
    expect(byView.setup.interim).toBe(true);
  });

  it('has an English string for every label', () => {
    for (const k of navLabelKeys()) expect(en[k], k).toBeTruthy();
  });
});
