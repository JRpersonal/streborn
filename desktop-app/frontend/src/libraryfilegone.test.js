// #919, #733, #726: a library preset whose music server answers but no longer
// has the music (a re-indexed Windows Media Player library, a MiniDLNA that
// replaced its files, a FRITZ!Box media server pointed at another USB stick).
// The agent refuses the press with {"code":"library-file-gone","server":...}
// and the desktop app passes that body through raw (friendlyError). These pin
// that the tile says the music is no longer on the server, the toast names the
// server and what to check, in both layouts (UI 2.0 decorates the same tiles
// and presses them through the same play()), and that the preset's own name
// cannot steer the tile to another message.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { libraryFileGoneName, libraryFileGoneNotice, libraryServerOffName } from './libraryserveroff.js';
import { srcOf } from './test/source.js';

const here = dirname(fileURLToPath(import.meta.url));
const langs = ['ar', 'de', 'en', 'es', 'fr', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant'];
const bundle = (l) => JSON.parse(readFileSync(join(here, 'i18n', 'bundles', `${l}.json`), 'utf8'));

const raw = JSON.stringify({
  code: 'library-file-gone',
  error: 'The music server PC: Media answers, but no longer has the music on this key.',
  gone: true, name: 'library-server-offline timeout refused', server: 'PC: Media', slot: 3,
});

describe('a library preset whose music server no longer has the music', () => {
  it('reads the server name out of the raw refusal', () => {
    expect(libraryFileGoneName(raw)).toBe('PC: Media');
    expect(libraryFileGoneName(new Error(raw))).toBe('PC: Media');
    expect(libraryFileGoneName('library-file-gone: no body')).toBe('');
    expect(libraryFileGoneName('spotify-not-logged-in: log in')).toBeNull();
    expect(libraryFileGoneName('box_not_ready')).toBeNull();
  });

  it('is matched by its code, not by words in the preset name', () => {
    const offWithName = JSON.stringify({ code: 'library-server-offline', name: 'library-file-gone', server: 'X' });
    expect(libraryFileGoneName(offWithName)).toBeNull();
    expect(libraryServerOffName(offWithName)).toBe('X');
    // And the other way round: the gone refusal is no server-off one.
    expect(libraryServerOffName(raw)).toBeNull();
  });

  it('names the server in the toast, in English and German', () => {
    const en = bundle('en');
    const de = bundle('de');
    const tEn = (k, p) => en[k].replace('{{name}}', p ? p.name : '');
    const tDe = (k, p) => de[k].replace('{{name}}', p ? p.name : '');
    expect(libraryFileGoneNotice('PC: Media', tEn))
      .toMatch(/^The music server PC: Media answers, but no longer has the music on this key\./);
    expect(libraryFileGoneNotice('PC: Media', tEn)).toContain('Manage libraries > Music');
    expect(libraryFileGoneNotice('PC: Media', tDe))
      .toMatch(/^Der Musikserver PC: Media antwortet, hat die Musik dieser Taste aber nicht mehr\./);
    expect(libraryFileGoneNotice('', tEn))
      .toMatch(/^The music server answers, but no longer has/);
  });

  it('labels the tile ahead of friendlyPlayError and shows the toast', () => {
    const play = srcOf('play');
    expect(play).toContain('const goneServer = libraryFileGoneName(errStr);');
    expect(play).toMatch(/state\.presetErrors\[slot\] = goneServer !== null\s*\? t\('play\.errMusicFileGone'\)\s*: friendlyPlayError\(/);
    expect(play).toContain('if (goneServer !== null) showToast(libraryFileGoneNotice(goneServer, t));');
  });

  it('carries the label and both sentences in every locale, without dashes', () => {
    for (const l of langs) {
      const b = bundle(l);
      expect(b['play.errMusicFileGone'], l).toBeTruthy();
      expect(b['play.errMusicFileGoneHelp'], l).toContain('{{name}}');
      expect(b['play.errMusicFileGoneHelpAny'], l).toBeTruthy();
      expect(b['play.errMusicFileGoneHelpAny'], l).not.toContain('{{name}}');
      for (const k of ['play.errMusicFileGone', 'play.errMusicFileGoneHelp', 'play.errMusicFileGoneHelpAny']) {
        expect(b[k], `${l} ${k}`).not.toMatch(/[–—]| - /);
      }
      if (l !== 'en') {
        expect(b['play.errMusicFileGoneHelp'], l).not.toBe(bundle('en')['play.errMusicFileGoneHelp']);
      }
    }
  });
});
