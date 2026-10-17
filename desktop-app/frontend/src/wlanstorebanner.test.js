// The "Save Wi-Fi" banner led to the top of Settings, where the only control
// that stores the network is a collapsed form labelled for MOVING the speaker.
// A user saved everything else on the page and the banner stayed (mail
// 2026-10-18). The banner now opens that form on the current network with a
// line saying only the password is missing. These tests pin the hand-over.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const here = dirname(fileURLToPath(import.meta.url));
const read = (...p) => readFileSync(join(here, ...p), 'utf8').replace(/\r\n/g, '\n');
const main = read('main.js');
const settings = read('views', 'settings.js');

describe('Save Wi-Fi banner', () => {
  it('marks the speaker before switching to Settings', () => {
    expect(main).toMatch(/wb\.onclick = \(\) => \{ state\.wlanStoreFor = noWifi\[0\]\.host; selectBox\(noWifi\[0\]\); switchView\('settings'\); \}/);
  });

  it('opens the form for that speaker only, once, with the current network', () => {
    const at = settings.indexOf('function wireWlanSwitch(box, currentSsid)');
    expect(at).toBeGreaterThan(-1);
    const fn = settings.slice(at, settings.indexOf('\n}\n', at));
    expect(fn).toContain('state.wlanStoreFor === box.host');
    expect(fn).toContain('state.wlanStoreFor = null;');
    expect(fn).toContain("form.classList.remove('hidden')");
    expect(fn).toContain('await CurrentWiFi()');
    expect(settings).toContain("wireWlanSwitch(box, wifi && !isWired ? wifi.ssid : '')");
    expect(settings).toContain('id="wlanStoreHint"');
  });

  it('has the hint in every language', () => {
    const dir = join(here, 'i18n', 'bundles');
    for (const f of readdirSync(dir).filter(n => n.endsWith('.json'))) {
      const b = JSON.parse(readFileSync(join(dir, f), 'utf8'));
      expect(b['settingsView.wlanStoreHint'], f).toBeTruthy();
    }
  });
});
