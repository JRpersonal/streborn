// The agent reports a recent playback dropout on a weak Wi-Fi signal
// (weakWifiDropout); the app names that speaker with a gentle hint that can be
// dismissed for a day.
import { describe, it, expect } from 'vitest';
import en from './i18n/bundles/en.json';
import { setLocale, t } from './i18n/index.js';
import {
  WEAK_WIFI_DISMISS_MS, boxHasWeakWifiDropout, weakWifiDismissActive, weakWifiDropoutBoxes,
} from './weakwifi.js';

const box = (extra) => ({ host: '192.0.2.10', kind: 'str', friendlyName: 'Kitchen', ...extra });

describe('boxHasWeakWifiDropout', () => {
  it('flags the two weak grades only', () => {
    expect(boxHasWeakWifiDropout(box({ weakWifiDropout: 'POOR_SIGNAL' }))).toBe(true);
    expect(boxHasWeakWifiDropout(box({ weakWifiDropout: 'MARGINAL_SIGNAL' }))).toBe(true);
    expect(boxHasWeakWifiDropout(box({ weakWifiDropout: 'GOOD_SIGNAL' }))).toBe(false);
    expect(boxHasWeakWifiDropout(box({}))).toBe(false);
    expect(boxHasWeakWifiDropout(null)).toBe(false);
  });

  it('stays quiet where another hint says more, or the reading cannot be fresh', () => {
    expect(boxHasWeakWifiDropout(box({ weakWifiDropout: 'POOR_SIGNAL', offline: true }))).toBe(false);
    expect(boxHasWeakWifiDropout(box({ weakWifiDropout: 'POOR_SIGNAL', kind: 'stock' }))).toBe(false);
    expect(boxHasWeakWifiDropout(box({ weakWifiDropout: 'POOR_SIGNAL', boxHealth: 'wedged' }))).toBe(false);
    // No internet: the station cannot play at all, that hint is the one to show.
    expect(boxHasWeakWifiDropout(box({ weakWifiDropout: 'POOR_SIGNAL', internet: 'unreachable' }))).toBe(false);
  });
});

describe('dismissal', () => {
  const now = Date.UTC(2026, 10, 3, 20, 0, 0);
  it('holds for a day', () => {
    expect(weakWifiDismissActive(String(now - 1000), now)).toBe(true);
    expect(weakWifiDismissActive(String(now - WEAK_WIFI_DISMISS_MS - 1), now)).toBe(false);
    expect(weakWifiDismissActive(null, now)).toBe(false);
    expect(weakWifiDismissActive('garbage', now)).toBe(false);
  });

  it('lists only the speakers not dismissed', () => {
    const a = box({ host: '192.0.2.10', weakWifiDropout: 'POOR_SIGNAL' });
    const b = box({ host: '192.0.2.11', weakWifiDropout: 'MARGINAL_SIGNAL' });
    const c = box({ host: '192.0.2.12' });
    const dismissed = (x) => (x.host === '192.0.2.11' ? String(now - 60000) : null);
    expect(weakWifiDropoutBoxes([a, b, c], dismissed, now)).toEqual([a]);
    expect(weakWifiDropoutBoxes(undefined)).toEqual([]);
  });
});

describe('wording', () => {
  it('blames neither the Wi-Fi band nor the security mode, and uses no dashes', () => {
    const text = en['speaker.weakWifiDropoutBanner'];
    expect(text).toContain('{{name}}');
    expect(text).not.toMatch(/GHz|2\.4|WPA|band/i);
    expect(text).not.toMatch(/[–—]| - /);
    setLocale('en');
    expect(t('speaker.weakWifiDropoutBanner', { name: 'Kitchen' })).toMatch(/^Kitchen: /);
  });
});
