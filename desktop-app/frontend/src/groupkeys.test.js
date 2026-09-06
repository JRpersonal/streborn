// The group-key helpers (#863), checked without a DOM, plus the i18n parity
// of every string the section and the key map can show.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import {
  normalizeDoc, templateFromBoxes, templateFromStored, validateTemplate,
  withTemplate, withoutTemplate, keyOf, bindKey, describeTemplate, webhookOnKey,
} from './groupkeys.js';

const living = { deviceID: 'AAAA', host: '192.0.2.10', friendlyName: 'Living room' };
const kitchen = { deviceID: 'BBBB', host: '192.0.2.11', friendlyName: 'Kitchen' };
const office = { deviceID: 'CCCC', host: '192.0.2.12', friendlyName: 'Office' };

describe('normalizeDoc', () => {
  it('accepts nothing, an older agent, and a full document', () => {
    expect(normalizeDoc(null)).toEqual({ templates: [], bindings: {} });
    expect(normalizeDoc({})).toEqual({ templates: [], bindings: {} });
    const d = normalizeDoc({
      templates: [{ name: 'Evening', master: { deviceID: 'AAAA', ip: '192.0.2.10' }, members: [{ deviceID: 'BBBB' }, {}], permanent: 1 }],
      bindings: { thumbsUp: 'Evening', thumbsDown: 'Gone', prev: 'Evening' },
    });
    expect(d.templates).toHaveLength(1);
    expect(d.templates[0].members).toEqual([{ deviceID: 'BBBB', ip: '', name: '' }]);
    expect(d.templates[0].permanent).toBe(true);
    // A binding to a template that does not exist, or on a key that cannot
    // carry a group, is dropped instead of rendered.
    expect(d.bindings).toEqual({ thumbsUp: 'Evening' });
  });
});

describe('templates', () => {
  it('builds one from the composed selection and from a stored permanent group', () => {
    const t = templateFromBoxes({ name: ' Evening ', master: living, members: [kitchen, office], permanent: false });
    expect(t.name).toBe('Evening');
    expect(t.master).toEqual({ deviceID: 'AAAA', ip: '192.0.2.10', name: 'Living room' });
    expect(t.members.map(m => m.ip)).toEqual(['192.0.2.11', '192.0.2.12']);
    expect(t.permanent).toBe(false);
    const s = templateFromStored({ masterBox: living, members: [{ box: kitchen, ip: '192.0.2.11', name: '' }, { box: null, ip: '192.0.2.99', name: 'Attic' }] }, 'Night');
    expect(s.permanent).toBe(true);
    expect(s.members).toEqual([
      { deviceID: 'BBBB', ip: '192.0.2.11', name: 'Kitchen' },
      { deviceID: '', ip: '192.0.2.99', name: 'Attic' },
    ]);
  });

  it('refuses a nameless, memberless or duplicate template', () => {
    const doc = { templates: [templateFromBoxes({ name: 'Evening', master: living, members: [kitchen] })], bindings: {} };
    expect(validateTemplate(templateFromBoxes({ name: '', master: living, members: [kitchen] }), doc)).toBe('multiroom.groupKeysNameRequired');
    expect(validateTemplate(templateFromBoxes({ name: 'Night', master: living, members: [] }), doc)).toBe('multiroom.groupKeysNeedGroup');
    expect(validateTemplate(templateFromBoxes({ name: 'evening', master: living, members: [kitchen] }), doc)).toBe('multiroom.groupKeysDuplicateName');
    expect(validateTemplate(templateFromBoxes({ name: 'Night', master: living, members: [kitchen] }), doc)).toBe('');
  });

  it('adds, binds, rebinds and removes without ever leaving a dangling binding', () => {
    let doc = { templates: [], bindings: {} };
    doc = withTemplate(doc, templateFromBoxes({ name: 'Evening', master: living, members: [kitchen] }));
    doc = withTemplate(doc, templateFromBoxes({ name: 'Party', master: kitchen, members: [living, office] }));
    doc = bindKey(doc, 'Evening', 'thumbsUp');
    expect(keyOf(doc, 'Evening')).toBe('thumbsUp');
    // The other template takes the same key: the first one loses it.
    doc = bindKey(doc, 'Party', 'thumbsUp');
    expect(doc.bindings).toEqual({ thumbsUp: 'Party' });
    expect(keyOf(doc, 'Evening')).toBe('');
    // A template moves keys: its old key is freed.
    doc = bindKey(doc, 'Party', 'thumbsDown');
    expect(doc.bindings).toEqual({ thumbsDown: 'Party' });
    // Unbinding.
    doc = bindKey(doc, 'Party', '');
    expect(doc.bindings).toEqual({});
    doc = bindKey(doc, 'Party', 'thumbsDown');
    doc = withoutTemplate(doc, 'Party');
    expect(doc.templates.map(t => t.name)).toEqual(['Evening']);
    expect(doc.bindings).toEqual({});
    // Bogus keys never land in the document.
    expect(bindKey(doc, 'Evening', 'prev').bindings).toEqual({});
  });

  it('describes a template with the freshest speaker names', () => {
    const t = templateFromBoxes({ name: 'Evening', master: living, members: [kitchen, { deviceID: 'ZZZZ', host: '192.0.2.50', friendlyName: 'Old name' }] });
    const renamed = { ...kitchen, friendlyName: 'Cuisine' };
    expect(describeTemplate(t, [living, renamed])).toBe('Living room + Cuisine, Old name');
    // Without a discovered box the stored name stands; without that, the address.
    expect(describeTemplate({ master: { ip: '192.0.2.1' }, members: [{ name: 'Attic' }] }, [])).toBe('192.0.2.1 + Attic');
  });
});

describe('webhookOnKey', () => {
  it('sees a per-key trigger and the legacy shared thumbs action', () => {
    expect(webhookOnKey(null, 'thumbsUp')).toBe(false);
    expect(webhookOnKey({ buttons: { thumbsUp: { enabled: true, url: 'http://x' } } }, 'thumbsUp')).toBe(true);
    expect(webhookOnKey({ buttons: { thumbsUp: { enabled: false, url: 'http://x' } } }, 'thumbsUp')).toBe(false);
    expect(webhookOnKey({ thumb: { enabled: true, url: 'http://x' } }, 'thumbsDown')).toBe(true);
    expect(webhookOnKey({ thumb: { enabled: true, url: 'http://x' } }, 'prev')).toBe(false);
    expect(webhookOnKey({ buttons: { thumbsDown: { enabled: true, type: 'wol', mac: 'AA:BB:CC:DD:EE:FF' } } }, 'thumbsDown')).toBe(true);
  });
});

// Every string the group keys section and the key map marker can show must
// exist in all bundles and must not be left in English in a non-English one.
describe('group keys i18n', () => {
  const bundles = ['en', 'de', 'fr', 'es', 'nl', 'pl', 'tr', 'uk', 'lt', 'lv', 'ar', 'ja', 'zh-Hant'];
  const read = (loc) => JSON.parse(
    readFileSync(new URL(`./i18n/bundles/${loc}.json`, import.meta.url), 'utf8'));
  const en = read('en');
  const keys = Object.keys(en).filter((k) => k.startsWith('multiroom.groupKeys') || k.startsWith('settingsView.webhookGroupKey') || k === 'settingsView.webhookRemoteLegendGroup');
  it('has the section strings in English at all', () => {
    expect(keys.length).toBeGreaterThanOrEqual(20);
  });
  it('is present and translated in every bundle, placeholders intact', () => {
    const ph = (s) => (String(s).match(/\{\{\s*\w+\s*\}\}/g) || []).sort().join('|');
    for (const loc of bundles.filter((l) => l !== 'en')) {
      const b = read(loc);
      for (const k of keys) {
        expect(b[k], `${loc}.json ${k}`).toBeTruthy();
        expect(b[k], `${loc}.json ${k} untranslated`).not.toBe(en[k]);
        expect(ph(b[k]), `${loc}.json ${k} placeholders`).toBe(ph(en[k]));
      }
    }
  });
});
