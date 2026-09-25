// The stick path had two panels and only ever scrolled to one of them.
//
// showAwaitBoxReadyPanel renders the "insert the stick and power-cycle" step
// into #setupAwaitResult, which the shell places AFTER the stick wizard's
// </details>, and scrolls it into view - on purpose, so the user reads the next
// step instead of dropping out. The confirm button in that panel then calls
// handoff(), and the install it starts renders into #setupResult, which sits
// ABOVE the wizard the user just used and is therefore off the top of the
// screen.
//
// Nothing cleared the await panel and nothing scrolled back, so the confirm
// button greyed out under a line that still said "speaker found and ready" and
// stayed that way: the phase checklist, the failure headline, the firmware
// route, the repair button and the Save-diagnostics button were all painted in
// a container the user could not see. Reported as "the app does nothing after I
// press the button".
//
// The view is read as source, the same way the other view tests do it: it
// cannot be imported without a DOM and the Wails runtime.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const here = dirname(fileURLToPath(import.meta.url));
const setup = readFileSync(join(here, 'views', 'setup.js'), 'utf8').replace(/\r\n/g, '\n');

const handoff = setup.slice(
  setup.indexOf('const handoff = () => {'),
  setup.indexOf('\n  };', setup.indexOf('const handoff = () => {')),
);

describe('the stick wizard hands the screen over with the install', () => {
  it('still has the two panels on opposite sides of the wizard', () => {
    const shell = setup.slice(setup.indexOf('<div id="setupResult"'), setup.indexOf('<div id="setupAwaitResult"'));
    // This ordering is the whole reason the handoff has to move the view: the
    // install panel is above the wizard, the await panel below it.
    expect(shell, 'the stick wizard must still sit between the two panels').toContain('<details class="setup-stick-details" id="setupStickDetails">');
    expect(shell).toContain('</details>');
  });

  it('renders the install into the panel above the wizard', () => {
    const wait = setup.slice(setup.indexOf('async function waitForBoxAfterSetup'), setup.indexOf('async function waitForBoxAfterSetup') + 1200);
    expect(wait).toContain("const setupResult = $('setupResult');");
    expect(wait).toContain('setupResult.innerHTML = baseHtml + extra;');
  });

  it('clears the await panel it is leaving behind', () => {
    expect(handoff, 'handoff must exist').toBeTruthy();
    expect(handoff).toContain("$('setupAwaitResult')");
    expect(handoff).toMatch(/awaitPanel\.innerHTML = ''/);
  });

  it('folds the stick wizard away so the install panel is reachable', () => {
    expect(handoff).toContain("$('setupStickDetails')");
    expect(handoff).toMatch(/stickDetails\.open = false/);
  });

  it('scrolls the install panel into view, after the install has rendered into it', () => {
    expect(handoff).toContain('scrollIntoView');
    const start = handoff.indexOf('waitForBoxAfterSetup(');
    const scroll = handoff.indexOf('scrollIntoView');
    expect(start, 'the install must start').toBeGreaterThan(-1);
    // The first render is synchronous, so scrolling AFTER the call lands on a
    // panel that already has content rather than on an empty div.
    expect(scroll, 'scroll after starting the install, not before').toBeGreaterThan(start);
    const res = handoff.slice(handoff.indexOf("const res = $('setupResult')"));
    expect(res).toContain("$('setupResult')");
    expect(res).toContain('scrollIntoView');
  });

  it('every route out of the waiting panel goes through handoff', () => {
    // The confirm button, the "try anyway" link on an outdated speaker and the
    // setup-network button. A bare waitForBoxAfterSetup call from any of them
    // would skip the clear and the scroll again.
    const watcher = setup.slice(
      setup.indexOf('async function watchForSpeakerReady'),
      setup.indexOf('async function waitForBoxAfterSetup'),
    );
    const calls = [...watcher.matchAll(/waitForBoxAfterSetup\(/g)];
    expect(calls.length, 'only handoff may start the install from the watcher').toBe(1);
    expect(watcher).toContain("arm(t('setup.awaitConfirmBtn'), handoff)");
    expect(watcher).toContain("ta.onclick = (e) => { e.preventDefault(); handoff(); }");
    expect(watcher).toContain("arm(t('setup.awaitSetupNetworkBtn'), handoff)");
  });
});
