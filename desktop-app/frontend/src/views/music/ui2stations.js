// views/music/ui2stations.js: the station rows of the UI 2.0 preview's Radio
// (#1286, docs/UI-2.0.md), as strings.
//
// A row in the preview carries what you choose a station by (logo, name,
// genre) and its actions, each named with the station and the speaker: play,
// favorite, put on a key, details. The technical data the current list prints
// on every row (country, bitrate, votes, codec, the stream address) moves into
// the details panel the row opens. A station the directory could not reach, or
// one that did not play, says so in its own row.
//
// Pure: strings and plain data, no DOM and no state, so vitest reads it as is.
// ui2radio.js renders and wires it.

import { t } from '../../i18n/index.js';
import { translateCountry, translateTags } from '../../localization.js';
import { logoImgTag } from '../../logos.js';
import { escapeAttr, escapeHtml, formatNumber } from '../../utils.js';

// stationKey is a station's identity across renders: the directory's uuid, or
// name and address for a station without one (the same identity the
// favourites store uses), so a row's status and open details survive a
// repaint of the list.
export function stationKey(s) {
  if (!s) return '';
  return s.stationuuid || `${s.name || ''}|${s.url || ''}`;
}

// genreLine is the short genre text of a row: the first two translated tags.
// A pasted stream address has none and says what it is instead.
export function genreLine(s) {
  if (!s) return '';
  if (s.synthetic) return t('search.playUrlCard');
  return translateTags(s.tags || '').slice(0, 2).join(', ');
}

// stationBroken reports whether the directory's last check could not reach
// the station. A pasted address was never checked and is not broken.
export function stationBroken(s) {
  return !!s && !s.synthetic && !s.lastcheckok;
}

// stationDetails lists what the details panel shows, as [label, value] pairs
// in display order; a value the directory does not have reads "Unknown" for
// the format and the bitrate (people ask about those two) and is left out
// otherwise. The stream address and the website are returned apart, since
// they render as code and as a link.
export function stationDetails(s) {
  if (!s) return { rows: [], stream: '', website: '' };
  const rows = [];
  const unknown = t('ui2.radio.unknown');
  if (!s.synthetic) {
    const country = translateCountry(s.country) || (s.countrycode || '');
    if (country) rows.push([t('ui2.radio.country'), country]);
    if (s.language) rows.push([t('ui2.radio.language'), String(s.language)]);
    const tags = translateTags(s.tags || '');
    if (tags.length) rows.push([t('ui2.radio.genres'), tags.join(', ')]);
  }
  const codec = String(s.codec || '').trim();
  const hls = s.hls === 1 || s.hls === '1';
  rows.push([t('ui2.radio.codec'), (codec && codec.toUpperCase() !== 'UNKNOWN' ? codec : unknown) + (hls ? ' (HLS)' : '')]);
  rows.push([t('ui2.radio.bitrate'), s.bitrate ? `${s.bitrate} kbit/s` : unknown]);
  if (!s.synthetic) {
    if (s.votes) rows.push([t('ui2.radio.votes'), formatNumber(s.votes)]);
    if (s.clicktrend) rows.push([t('ui2.radio.trend'), s.clicktrend > 0 ? `+${s.clicktrend}` : String(s.clicktrend)]);
    rows.push([t('ui2.radio.lastCheck'), s.lastcheckok ? t('search.checkOk') : t('search.checkBad')]);
  }
  const website = typeof s.homepage === 'string' && /^https?:\/\//i.test(s.homepage) ? s.homepage : '';
  return { rows, stream: String(s.url_resolved || s.url || ''), website };
}

// The row's own line under the name: an action in progress, its outcome, or
// an error. kind is busy, ok, warn or error; the words say it, the colour only
// repeats them.
export function rowStatusHTML(status) {
  const kind = status && status.kind ? status.kind : '';
  const text = status && status.text ? status.text : '';
  return `<span class="ui2-st-status${kind ? ` is-${kind}` : ''}" role="status">${escapeHtml(text)}</span>`;
}

// stationRowHTML is one row. ctx: speaker (the name every action carries),
// fav (starred), open (details shown), status ({ kind, text } or null).
export function stationRowHTML(s, i, ctx = {}) {
  const name = (s && s.name) || t('search.unnamed');
  const speaker = ctx.speaker || '';
  const did = `ui2StDetails${i}`;
  const nid = `ui2StName${i}`;
  const genre = genreLine(s);
  const broken = stationBroken(s)
    ? `<span class="ui2-st-broken">${escapeHtml(t('ui2.radio.checkBad'))}</span>`
    : '';
  const fav = !!ctx.fav;
  const favLabel = t(fav ? 'ui2.radio.favRemove' : 'ui2.radio.favAdd', { name });
  const d = stationDetails(s);
  const rows = d.rows.map(([k, v]) => `<dt>${escapeHtml(k)}</dt><dd>${escapeHtml(v)}</dd>`).join('');
  const stream = d.stream
    ? `<dt>${escapeHtml(t('ui2.radio.stream'))}</dt><dd><code class="ui2-st-url">${escapeHtml(d.stream)}</code></dd>`
    : '';
  const site = d.website
    ? `<dt>${escapeHtml(t('footer.website'))}</dt><dd><a href="#" class="ui2-st-site" data-act="site" data-i="${i}">${escapeHtml(d.website)}</a></dd>`
    : '';
  const logo = logoImgTag(s || {}, 'fav').replace('<img ', '<img alt="" ');
  return `<li class="ui2-st${broken ? ' is-broken' : ''}${ctx.open ? ' is-open' : ''}" data-i="${i}" data-key="${escapeAttr(stationKey(s))}">`
    + '<div class="ui2-st-main">'
    + `<div class="result-logo ui2-st-logo">${logo}</div>`
    + '<div class="ui2-st-text">'
    + `<span class="ui2-st-name" id="${nid}">${escapeHtml(name)}</span>`
    + (genre ? `<span class="ui2-st-genre">${escapeHtml(genre)}</span>` : '')
    + broken
    + rowStatusHTML(ctx.status)
    + '</div>'
    + '<div class="ui2-st-actions">'
    + `<button type="button" class="u-btn u-btn--icon ui2-st-btn" data-act="play" data-i="${i}" aria-label="${escapeAttr(t('ui2.radio.play', { name, speaker }))}" title="${escapeAttr(t('ui2.radio.play', { name, speaker }))}"><span aria-hidden="true">&#9654;</span></button>`
    + `<button type="button" class="u-btn u-btn--icon ui2-st-btn ui2-st-fav${fav ? ' is-fav' : ''}" data-act="fav" data-i="${i}" aria-pressed="${fav ? 'true' : 'false'}" aria-label="${escapeAttr(favLabel)}" title="${escapeAttr(favLabel)}"><span aria-hidden="true">${fav ? '&#9733;' : '&#9734;'}</span></button>`
    + `<button type="button" class="u-btn u-btn--secondary ui2-st-key" data-act="key" data-i="${i}" aria-label="${escapeAttr(t('ui2.radio.putOnKeyName', { name, speaker }))}">${escapeHtml(t('ui2.radio.putOnKey'))}</button>`
    + `<button type="button" class="u-btn u-btn--icon ui2-st-btn ui2-st-more" data-act="details" data-i="${i}" aria-expanded="${ctx.open ? 'true' : 'false'}" aria-controls="${did}" aria-label="${escapeAttr(t('ui2.radio.detailsLabel', { name }))}" title="${escapeAttr(t('ui2.radio.details'))}"><span aria-hidden="true">&#9432;</span></button>`
    + '</div>'
    + '</div>'
    + `<div class="ui2-st-details" id="${did}" role="region" aria-labelledby="${nid}"${ctx.open ? '' : ' hidden'}>`
    + `<dl>${rows}${stream}${site}</dl>`
    + '</div>'
    + '</li>';
}

// tabFor maps the search box's mode to the preview's tab: Favorites shows the
// stored favourites, every other mode (top list, search, Pandora) is Discover.
export function tabFor(mode) {
  return mode === 'favorites' ? 'favorites' : 'discover';
}

// discoverFiltersApply reports whether the Discover filters (only
// Bose-compatible streams, a minimum bitrate) narrow the list. The preview's
// Favorites tab hides those controls, so there they must not hide a starred
// station either: a filter the user cannot see is one they cannot undo. The
// current layout keeps the filters visible above favourites and applies them.
export function discoverFiltersApply(ui2, mode) {
  return !(ui2 && tabFor(mode) === 'favorites');
}

// keyHoldsStation reports whether a preset list read back from the speaker
// shows the station on the slot: "done" only when the speaker says so.
export function keyHoldsStation(presets, slot, station, url) {
  if (!Array.isArray(presets) || !station) return false;
  const p = presets.find((x) => x && x.slot === slot);
  if (!p) return false;
  return (!!url && p.stream_url === url) || (!!station.name && p.name === station.name);
}
