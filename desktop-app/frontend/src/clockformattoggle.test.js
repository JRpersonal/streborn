// Changing only the 12/24h format could switch the clock display off.
//
// The Bose body carries userEnable and timeFormat in one write, so the format
// cannot be sent without asserting on or off. The on/off came from
// `let clockEnabled = false`, assigned only inside a SUCCESSFUL
// GET /clockDisplay. That GET is documented to fail sometimes (the box drops the
// request while BoseApp restarts, which is the whole reason the panel has an
// "unknown" state), and after such a failure the variable still said false. A
// user who touched nothing but the format dropdown then sent
// userEnable="false" to a speaker whose clock was on, and nothing said so.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const here = dirname(fileURLToPath(import.meta.url));
const settings = readFileSync(join(here, 'views', 'settings.js'), 'utf8').replace(/\r\n/g, '\n');
const bundleDir = join(here, 'i18n', 'bundles');
const langs = ['ar', 'de', 'en', 'es', 'fr', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant'];

// The clock block, from the format preselect to the resume-on-power-on section.
const block = settings.slice(
  settings.indexOf("// Preselect the box's current 12/24h format"),
  settings.indexOf('// Resume last station on power-on'),
);

describe('the clock state is tri-state, not a boolean that defaults to off', () => {
  it('has no boolean defaulting to false any more', () => {
    expect(block).not.toContain('let clockEnabled = false');
    expect(settings).not.toContain('clockEnabled');
    expect(block).toContain('let clockState = null;');
  });

  it('records unknown as unknown on both failure shapes', () => {
    // a non-true/false body, and a throwing call
    expect(block).toContain("clockState = s === 'true' ? true : (s === 'false' ? false : null);");
    const c = block.slice(block.indexOf('} catch {'), block.indexOf('};', block.indexOf('} catch {')));
    expect(c).toContain('clockState = null;');
  });
});

describe('a format change never guesses the on/off half of the write', () => {
  it('re-reads the state at the moment of the change', () => {
    const handler = block.slice(block.indexOf('clockFormat.onchange'));
    expect(handler).toContain('await refreshClock();');
  });

  it('stops the write when the speaker did not say', () => {
    const handler = block.slice(block.indexOf('clockFormat.onchange'));
    expect(handler).toContain('if (clockState === null)');
    expect(handler).toContain('settingsView.clockFormatUnknownState');
    // and returns before posting anything
    const guard = handler.slice(handler.indexOf('if (clockState === null)'), handler.indexOf('lastFormat = clockFormat.value'));
    expect(guard).toContain('return;');
    expect(guard).not.toContain('postClock');
  });

  it('puts the dropdown back so it does not claim a format the speaker never got', () => {
    const handler = block.slice(block.indexOf('clockFormat.onchange'));
    expect(handler).toContain('clockFormat.value = lastFormat;');
    // lastFormat follows the value the box reported at load
    expect(block).toContain('lastFormat = clockFormat.value;');
  });

  it('sends the state the speaker reported when it did answer', () => {
    const handler = block.slice(block.indexOf('clockFormat.onchange'));
    expect(handler).toContain('await postClock(clockState);');
  });
});

describe('the new message exists in every bundle', () => {
  for (const lang of langs) {
    it(lang, () => {
      const b = JSON.parse(readFileSync(join(bundleDir, `${lang}.json`), 'utf8'));
      expect(b['settingsView.clockFormatUnknownState']).toBeTruthy();
    });
  }

  it('the German string keeps its umlauts', () => {
    const de = JSON.parse(readFileSync(join(bundleDir, 'de.json'), 'utf8'));
    expect(de['settingsView.clockFormatUnknownState']).toMatch(/[äöüß]/);
  });
});

describe('a slow read cannot overwrite a choice the user already made', () => {
  it('the preselect steps aside once the dropdown has been touched', () => {
    expect(block).toContain('let userTouchedFormat = false;');
    expect(block).toContain("clockFormat.addEventListener('input', () => { userTouchedFormat = true; });");
    const then = block.slice(block.indexOf('GetClockFormat24(boseHost).then'));
    expect(then.slice(0, 260)).toContain('if (userTouchedFormat) return;');
  });
});

describe('the on/off highlight tells the truth after a failed write', () => {
  it('the press remembers the state it came from and puts it back', () => {
    expect(block).toContain('const before = clockState;');
    expect(block).toContain('if (!ok) paintClock(before);');
  });

  it('postClock reports whether the speaker took it', () => {
    const post = block.slice(block.indexOf('const postClock = async'), block.indexOf('const pressClock'));
    expect(post).toContain('return true;');
    expect(post).toContain('return false;');
  });

  it('both buttons go through the same press', () => {
    expect(block).toContain('clockOn.onclick = () => pressClock(clockOn, true);');
    expect(block).toContain('clockOff.onclick = () => pressClock(clockOff, false);');
  });
});
