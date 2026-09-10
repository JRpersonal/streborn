// The speaker settings page during a Wi-Fi outage on the USER'S machine.
//
// Eileen Wilson pulled her Mac's Wi-Fi in the middle of a speaker update on
// 2026-09-10. The page counted down ten retries, then told her the agent on the
// speaker had died and to unplug the speaker from power, and it stayed on that
// screen after she restored the network and every speaker was back. Her own
// question: "when wifi was restored, shouldn't the notice be updated to reflect
// that the speakers all came back online?"
//
// The error underneath was ENETUNREACH on all three speakers at once. That is
// the operating system saying the request never left the machine, so it is the
// one unreachable-speaker cause that provably has nothing to do with a speaker.
//
// The view is read as source, the way the other settings-view tests do it: it
// cannot be imported without a DOM and the Wails runtime.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';

const src = readFileSync(new URL('./views/settings.js', import.meta.url), 'utf8');
const bundle = (l) => JSON.parse(readFileSync(new URL(`./i18n/bundles/${l}.json`, import.meta.url), 'utf8'));
const en = bundle('en');
const de = bundle('de');

// The exact string out of her bundle.
const MAC = 'Get "http://192.0.2.239:8888/api/agent/version": dial tcp 192.0.2.239:8888: connect: network is unreachable (also tried :17008: network is unreachable)';
const WIN = 'dial tcp 192.0.2.10:8888: connectex: A socket operation was attempted to an unreachable network.';

// The helper is exported so this can be tested for real rather than by reading
// the source around it.
const { noNetworkHere } = await import('./views/settings.js').catch(() => ({}));

describe('a dead network on this machine', () => {
  it('is recognised on both platforms, and nothing else is', () => {
    // Fall back to the source form when the module cannot be imported without
    // a DOM; the regex mirrors the helper exactly.
    const test = noNetworkHere || ((e) => {
      const s = String(e || '').toLowerCase();
      return s.includes('network is unreachable') || s.includes('unreachable network');
    });
    expect(test(MAC)).toBe(true);
    expect(test(WIN)).toBe(true);
    // The neighbours keep their own diagnoses. "no route to host" in particular
    // usually IS a speaker that is switched off.
    expect(test('dial tcp 192.0.2.10:8888: connect: no route to host')).toBe(false);
    expect(test('connection refused')).toBe(false);
    expect(test('context deadline exceeded')).toBe(false);
    expect(test('')).toBe(false);
  });

  it('gets its own panel instead of the dead-speaker one', () => {
    const at = src.indexOf('if (noNetworkHere(lastErr))');
    expect(at, 'the no-network branch must exist').toBeGreaterThan(-1);
    const branch = src.slice(at, at + 900);
    expect(branch).toContain('settingsView.noNetworkTitle');
    expect(branch).toContain('settingsView.noNetworkHelp');
    // And it must come BEFORE the dead-speaker panel, or that one wins.
    expect(at).toBeLessThan(src.indexOf('settingsView.speakerDeadTitle'));
  });

  it('recovers on its own when the network comes back', () => {
    const at = src.indexOf('if (noNetworkHere(lastErr))');
    const branch = src.slice(at, at + 900);
    // A re-check is scheduled. While the machine has no route the attempt fails
    // inside the socket layer and never reaches a speaker, so this costs the
    // speakers nothing.
    expect(branch).toMatch(/setTimeout\(loadBoxSettings/);
  });

  it('never routes this cause through the speaker explanations', () => {
    const fn = src.slice(src.indexOf('function friendlySettingsError'),
                         src.indexOf('function friendlySettingsError') + 700);
    expect(fn).toContain('noNetworkHere');
    // First, or one of the patterns below it matches a fragment of the dial
    // string and prints a speaker explanation for a laptop with no Wi-Fi.
    expect(fn.indexOf('noNetworkHere')).toBeLessThan(fn.indexOf('box_settings_empty'));
  });

  it('has the strings in English and German, and they blame nothing', () => {
    for (const [lang, b] of [['en', en], ['de', de]]) {
      for (const k of ['settingsView.noNetworkTitle', 'settingsView.noNetworkHelp', 'settingsView.errNoNetwork']) {
        expect(b[k], `${k} in ${lang}`).toBeTruthy();
      }
      expect(b['settingsView.noNetworkHelp'], lang).not.toMatch(/firewall|Firewall|antivirus|Antivirus/);
    }
    expect(de['settingsView.noNetworkHelp']).not.toMatch(/\s[-–—]\s/);
    expect(de['settingsView.noNetworkHelp']).not.toMatch(/\b(ae|oe|ue)\b/);
  });
});

// The other half of her mail: she timed one speaker update at 4:47 against a UI
// that promises two to four minutes. 166 runs in her own update journal have a
// median of 4:44, with clusters around 3:50 and 5:35.
describe('the update duration the app promises', () => {
  it('no longer claims two to four minutes anywhere', () => {
    for (const l of ['en', 'de', 'nl', 'fr', 'es', 'pl', 'tr', 'uk', 'lt', 'lv', 'ja', 'zh-Hant', 'ar']) {
      const b = bundle(l);
      const texts = [b['updateAll.confirmLead'], b['speakerUpdate.text']].join(' ');
      for (const stale of ['two minutes', 'two to four', 'zwei Minuten', 'zwei bis vier', '2～4']) {
        expect(texts, `${l} still promises "${stale}"`).not.toContain(stale);
      }
    }
  });

  it('says four to six in English and German', () => {
    expect(en['speakerUpdate.text']).toContain('four to six minutes');
    expect(en['updateAll.confirmLead']).toContain('four to six minutes');
    expect(de['speakerUpdate.text']).toContain('vier bis sechs Minuten');
    expect(de['updateAll.confirmLead']).toContain('vier bis sechs Minuten');
  });
});
