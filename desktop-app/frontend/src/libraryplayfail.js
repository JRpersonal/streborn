// Two library traps from one report (#1634).
//
// 1. A Mac writes a "._name" companion file next to every file it copies onto
//    a stick or disk formatted for Windows (FAT, exFAT). It holds Finder
//    metadata, 4 KB of it, and no audio. A media server that indexes by file
//    extension (a FRITZ!Box does) lists "._01 - Song.mp3" as a track of its
//    own, and since "." sorts first those companions head every alphabetical
//    track list. Clicking one can only fail, and the app then guessed at a file
//    format. They are hidden from the list, from "Play all" and from a saved
//    folder.
//
// 2. A speaker that refuses what STR pushes it (the restart banner's state)
//    drops every track without trying it. Telling that owner, track after
//    track, that the file format may be wrong sends them looking at their
//    music; the banner's own sentence is the answer then.

import { stormBannerBoxes } from './utils.js';

// isMacMetadataItem reports whether a library item is such a "._" companion:
// its title or the file name in its address starts with "._".
export function isMacMetadataItem(item) {
  if (!item) return false;
  if (String(item.title || '').startsWith('._')) return true;
  const url = String(item.streamURL || '');
  if (!url) return false;
  let path = url;
  try {
    path = new URL(url).pathname;
  } catch {
    // not an absolute URL: look at the raw text
  }
  const last = path.split('/').pop() || '';
  let name = last;
  try {
    name = decodeURIComponent(last);
  } catch {
    // a malformed escape: the raw segment is what there is
  }
  return name.startsWith('._');
}

// withoutMacMetadata drops the "._" companions from a list of library items.
export function withoutMacMetadata(items) {
  return (items || []).filter((it) => !isMacMetadataItem(it));
}

// speakerRefusingPushes reports whether the speaker a play went to is in the
// state the restart banner is about (Bose 1036 storm or the silent refusal),
// looked up in the live box list, which carries those flags, by host.
export function speakerRefusingPushes(boxes, box) {
  if (!box) return false;
  const live = (boxes || []).find((b) => b && b.host === box.host) || box;
  return stormBannerBoxes([live]).length > 0;
}
