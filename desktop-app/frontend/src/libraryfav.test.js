// A music-library album, playlist or track starred from Recently played was
// saved as a radio station: played back from Favourites it was filed under
// Radio with no cover, a folder played its first track only and a file could
// loop in the stream proxy (#1605). These pin that the favourite keeps its
// library identity and that the Favourites play button replays it the way its
// Recently-played card does.
import { describe, it, expect } from 'vitest';
import { isFolderKey, isLibraryFav, libraryFavFields, libraryFavCard } from './libraryfav.js';
import { srcOf } from './test/source.js';

const folderCard = {
  source: 'upnp', cardKey: 'queue:uuid:AAAA-BBBB:64$0', name: 'Jazz',
  art: 'http://192.0.2.10:9790/cover.jpg', url: 'http://192.0.2.10:9790/1.flac', mime: 'audio/flac', durationSec: 0,
};
const trackCard = {
  source: 'upnp', cardKey: 'http://192.0.2.10:9790/2.mp3', name: 'Song',
  art: 'http://192.0.2.10:9790/c.jpg', url: 'http://192.0.2.10:9790/2.mp3', mime: 'audio/mpeg', durationSec: 187.4,
};

describe('library favourites (#1605)', () => {
  it('marks a library card, and only a library card', () => {
    expect(libraryFavFields(trackCard)).toEqual({ source: 'upnp', mime: 'audio/mpeg', durationSec: 187 });
    expect(libraryFavFields(folderCard)).toEqual({ source: 'upnp', mime: 'audio/flac' });
    expect(libraryFavFields({ source: 'radio', cardKey: 'http://s/x', mime: '' })).toEqual({});
    expect(libraryFavFields(null)).toEqual({});
  });

  it('recognises a library favourite, and a folder starred before the mark', () => {
    expect(isLibraryFav({ stationuuid: trackCard.cardKey, source: 'upnp' })).toBe(true);
    expect(isLibraryFav({ stationuuid: 'queue:uuid:AAAA:64$0', name: 'Old album' })).toBe(true);
    expect(isLibraryFav({ stationuuid: 'folder:srv/64$0' })).toBe(true);
    // A radio station, and an old single-track favourite nothing marks.
    expect(isLibraryFav({ stationuuid: '9617a958-0601-11e8-ae97-52543be04c81', url: 'http://s/x' })).toBe(false);
    expect(isLibraryFav({ stationuuid: 'http://192.0.2.10:9790/2.mp3' })).toBe(false);
    expect(isFolderKey('http://example/queue:x')).toBe(false);
  });

  it('turns the favourite back into the card the replay plays', () => {
    const fav = { stationuuid: folderCard.cardKey, name: 'Jazz', url: folderCard.url, url_resolved: folderCard.url,
      favicon: folderCard.art, source: 'upnp', mime: 'audio/flac' };
    expect(libraryFavCard(fav)).toEqual({
      source: 'upnp', cardKey: folderCard.cardKey, name: 'Jazz', art: folderCard.art,
      url: folderCard.url, mime: 'audio/flac', durationSec: 0,
    });
  });

  it('keeps the mark through the favourite store', () => {
    const fm = srcOf('favMinimal');
    expect(fm).toContain("source: s.source === 'upnp'");
    expect(fm).toContain('mime: s.mime');
    expect(fm).toContain('durationSec:');
  });

  it('stars a Recently-played card with its library identity', () => {
    expect(srcOf('wireCard')).toContain('deps.toggleFav({ ...cardStation(c), ...libraryFavFields(c) })');
  });

  it('plays a library favourite through the library replay, not as a station', () => {
    const rs = srcOf('renderSearchResults');
    const lib = rs.indexOf('isLibraryFav(s)) {');
    const station = rs.indexOf('await playStation(s)');
    expect(lib).toBeGreaterThan(-1);
    expect(rs).toContain('replayLibraryCard(libraryFavCard(s), effectivePlayTarget())');
    expect(lib).toBeLessThan(station);
  });

  it('replays a folder as a whole queue before the single track', () => {
    const r = srcOf('replayLibraryCard');
    expect(r.indexOf('replayFolderCard(c, box)')).toBeGreaterThan(-1);
    expect(r.indexOf('replayFolderCard(c, box)')).toBeLessThan(r.indexOf('replayLibraryTrack(c, box, art)'));
  });
});
