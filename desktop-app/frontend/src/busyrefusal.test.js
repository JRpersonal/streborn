// The one-write-per-speaker guard (desktop-app/boxbusy.go) refuses a second
// write to a speaker with an error that starts with the STR_BUSY: token. The
// update flow has shown a plain sentence for it since 2026-09-14; the removal,
// the firmware flash, the install and the SSH repair showed the raw Go error,
// token included ("Could not remove STR: STR_BUSY: this speaker is already
// being written to (an update is running); wait for that to finish").
//
// Their sentence is its own key, speaker.busyOtherTask: the refusal can come
// from an install, an update or a removal, so it must not claim "an update is
// already running" the way the update's second-click message rightly does.
import { describe, it, expect, beforeAll } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { setLocale } from './i18n/index.js';
import { BOX_BUSY_TOKEN, busyRefusalText } from './update/shared.js';
import { fwFlashStatus } from './fwguide.js';
import en from './i18n/bundles/en.json';
import de from './i18n/bundles/de.json';

const here = dirname(fileURLToPath(import.meta.url));
const read = (rel) => readFileSync(join(here, rel), 'utf8');

// What boxbusy.go's claim returns, as the window receives it.
const REFUSAL = 'STR_BUSY: this speaker is already being written to (an update is running); wait for that to finish';
const KEY = 'speaker.busyOtherTask';
const sentence = (name) => en[KEY].replace('{{name}}', name);

setLocale('en');
beforeAll(() => { setLocale('en'); });

describe('busyRefusalText', () => {
  it('turns a refusal into the still-busy sentence, without the token', () => {
    const out = busyRefusalText(REFUSAL, 'Kitchen');
    expect(out).toBe(sentence('Kitchen'));
    expect(out).not.toContain(BOX_BUSY_TOKEN);
  });

  it('reads the refusal from a rejected call, an Error or a result message alike', () => {
    expect(busyRefusalText(new Error(REFUSAL), 'Kitchen')).toBe(sentence('Kitchen'));
    expect(busyRefusalText({ message: REFUSAL }, 'Kitchen')).toBe(sentence('Kitchen'));
    expect(busyRefusalText('Error: ' + REFUSAL, 'Kitchen')).toBe(sentence('Kitchen'));
  });

  it('is empty for anything that is not a refusal, so a real failure keeps its own text', () => {
    expect(busyRefusalText('ssh: handshake failed', 'Kitchen')).toBe('');
    expect(busyRefusalText('', 'Kitchen')).toBe('');
    expect(busyRefusalText(null, 'Kitchen')).toBe('');
    expect(busyRefusalText(undefined)).toBe('');
  });

  it('still gives a sentence when the speaker has no label', () => {
    expect(busyRefusalText(REFUSAL)).toBe(sentence(en['common.unknown']));
  });
});

describe('the still-busy sentence', () => {
  it('does not claim an update is running: the refusal can come from an install or a removal', () => {
    expect(en[KEY]).toBe('{{name}} is still busy with another task (an install, update or removal). Please wait until it has finished and try again.');
    expect(de[KEY]).toBe('{{name}} ist noch mit einer anderen Aufgabe beschäftigt (Installation, Update oder Entfernen). Bitte warte, bis sie abgeschlossen ist, und versuche es dann erneut.');
    expect(busyRefusalText(REFUSAL, 'Kitchen')).not.toBe(en['update.alreadyRunning'].replace('{{name}}', 'Kitchen'));
  });

  it('is translated in every bundle, names the speaker and carries no dash', () => {
    const files = readdirSync(join(here, 'i18n', 'bundles')).filter((f) => f.endsWith('.json'));
    expect(files.length).toBeGreaterThanOrEqual(13);
    for (const f of files) {
      const text = JSON.parse(read(join('i18n', 'bundles', f)))[KEY];
      expect(text, f).toBeTruthy();
      expect(text, f).toContain('{{name}}');
      expect(text, f).not.toMatch(/[\u2013\u2014]/);
      if (f !== 'en.json') expect(text, f).not.toBe(en[KEY]);
    }
  });

  it('leaves the update flow its own second-click message', () => {
    for (const f of ['update/single.js', 'update/all.js']) {
      const src = read(f);
      expect(src, f).toContain("t('update.alreadyRunning'");
      expect(src, f).not.toContain(KEY);
    }
  });
});

describe('a refused firmware flash', () => {
  it('shows the sentence, not the failure line around the raw error', () => {
    const out = fwFlashStatus({ phase: 'error', error: REFUSAL, name: 'SoundTouch 20' });
    expect(out).toBe(sentence('SoundTouch 20'));
    expect(out).not.toContain(BOX_BUSY_TOKEN);
  });

  it('keeps the failure line for a flash that really failed', () => {
    expect(fwFlashStatus({ phase: 'error', error: 'the speaker does not answer' }))
      .toBe(en['fwGuide.autoFailed'].replace('{{error}}', 'the speaker does not answer'));
  });

  it('hands the flash error to the status line with a name for the sentence', () => {
    expect(read('fwguide.js')).toMatch(/set\(\{ phase: 'error', error: [^\n]*name: /);
  });
});

// The click handlers live in view modules that need a DOM and the Wails
// bindings, so the wiring is pinned from the source, the way the repo's other
// view checks do it.
describe('every flow that can be refused routes the refusal through the helper', () => {
  it('the removal, for the result message and for a rejected call', () => {
    const src = read('views/settings/actions.js');
    const i = src.indexOf('await UninstallSTR(box.host)');
    expect(i).toBeGreaterThan(0);
    const handler = src.slice(i, src.indexOf('finally', i));
    expect(handler).toContain('busyRefusalText(r.message, settingsBoxLabel(box))');
    expect(handler).toMatch(/catch \(e\) \{\s*reportError\(rmBtn, busyRefusalText\(e, settingsBoxLabel\(box\)\) \|\| e\);/);
    // The refusal branch comes before the generic "Could not remove STR" one.
    expect(handler.indexOf('busyRefusalText(r && r.message)'))
      .toBeLessThan(handler.indexOf("t('settingsView.removeSTRFailed'"));
  });

  // A refused install or repair did not fail: the sentence stands alone,
  // without the "Install failed:" / "SSH repair failed:" prefix, and the
  // install does not offer the failure report for it.
  it('the install, without the failure prefix and without the failure report', () => {
    const src = read('views/setup.js');
    expect(src).toMatch(/const busyMsg = busyRefusalText\(err, /);
    expect(src).toMatch(/escapeHtml\(busyMsg \|\| t\('setup\.installFailed', \{ msg: String\(err\) \}\)\)/);
    expect(src).toMatch(/\+ \(busyMsg \? '' : installFailureReportHtml\(\)\)\);/);
  });

  it('the SSH repair, without the failure prefix', () => {
    const src = read('views/setup.js');
    expect(src).toMatch(/escapeHtml\(busyRefusalText\(e, .*\) \|\| t\('setup\.repairSSHFailed', \{ msg: String\(e\) \}\)\)/);
  });
});
