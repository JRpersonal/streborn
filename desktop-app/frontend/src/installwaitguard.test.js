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
import { allSource, srcOf } from './test/source.js';

const all = allSource();
// The wait panel renderer, a function nested inside waitForBoxAfterSetup.
const panel = srcOf('renderInstallWaiting');

describe('the install counts as running while the wait panel is up', () => {
  it('installRunActive covers the panel, not only the awaited call', () => {
    expect(all).toContain('return networkInstallRunning || installWaitPanelLive;');
  });

  it('the panel claims the flag when it is drawn and releases it when it stops', () => {
    // The flag is claimed before the panel's own helpers are defined.
    const fn = panel.slice(0, panel.indexOf('const mine = () =>'));
    expect(fn).toContain('installWaitPanelLive = true;');
    const stop = panel.slice(panel.indexOf('const stopAll = () => {'));
    expect(stop.slice(0, 400)).toContain('installWaitPanelLive = false;');
  });

  it('every way out of the panel goes through stopAll', () => {
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
    const fn = panel.slice(0, panel.indexOf('const mine = () =>'));
    expect(fn).toContain("$('setupHeroInstall')");
    expect(fn).toContain('disabled = true');
  });

  it('the finally does not hand it back while the panel is live', () => {
    const start = srcOf('startNetworkInstall');
    const fin = start.slice(start.indexOf('networkInstallRunning = false;'));
    expect(fin.slice(0, 500)).toContain('!installWaitPanelLive');
  });

  it('stopAll re-enables it, so a finished wait leaves a usable screen', () => {
    const stop = panel.slice(panel.indexOf('const stopAll = () => {'));
    // Re-enabled unless an update from this app is still finishing on the
    // speaker, which keeps it disabled (installhold.test.js).
    expect(stop.slice(0, 400)).toContain('hero.disabled = !!currentHeroHold()');
  });
});

describe('the periodic refresh can no longer wipe a live panel', () => {
  it('the refresh gates the wipe on installRunActive', () => {
    expect(all).toContain("if (!installRunActive()) { const r = $('setupResult'); if (r) r.innerHTML = ''; }");
  });
});
