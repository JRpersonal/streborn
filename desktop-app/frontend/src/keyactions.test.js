// The key-action helpers (sleep timer, snooze, bass steps), checked without a
// DOM, plus the i18n parity of every string the key map and the group-keys
// section show for them.
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import {
  normalizeKeyActions, sleepOn, snoozeOn, bassOn, isBassAction, actionOn, withSleep, withAction, actionAllowed, sleepBlockedBy,
} from './keyactions.js';

describe('bass steps', () => {
  it('go on the thumbs keys only, like the agent allows', () => {
    for (const a of ['bassUp', 'bassDown']) {
      expect(actionAllowed(a, 'thumbsUp')).toBe(true);
      expect(actionAllowed(a, 'thumbsDown')).toBe(true);
      expect(actionAllowed(a, 'aux')).toBe(false);
      expect(actionAllowed(a, 'next')).toBe(false);
    }
  });
  it('normalise to their canonical spelling, and never land on AUX', () => {
    expect(normalizeKeyActions({ keys: { thumbsUp: 'bassup', thumbsDown: ' BassDown ', aux: 'bassUp' } }))
      .toEqual({ keys: { thumbsUp: 'bassUp', thumbsDown: 'bassDown' } });
  });
  it('are told apart from the other actions', () => {
    let d = withAction({ keys: {} }, 'thumbsUp', 'bassUp');
    d = withAction(d, 'thumbsDown', 'bassDown');
    expect(d).toEqual({ keys: { thumbsUp: 'bassUp', thumbsDown: 'bassDown' } });
    expect(bassOn(d, 'thumbsUp')).toBe(true);
    expect(actionOn(d, 'thumbsDown')).toBe('bassDown');
    expect(sleepOn(d, 'thumbsUp') || snoozeOn(d, 'thumbsUp')).toBe(false);
    expect(isBassAction('sleep')).toBe(false);
    expect(withAction(d, 'aux', 'bassDown')).toEqual(d);
  });
});

describe('key action select', () => {
  // The settings view's "Built-in action" select offers every action the
  // agent knows; the options are the view's own markup.
  const src = readFileSync(new URL('./views/settings/advanced.js', import.meta.url), 'utf8');
  it('offers the bass steps next to the sleep timer and snooze', () => {
    for (const v of ['sleep', 'snooze', 'bassUp', 'bassDown']) {
      expect(src, `option ${v}`).toContain(`<option value="${v}"`);
    }
    expect(src).toContain("t('settingsView.bassKeyUpLabel')");
    expect(src).toContain("t('settingsView.bassKeyDownLabel')");
  });
});

describe('normalizeKeyActions', () => {
  it('accepts nothing, an older agent, and a full document', () => {
    expect(normalizeKeyActions(null)).toEqual({ keys: {} });
    expect(normalizeKeyActions({})).toEqual({ keys: {} });
    // only the keys that can carry it, only known actions
    expect(normalizeKeyActions({ keys: { thumbsUp: ' Sleep ', prev: 'sleep', thumbsDown: 'reboot' } }))
      .toEqual({ keys: { thumbsUp: 'sleep' } });
  });
  it('keeps snooze on the thumbs keys and AUX, but never the sleep timer on AUX', () => {
    expect(normalizeKeyActions({ keys: { thumbsDown: 'Snooze', aux: 'snooze' } }))
      .toEqual({ keys: { thumbsDown: 'snooze', aux: 'snooze' } });
    expect(normalizeKeyActions({ keys: { aux: 'sleep', prev: 'snooze' } })).toEqual({ keys: {} });
  });
});

describe('withSleep', () => {
  it('switches the timer on and off one key, leaving the other alone', () => {
    let d = withSleep({ keys: {} }, 'thumbsUp', true);
    expect(sleepOn(d, 'thumbsUp')).toBe(true);
    d = withSleep(d, 'thumbsDown', true);
    d = withSleep(d, 'thumbsUp', false);
    expect(d).toEqual({ keys: { thumbsDown: 'sleep' } });
  });
  it('never puts the timer on a key that cannot carry it', () => {
    expect(withSleep({ keys: {} }, 'prev', true)).toEqual({ keys: {} });
    expect(withSleep(null, 'playPause', true)).toEqual({ keys: {} });
    expect(withSleep({ keys: {} }, 'aux', true)).toEqual({ keys: {} });
  });
  it('does not change the document it was given', () => {
    const d = { keys: { thumbsUp: 'sleep' } };
    withSleep(d, 'thumbsUp', false);
    expect(d).toEqual({ keys: { thumbsUp: 'sleep' } });
  });
});

describe('withAction', () => {
  it('puts snooze on AUX and a thumbs key, one action per key', () => {
    let d = withAction({ keys: {} }, 'aux', 'snooze');
    d = withAction(d, 'thumbsUp', 'sleep');
    d = withAction(d, 'thumbsUp', 'snooze');
    expect(d).toEqual({ keys: { aux: 'snooze', thumbsUp: 'snooze' } });
    expect(snoozeOn(d, 'aux')).toBe(true);
    expect(actionOn(d, 'thumbsUp')).toBe('snooze');
    expect(actionOn(d, 'thumbsDown')).toBe('');
  });
  it('clears a key with no action, and refuses a key that cannot carry one', () => {
    expect(withAction({ keys: { aux: 'snooze' } }, 'aux', '')).toEqual({ keys: {} });
    expect(withAction({ keys: {} }, 'aux', 'sleep')).toEqual({ keys: {} });
    expect(withAction({ keys: {} }, 'next', 'snooze')).toEqual({ keys: {} });
  });
  it('matches the agent: snooze on thumbs and AUX, sleep on thumbs only', () => {
    expect(actionAllowed('snooze', 'aux')).toBe(true);
    expect(actionAllowed('snooze', 'thumbsDown')).toBe(true);
    expect(actionAllowed('sleep', 'aux')).toBe(false);
    expect(actionAllowed('sleep', 'thumbsUp')).toBe(true);
    expect(actionAllowed('reboot', 'thumbsUp')).toBe(false);
  });
});

describe('sleepBlockedBy', () => {
  it('names the group that sits on the key', () => {
    expect(sleepBlockedBy({ thumbsUp: 'Evening' }, 'thumbsUp')).toBe('Evening');
    expect(sleepBlockedBy({ thumbsUp: 'Evening' }, 'thumbsDown')).toBe('');
    expect(sleepBlockedBy(null, 'thumbsUp')).toBe('');
  });
});

describe('key action i18n', () => {
  const bundles = ['en', 'de', 'fr', 'es', 'nl', 'pl', 'tr', 'uk', 'lt', 'lv', 'ar', 'ja', 'zh-Hant'];
  const read = (loc) => JSON.parse(
    readFileSync(new URL(`./i18n/bundles/${loc}.json`, import.meta.url), 'utf8'));
  const en = read('en');
  const keys = Object.keys(en).filter((k) => k.startsWith('settingsView.sleepKey')
    || k.startsWith('settingsView.snoozeKey') || k.startsWith('settingsView.keyAction')
    || k === 'settingsView.webhookSleepKeyNote' || k === 'settingsView.webhookRemoteLegendSleep'
    || k === 'settingsView.webhookSnoozeKeyNote' || k === 'settingsView.webhookRemoteLegendSnooze'
    || k === 'multiroom.groupKeysKeySleep' || k === 'multiroom.groupKeysSleepHint'
    || k === 'multiroom.groupKeysKeySnooze' || k === 'multiroom.groupKeysSnoozeHint'
    || k.startsWith('settingsView.bassKey')
    || k === 'settingsView.webhookBassKeyNote' || k === 'settingsView.webhookRemoteLegendBass'
    || k === 'multiroom.groupKeysKeyBass' || k === 'multiroom.groupKeysBassHint');
  it('has the strings in English at all', () => {
    expect(keys.length).toBe(32);
  });
  it('is present and translated in every bundle, placeholders intact', () => {
    const ph = (s) => (String(s).match(/\{\{\s*\w+\s*\}\}/g) || []).sort().join('|');
    for (const loc of bundles.filter((l) => l !== 'en')) {
      const b = read(loc);
      for (const k of keys) {
        expect(b[k], `${loc}.json ${k}`).toBeTruthy();
        expect(b[k], `${loc}.json ${k} untranslated`).not.toBe(en[k]);
        expect(ph(b[k]), `${loc}.json ${k} placeholders`).toBe(ph(en[k]));
      }
    }
  });
});
