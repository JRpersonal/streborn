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
  GetQueue: vi.fn(),
  SaveLibraryPreset: vi.fn(),
  SaveFolderPreset: vi.fn(),
  Status: vi.fn(),
  EnableBoxMediaServer: vi.fn(),
  boxFetch: vi.fn(),
}));

const { libraryUSBNote, localizeUSBServers, isUSBFileURL, usbHiResSkippedNote } = await import('./views/library.js');
const { friendlyErrorText } = await import('./utils.js');
const api = await import('./api.js');
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

  it('adds the volume label when the stick has one', () => {
    const [s] = localizeUSBServers([{ udn: 'u', friendlyName: 'USB stick MUSIK (Kitchen)', usbSpeaker: 'Kitchen', usbLabel: 'MUSIK' }]);
    expect(s.friendlyName).toBe('USB stick MUSIK (Kitchen)');
    setLocale('de');
    const [d] = localizeUSBServers([{ udn: 'u', usbSpeaker: 'Küche', usbLabel: 'MUSIK' }]);
    expect(d.friendlyName).toBe('USB-Stick MUSIK (Küche)');
    setLocale('en');
  });

  it('copes with nothing', () => {
    expect(localizeUSBServers(null)).toEqual([]);
  });
});

describe('the USB stick name with its label', () => {
  const bundles = ['de', 'fr', 'es', 'it', 'nl', 'pl', 'tr', 'uk', 'lt', 'lv', 'ar', 'ja', 'zh-Hant'];

  it('is in every bundle, with both placeholders, next to the plain name', () => {
    expect(en['library.usbServerNameLabel']).toBe('USB stick {{label}} ({{name}})');
    for (const loc of bundles) {
      const b = read(loc);
      const v = b['library.usbServerNameLabel'];
      expect(v, loc).toBeTruthy();
      expect(v, `${loc} untranslated`).not.toBe(en['library.usbServerNameLabel']);
      expect(v, `${loc} placeholders`).toContain('{{label}}');
      expect(v, `${loc} placeholders`).toContain('{{name}}');
      // The same words as the plain name, the label added.
      expect(v.replace('{{label}}', '').replace(/\s+/g, ''), loc).toBe(b['library.usbServerName'].replace(/\s+/g, ''));
    }
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

// A file on the stick above 48 kHz is refused by the agent with the code
// library-file-hires (friendlyError passes "code: message"), and a folder steps
// over such files and says how many (skippedHiRes in the speaker's queue).
describe('hi-res files on the USB stick', () => {
  beforeAll(() => { setLocale('en'); });

  it('tells a stick file from any other', () => {
    expect(isUSBFileURL('http://192.0.2.10:8888/usb/file/Album%201/a.flac')).toBe(true);
    expect(isUSBFileURL('http://192.0.2.50:9000/MediaItems/1.flac')).toBe(false);
    expect(isUSBFileURL('http://192.0.2.10:8888/usb/art/cover.jpg')).toBe(false);
    expect(isUSBFileURL(null)).toBe(false);
  });

  it('turns the refusal into the sentence, in the user language', () => {
    const raw = 'library-file-hires: This speaker cannot play this file: it has a sample rate above 48 kHz.';
    expect(friendlyErrorText(raw)).toBe(en['library.usbHiRes']);
    expect(friendlyErrorText(`PlayURL: ${raw}`)).toBe(en['library.usbHiRes']);
    setLocale('de');
    expect(friendlyErrorText(raw)).toContain('über 48 kHz');
    setLocale('en');
  });

  it('asks the speaker once, only for a folder with stick files', async () => {
    api.GetQueue.mockReset();
    api.GetQueue.mockResolvedValue({ active: true, skippedHiRes: 2 });
    const box = { host: '192.0.2.10', port: 8888 };
    expect(await usbHiResSkippedNote(box, [{ url: 'http://192.0.2.50:9000/a.flac' }])).toBe('');
    expect(api.GetQueue).not.toHaveBeenCalled();
    const note = await usbHiResSkippedNote(box, [{ url: 'http://192.0.2.10:8888/usb/file/a.flac' }]);
    expect(note).toContain('(2 skipped)');
    expect(api.GetQueue).toHaveBeenCalledTimes(1);
    api.GetQueue.mockResolvedValue({ active: true });
    expect(await usbHiResSkippedNote(box, [{ url: 'http://192.0.2.10:8888/usb/file/a.flac' }])).toBe('');
    api.GetQueue.mockRejectedValue(new Error('offline'));
    expect(await usbHiResSkippedNote(box, [{ url: 'http://192.0.2.10:8888/usb/file/a.flac' }])).toBe('');
  });

  it('has its strings in every bundle, translated, German informal and without dashes', () => {
    const keys = { 'library.usbHiRes': '', 'library.usbHiResSkipped': '{{n}}', 'library.usbCannotDecode': '{{title}}', 'play.errHiRes': '' };
    const bundles = ['de', 'fr', 'es', 'it', 'nl', 'pl', 'tr', 'uk', 'lt', 'lv', 'ar', 'ja', 'zh-Hant'];
    for (const [k, ph] of Object.entries(keys)) {
      expect(en[k], k).toBeTruthy();
      expect(en[k]).not.toMatch(/[–—]/);
      for (const loc of bundles) {
        const b = read(loc);
        expect(b[k], `${loc} ${k}`).toBeTruthy();
        expect(b[k], `${loc} ${k} untranslated`).not.toBe(en[k]);
        expect(b[k], `${loc} ${k} dash`).not.toMatch(/[–—]/);
        if (ph) expect(b[k], `${loc} ${k} placeholder`).toContain(ph);
      }
    }
    const de = read('de');
    for (const k of Object.keys(keys)) expect(de[k]).not.toMatch(/\bSie\b|\bIhre?\b/);
    expect(de['library.usbHiResSkipped']).toContain('übersprungen');
  });
});
