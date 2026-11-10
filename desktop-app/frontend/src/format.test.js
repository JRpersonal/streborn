import { describe, expect, it } from 'vitest';
import { agoText, formatClock } from './format.js';
import { formatRemaining } from './utils.js';

describe('formatClock', () => {
  it('renders whole seconds as m:ss', () => {
    expect(formatClock(0)).toBe('0:00');
    expect(formatClock(7)).toBe('0:07');
    expect(formatClock(60)).toBe('1:00');
    expect(formatClock(187)).toBe('3:07');
    expect(formatClock(4320)).toBe('72:00');
  });

  it('cuts a fraction off and never goes below zero', () => {
    expect(formatClock(59.9)).toBe('0:59');
    expect(formatClock(-5)).toBe('0:00');
    expect(formatClock(undefined)).toBe('0:00');
    expect(formatClock(NaN)).toBe('0:00');
  });

  it('matches the formats it replaced', () => {
    // The library's track length (whole seconds from the media server).
    const formatDuration = (sec) => `${Math.floor(sec / 60)}:${(sec % 60).toString().padStart(2, '0')}`;
    // The now-playing bar's elapsed and total time.
    const fmtClock = (sec) => { sec = Math.max(0, Math.floor(sec)); return Math.floor(sec / 60) + ':' + String(sec % 60).padStart(2, '0'); };
    // The install checklist's running timer, from milliseconds.
    const fmtMS = (ms) => { const s = Math.max(0, Math.round(ms / 1000)); return Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0'); };
    for (const sec of [1, 9, 59, 60, 61, 599, 600, 3599, 3600, 86399]) {
      expect(formatClock(sec)).toBe(formatDuration(sec));
      expect(formatClock(sec)).toBe(fmtClock(sec));
    }
    for (const sec of [-3, 0, 0.4, 59.99, 61.5]) expect(formatClock(sec)).toBe(fmtClock(sec));
    for (const ms of [-200, 0, 499, 500, 59499, 59500, 754321]) {
      expect(formatClock(Math.round(ms / 1000))).toBe(fmtMS(ms));
    }
  });

  it('carries the countdown, which rounds up', () => {
    expect(formatRemaining(1)).toBe('0:01');
    expect(formatRemaining(60000)).toBe('1:00');
    expect(formatRemaining(60001)).toBe('1:01');
    expect(formatRemaining(-50)).toBe('0:00');
  });
});

describe('agoText', () => {
  it('says minutes, hours and days in the given language', () => {
    expect(agoText(600, 'en')).toBe('10 minutes ago');
    expect(agoText(7200, 'en')).toBe('2 hours ago');
    expect(agoText(30, 'en')).toBe('1 minute ago');
    expect(agoText(600, 'de')).toBe('vor 10 Minuten');
    expect(agoText(172800, 'en')).toBe('2 days ago');
  });

  it('never says less than a minute and tolerates a missing value', () => {
    expect(agoText(0, 'en')).toBe('1 minute ago');
    expect(agoText(-40, 'en')).toBe('1 minute ago');
    expect(agoText(undefined, 'en')).toBe('1 minute ago');
  });

  it('matches the classic picker tooltip it replaced', () => {
    const offlineAgo = (sec) => {
      const rtf = new Intl.RelativeTimeFormat('en', { numeric: 'auto' });
      if (sec < 3600) return rtf.format(-Math.max(1, Math.round(sec / 60)), 'minute');
      if (sec < 86400) return rtf.format(-Math.round(sec / 3600), 'hour');
      return rtf.format(-Math.round(sec / 86400), 'day');
    };
    for (const sec of [0, 1, 29, 30, 89, 90, 3599, 3600, 5400, 86399, 86400, 400000]) {
      expect(agoText(sec, 'en')).toBe(offlineAgo(sec));
    }
  });
});
