import { describe, it, expect, vi, beforeEach } from 'vitest';

vi.mock('./api.js', () => ({
  SaveFolderPreset: vi.fn(() => Promise.resolve()),
  SaveLibraryPreset: vi.fn(() => Promise.resolve()),
}));

import { SaveFolderPreset, SaveLibraryPreset } from './api.js';
import { libraryTrackRef, saveLibraryTrackPreset } from './librarytrackref.js';

// #919: a library key stores the track's object on its media server, so the
// speaker can ask for the current address when the server renumbered its files.
describe('library track ref', () => {
  beforeEach(() => {
    SaveFolderPreset.mockClear();
    SaveLibraryPreset.mockClear();
  });

  it('names the server, the object, its folder and its title', () => {
    expect(libraryTrackRef({ id: 7, parentID: 'c-1', title: 'Song' }, 'uuid:pc')).toEqual({
      server_udn: 'uuid:pc', object_id: '7', parent_id: 'c-1', title: 'Song',
    });
    expect(libraryTrackRef({ title: 'no id' }, 'uuid:pc')).toBeNull();
    expect(libraryTrackRef(null, 'uuid:pc')).toBeNull();
  });

  it('sends the whole preset with its ref', async () => {
    const ref = libraryTrackRef({ id: 'obj-7', parentID: 'c-1', title: 'Song' }, 'uuid:pc');
    await saveLibraryTrackPreset('box', 8888, 6, {
      name: 'Song', url: 'http://192.0.2.5:10243/WMPNSSv4/1/x.mp3', art: 'http://192.0.2.5/a.jpg',
      source: 'PC: Media', durationSec: 200, ref,
    });
    expect(SaveLibraryPreset).not.toHaveBeenCalled();
    const [host, port, slot, json] = SaveFolderPreset.mock.calls[0];
    expect([host, port, slot]).toEqual(['box', 8888, 6]);
    expect(JSON.parse(json)).toEqual({
      name: 'Song', stream_url: 'http://192.0.2.5:10243/WMPNSSv4/1/x.mp3', type: 'radio',
      source: 'PC: Media', art: 'http://192.0.2.5/a.jpg', duration_sec: 200, track: ref,
    });
  });

  it('keeps the old save without a ref', async () => {
    await saveLibraryTrackPreset('box', 8888, 2, {
      name: 'Song', url: 'http://192.0.2.5/x.mp3', source: 'NAS', durationSec: 10, ref: null,
    });
    expect(SaveFolderPreset).not.toHaveBeenCalled();
    expect(SaveLibraryPreset).toHaveBeenCalledWith('box', 8888, 2, 'Song', 'http://192.0.2.5/x.mp3', '', 0, 'NAS', 10);
  });
});
