import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { QUEUE_END_SERVER_GONE, queueEndLabelKey } from './queueend.js';

const gone = { active: false, lastEnd: { reason: QUEUE_END_SERVER_GONE, at: '2026-11-13T12:18:59Z' } };

describe('queueEndLabelKey (#1890)', () => {
  it('names the gone music server where "stopped" would stand', () => {
    expect(queueEndLabelKey(gone, 'STOP_STATE', 'UPNP')).toBe('status.musicServerGone');
    expect(queueEndLabelKey(gone, '', 'INVALID_SOURCE')).toBe('status.musicServerGone');
    expect(queueEndLabelKey(gone, '', '')).toBe('status.musicServerGone');
  });

  it('says nothing once something plays again', () => {
    for (const ps of ['PLAY_STATE', 'BUFFERING_STATE', 'PAUSE_STATE']) {
      expect(queueEndLabelKey(gone, ps, 'UPNP')).toBe('');
    }
  });

  it('leaves another source and standby to their own words', () => {
    for (const src of ['STANDBY', 'AUX', 'BLUETOOTH', 'SPOTIFY', 'LOCAL_INTERNET_RADIO']) {
      expect(queueEndLabelKey(gone, 'STOP_STATE', src)).toBe('');
    }
  });

  it('needs an ended queue with that reason', () => {
    expect(queueEndLabelKey(null, 'STOP_STATE', 'UPNP')).toBe('');
    expect(queueEndLabelKey({ active: false }, 'STOP_STATE', 'UPNP')).toBe('');
    expect(queueEndLabelKey({ ...gone, active: true }, 'STOP_STATE', 'UPNP')).toBe('');
    expect(queueEndLabelKey({ active: false, lastEnd: { reason: 'other' } }, 'STOP_STATE', 'UPNP')).toBe('');
  });

  it('has the words in every language bundle', () => {
    const dir = new URL('./i18n/bundles/', import.meta.url);
    for (const lang of ['ar', 'de', 'en', 'es', 'fr', 'ja', 'lt', 'lv', 'nl', 'pl', 'tr', 'uk', 'zh-Hant']) {
      const b = JSON.parse(readFileSync(new URL(lang + '.json', dir), 'utf8'));
      expect(b['status.musicServerGone'], lang).toBeTruthy();
    }
  });

  it('is what the player line uses', () => {
    const src = readFileSync(new URL('./views/music/nowplaying.js', import.meta.url), 'utf8');
    expect(src).toContain('queueEndLabelKey(state.queue, ps, srcU)');
  });
});
