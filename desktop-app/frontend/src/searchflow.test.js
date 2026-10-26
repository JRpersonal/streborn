// Tests for the pure search-flow decision helpers in searchflow.js.
import { describe, it, expect } from 'vitest';
import {
  isStreamURL,
  hostnameOf,
  syntheticStationForURL,
  normalizeDetailedSearch,
  relaxedHintVisible,
  stationBitrateCell,
  searchModePressed,
  stationCheckDot,
} from './searchflow.js';

describe('isStreamURL', () => {
  it('accepts absolute http and https URLs, any case, with padding', () => {
    expect(isStreamURL('http://radio.example.com/stream')).toBe(true);
    expect(isStreamURL('https://radio.example.com/live.mp3')).toBe(true);
    expect(isStreamURL('HTTPS://RADIO.EXAMPLE.COM/X')).toBe(true);
    expect(isStreamURL('  http://radio.example.com  ')).toBe(true);
  });
  it('keeps everything else a name search', () => {
    expect(isStreamURL('BBC Radio 4')).toBe(false);
    expect(isStreamURL('radio 91.8')).toBe(false);
    expect(isStreamURL('www.radio.example.com')).toBe(false); // no scheme
    expect(isStreamURL('ftp://radio.example.com/x')).toBe(false);
    expect(isStreamURL('httpx://radio.example.com')).toBe(false);
    expect(isStreamURL('')).toBe(false);
    expect(isStreamURL(null)).toBe(false);
    expect(isStreamURL(undefined)).toBe(false);
  });
  it('does not fire on a URL mentioned mid-query', () => {
    expect(isStreamURL('play http://radio.example.com')).toBe(false);
  });
});

describe('hostnameOf', () => {
  it('extracts the hostname from a normal URL', () => {
    expect(hostnameOf('http://radio.example.com/live.mp3?x=1')).toBe('radio.example.com');
    expect(hostnameOf('https://radio.example.com:8000/stream')).toBe('radio.example.com');
  });
  it('falls back to a regex when the URL constructor rejects the input', () => {
    // A space in the path is invalid per WHATWG URL in some engines but
    // still identifies a host; the regex path must cover it.
    expect(hostnameOf('http://radio.example.com/a b')).toBe('radio.example.com');
  });
  it('falls back to the raw input when nothing parses', () => {
    expect(hostnameOf('not a url')).toBe('not a url');
    expect(hostnameOf('')).toBe('');
  });
});

describe('syntheticStationForURL', () => {
  it('builds the minimal station shape playStation and the save path need', () => {
    const s = syntheticStationForURL('http://radio.example.com:8000/live');
    expect(s.synthetic).toBe(true);
    expect(s.name).toBe('radio.example.com');
    expect(s.url).toBe('http://radio.example.com:8000/live');
    expect(s.url_resolved).toBe('http://radio.example.com:8000/live');
    // The preset save path and the play path must tolerate these being empty.
    expect(s.stationuuid).toBe('');
    expect(s.codec).toBe('');
    expect(s.bitrate).toBe(0);
    expect(s.homepage).toBe('');
  });
  it('trims the pasted URL', () => {
    const s = syntheticStationForURL('  http://radio.example.com/x  ');
    expect(s.url_resolved).toBe('http://radio.example.com/x');
  });
});

describe('normalizeDetailedSearch', () => {
  it('passes a well-formed result through', () => {
    const st = [{ name: 'A' }];
    expect(normalizeDetailedSearch({ stations: st, relaxed: true }))
      .toEqual({ stations: st, relaxed: true });
  });
  it('tolerates null, missing and malformed fields', () => {
    expect(normalizeDetailedSearch(null)).toEqual({ stations: [], relaxed: false });
    expect(normalizeDetailedSearch({})).toEqual({ stations: [], relaxed: false });
    expect(normalizeDetailedSearch({ stations: null, relaxed: 1 })).toEqual({ stations: [], relaxed: true });
    expect(normalizeDetailedSearch({ stations: 'nope' })).toEqual({ stations: [], relaxed: false });
  });
});

describe('relaxedHintVisible', () => {
  it('shows only for a relaxed, undismissed fetch result', () => {
    expect(relaxedHintVisible(true, false, 'search')).toBe(true);
    expect(relaxedHintVisible(true, false, 'top')).toBe(true);
  });
  it('hides when not relaxed, dismissed, or on the favorites view', () => {
    expect(relaxedHintVisible(false, false, 'search')).toBe(false);
    expect(relaxedHintVisible(true, true, 'search')).toBe(false);
    expect(relaxedHintVisible(true, false, 'favorites')).toBe(false);
  });
});

describe('stationBitrateCell (#1521)', () => {
  it('shows a known bitrate', () => {
    expect(stationBitrateCell({ bitrate: 128 })).toBe('128 kbit/s');
    expect(stationBitrateCell({ bitrate: '320' })).toBe('320 kbit/s');
  });
  it('leaves the cell out when the bitrate is unknown', () => {
    expect(stationBitrateCell({ bitrate: 0 })).toBe('');
    expect(stationBitrateCell({})).toBe('');
    expect(stationBitrateCell({ bitrate: null })).toBe('');
    expect(stationBitrateCell({ bitrate: 'x' })).toBe('');
    expect(stationBitrateCell(null)).toBe('');
  });
});

describe('stationCheckDot (#1521)', () => {
  const rb = (extra) => ({ stationuuid: '9617a958-0601-11e8-ae97-52543be04c81', ...extra });
  it('green for a radio-browser station that passed its last check', () => {
    expect(stationCheckDot(rb({ lastcheckok: 1 }))).toBe('ok');
    expect(stationCheckDot(rb({ lastcheckok: true }))).toBe('ok');
    expect(stationCheckDot(rb({ lastcheckok: 1, lastchecktime: '2026-10-25 09:44:00' }))).toBe('ok');
  });
  it('still red for a radio-browser station that failed its last check', () => {
    expect(stationCheckDot(rb({ lastcheckok: 0 }))).toBe('bad');
    expect(stationCheckDot(rb({ lastcheckok: false }))).toBe('bad');
    expect(stationCheckDot(rb({ lastcheckok: 0, lastchecktime: '2026-10-25 09:44:00' }))).toBe('bad');
  });
  it('no dot for a library item or own station that was never checked', () => {
    // The shape a Recently played card is starred with: no lastcheckok at all.
    expect(stationCheckDot({ stationuuid: 'lib:playlist-1', name: 'Road trip', bitrate: 0 })).toBe('');
    expect(stationCheckDot(rb({ lastcheckok: undefined }))).toBe('');
    expect(stationCheckDot(rb({ lastcheckok: null }))).toBe('');
    expect(stationCheckDot({ name: 'My stream', url: 'http://radio.example.com/live', lastcheckok: 0 })).toBe('');
  });
  it('no dot for the synthetic play-this-URL card', () => {
    expect(stationCheckDot(syntheticStationForURL('http://radio.example.com/live'))).toBe('');
    expect(stationCheckDot({ ...rb({ lastcheckok: 0 }), synthetic: true })).toBe('');
  });
  it('no dot for a directory entry radio-browser has not checked yet', () => {
    expect(stationCheckDot(rb({ lastcheckok: 0, lastchecktime: '' }))).toBe('');
    expect(stationCheckDot(rb({ lastcheckok: 0, lastchecktime: '0000-00-00 00:00:00' }))).toBe('');
  });
  it('no dot for nothing', () => {
    expect(stationCheckDot(null)).toBe('');
    expect(stationCheckDot(undefined)).toBe('');
  });
});

describe('searchModePressed (#1521)', () => {
  const none = { topBtn: false, favModeBtn: false, pandoraModeBtn: false };
  it('presses the button of the list on screen, only that one', () => {
    expect(searchModePressed('top', true)).toEqual({ ...none, topBtn: true });
    expect(searchModePressed('favorites', true)).toEqual({ ...none, favModeBtn: true });
    expect(searchModePressed('pandora', true)).toEqual({ ...none, pandoraModeBtn: true });
  });
  it('presses nothing for a search result list', () => {
    expect(searchModePressed('search', true)).toEqual(none);
  });
  it('presses nothing while no list is open yet', () => {
    expect(searchModePressed('top', false)).toEqual(none);
    expect(searchModePressed('favorites', undefined)).toEqual(none);
    expect(searchModePressed('', true)).toEqual(none);
  });
});
