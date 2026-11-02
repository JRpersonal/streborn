// librarysort.js: the Library's sort order (#1257).
//
// Folders and tracks come in the order the media server sends them, and a
// server on Linux puts a name starting with "_" at the bottom, which is where
// nobody looks for the folder they named that way to keep it on top. "A to Z"
// puts names starting with "_" first, then those starting with a digit, then
// the rest, case and accents ignored and numbers compared as numbers ("2"
// before "10"). An album keeps its track order and a playlist the order its
// owner gave it: A to Z is for folders and loose files.
//
// The same rule lives on the speaker (internal/webui/librarysort.go, used when
// it walks a folder of folders) and on the phone page; librarysort.test.js and
// phonelibrarysort.test.js pin the case list the Go test pins.
//
// The choice is this viewer's: remembered in this app, not on a speaker.

import { isPlaylistClass } from './trackorder.js';

export const SORT_SERVER = 'server';
export const SORT_AZ = 'az';
export const LIB_SORT_KEY = 'str.library.sort';

// loadLibrarySort is the remembered order, server order when none is stored or
// the storage is blocked.
export function loadLibrarySort() {
  try {
    return localStorage.getItem(LIB_SORT_KEY) === SORT_AZ ? SORT_AZ : SORT_SERVER;
  } catch {
    return SORT_SERVER;
  }
}

export function saveLibrarySort(mode) {
  try {
    localStorage.setItem(LIB_SORT_KEY, mode === SORT_AZ ? SORT_AZ : SORT_SERVER);
  } catch { /* blocked storage: the choice lasts until the app closes */ }
}

// Letters that decomposition leaves alone, folded by hand.
const EXTRA_FOLD = { ß: 'ss', æ: 'ae', ø: 'o', ł: 'l', ı: 'i' };

// foldName is a name as A to Z compares it: trimmed, lower case, accents off.
export function foldName(name) {
  let s = String(name || '').trim().toLowerCase();
  try {
    s = s.normalize('NFD').replace(/[̀-ͯ]/g, '');
  } catch { /* no normalize: compare with the accents */ }
  return s.replace(/[ßæøłı]/g, (c) => EXTRA_FOLD[c]);
}

// nameRank is the group a name sorts in: "_" first, a digit next, the rest.
function nameRank(name) {
  const c = String(name || '').trim().charAt(0);
  if (c === '_') return 0;
  if (c >= '0' && c <= '9') return 1;
  return 2;
}

const isDigit = (c) => c >= '0' && c <= '9';

// naturalCompare compares runs of digits by their number and everything else
// by code point, so "track 2" comes before "track 10".
function naturalCompare(a, b) {
  const x = Array.from(a);
  const y = Array.from(b);
  let i = 0;
  let j = 0;
  while (i < x.length && j < y.length) {
    if (isDigit(x[i]) && isDigit(y[j])) {
      const si = i;
      while (i < x.length && isDigit(x[i])) i++;
      const sj = j;
      while (j < y.length && isDigit(y[j])) j++;
      const na = x.slice(si, i).join('').replace(/^0+/, '');
      const nb = y.slice(sj, j).join('').replace(/^0+/, '');
      if (na.length !== nb.length) return na.length - nb.length;
      if (na !== nb) return na < nb ? -1 : 1;
      continue;
    }
    if (x[i] !== y[j]) return x[i].codePointAt(0) - y[j].codePointAt(0);
    i++;
    j++;
  }
  return (x.length - i) - (y.length - j);
}

// compareNames orders two names A to Z.
export function compareNames(a, b) {
  const r = nameRank(a) - nameRank(b);
  if (r) return r;
  return naturalCompare(foldName(a), foldName(b));
}

// sortBy returns a sorted copy, stable, the input left alone.
function sortBy(list, title) {
  return list
    .map((v, i) => ({ v, i }))
    .sort((a, b) => compareNames(title(a.v), title(b.v)) || (a.i - b.i))
    .map((x) => x.v);
}

// sortFolders returns the folders in the chosen order.
export function sortFolders(containers, mode) {
  if (mode !== SORT_AZ || !Array.isArray(containers) || containers.length < 2) return containers || [];
  return sortBy(containers, (c) => c && c.title);
}

// keepsTrackOrder says whether a folder's tracks keep their order under A to
// Z: an album (by its class, or because every track carries its own number)
// and a playlist.
export function keepsTrackOrder(items, containerClass) {
  const cls = String(containerClass || '').trim().toLowerCase();
  if (cls.startsWith('object.container.album') || isPlaylistClass(cls)) return true;
  if (!Array.isArray(items) || items.length < 2) return true;
  const seen = new Set();
  for (const it of items) {
    const n = Number(it && it.trackNumber) || 0;
    if (n <= 0) return false;
    const k = `${Number(it.discNumber) || 0}:${n}`;
    if (seen.has(k)) return false;
    seen.add(k);
  }
  return true;
}

// sortTracks returns a folder's tracks in the chosen order.
export function sortTracks(items, mode, containerClass) {
  if (mode !== SORT_AZ || keepsTrackOrder(items, containerClass)) return items || [];
  return sortBy(items, (it) => it && it.title);
}
