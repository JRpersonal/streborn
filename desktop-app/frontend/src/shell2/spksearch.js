// shell2/spksearch.js: finding a speaker in the UI 2.0 sidebar list.
//
// With many speakers the "other speakers" part of the sidebar is a long
// scroll box, and the speaker you are looking for is often known by what it
// plays ("the one on Jazz24") or by its model, not only by its name. Once the
// list has more than SEARCH_AFTER_ROWS rows a search field shows above it and
// narrows the other speakers to the ones that match. The pinned group stays
// as it is (docs/UI-2.0.md: "the playing group stays pinned on top"): it is
// what the player controls, so a search must never hide it.
//
// Pure: model in, model and markup out. What a speaker plays comes in through
// a lookup (mount.js builds it from the every-speaker poll), so vitest covers
// all of it without a DOM or a speaker.

import { t } from '../i18n/index.js';
import { decodeXmlEntities, escapeAttr, escapeHtml } from '../utils.js';

// The field shows once the list has MORE rows than this. Eight rows fit the
// sidebar of the default window; beyond that the list scrolls and finding a
// name by eye starts to take a while.
export const SEARCH_AFTER_ROWS = 8;

const rowsOf = (model) => [...((model && model.pinned) || []), ...((model && model.others) || [])];

// searchShown says whether the list is long enough for the field.
export function searchShown(model) {
  return rowsOf(model).length > SEARCH_AFTER_ROWS;
}

// foldText is the form both sides are compared in: lower case, accents off
// ("Küche" is found by "kuche" too), the underscores of a raw source enum
// ("LOCAL_INTERNET_RADIO") and runs of spaces as one space.
export function foldText(s) {
  return String(s == null ? '' : s)
    .normalize('NFD')
    .replace(/[̀-ͯ]/g, '')
    .toLowerCase()
    .replace(/[_\s]+/g, ' ')
    .trim();
}

// Sources that mean nothing plays: a speaker asleep or without a source is
// not found by "standby".
const IDLE_SOURCES = new Set(['', 'STANDBY', 'INVALID_SOURCE']);

// nowLineOf reads what a speaker plays from its /now_playing answer: the raw
// source and the station (or, without one, the item's name), the same reading
// the status poll does for the speaker you control (views/music/status.js).
// null while it sleeps or plays nothing.
export function nowLineOf(xml) {
  if (typeof xml !== 'string' || !xml) return null;
  const source = (xml.match(/nowPlaying[^>]*source="([^"]*)"/) || [])[1] || '';
  if (IDLE_SOURCES.has(source.toUpperCase())) return null;
  const station = decodeXmlEntities((xml.match(/<stationName>([^<]+)<\/stationName>/) || [])[1]
    || (xml.match(/<itemName>([^<]+)<\/itemName>/) || [])[1] || '');
  return { source, station: String(station || '').trim() };
}

// rowWords is everything a row can be found by: its name (a pair's "(stereo)"
// name included), its model, and per speaker of the row the words terms(host)
// returns (the source, its friendly name, the station).
function rowWords(row, terms) {
  const words = [row.name, row.model];
  if (typeof terms === 'function') {
    for (const h of row.hosts || [row.host]) {
      let more = [];
      try { more = terms(h) || []; } catch (_) { more = []; }
      words.push(...more);
    }
  }
  return foldText(words.filter(Boolean).join(' \u0001 '));
}

// rowMatches: every word of the query is somewhere in the row ("jazz
// portable" finds the Portable that plays Jazz24). An empty query matches.
export function rowMatches(row, query, terms) {
  const q = foldText(query);
  if (!q) return true;
  const hay = rowWords(row || {}, terms);
  return q.split(' ').every((w) => hay.includes(w));
}

// filterSidebarModel narrows the other speakers to those that match and
// leaves the pinned group whole. The model it returns carries the search:
//
//   query    the query as typed ('' without one)
//   total    every row of the list, pinned included
//   shown    the rows on screen after the search
//   noMatch  no row matches at all, pinned included: the list says so
//
// The others keep the list's own order, as without a search.
export function filterSidebarModel(model, query, terms) {
  const m = model || { pinned: [], others: [] };
  const all = rowsOf(m);
  const q = foldText(query) ? String(query) : '';
  if (!q) return { ...m, search: { query: '', total: all.length, shown: all.length, noMatch: false } };
  const others = (m.others || []).filter((r) => rowMatches(r, q, terms));
  const pinnedHits = (m.pinned || []).filter((r) => rowMatches(r, q, terms)).length;
  return {
    ...m,
    others,
    search: {
      query: q,
      total: all.length,
      shown: (m.pinned || []).length + others.length,
      noMatch: pinnedHits + others.length === 0,
    },
  };
}

// searchFieldHTML is the field above the lists. It lives outside the lists
// the repaint replaces, so typing is never interrupted by a poll.
export function searchFieldHTML() {
  return '<div class="ui2-spk-find" id="ui2SpkFind" hidden>'
    + `<input type="search" class="ui2-spk-q" id="ui2SpkQ" autocomplete="off" spellcheck="false" aria-controls="ui2SpkLists"`
    + ` placeholder="${escapeAttr(t('ui2.side.findPlaceholder'))}" aria-label="${escapeAttr(t('ui2.side.findLabel'))}">`
    + '</div>';
}

// noMatchHTML is the inline answer when nothing matches, with the one way
// out next to it (Escape in the field does the same).
export function noMatchHTML(query) {
  return '<div class="ui2-spk-nomatch">'
    + `<p class="ui2-spk-empty">${escapeHtml(t('ui2.side.findNone', { query }))}</p>`
    + `<button type="button" class="u-btn u-btn--secondary ui2-spk-find-clear" data-spk-find-clear>${escapeHtml(t('ui2.side.findClear'))}</button>`
    + '</div>';
}

// searchStatusText is what the sidebar's live region says after a search:
// how many speakers show, or that none matches.
export function searchStatusText(search) {
  if (!search || !search.query) return '';
  if (search.noMatch) return t('ui2.side.findNone', { query: search.query });
  return t('ui2.side.findShown', { n: search.shown, total: search.total });
}
