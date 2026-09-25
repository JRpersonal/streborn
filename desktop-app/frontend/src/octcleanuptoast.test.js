// The cleanup button reported success whatever happened. On the ST30 in #986 it
// matched no file at all, healed nothing, and toasted "leftovers removed (0)",
// so the reporter believed his speaker was clean while every preset went on
// answering "not logged in" for three more days.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const here = dirname(fileURLToPath(import.meta.url));
const settings = readFileSync(join(here, 'views', 'settings.js'), 'utf8');
const main = readFileSync(join(here, 'main.js'), 'utf8');
const state = readFileSync(join(here, 'state.js'), 'utf8');
const bundleDir = join(here, 'i18n', 'bundles');
const langs = ['ar', 'de', 'en', 'es', 'fr', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant'];

// The click handler, from its comment to the label restore.
const handler = settings.slice(
  settings.indexOf("const rmConflictBtn = $('boxRemoveConflictBtn')"),
  settings.indexOf("const tfrBtn = $('boxTrueFactoryResetBtn')"),
);

describe('the conflicting-mod cleanup says what it actually did', () => {
  it('reports a cleanup that removed nothing as a problem, not a success', () => {
    expect(handler).toContain('settingsView.removeConflictNothingToast');
    expect(handler).toContain('!removed.length');
    // A modal, not a toast: the whole defect was an outcome nobody noticed.
    expect(handler).toMatch(/showError\(notes\.join/);
  });

  it('surfaces stillDetected instead of swallowing it', () => {
    expect(handler).toContain('res.stillDetected');
    expect(handler).toContain('settingsView.removeConflictStillToast');
    // And names what is left, the foreign cloud address first.
    expect(handler).toContain('res.foreignCloudURL');
  });

  it('says the healed cloud address needs the restart', () => {
    expect(handler).toContain('res.cloudURLHealed');
    expect(handler).toContain('settingsView.removeConflictCloudToast');
  });

  it('offers the button for a foreign cloud address with no leftover files', () => {
    // These survive a factory reset, so the marker files can be long gone while
    // the speaker still cannot reach STR at all.
    expect(settings).toContain('box.foreignCloudURL');
    expect(settings).toContain('settingsView.healCloudURLBtn');
    expect(handler).toContain('cloudOnly');
  });
});

describe('the banner names the speaker asking a dead cloud address', () => {
  it('has its own line, not folded into the conflicting-mod one', () => {
    expect(main).toContain('speaker.foreignCloudBanner');
    expect(main).toContain('b.foreignCloudURL && !b.conflictingMod');
  });

  it('is not dismissible: nothing plays on such a speaker', () => {
    const banner = main.slice(
      main.indexOf('const foreignCloud = boxes.filter'),
      main.indexOf('const noWifi = boxes.filter'),
    );
    expect(banner).not.toContain('warnDismissed');
  });

  it('never survives in the discovery cache as a stale warning', () => {
    expect(state).toContain("'foreignCloudURL'");
  });
});

describe('every bundle carries the new strings', () => {
  const keys = [
    'settingsView.removeConflictNothingToast',
    'settingsView.removeConflictCloudToast',
    'settingsView.removeConflictStillToast',
    'settingsView.healCloudURLBtn',
    'settingsView.healCloudURLHelp',
    'speaker.foreignCloudBanner',
  ];
  for (const lang of langs) {
    it(lang, () => {
      const b = JSON.parse(readFileSync(join(bundleDir, `${lang}.json`), 'utf8'));
      for (const k of keys) {
        expect(b[k], `${lang} is missing ${k}`).toBeTruthy();
      }
      // The placeholders have to survive translation or the sentence loses the
      // one fact it exists to carry.
      expect(b['settingsView.healCloudURLHelp']).toContain('{{url}}');
      expect(b['speaker.foreignCloudBanner']).toContain('{{url}}');
      expect(b['speaker.foreignCloudBanner']).toContain('{{name}}');
      expect(b['settingsView.removeConflictStillToast']).toContain('{{detail}}');
    });
  }

  it('the German strings keep their umlauts', () => {
    const de = JSON.parse(readFileSync(join(bundleDir, 'de.json'), 'utf8'));
    expect(de['settingsView.healCloudURLBtn']).toMatch(/[äöüß]/);
    expect(de['settingsView.removeConflictStillToast']).toMatch(/[äöüß]/);
  });
});

describe('a healed address waiting for the restart is not a failure', () => {
  it('reads as the cloud-reset message, not as "nothing to remove"', () => {
    expect(handler).toContain('res.cloudURLRestartPending');
    // The modal-vs-toast decision has to exclude it too, or a pending restart
    // pops an error box.
    expect(handler).toMatch(/!removed\.length && !res\.cloudURLHealed && !res\.cloudURLRestartPending/);
  });
});
