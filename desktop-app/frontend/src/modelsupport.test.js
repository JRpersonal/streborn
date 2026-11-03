// The honest model note in the setup picker. About two dozen mails asked
// whether STR works on a given model (Lifestyle, CineMate, SoundTouch 120,
// Smart Speaker 500, Home Speaker and others) before or after the install was
// tried. The picker now says what docs/MODELS.md knows BEFORE the install:
// no keys 1 to 6, not confirmed yet (own risk), or not a SoundTouch at all.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { setLocale, t } from './i18n/index.js';
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
    for (const m of ['SoundTouch 300', 'SoundTouch 520', 'SoundTouch 130', 'SoundTouch 120', 'SoundTouch 220',
      'Cinemate', 'CineMate 520',
      'Lifestyle', 'SoundTouch SA-4', 'SoundTouch SA-5', 'SoundTouch Wireless Link Adapter', 'SoundTouch Wireless Adapter']) {
      expect(boxModelSupport(m), m).toBe('limited');
    }
  });

  it('never mistakes the SoundTouch 300 for a SoundTouch 30', () => {
    expect(boxModelSupport('SoundTouch 300')).toBe('limited');
    expect(boxModelSupport('SoundTouch 30')).toBe('supported');
  });

  it('reads a raw Bose mDNS product code like the /info type', () => {
    // discovery.stockModelLabel passes codes it has no label for through raw.
    expect(boxModelSupport('soundtouch_300')).toBe('limited');
    expect(boxModelSupport('soundtouch_520')).toBe('limited');
    expect(boxModelSupport('SOUNDTOUCH_20')).toBe('supported');
    expect(boxModelSupport('wave_soundtouch')).toBe('supported');
    expect(boxModelSupport('soundtouch_portable')).toBe('supported');
    expect(boxModelSupport('soundtouch__')).toBe('unknown');
  });

  it('reads a model MODELS.md has no row for as unverified', () => {
    for (const m of ['Soundbar', 'SoundTouch Stereo JC', 'Acoustimass']) {
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
    // A row may carry a parenthetical after its name: an alias ("also sold as
    // **SoundTouch 130**") or the sub-models ("Lifestyle 535, 600 and 650").
    // Every bold name in it is a model string an owner may see, so each one is
    // checked too.
    const rows = [...md.matchAll(/^\| \*\*([^*]+)\*\*(?: \(([^)]*)\))? \|[^|]*\| \*\*(Verified|Working)/gm)];
    const names = [];
    for (const r of rows) {
      names.push(r[1]);
      for (const a of (r[2] || '').matchAll(/\*\*([^*]+)\*\*/g)) names.push(a[1]);
    }
    expect(rows.length).toBeGreaterThanOrEqual(16);
    for (const alias of ['SoundTouch 120', 'SoundTouch 130', 'SoundTouch 220']) {
      expect(names).toContain(alias);
    }
    for (const name of names) {
      expect(['supported', 'limited'], name).toContain(boxModelSupport(name));
    }
  });
});

describe('modelNoteKey', () => {
  it('maps each verdict to its note', () => {
    expect(modelNoteKey('SoundTouch 10')).toBe('');
    expect(modelNoteKey('Lifestyle')).toBe('setup.limitedNote');
    expect(modelNoteKey('SoundTouch 120')).toBe('setup.limitedNote');
    expect(modelNoteKey('Soundbar')).toBe('setup.unverifiedNote');
    expect(modelNoteKey('Bose Home Speaker 500')).toBe('setup.notSoundTouchNote');
  });

  it('has every note in all bundles', () => {
    const dir = new URL('./i18n/bundles/', import.meta.url);
    const files = readdirSync(dir).filter((f) => f.endsWith('.json'));
    expect(files.length).toBe(13);
    for (const f of files) {
      const b = JSON.parse(readFileSync(new URL(f, dir), 'utf8'));
      for (const k of ['setup.limitedNote', 'setup.unverifiedNote', 'setup.notSoundTouchNote']) {
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
    for (const k of ['setup.unverifiedNote', 'setup.notSoundTouchNote']) {
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
  });

  it('is drawn under the row in UI 2.0 Find', () => {
    const m = findModel([
      STOCK(51, 'Kitchen', 'SoundTouch 10'),
      STOCK(52, 'Cinema', 'Soundbar'),
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
    expect(m.candidates[0].modelNote).toBe('setup.limitedNote');
  });

  it('keeps the empty list to the Bose app hint, with no not-a-SoundTouch line', () => {
    // No discovery signal sees a Bose Music device, so a static line here would
    // only push the Bose app hint down for every SoundTouch owner.
    const html = findHTML(findModel([], {}));
    expect(html).toContain(t('setup.targetEmptyBoseApp'));
    expect(html).not.toContain('Only SoundTouch speakers show up here.');
    expect(srcOf('renderSetupTargetPicker')).not.toContain('targetEmptyNotSoundTouch');
  });
});
