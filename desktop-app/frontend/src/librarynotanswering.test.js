// A media server the Library list only remembers, and what happens when it is
// added to the speakers anyway (#770).
//
// #733: the reporter picked a remembered WD NAS out of the Library list while
// the NAS was switched off and added it to all three of his speakers. Every
// browse of it failed from then on, and nothing in the app had said the entry
// came from memory instead of from a server that had just answered. Three
// releases were spent looking for the cause inside STR.
import { describe, it, expect, vi, beforeEach, beforeAll, afterEach } from 'vitest';
import { readFileSync } from 'node:fs';

// The view module is imported for real, so the picker label and the add-to-
// speakers path are the ones that ship. Only the two module boundaries it talks
// to are replaced: the Wails bindings and the modal.
vi.mock('./api.js', () => ({
  ProbeTrackDelivery: vi.fn(),
  ListMediaServers: vi.fn(),
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
vi.mock('./utils.js', async (importOriginal) => ({
  ...(await importOriginal()),
  confirmWarn: vi.fn(),
}));

const { EnableBoxMediaServer } = await import('./api.js');
const { confirmWarn } = await import('./utils.js');
const { libraryServerLabel, libraryPutServerOnSpeakers, serverNotAnswering } =
  await import('./views/library.js');
const { state } = await import('./state.js');
const { setLocale } = await import('./i18n/index.js');
const en = JSON.parse(readFileSync(new URL('./i18n/bundles/en.json', import.meta.url), 'utf8'));

const live = {
  udn: 'uuid:fritz', friendlyName: 'FRITZ!Box 7590', modelName: 'FRITZ!Box',
  address: '192.0.2.1:49000', manual: false, notAnswering: false,
};
const remembered = {
  udn: 'uuid:nas', friendlyName: 'WD My Cloud', modelName: 'My Cloud',
  address: '192.0.2.20:9000', manual: true, notAnswering: true,
};
const speakers = [
  { host: '192.0.2.10', port: 8090, deviceID: 'AABBCCDDEEFF', friendlyName: 'Living room' },
  { host: '192.0.2.11', port: 8090, deviceID: 'AABBCCDDEE01', friendlyName: 'Kitchen' },
];

describe('the Library server picker', () => {
  beforeAll(() => { setLocale('en'); });

  it('marks an entry nothing answered for, and leaves a live one plain', () => {
    expect(libraryServerLabel(live)).toBe('FRITZ!Box 7590 (FRITZ!Box)');
    const mark = en['library.serverNotAnswering'];
    expect(mark).toBeTruthy();
    expect(libraryServerLabel(remembered)).toContain('WD My Cloud');
    expect(libraryServerLabel(remembered)).toContain(mark);
    expect(serverNotAnswering(remembered)).toBe(true);
    expect(serverNotAnswering(live)).toBe(false);
  });

  it('falls back to the address when a server advertises no name', () => {
    expect(libraryServerLabel({ udn: 'uuid:x', address: '192.0.2.30:8200' }))
      .toBe('192.0.2.30:8200');
  });
});

describe('adding a server to the speakers', () => {
  beforeEach(() => {
    globalThis.document = { getElementById: () => null };
    state.boxes = speakers;
    EnableBoxMediaServer.mockReset();
    EnableBoxMediaServer.mockResolvedValue(undefined);
    confirmWarn.mockReset();
  });
  afterEach(() => {
    delete globalThis.document;
    state.boxes = [];
  });

  it('asks first when the server is not answering, and touches no speaker on a cancel', async () => {
    confirmWarn.mockResolvedValue(false);
    await libraryPutServerOnSpeakers({}, remembered);
    expect(confirmWarn).toHaveBeenCalledTimes(1);
    // The prompt has to name the server, which is what the reporter was missing.
    expect(String(confirmWarn.mock.calls[0][1])).toContain('WD My Cloud');
    expect(EnableBoxMediaServer).not.toHaveBeenCalled();
  });

  it('goes ahead on every speaker when the user confirms, so a sleeping NAS is not blocked', async () => {
    confirmWarn.mockResolvedValue(true);
    await libraryPutServerOnSpeakers({}, remembered);
    expect(confirmWarn).toHaveBeenCalledTimes(1);
    expect(EnableBoxMediaServer).toHaveBeenCalledTimes(2);
    // The id the speakers use carries no "uuid:" prefix.
    expect(EnableBoxMediaServer.mock.calls[0][2]).toBe('nas');
  });

  it('asks nothing at all for a server that answered this scan', async () => {
    await libraryPutServerOnSpeakers({}, live);
    expect(confirmWarn).not.toHaveBeenCalled();
    expect(EnableBoxMediaServer).toHaveBeenCalledTimes(2);
  });
});

describe('the not-answering strings', () => {
  const bundles = ['en', 'de', 'fr', 'es', 'nl', 'pl', 'tr', 'uk', 'lt', 'lv', 'ar', 'ja', 'zh-Hant'];
  const read = (loc) => JSON.parse(
    readFileSync(new URL(`./i18n/bundles/${loc}.json`, import.meta.url), 'utf8'));
  const keys = [
    'library.serverNotAnswering', 'library.serverNotAnsweringNote',
    'library.notAnsweringWarnTitle', 'library.notAnsweringWarnBody',
  ];

  it('are present and translated in every bundle, placeholders intact', () => {
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
