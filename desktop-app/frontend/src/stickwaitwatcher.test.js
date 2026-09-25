// The setup watcher had one timestamp doing two jobs, and the reassuring state
// won every time because of it.
//
// firstSeenOnNetwork was set when the speaker's :8090 answered and reset to 0 on
// every miss. The grace check then read "!firstSeenOnNetwork || within 90s" as
// "still booting", so a speaker that discovery listed but that never answered
// restarted the grace on each of the ~120 polls in the six-minute budget and read
// "your speaker is still starting up" the whole way. The two states that carry an
// instruction, firmware too old and booted without the stick, were reachable only
// by a speaker that WAS answering.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const here = dirname(fileURLToPath(import.meta.url));
const setup = readFileSync(join(here, 'views', 'setup.js'), 'utf8').replace(/\r\n/g, '\n');
const bundleDir = join(here, 'i18n', 'bundles');
const langs = ['ar', 'de', 'en', 'es', 'fr', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant'];

const watcher = setup.slice(
  setup.indexOf('async function watchForSpeakerReady'),
  setup.indexOf('// outdatedFirmwareHtml') >= 0
    ? setup.indexOf('// outdatedFirmwareHtml')
    : setup.indexOf('// INSTALL_HELP_STEPS maps'),
);

describe('the watcher separates being on the network from answering', () => {
  it('keeps two timestamps', () => {
    expect(watcher).toContain('let firstSeenOnNetwork = 0;');
    expect(watcher).toContain('let lastAnsweredBosePort = 0;');
  });

  it('never resets the discovery clock on a missed Bose-port read', () => {
    // This is the whole bug: the old line was `else { firstSeenOnNetwork = 0; }`.
    expect(watcher).not.toMatch(/firstSeenOnNetwork = 0;\s*\}?\s*\/\/ discovery listed it/);
    expect(watcher).toContain('if (!firstSeenOnNetwork) firstSeenOnNetwork = Date.now();');
    // and the grace no longer treats "no timestamp" as "booting"
    expect(watcher).toContain('if ((Date.now() - firstSeenOnNetwork) < GRACE_MS)');
    expect(watcher).not.toContain('if (!firstSeenOnNetwork || (Date.now() - firstSeenOnNetwork) < GRACE_MS)');
  });

  it('has a state for a speaker that is listed but silent', () => {
    expect(watcher).toContain("lastSeenState = 'silent'");
    expect(watcher).toContain('setup.awaitOnNetworkSilent');
  });

  it('still calls a speaker that has only just gone quiet a restart', () => {
    // A speaker whose :8090 answered a moment ago and stopped IS rebooting, and
    // gets another grace window rather than the warning.
    expect(watcher).toContain('if (lastAnsweredBosePort && (Date.now() - lastAnsweredBosePort) < GRACE_MS)');
  });

  it('counts the silent state as having been on the network at timeout', () => {
    const tail = setup.slice(setup.indexOf('const wasOnNetwork'), setup.indexOf('setup.awaitSearchAgain'));
    expect(tail).toContain("lastSeenState === 'silent'");
    expect(tail).toContain("lastSeenState === 'booting'");
  });

  it('leaves the firmware and no-stick verdicts to a speaker that answered', () => {
    const after = watcher.slice(watcher.indexOf("lastSeenState = 'silent'"));
    expect(after).toContain('if (f && (f.outdated || !f.short))');
    expect(after).toContain("lastSeenState = 'no-stick'");
  });
});

describe('the new message exists everywhere and names the speaker', () => {
  for (const lang of langs) {
    it(lang, () => {
      const b = JSON.parse(readFileSync(join(bundleDir, `${lang}.json`), 'utf8'));
      expect(b['setup.awaitOnNetworkSilent']).toBeTruthy();
      expect(b['setup.awaitOnNetworkSilent']).toContain('{{model}}');
    });
  }

  // This particular sentence happens to need no umlaut, so asserting one would
  // be a false constraint. What must never appear is an ASCII substitute for one,
  // which is the way German strings have actually drifted in this repo.
  it('the German string spells German, not transliterated German', () => {
    const de = JSON.parse(readFileSync(join(bundleDir, 'de.json'), 'utf8'));
    expect(de['setup.awaitOnNetworkSilent'])
      .not.toMatch(/\b(fuer|ueber|koennen|muessen|waehrend|naechste|groesser|laeuft|noetig|zurueck)\b/i);
  });
});

describe('only the newest watcher owns the screen', () => {
  it('a new panel retires the one already running', () => {
    expect(setup).toContain('watchGeneration++;');
    expect(setup).toContain('watchForSpeakerReady({ ssid, pass, html, generation: watchGeneration });');
    expect(setup).toContain('const mine = () => generation === watchGeneration;');
  });

  it('a retired watcher writes neither the status line nor the button', () => {
    const status = setup.slice(setup.indexOf('const setStatus = (cls, txt, extraHtml) => {'));
    expect(status.slice(0, 200)).toContain('if (!mine()) return;');
    const arm = setup.slice(setup.indexOf('const arm = (label, onclick) => {'));
    expect(arm.slice(0, 200)).toContain('if (!mine()) return;');
  });

  it('a retired watcher stops polling and stops its ticker', () => {
    expect(setup).toContain('while (Date.now() < deadline && !ready && !aborted && mine()) {');
    expect(setup).toContain('if (ready || aborted || !mine()) { stopTicker(); return; }');
  });
});

// The await panel renders BELOW the stick wizard on purpose, because a user
// looking at the button they just pressed never scrolls up to find it. The
// install then renders ABOVE it, in a different element, so from the moment the
// user confirmed, the progress was off screen while the finished "do this on the
// speaker now" steps stayed on it.
describe('the install is shown where the user is looking', () => {
  const fn = setup.slice(setup.indexOf('async function waitForBoxAfterSetup'),
    setup.indexOf('const deadline = Date.now() + 5 * 60 * 1000'));

  it('takes the finished await panel down', () => {
    expect(fn).toContain("const awaitPanel = $('setupAwaitResult');");
    expect(fn).toContain("awaitPanel.innerHTML = '';");
  });

  it('never clears the element it is about to render into', () => {
    expect(fn).toContain('awaitPanel !== setupResult');
  });

  it('brings the user to the progress', () => {
    expect(fn).toContain('setupResult.scrollIntoView(');
  });
});
