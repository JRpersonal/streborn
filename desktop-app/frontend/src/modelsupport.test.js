// The honest model note in the setup picker. About two dozen mails asked
// whether STR works on a given model (Lifestyle, CineMate, SoundTouch 120,
// Smart Speaker 500, Home Speaker and others) before or after the install was
// tried. The picker now says what docs/MODELS.md knows BEFORE the install:
// no keys 1 to 6, not confirmed yet (own risk), or not a SoundTouch at all.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { setLocale } from './i18n/index.js';
import { boxModelSupport, modelNoteKey } from './utils.js';
import { findHTML, findModel } from './shell2/setup/find.js';
import { srcOf } from './test/source.js';

setLocale('en');

describe('boxModelSupport', () => {
  it('reads the speakers with keys 1 to 6 as supported', () => {
    for (const m of ['SoundTouch 10', 'SoundTouch 20', 'SoundTouch 30', 'SoundTouch Portable', 'Wave SoundTouch']) {
      expect(boxModelSupport(m), m).toBe('supported');
    }
  });

  it('reads the working models without keys 1 to 6 as limited', () => {
    // The /info type strings seen in the field (docs/MODEL-VARIANTS.md).
    for (const m of ['SoundTouch 300', 'SoundTouch 520', 'SoundTouch 130', 'Cinemate', 'CineMate 520',
      'Lifestyle', 'SoundTouch SA-4', 'SoundTouch SA-5', 'SoundTouch Wireless Link Adapter', 'SoundTouch Wireless Adapter']) {
      expect(boxModelSupport(m), m).toBe('limited');
    }
  });

  it('never mistakes the SoundTouch 300 for a SoundTouch 30', () => {
    expect(boxModelSupport('SoundTouch 300')).toBe('limited');
    expect(boxModelSupport('SoundTouch 30')).toBe('supported');
  });

  it('reads a model MODELS.md has no row for as unverified', () => {
    for (const m of ['SoundTouch 120', 'SoundTouch 220', 'Soundbar', 'SoundTouch Stereo JC', 'Acoustimass']) {
      expect(boxModelSupport(m), m).toBe('unverified');
    }
  });

  it('reads a Bose Music or Bluetooth product as not a SoundTouch', () => {
    for (const m of ['Bose Home Speaker 500', 'Bose Smart Speaker', 'Bose Portable Smart Speaker',
      'Bose Portable Home Speaker', 'Bose Smart Soundbar 600', 'Bose Soundbar 700', 'Bose Smart Ultra Soundbar',
      'SoundLink Revolve', 'Bose TV Speaker']) {
      expect(boxModelSupport(m), m).toBe('notSoundTouch');
    }
  });

  it('keeps a speaker without a type free of any note', () => {
    for (const m of ['', null, undefined, 'SoundTouch', '  ']) {
      expect(boxModelSupport(m)).toBe('unknown');
      expect(modelNoteKey(m)).toBe('');
    }
  });

  it('classifies every Verified or Working model in docs/MODELS.md as installable without the unverified note', () => {
    const md = readFileSync(new URL('../../../docs/MODELS.md', import.meta.url), 'utf8');
    const rows = [...md.matchAll(/^\| \*\*([^*]+)\*\* \|[^|]*\| \*\*(Verified|Working)/gm)].map((r) => r[1]);
    expect(rows.length).toBeGreaterThanOrEqual(12);
    for (const name of rows) {
      expect(['supported', 'limited'], name).toContain(boxModelSupport(name));
    }
  });
});

describe('modelNoteKey', () => {
  it('maps each verdict to its note', () => {
    expect(modelNoteKey('SoundTouch 10')).toBe('');
    expect(modelNoteKey('Lifestyle')).toBe('setup.limitedNote');
    expect(modelNoteKey('SoundTouch 120')).toBe('setup.unverifiedNote');
    expect(modelNoteKey('Bose Home Speaker 500')).toBe('setup.notSoundTouchNote');
  });

  it('has every note and the empty-list line in all bundles', () => {
    const dir = new URL('./i18n/bundles/', import.meta.url);
    const files = readdirSync(dir).filter((f) => f.endsWith('.json'));
    expect(files.length).toBe(13);
    for (const f of files) {
      const b = JSON.parse(readFileSync(new URL(f, dir), 'utf8'));
      for (const k of ['setup.limitedNote', 'setup.unverifiedNote', 'setup.notSoundTouchNote', 'setup.targetEmptyNotSoundTouch']) {
        expect(typeof b[k], `${f} ${k}`).toBe('string');
        expect(b[k].length, `${f} ${k}`).toBeGreaterThan(10);
        // No dashes as punctuation in user texts.
        expect(b[k], `${f} ${k}`).not.toMatch(/[–—]| - /);
      }
    }
  });

  it('says own risk for an unverified model and never blames the network', () => {
    const en = JSON.parse(readFileSync(new URL('./i18n/bundles/en.json', import.meta.url), 'utf8'));
    expect(en['setup.unverifiedNote']).toMatch(/own risk/);
    expect(en['setup.notSoundTouchNote']).toMatch(/not a SoundTouch/);
    for (const k of ['setup.unverifiedNote', 'setup.notSoundTouchNote', 'setup.targetEmptyNotSoundTouch']) {
      expect(en[k]).not.toMatch(/GHz|WPA|firewall/i);
    }
  });
});

describe('the note in both layouts', () => {
  const STOCK = (n, name, model) => ({ host: `192.0.2.${n}`, port: 8090, name, friendlyName: name, model, kind: 'stock' });

  it('is drawn under the card in the classic picker', () => {
    const picker = srcOf('renderSetupTargetPicker');
    const stockLoop = picker.slice(picker.indexOf('for (const b of stockBoxes)'));
    expect(stockLoop).toContain('modelNoteKey(b.model || b.type)');
    expect(stockLoop).toContain('setup-target-limited-note');
    // The empty list explains the Bose Music devices that never show up.
    expect(picker).toContain("t('setup.targetEmptyNotSoundTouch')");
  });

  it('is drawn under the row in UI 2.0 Find', () => {
    const m = findModel([
      STOCK(51, 'Kitchen', 'SoundTouch 10'),
      STOCK(52, 'Cinema', 'SoundTouch 120'),
      STOCK(53, 'Console', 'Lifestyle'),
    ], {});
    const by = Object.fromEntries(m.candidates.map((c) => [c.name, c]));
    expect(by.Kitchen.modelNote).toBe('');
    expect(by.Cinema.modelNote).toBe('setup.unverifiedNote');
    expect(by.Console.modelNote).toBe('setup.limitedNote');
    expect(by.Console.limited).toBe(true);
    const html = findHTML(m);
    expect(html).toContain('STR has not been tested on this model yet.');
    expect(html).toContain('no hardware preset buttons 1 to 6');
    // The note still leaves the speaker selectable: nothing here blocks.
    expect(html).toMatch(/value="192\.0\.2\.52"(?! disabled)/);
  });

  it('reads the model from type when model is absent', () => {
    const m = findModel([{ host: '192.0.2.60', name: 'X', type: 'SoundTouch 120', kind: 'stock' }], {});
    expect(m.candidates[0].modelNote).toBe('setup.unverifiedNote');
  });

  it('explains the missing Bose Music devices in the empty Find list', () => {
    const html = findHTML(findModel([], {}));
    expect(html).toContain('Only SoundTouch speakers show up here.');
    expect(findHTML(findModel([], {}), { searching: true })).not.toContain('Only SoundTouch speakers show up here.');
  });
});
