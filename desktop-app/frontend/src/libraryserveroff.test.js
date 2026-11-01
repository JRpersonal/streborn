// #1339: a library preset pressed while its music server is switched off. The
// agent refuses the press with {"code":"library-server-offline","server":...}
// and the desktop app passes that body through raw (friendlyError). These pin
// that the tile says the music server is not answering, the toast names the
// server, and the preset's own name cannot steer the tile to another message.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { libraryServerOffName, libraryServerOffNotice } from './libraryserveroff.js';
import { srcOf, srcBetween } from './test/source.js';

const here = dirname(fileURLToPath(import.meta.url));
const langs = ['ar', 'de', 'en', 'es', 'fr', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant'];
const bundle = (l) => JSON.parse(readFileSync(join(here, 'i18n', 'bundles', `${l}.json`), 'utf8'));

const raw = JSON.stringify({
  code: 'library-server-offline',
  error: 'The music server MinimServer is not answering. Switch it on and try again.',
  name: 'Lookup timeout refused', offline: true, server: 'MinimServer', slot: 3,
});

function friendlyPlayError() {
  return new Function('t', 'deps', 'escalationForCount', 'noteNotReady', 'libraryServerOffName',
    `${srcOf('friendlyPlayError')}; return friendlyPlayError;`)(
    (k) => k,
    { stereoStaleForError: () => null },
    () => 'play.errNotReady',
    () => 1,
    libraryServerOffName,
  );
}

describe('a library preset whose music server is off', () => {
  it('reads the server name out of the raw refusal', () => {
    expect(libraryServerOffName(raw)).toBe('MinimServer');
    expect(libraryServerOffName(new Error(raw))).toBe('MinimServer');
    expect(libraryServerOffName('library-server-offline: no body')).toBe('');
    expect(libraryServerOffName('spotify-not-logged-in: log in')).toBeNull();
    expect(libraryServerOffName('box_not_ready')).toBeNull();
  });

  it('labels the tile ahead of every other check', () => {
    // The preset name in the body holds "lookup", "timeout" and "refused".
    expect(friendlyPlayError()(raw, '')).toBe('play.errMusicServerOff');
  });

  it('names the server in the toast, in English and German', () => {
    const en = bundle('en');
    const de = bundle('de');
    const tEn = (k, p) => en[k].replace('{{name}}', p ? p.name : '');
    const tDe = (k, p) => de[k].replace('{{name}}', p ? p.name : '');
    expect(libraryServerOffNotice('MinimServer', tEn))
      .toBe('The music server MinimServer is not answering. Switch it on and try again.');
    expect(libraryServerOffNotice('MinimServer', tDe))
      .toBe('Der Musikserver MinimServer antwortet nicht. Schalte ihn ein und versuch es noch einmal.');
    expect(libraryServerOffNotice('', tEn))
      .toBe('The music server is not answering. Switch it on and try again.');
  });

  it('shows the toast on the refusal', () => {
    expect(srcBetween('const offServer = libraryServerOffName(errStr);', '\n  }'))
      .toContain('showToast(libraryServerOffNotice(offServer, t))');
  });

  it('carries the label and both sentences in every locale', () => {
    for (const l of langs) {
      const b = bundle(l);
      expect(b['play.errMusicServerOff'], l).toBeTruthy();
      expect(b['play.errMusicServerOffHelp'], l).toContain('{{name}}');
      expect(b['play.errMusicServerOffHelpAny'], l).toBeTruthy();
      expect(b['play.errMusicServerOffHelpAny'], l).not.toContain('{{name}}');
    }
  });
});
