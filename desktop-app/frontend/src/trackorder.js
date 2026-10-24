// trackorder.js: album track order for a Library folder (#1487), the frontend
// mirror of dlna.AlbumTrackOrder in dlna/trackorder.go. Keep the two in step.
//
// The Go side already orders every folder that fits in one Browse page. Only a
// folder longer than one page is ordered here, once every page is in, because
// the Go side only ever sees one page of it. The rule is the same:
//   - every track has a track number above 0 and no two share the same
//     (disc, track) pair, or the server order stays;
//   - never inside a playlist container: its order is the user's;
//   - stable, and the input array is never modified.
// Folders are a separate list in the page and are not touched.

// isPlaylistClass reports whether a upnp:class names a playlist container.
export function isPlaylistClass(cls) {
  return String(cls || '').trim().toLowerCase().startsWith('object.container.playlistcontainer');
}

// albumTrackOrder returns the items in (disc, track) order when they look like
// an album, otherwise the very same array.
export function albumTrackOrder(items) {
  if (!Array.isArray(items) || items.length < 2) return items;
  const seen = new Set();
  for (const it of items) {
    const n = Number(it && it.trackNumber) || 0;
    if (n <= 0) return items;
    const key = `${Number(it.discNumber) || 0}:${n}`;
    if (seen.has(key)) return items;
    seen.add(key);
  }
  const disc = (it) => Number(it.discNumber) || 0;
  // Array.prototype.sort is stable (ES2019); the index tie-break makes that
  // explicit anyway.
  return items
    .map((it, i) => ({ it, i }))
    .sort((a, b) => (disc(a.it) - disc(b.it))
      || (Number(a.it.trackNumber) - Number(b.it.trackNumber))
      || (a.i - b.i))
    .map((x) => x.it);
}

// orderLongFolder applies albumTrackOrder to a folder the frontend assembled
// from several pages. containerClass is the folder's own class as the first
// page reported it; an unknown class keeps the server order, because
// re-ordering a playlist is worse than an alphabetical album.
export function orderLongFolder(items, containerClass) {
  if (!containerClass || isPlaylistClass(containerClass)) return items;
  return albumTrackOrder(items);
}
