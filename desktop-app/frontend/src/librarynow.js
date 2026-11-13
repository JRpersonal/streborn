// librarynow.js: which music-library track is playing right now, for the
// player line and for the marker on the Library's track rows (discussion
// #1874).
//
// While a folder plays as a queue, the speaker's agent is the one that hands
// the speaker each track, so its queue knows the running track the moment it
// changes. The speaker's own now_playing does not: it can name the previous
// track for several seconds after the next one is already audible. The line
// used to print that lagging title with no artist and album at all (those were
// only on the queue line, which the UI 2.0 player folds away), and the row
// marker was the one-off answer to a single click, so it stayed on the track
// that was clicked while the folder moved on, and was gone after the Library
// was drawn again.
//
// Pure: plain data in, plain data out, so vitest reads it without a DOM.

import { isPlaying } from './boxes.js';

// Locations that belong to something other than a library queue: a preset
// station through the stream proxy, STR's Spotify stream, a station
// descriptor, or the speaker's own Spotify receiver. A queue track is a media
// server's file, or one wrapped in /stream/raw for an HTTPS server.
const OTHER_SOURCE = [
  /\/stream\/\d+(?:[/?#]|$)/,
  /\/spotify\//,
  /\/station\?data=/,
  /\/playback\/container\//,
];

// queueNowItem returns the queue item the agent says plays now, or null when
// no queue runs or the speaker is busy with something else.
//
//   queue       state.queue, the agent's GET /api/queue reply
//   location    the speaker's now-playing location
//   optimistic  the app has just started something itself (optimisticUntil):
//               the queue reply on hand may still describe the folder that
//               play replaced, so it must not name the line yet
export function queueNowItem(queue, { location = '', optimistic = false } = {}) {
  if (optimistic) return null;
  if (!queue || !queue.active || !Array.isArray(queue.items)) return null;
  const it = (typeof queue.pos === 'number' && queue.pos >= 0) ? queue.items[queue.pos] : null;
  if (!it || typeof it !== 'object') return null;
  const loc = String(location || '');
  if (OTHER_SOURCE.some((re) => re.test(loc))) return null;
  return it;
}

// queueNowURL is the media server URL of the track the queue is on, as the
// agent reports it, or '' from an agent older than that field.
export function queueNowURL(queue) {
  return queue && typeof queue.url === 'string' ? queue.url : '';
}

// nowLine joins the title and the "artist — album" meta for the player line,
// in the shape a single track's line already has.
export function nowLine(title, meta) {
  if (title && meta) return `${title} · ${meta}`;
  return title || meta || '';
}

// playingLibraryURLs lists the URLs a Library row may match to be the track
// that plays: the queue's running track when the agent names it, otherwise the
// speaker's location (and the file inside a /stream/raw wrapper). Empty while
// the speaker plays nothing.
//
//   st.queue, st.location, st.decodedLocation, st.playState, st.optimistic
export function playingLibraryURLs({ queue = null, location = '', decodedLocation = '', playState = '', optimistic = false } = {}) {
  if (!isPlaying(playState)) return [];
  if (queueNowItem(queue, { location, optimistic })) {
    const url = queueNowURL(queue);
    if (url) return [url];
  }
  const out = [];
  if (location) out.push(location);
  if (decodedLocation && decodedLocation !== location) out.push(decodedLocation);
  return out;
}

// libraryRowStatus is the line under a Library track row. stored is the
// row's own feedback (views/library.js setLibStatus): "Starting on X" while a
// click waits for the speaker, "On key 3 of X" after a save, a failure when a
// track did not start. playing says whether the row is the track that plays
// now.
//
// The answer to a press comes first: a click still waiting ("Playing on X" is
// said only once the speaker plays it) or a save that just landed. Then the
// live marker, which follows the running track. A stored failure stays only
// while that row does not play: a track that failed once and plays now is
// playing.
export function libraryRowStatus(stored, playing, speaker, t) {
  if (stored && (stored.tone === 'busy' || stored.tone === 'ok')) return stored;
  if (playing) return { tone: 'ok', text: t('ui2.library.playingOn', { name: speaker }) };
  return stored || null;
}
