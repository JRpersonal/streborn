// The mock backend of the screenshot harness and the Playwright suite
// (testkit/desktop-mock.mjs) answers Go bindings by NAME. A Go method that is
// renamed or removed would leave the mock answering a name the app no longer
// calls, and the real call would fall through to the default null without
// anyone noticing. So every name the mock models must exist in the generated
// bindings, and the list the suite reads must be the model the page runs.

import { afterEach, describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { MODELLED_BINDINGS, installDesktopMock } from '../testkit/desktop-mock.mjs';
import { DEMO } from '../testkit/demo-data.mjs';
import { mergeScenario, resolveScenario } from '../testkit/scenario.mjs';

const dts = readFileSync(fileURLToPath(new URL('../wailsjs/go/main/App.d.ts', import.meta.url)), 'utf8');
const BOUND = new Set([...dts.matchAll(/^export function (\w+)\(/gm)].map((m) => m[1]));

// installDesktopMock runs in a page; give it just enough of one.
function fakePage() {
  const store = () => {
    const m = new Map();
    return { getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, String(v)), removeItem: (k) => m.delete(k), m };
  };
  const ls = store();
  const win = { sessionStorage: store(), addEventListener() {} };
  globalThis.window = win;
  globalThis.localStorage = ls;
  return { win, ls };
}

describe('testkit desktop mock', () => {
  afterEach(() => { delete globalThis.window; delete globalThis.localStorage; });

  it('finds the generated bindings', () => {
    expect(BOUND.size).toBeGreaterThan(150);
  });

  it('models only bindings that exist in App.d.ts', () => {
    const missing = MODELLED_BINDINGS.filter((n) => !BOUND.has(n));
    expect(missing).toEqual([]);
  });

  it('MODELLED_BINDINGS is exactly the model the page runs', () => {
    const { win } = fakePage();
    installDesktopMock({ scenario: resolveScenario({}), demo: DEMO });
    expect([...win.__strMock.models].sort()).toEqual([...MODELLED_BINDINGS].sort());
  });

  it('answers overrides, byHost, times and the model, and records each call', async () => {
    const { win } = fakePage();
    const sc = resolveScenario(mergeScenario({}, {
      bindings: {
        BoxSettings: { byHost: { 'demobox.local': { error: 'down' } } },
        GetPresets: { value: [], times: 1 },
      },
    }));
    installDesktopMock({ scenario: sc, demo: DEMO });
    const App = win.go.main.App;
    await expect(App.BoxSettings('demobox.local', 8888)).rejects.toBe('down');
    const bedroom = await App.BoxSettings('192.0.2.52', 8888);
    expect(bedroom.info.name).toBe('Bedroom');
    expect(bedroom.volume.actual).toBe(18);
    expect(await App.GetPresets('demobox.local', 8888)).toEqual([]);
    expect((await App.GetPresets('demobox.local', 8888)).length).toBe(6);
    await App.PlaySlot('192.0.2.52', 8888, 4);
    expect(await App.Status('192.0.2.52', 8888)).toContain('<itemName>Jazz24</itemName>');
    expect(win.__strMock.calls.map((c) => c.name)).toEqual(['BoxSettings', 'BoxSettings', 'GetPresets', 'GetPresets', 'PlaySlot', 'Status']);
    expect(await App.NoSuchBinding()).toBe(null);
  });

  it('preset saves store what the backend would (argument orders of app_presets.go)', async () => {
    fakePage();
    installDesktopMock({ scenario: resolveScenario({}), demo: DEMO });
    const App = globalThis.window.go.main.App;
    const slot = async (n) => (await App.GetPresets('demobox.local', 8888)).find((p) => p.slot === n);
    await App.SaveFolderPreset('demobox.local', 8888, 2, JSON.stringify({ name: 'Jazz folder', type: 'queue', items: [] }));
    expect(await slot(2)).toMatchObject({ slot: 2, name: 'Jazz folder', type: 'queue', items: [] });
    await App.SaveSpotifyPreset('demobox.local', 8888, 3, 'Mix', 'spotify:playlist:abc', 'acct');
    expect(await slot(3)).toMatchObject({ name: 'Mix', type: 'spotify', uri: 'spotify:playlist:abc', account: 'acct' });
    await App.SaveLibraryPreset('demobox.local', 8888, 4, 'Song', 'http://192.0.2.9/s.mp3', 'http://192.0.2.9/a.jpg', 320, 'NAS', 215);
    expect(await slot(4)).toMatchObject({ name: 'Song', type: 'radio', stream_url: 'http://192.0.2.9/s.mp3', bitrate: 320, source: 'NAS', duration_sec: 215 });
    await App.SetPreset('demobox.local', 8888, 5, 'FM', 'http://192.0.2.9/fm', '', 128, 'https://example.com', 'AAC+');
    expect(await slot(5)).toMatchObject({ name: 'FM', type: 'radio', homepage: 'https://example.com', codec: 'AAC+' });
    expect((await slot(5)).uri).toBeUndefined();
    expect((await App.GetPresets('demobox.local', 8888)).length).toBe(6);
  });

  it('delivers emitted events to EventsOn listeners until they are removed', () => {
    const { win } = fakePage();
    installDesktopMock({ scenario: resolveScenario({}), demo: DEMO });
    const got = [];
    const off = win.runtime.EventsOnMultiple('install:progress', (p) => got.push(p), -1);
    win.runtime.EventsOnMultiple('install:progress', (p) => got.push('once:' + p), 1);
    win.__strMock.emit('install:progress', 'a');
    win.__strMock.emit('install:progress', 'b');
    off();
    win.__strMock.emit('install:progress', 'c');
    expect(got).toEqual(['a', 'once:a', 'b']);
    expect(win.__strMock.events['install:progress']).toBe(0);
  });

  it('banner patches add the outdated speaker and keep explicit overrides on top', () => {
    const sc = resolveScenario({ banners: { speakerUpdate: true, appUpdate: true }, bindings: { CheckAppUpdate: { value: {} } } });
    expect(sc.boxes.map((b) => b.name)).toContain('Hallway');
    expect(sc.bindings.CheckAppUpdate).toEqual({ value: {} });
  });
});
