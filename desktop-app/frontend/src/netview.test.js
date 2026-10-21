// The network section's decision, pinned per speaker shape. The case that
// started it: a scm ST20 the owner had moved to a new network through Bose's
// setup page, shown with STR's old network as "SSID" (support mail,
// 2026-10-21). See netview.js.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import { networkView } from './netview.js';

const eth = (extra = {}) => ({
  name: 'eth0', type: 'ETHERNET_INTERFACE', state: 'NETWORK_ETHERNET_CONNECTED', ipAddress: '192.0.2.10', ...extra,
});

describe('networkView', () => {
  it('never shows STR\'s own network as the live one on a coprocessor speaker', () => {
    const v = networkView({ interfaces: [eth({ strSSID: 'OldNet' })] });
    expect(v.kind).toBe('unknown-link');
    expect(v.ssid).toBe('');
    expect(v.strSSID).toBe('OldNet');
    expect(v.ip).toBe('192.0.2.10');
  });

  it('calls a coprocessor speaker with a signal class Wi-Fi, name unknown', () => {
    const v = networkView({ interfaces: [eth({ signal: 'MARGINAL_SIGNAL', strSSID: 'OldNet' })] });
    expect(v.kind).toBe('wifi-unnamed');
    expect(v.ssid).toBe('');
    expect(v.signal).toBe('MARGINAL_SIGNAL');
  });

  it('shows the name a coprocessor speaker reports itself (ST30, Portable)', () => {
    const v = networkView({ interfaces: [eth({ ssid: 'HomeNet', ssidSource: 'box' })] });
    expect(v.kind).toBe('wifi');
    expect(v.ssid).toBe('HomeNet');
    expect(v.coprocessor).toBe(true);
  });

  it('says cable on a speaker that also has a Wi-Fi radio (#641)', () => {
    const v = networkView({
      interfaces: [
        { name: 'wlan0', type: 'WIFI_INTERFACE', state: 'NETWORK_WIFI_DISCONNECTED', ssid: 'OldNet' },
        eth({ strSSID: 'OldNet' }),
      ],
    });
    expect(v.kind).toBe('wired');
    expect(v.ssid).toBe('');
    expect(v.strSSID).toBe('');
  });

  it('shows the live Wi-Fi of a wpa speaker', () => {
    const v = networkView({
      interfaces: [{ name: 'wlan0', type: 'WIFI_INTERFACE', state: 'NETWORK_WIFI_CONNECTED', ssid: 'HomeNet', signal: 'GOOD_SIGNAL', frequencyKHz: 2412000, ipAddress: '192.0.2.11' }],
    });
    expect(v).toMatchObject({ kind: 'wifi', ssid: 'HomeNet', signal: 'GOOD_SIGNAL', frequencyKHz: 2412000, coprocessor: false });
  });

  it('reports no connection when nothing is up', () => {
    expect(networkView({ interfaces: [] }).kind).toBe('none');
    expect(networkView(undefined).kind).toBe('none');
  });
});

describe('network section strings', () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const dir = join(here, 'i18n', 'bundles');
  const keys = ['settingsView.wlanSsidUnknown', 'settingsView.netConnection', 'settingsView.netConnUnknown', 'settingsView.wlanStrConfigured'];
  it('exist in every language, with the placeholder and without em dashes', () => {
    for (const f of readdirSync(dir).filter(n => n.endsWith('.json'))) {
      const b = JSON.parse(readFileSync(join(dir, f), 'utf8'));
      for (const k of keys) {
        expect(b[k], `${f} ${k}`).toBeTruthy();
        expect(b[k], `${f} ${k}`).not.toContain('—');
      }
      expect(b['settingsView.wlanStrConfigured'], f).toContain('{{ssid}}');
    }
  });
});
