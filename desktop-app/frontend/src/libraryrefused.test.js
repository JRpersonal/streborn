// A media server that refuses this PC (QNAP field case): it answered the PC's
// device description request with a SOAP fault while the speaker was served
// normally, so it could not be listed, and the only entry left for that NAS was
// its admin device, which cannot be browsed. The Library has to say why the
// server is missing and where the fix is.
import { describe, it, expect, vi, beforeAll } from 'vitest';
import { readFileSync } from 'node:fs';

vi.mock('./api.js', () => ({
  ProbeTrackDelivery: vi.fn(),
  ListMediaServers: vi.fn(),
  LibraryRefusedServers: vi.fn(),
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

const { libraryRefusedNote } = await import('./views/library.js');
const { setLocale } = await import('./i18n/index.js');
const read = (loc) => JSON.parse(
  readFileSync(new URL(`./i18n/bundles/${loc}.json`, import.meta.url), 'utf8'));
const en = read('en');

describe('libraryRefusedNote', () => {
  beforeAll(() => { setLocale('en'); });

  it('names every refusing server by address', () => {
    const html = libraryRefusedNote([
      { address: '192.0.2.40:8200', detail: 'the server answered UPnP error 501 (action failed)' },
    ]);
    expect(html).toContain('192.0.2.40:8200');
    expect(html).toContain('refuses this computer');
  });

  it('renders nothing when no server refused, or for junk input', () => {
    expect(libraryRefusedNote([])).toBe('');
    expect(libraryRefusedNote(null)).toBe('');
    expect(libraryRefusedNote([{ detail: 'no address' }])).toBe('');
  });

  it('escapes what the server sent', () => {
    const html = libraryRefusedNote([{ address: '<b>x</b>', detail: '"><img>' }]);
    expect(html).not.toContain('<b>x</b>');
    expect(html).not.toContain('"><img>');
  });
});

describe('the refusal string', () => {
  const bundles = ['de', 'fr', 'es', 'nl', 'pl', 'tr', 'uk', 'lt', 'lv', 'ar', 'ja', 'zh-Hant'];
  const k = 'library.serverRefusesPC';

  it('is present and translated in every bundle, placeholder intact', () => {
    expect(en[k]).toContain('{{address}}');
    for (const loc of bundles) {
      const b = read(loc);
      expect(b[k], `${loc}.json`).toBeTruthy();
      expect(b[k], `${loc}.json untranslated`).not.toBe(en[k]);
      expect(b[k], `${loc}.json placeholder`).toContain('{{address}}');
    }
  });
});
