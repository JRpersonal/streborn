// The one-write-per-speaker guard (desktop-app/boxbusy.go) refuses a second
// write to a speaker with an error that starts with the STR_BUSY: token. The
// update flow has shown a plain sentence for it since 2026-09-14; the removal,
// the firmware flash, the install and the SSH repair showed the raw Go error,
// token included ("Could not remove STR: STR_BUSY: this speaker is already
// being written to (an update is running); wait for that to finish").
import { describe, it, expect, beforeAll } from 'vitest';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { setLocale } from './i18n/index.js';
import { BOX_BUSY_TOKEN, busyRefusalText, busyRefusalOr } from './update/shared.js';
import { fwFlashStatus } from './fwguide.js';
import en from './i18n/bundles/en.json';

const here = dirname(fileURLToPath(import.meta.url));
const read = (rel) => readFileSync(join(here, rel), 'utf8');

// What boxbusy.go's claim returns, as the window receives it.
const REFUSAL = 'STR_BUSY: this speaker is already being written to (an update is running); wait for that to finish';
const sentence = (name) => en['update.alreadyRunning'].replace('{{name}}', name);

setLocale('en');
beforeAll(() => { setLocale('en'); });

describe('busyRefusalText', () => {
  it('turns a refusal into the sentence the update flow shows, without the token', () => {
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

describe('busyRefusalOr', () => {
  it('gives the sentence for a refusal and the error as it came for everything else', () => {
    expect(busyRefusalOr(REFUSAL, 'Kitchen')).toBe(sentence('Kitchen'));
    expect(busyRefusalOr('ssh: handshake failed', 'Kitchen')).toBe('ssh: handshake failed');
    expect(busyRefusalOr(new Error('boom'), 'Kitchen')).toBe('Error: boom');
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

  it('the install and the SSH repair', () => {
    const src = read('views/setup.js');
    expect(src).toMatch(/t\('setup\.installFailed', \{ msg: busyRefusalOr\(err, /);
    expect(src).toMatch(/t\('setup\.repairSSHFailed', \{ msg: busyRefusalOr\(e, /);
  });
});
