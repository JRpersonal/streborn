// A music stick plugged into a speaker (#1136) joins the Library as one more
// server, named after the speaker in the user's language, and a stick that
// cannot be offered (an install stick, exFAT, ...) gets one line saying what to
// do instead of silently missing.
import { describe, it, expect, vi, beforeAll } from 'vitest';
import { readFileSync } from 'node:fs';

vi.mock('./api.js', () => ({
  ProbeTrackDelivery: vi.fn(),
  ListMediaServers: vi.fn(),
  LibraryRefusedServers: vi.fn(),
  LibraryUSBNotices: vi.fn(),
  BrowseLibrary: vi.fn(),
  AddMediaServerByURL: vi.fn(),
  RemoveManualMediaServer: vi.fn(),
  PlayURL: vi.fn(),
  StartQueue: vi.fn(),
  SaveLibraryPreset: vi.fn(),
  SaveFolderPreset: vi.fn(),
  Status: vi.fn(),
  EnableBoxMediaServer: vi.fn(),
  boxFetch: vi.fn(),
}));

const { libraryUSBNote, localizeUSBServers } = await import('./views/library.js');
const { setLocale } = await import('./i18n/index.js');
const read = (loc) => JSON.parse(
  readFileSync(new URL(`./i18n/bundles/${loc}.json`, import.meta.url), 'utf8'));
const en = read('en');

describe('localizeUSBServers', () => {
  beforeAll(() => { setLocale('en'); });

  it('names the stick after its speaker and leaves every other server alone', () => {
    const nas = { udn: 'uuid:nas', friendlyName: 'NAS', modelName: 'MiniDLNA' };
    const out = localizeUSBServers([
      nas,
      { udn: 'uuid:str-usb-x', friendlyName: 'USB stick (Kitchen)', modelName: 'USB stick', usbSpeaker: 'Kitchen' },
    ]);
    expect(out[0]).toBe(nas);
    expect(out[1].friendlyName).toBe('USB stick (Kitchen)');
    expect(out[1].modelName).toBe('');
    expect(out[1].usbSpeaker).toBe('Kitchen');
  });

  it('speaks German in German', () => {
    setLocale('de');
    const [s] = localizeUSBServers([{ udn: 'u', friendlyName: 'USB stick (Küche)', usbSpeaker: 'Küche' }]);
    expect(s.friendlyName).toBe('USB-Stick (Küche)');
    setLocale('en');
  });

  it('copes with nothing', () => {
    expect(localizeUSBServers(null)).toEqual([]);
  });
});

describe('libraryUSBNote', () => {
  beforeAll(() => { setLocale('en'); });

  it('says per stick what is wrong and what to do', () => {
    const html = libraryUSBNote([
      { speaker: 'Kitchen', code: 'not-fat32' },
      { speaker: 'Bath', code: 'install-stick' },
    ]);
    expect(html).toContain('Kitchen is not formatted as FAT32');
    expect(html).toContain('Bath is an ST Reborn install stick');
  });

  it('falls back to "could not be read" for a code it does not know', () => {
    expect(libraryUSBNote([{ speaker: 'Kitchen', code: 'something-new' }])).toContain('could not be read');
  });

  it('renders nothing for no notices or junk, and escapes the name', () => {
    expect(libraryUSBNote([])).toBe('');
    expect(libraryUSBNote(null)).toBe('');
    expect(libraryUSBNote([{ code: 'not-fat32' }])).toBe('');
    expect(libraryUSBNote([{ speaker: '<b>x</b>', code: 'unreadable' }])).not.toContain('<b>x</b>');
  });
});

describe('the USB stick strings', () => {
  const keys = ['library.usbServerName', 'library.usbUnsupportedModel', 'library.usbInstallStick',
    'library.usbNotFAT32', 'library.usbBigClusters', 'library.usbUnreadable'];
  const bundles = ['de', 'fr', 'es', 'it', 'nl', 'pl', 'tr', 'uk', 'lt', 'lv', 'ar', 'ja', 'zh-Hant'];

  it('are in every bundle, translated, with the speaker placeholder', () => {
    for (const k of keys) {
      expect(en[k]).toContain('{{name}}');
      for (const loc of bundles) {
        const b = read(loc);
        expect(b[k], `${loc} ${k}`).toBeTruthy();
        expect(b[k], `${loc} ${k} untranslated`).not.toBe(en[k]);
        expect(b[k], `${loc} ${k} placeholder`).toContain('{{name}}');
      }
    }
  });

  it('keep German informal, with real umlauts and no em dash', () => {
    const de = read('de');
    for (const k of keys) {
      expect(de[k]).not.toMatch(/\bSie\b|—/);
      expect(de[k]).not.toMatch(/ae|oe|ue/);
    }
  });
});
