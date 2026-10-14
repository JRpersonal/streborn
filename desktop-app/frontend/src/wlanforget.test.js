import { describe, it, expect } from 'vitest';
import { forgetRefusalKey, storedNetworksView, storedNetworksHtml } from './wlanforget.js';

const esc = (s) => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
const t = (k) => k;

const field = {
  mechanism: 'wpa',
  current: 'HomeNet',
  networks: [
    { ssid: 'OldNet', current: false, open: false, publicHotspot: false },
    { ssid: 'Vodafone Homespot', current: false, open: true, publicHotspot: true },
    { ssid: 'HomeNet', current: true, open: false, publicHotspot: false },
  ],
  forgetOthers: { removable: 2, code: '', reason: '' },
};

describe('storedNetworksView', () => {
  it('puts the current network first and offers the button', () => {
    const v = storedNetworksView(field);
    expect(v.networks[0].ssid).toBe('HomeNet');
    expect(v.keep).toBe('HomeNet');
    expect(v.removable).toBe(2);
    expect(v.canForget).toBe(true);
    expect(v.refusalKey).toBe('');
  });

  it('does not offer the button when there is nothing to forget', () => {
    const v = storedNetworksView({ ...field, networks: [field.networks[2]], forgetOthers: { removable: 0 } });
    expect(v.canForget).toBe(false);
  });

  it('maps a refusal to its sentence and hides the button', () => {
    const v = storedNetworksView({ ...field, forgetOthers: { removable: 0, code: 'chassis-unsupported', reason: 'x' } });
    expect(v.canForget).toBe(false);
    expect(v.refusalKey).toBe('settingsView.wlanForgetRefused.chassis');
  });

  it('never offers the button without a current network', () => {
    const nets = field.networks.map((n) => ({ ...n, current: false }));
    expect(storedNetworksView({ ...field, networks: nets }).canForget).toBe(false);
  });

  it('survives an old agent answer', () => {
    const v = storedNetworksView(null);
    expect(v.networks).toEqual([]);
    expect(v.canForget).toBe(false);
  });
});

describe('forgetRefusalKey', () => {
  it('knows every agent code', () => {
    for (const c of ['not-associated', 'ethernet', 'target-mismatch', 'keep-not-stored', 'would-empty', 'chassis-unsupported']) {
      expect(forgetRefusalKey(c)).not.toBe('');
    }
    expect(forgetRefusalKey('something-new')).toBe('');
  });
});

describe('storedNetworksHtml', () => {
  it('marks the current network and the hotspot, and escapes names', () => {
    const html = storedNetworksHtml(storedNetworksView({
      ...field,
      networks: [...field.networks, { ssid: '<b>x</b>', current: false, open: true }],
    }), t, esc);
    expect(html).toContain('is-current');
    expect(html).toContain('settingsView.wlanStoredHotspot');
    expect(html).toContain('settingsView.wlanStoredOpen');
    expect(html).not.toContain('<b>x</b>');
  });

  it('says so when the speaker reports none', () => {
    expect(storedNetworksHtml(storedNetworksView({ networks: [] }), t, esc)).toContain('settingsView.wlanStoredNone');
  });
});
