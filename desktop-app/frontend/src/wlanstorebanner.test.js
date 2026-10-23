// The "Save Wi-Fi" banner led to the top of Settings, where the only control
// that stores the network is a collapsed form labelled for MOVING the speaker.
// A user saved everything else on the page and the banner stayed (mail
// 2026-10-18). The banner now opens that form on the current network with a
// line saying only the password is missing. These tests pin the hand-over.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import { allSource, srcOf } from './test/source.js';

const here = dirname(fileURLToPath(import.meta.url));
// The banner wiring sits in main.js and the form in the settings Network
// group; both are read by name through the source helper.
const src = allSource();

describe('Save Wi-Fi banner', () => {
  it('marks the speaker before switching to Settings', () => {
    expect(src).toMatch(/wb\.onclick = \(\) => \{ state\.wlanStoreFor = noWifi\[0\]\.host; selectBox\(noWifi\[0\]\); switchView\('settings'\); \}/);
  });

  it('opens the form for that speaker only, once, with the current network', () => {
    const fn = srcOf('wireWlanSwitch');
    expect(fn).toMatch(/^function wireWlanSwitch\(box, currentSsid\)/);
    expect(fn).toContain('state.wlanStoreFor === box.host');
    expect(fn).toContain('state.wlanStoreFor = null;');
    expect(fn).toContain("form.classList.remove('hidden')");
    expect(fn).toContain('await CurrentWiFi()');
    expect(src).toContain("wireWlanSwitch(box, nv.kind === 'wifi' ? nv.ssid : '')");
    expect(src).toContain('id="wlanStoreHint"');
  });

  it('has the hint in every language', () => {
    const dir = join(here, 'i18n', 'bundles');
    for (const f of readdirSync(dir).filter(n => n.endsWith('.json'))) {
      const b = JSON.parse(readFileSync(join(dir, f), 'utf8'));
      expect(b['settingsView.wlanStoreHint'], f).toBeTruthy();
    }
  });
});
