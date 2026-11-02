// pandorafav.test.js: a Pandora station as a favourite (#1663).
import { describe, it, expect } from 'vitest';
import { isPandoraFav, pandoraFavFromRow, pandoraFavKey, pandoraRowFromFav } from './pandorafav.js';

const row = { name: 'Jazz Radio', location: '111', type: 'stationurl', art: 'https://img.example.com/111.jpg', presetable: true };

describe('pandorafav', () => {
  it('stars a Pandora row under the key of its Recently-played card', () => {
    const fav = pandoraFavFromRow(row);
    expect(fav).toEqual({
      stationuuid: 'pandora:111', name: 'Jazz Radio', url: '', url_resolved: '',
      favicon: 'https://img.example.com/111.jpg', source: 'pandora', location: '111', itemType: 'stationurl',
    });
    expect(pandoraFavKey(' 111 ')).toBe('pandora:111');
    expect(isPandoraFav(fav)).toBe(true);
  });

  it('turns the favourite back into the row the Pandora list plays', () => {
    expect(pandoraRowFromFav(pandoraFavFromRow(row))).toEqual(row);
    expect(pandoraRowFromFav({ source: 'pandora', name: 'X', location: '9' }).type).toBe('stationurl');
  });

  it('tells a Pandora favourite from a station', () => {
    expect(isPandoraFav({ stationuuid: 'u', url: 'http://s/x' })).toBe(false);
    expect(isPandoraFav({ source: 'pandora' })).toBe(false);
    expect(isPandoraFav({ source: 'upnp', location: '1' })).toBe(false);
    expect(isPandoraFav(null)).toBe(false);
  });
});
