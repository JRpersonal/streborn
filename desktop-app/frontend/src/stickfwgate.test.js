// The setup wizard's firmware gate for the USB stick, checked without a DOM.
//
// Support case 2026-11: the install over the network said firmware-too-old for
// a SoundTouch 20 on 9.0.41, and two minutes later the wizard wrote its 14
// install files to the stick anyway.
import { describe, it, expect, beforeAll } from 'vitest';
import { readFileSync } from 'node:fs';
import { setLocale } from './i18n/index.js';
import { stickGateHost, stickFirmwareGate, stickFwTooOldHtml } from './stickfwgate.js';
import en from './i18n/bundles/en.json';
import de from './i18n/bundles/de.json';

setLocale('en');
beforeAll(() => { setLocale('en'); });

const blocked = { blocked: true, firmware: '9.0.41', model: 'SoundTouch 20', moduleType: 'scm', variant: '', source: 'install' };

describe('stick firmware gate', () => {
  it('gates the speaker picked as the target, never a factory-reset one', () => {
    expect(stickGateHost({ kind: 'stock', box: { host: '192.0.2.1' } })).toBe('192.0.2.1');
    expect(stickGateHost({ kind: 'str', box: { host: ' 192.0.2.2 ' } })).toBe('192.0.2.2');
    expect(stickGateHost({ kind: 'factory-reset', box: null })).toBe('');
    expect(stickGateHost(null)).toBe('');
  });

  it('blocks only on a blocked answer, and never on a failure or a missing binding', async () => {
    expect(await stickFirmwareGate(async () => blocked, '192.0.2.1')).toEqual(blocked);
    expect(await stickFirmwareGate(async () => ({ blocked: false }), '192.0.2.1')).toBeNull();
    expect(await stickFirmwareGate(async () => { throw new Error('STR_MISSING_BINDING'); }, '192.0.2.1')).toBeNull();
    let asked = false;
    expect(await stickFirmwareGate(async () => { asked = true; return blocked; }, '')).toBeNull();
    expect(asked).toBe(false);
    expect(await stickFirmwareGate(undefined, '192.0.2.1')).toBeNull();
  });

  it('says nothing was written and shows the firmware ladder for the speaker', () => {
    const html = stickFwTooOldHtml(blocked, { host: '192.0.2.1', model: 'SoundTouch 20' });
    expect(html).toContain('data-stick-gate="firmware-too-old"');
    expect(html).toContain('9.0.41');
    expect(html).toContain('id="setupFwBanner"');
    expect(html).toContain('192.0.2.1');
    expect(stickFwTooOldHtml({ blocked: false }, null)).toBe('');
    expect(stickFwTooOldHtml(null, null)).toBe('');
  });

  it('has the message in English and German, with the version placeholder', () => {
    for (const b of [en, de]) {
      expect(b['setup.stickFwTooOld']).toContain('{{firmware}}');
    }
    expect(en['setup.stickFwTooOld']).toMatch(/nothing was written/);
  });

  it('runs before the stick is checked, formatted or written in doSetup', () => {
    const src = readFileSync(new URL('./views/setup.js', import.meta.url), 'utf8');
    const start = src.indexOf('async function doSetup()');
    expect(start).toBeGreaterThan(0);
    const body = src.slice(start);
    const gate = body.indexOf('stickFirmwareGate(StickInstallFirmwareGate');
    expect(gate).toBeGreaterThan(0);
    for (const later of ['await CheckStick(', 'await FormatStick(', 'await WriteStickFiles(']) {
      expect(body.indexOf(later)).toBeGreaterThan(gate);
    }
  });
});
