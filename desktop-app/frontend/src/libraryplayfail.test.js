import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { isMacMetadataItem, withoutMacMetadata, speakerRefusingPushes } from './libraryplayfail.js';

// #1634: a FRITZ!Box listed a Mac's "._" companion files as tracks, they head
// every alphabetical list, and each click ended in a format guess.
describe('Mac "._" companion files', () => {
  it('recognises a companion by its title or by its file name', () => {
    expect(isMacMetadataItem({ title: '._01 - ANNE KAFFEEKANNE', streamURL: 'http://192.0.2.1:49200/AUDIO/DLNA-1-0/x.mp3' })).toBe(true);
    expect(isMacMetadataItem({ title: 'Anne Kaffeekanne', streamURL: 'http://192.0.2.1:49200/AUDIO/DLNA-1-0/._01%20-%20ANNE%20KAFFEEKANNE.mp3' })).toBe(true);
    expect(isMacMetadataItem({ title: 'x', streamURL: 'http://192.0.2.1:49200/AUDIO/DLNA-1-0/._(Disc%202)%2008%20-%20Jessica.mp3' })).toBe(true);
  });

  it('leaves real tracks alone, dotted names and odd addresses included', () => {
    expect(isMacMetadataItem({ title: '01 - ZEIT, DASS SICH WAS DREHT [Explicit]', streamURL: 'http://192.0.2.1:49200/AUDIO/DLNA-1-0/01%20-%20ZEIT,%20DASS%20SICH%20WAS%20DREHT%20%5bExplicit%5d.mp3' })).toBe(false);
    expect(isMacMetadataItem({ title: '.38 Special - Caught Up in You', streamURL: 'http://192.0.2.1/.38%20Special.mp3' })).toBe(false);
    expect(isMacMetadataItem({ title: 'Song', streamURL: 'http://192.0.2.1/dir._x/song.mp3' })).toBe(false);
    expect(isMacMetadataItem({ title: 'Song', streamURL: 'http://192.0.2.1/%E0%A4%A.mp3' })).toBe(false);
    expect(isMacMetadataItem({ title: 'Song' })).toBe(false);
    expect(isMacMetadataItem(null)).toBe(false);
  });

  it('drops only the companions from a page', () => {
    const page = [
      { title: '._01 - Baila Conmigo' },
      { title: '01 - Baila Conmigo' },
      { title: '._01 - Auf dem Weg (Linke Spur) [Explicit]' },
    ];
    expect(withoutMacMetadata(page).map((it) => it.title)).toEqual(['01 - Baila Conmigo']);
    expect(withoutMacMetadata(undefined)).toEqual([]);
  });

  it('is applied where a folder page is collected, and paging still counts the server page', () => {
    const src = readFileSync(new URL('./views/library.js', import.meta.url), 'utf8');
    expect(src).toContain('acc.items = acc.items.concat(withoutMacMetadata(page.items));');
    expect(src).toContain('const got = (page.containers || []).length + (page.items || []).length;');
  });
});

// The same report: the speaker refused every push, and each track was blamed on
// its format. With the restart banner up for that speaker, the toast says what
// the banner says.
describe('a track on a speaker that refuses STR', () => {
  it('follows the live box flags, found by host', () => {
    const boxes = [{ host: '192.0.2.10', recallRefusal: true }, { host: '192.0.2.11' }];
    expect(speakerRefusingPushes(boxes, { host: '192.0.2.10' })).toBe(true);
    expect(speakerRefusingPushes(boxes, { host: '192.0.2.11' })).toBe(false);
    expect(speakerRefusingPushes([], { host: '192.0.2.12', storm1036: true })).toBe(true);
  });

  it('leaves a wedged speaker to its own banner, and no box to nothing', () => {
    expect(speakerRefusingPushes([{ host: 'a', recallRefusal: true, boxHealth: 'wedged' }], { host: 'a' })).toBe(false);
    expect(speakerRefusingPushes([], null)).toBe(false);
  });

  it('is asked before the format guess', () => {
    const src = readFileSync(new URL('./views/library.js', import.meta.url), 'utf8');
    const verify = src.slice(src.indexOf('async function verifyLibraryPlayback'));
    const refusal = verify.indexOf('speakerRefusingPushes(state.boxes, box)');
    const guess = verify.indexOf("t('library.formatMaybeUnsupported'");
    expect(refusal).toBeGreaterThan(-1);
    expect(refusal).toBeLessThan(guess);
  });
});
