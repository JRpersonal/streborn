// Universal Media Server converts songs for receivers it has no profile for,
// and the speaker then loads the song and stays silent with a decoder error.
// The library shows the setting that stops it, but only for a UMS server.
import { describe, it, expect, vi } from 'vitest';

vi.mock('./api.js', () => ({}));

const { isUniversalMediaServer, libraryUMSNote } = await import('./views/library.js');
const { setLocale } = await import('./i18n/index.js');

describe('Universal Media Server hint', () => {
  it('recognises UMS by manufacturer, model or name', () => {
    expect(isUniversalMediaServer({ manufacturer: 'Universal Media Server' })).toBe(true);
    expect(isUniversalMediaServer({ modelName: 'UMS' })).toBe(true);
    expect(isUniversalMediaServer({ friendlyName: 'Universal Media Server [vm]' })).toBe(true);
  });

  it('leaves every other server alone', () => {
    expect(isUniversalMediaServer({ manufacturer: 'AVM', modelName: 'FRITZ!Box 7590' })).toBe(false);
    expect(isUniversalMediaServer({ modelName: 'Windows Media Player Sharing' })).toBe(false);
    expect(isUniversalMediaServer(null)).toBe(false);
    expect(libraryUMSNote({ manufacturer: 'Synology' })).toBe('');
  });

  it('names the setting in the note', async () => {
    await setLocale('en');
    const html = libraryUMSNote({ manufacturer: 'Universal Media Server' });
    expect(html).toContain('<details class="library-ums-note">');
    expect(html).toContain('disable_transcode_for_extensions = mp3,flac');
  });
});
