// A single music-library track on a key, saved with what the track IS on its
// media server and not only with the address it happened to have (#919).
//
// Some media servers hand out file addresses that only live until the server
// restarts: Windows' own media sharing builds them around ids it hands out
// anew every time. A key that stored only the address went silent after a PC
// restart while the music had not moved. With the server and the track's
// object id on the key, the speaker asks the server for the current address
// when the stored one stopped working.

import { SaveFolderPreset, SaveLibraryPreset } from './api.js';

// libraryTrackRef is the ref a library item is saved with: the media server's
// UDN, the item's object id, the folder it was listed in and its title. null
// when the item carries no object id (an older app's remembered play). udn may
// be empty: the speaker then finds the server by the key's server name.
export function libraryTrackRef(item, udn) {
  if (!item || !item.id) return null;
  return {
    server_udn: udn || '',
    object_id: String(item.id),
    parent_id: item.parentID ? String(item.parentID) : '',
    title: item.title || '',
  };
}

// saveLibraryTrackPreset stores a single library track on a key. With a ref
// the preset goes to the speaker as a whole (SaveFolderPreset is the plain
// "store this preset object" call) so the ref travels with it; an agent that
// does not know the field ignores it. Without one it is the old save.
export function saveLibraryTrackPreset(host, port, slot, track) {
  const name = track.name || '(track)';
  if (!track.ref) {
    return SaveLibraryPreset(host, port, slot, name, track.url, track.art || '', 0,
      track.source || '', track.durationSec || 0);
  }
  const payload = {
    name,
    stream_url: track.url,
    type: 'radio',
    source: track.source || '',
    track: track.ref,
  };
  if (track.art) payload.art = track.art;
  if (track.durationSec > 0) payload.duration_sec = track.durationSec;
  return SaveFolderPreset(host, port, slot, JSON.stringify(payload));
}
