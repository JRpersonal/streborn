// The install guard was released while the install was still in flight.
//
// startNetworkInstall awaits waitForBoxAfterSetup, but that call returns as soon
// as the "the speaker has not answered yet" panel is DRAWN: renderInstallWaiting
// wires two listeners and returns. So the finally clause cleared
// networkInstallRunning and re-enabled the hero button underneath a live panel.
//
// Two things followed. main.js empties #setupResult on its periodic refresh
// whenever installRunActive() is false, so leaving the Setup tab and coming back
// wiped the panel; the next event then found its marker gone, dropped both
// listeners, and the install:late rescue could never render. And the Install
// button was live under the one screen whose purpose is to prevent a second
// install, which is what a SoundTouch 300 owner pressed, twice, in the week of
// 2026-09-24 while his soundbar sat blinking with every port dead.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const here = dirname(fileURLToPath(import.meta.url));
const setup = readFileSync(join(here, 'views', 'setup.js'), 'utf8').replace(/\r\n/g, '\n');
const main = readFileSync(join(here, 'main.js'), 'utf8').replace(/\r\n/g, '\n');

describe('the install counts as running while the wait panel is up', () => {
  it('installRunActive covers the panel, not only the awaited call', () => {
    expect(setup).toContain('return networkInstallRunning || installWaitPanelLive;');
  });

  it('the panel claims the flag when it is drawn and releases it when it stops', () => {
    // Search the end anchor FROM the start offset. waitForBoxAfterSetup grew a
    // 'const mine' of its own further up the file, and the unanchored indexOf
    // found that one instead and sliced backwards into an empty string.
    const at = setup.indexOf('function renderInstallWaiting');
    const fn = setup.slice(at, setup.indexOf('const mine = () =>', at));
    expect(fn).toContain('installWaitPanelLive = true;');
    const stop = setup.slice(setup.indexOf('const stopAll = () => {', setup.indexOf('function renderInstallWaiting')));
    expect(stop.slice(0, 400)).toContain('installWaitPanelLive = false;');
  });

  it('every way out of the panel goes through stopAll', () => {
    const panel = setup.slice(setup.indexOf('function renderInstallWaiting'),
      setup.indexOf('async function finishInstall'));
    // the user pressing stop, a late answer, the panel being replaced, and the
    // belt-and-braces timer
    expect(panel).toContain('stop.onclick = () => { stopAll(); giveUp(); }');
    expect(panel).toContain("offLate = EventsOn('install:late'");
    expect(panel.slice(panel.indexOf("EventsOn('install:late'"))).toContain('stopAll();');
    expect(panel).toContain('if (!mine()) { stopAll(); return false; }');
    expect(panel).toContain('setTimeout(stopAll,');
  });
});

describe('the Install button stays out of reach under the wait panel', () => {
  it('the panel disables it', () => {
    // Search the end anchor FROM the start offset. waitForBoxAfterSetup grew a
    // 'const mine' of its own further up the file, and the unanchored indexOf
    // found that one instead and sliced backwards into an empty string.
    const at = setup.indexOf('function renderInstallWaiting');
    const fn = setup.slice(at, setup.indexOf('const mine = () =>', at));
    expect(fn).toContain("$('setupHeroInstall')");
    expect(fn).toContain('disabled = true');
  });

  it('the finally does not hand it back while the panel is live', () => {
    const fin = setup.slice(setup.indexOf('networkInstallRunning = false;', setup.indexOf('async function startNetworkInstall')));
    expect(fin.slice(0, 500)).toContain('!installWaitPanelLive');
  });

  it('stopAll re-enables it, so a finished wait leaves a usable screen', () => {
    const stop = setup.slice(setup.indexOf('const stopAll = () => {', setup.indexOf('function renderInstallWaiting')));
    expect(stop.slice(0, 400)).toContain('disabled = false');
  });
});

describe('the periodic refresh can no longer wipe a live panel', () => {
  it('main.js gates the wipe on installRunActive', () => {
    expect(main).toContain("if (!installRunActive()) { const r = $('setupResult'); if (r) r.innerHTML = ''; }");
  });
});
