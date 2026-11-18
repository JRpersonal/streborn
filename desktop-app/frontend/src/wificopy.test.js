// STR keeps its own copy of a speaker's Wi-Fi by itself (wificopy.js). The
// "Save Wi-Fi" banner it replaces read like a required action on a speaker
// that worked fine, and its button restarted the speaker (maintainer's own
// SoundTouch 20, 2026-11-15).
import { describe, it, expect, beforeEach, vi } from 'vitest';
import { readFileSync, readdirSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import { state } from './state.js';
import {
  WIFI_COPY_RETRY_MS,
  keepWifiCopies,
  keepWifiCopy,
  noteWifiCopyResult,
  paintWifiCopyNote,
  wifiCopyCandidates,
  wifiCopyNoteShows,
} from './wificopy.js';
import { allSource, srcOf } from './test/source.js';

const here = dirname(fileURLToPath(import.meta.url));
const missing = { host: '192.0.2.51', port: 17008, wlanCredsMissing: true };
const fine = { host: '192.0.2.52', port: 8888 };

beforeEach(() => {
  state.wifiCopy = {};
  state.boxes = [];
});

describe('wifiCopyCandidates', () => {
  it('picks the speakers without a copy that answer and were not tried lately', () => {
    const now = 10 * WIFI_COPY_RETRY_MS;
    const tried = {
      '192.0.2.60': { at: now - 1000, stored: false },
      '192.0.2.61': { at: now - WIFI_COPY_RETRY_MS, stored: false },
      '192.0.2.62': { at: now - 1000, pending: true },
      '192.0.2.63': { at: 0, stored: true },
    };
    const boxes = [
      missing, fine,
      { host: '192.0.2.60', wlanCredsMissing: true },
      { host: '192.0.2.61', wlanCredsMissing: true },
      { host: '192.0.2.62', wlanCredsMissing: true },
      { host: '192.0.2.63', wlanCredsMissing: true },
      { host: '192.0.2.64', wlanCredsMissing: true, offline: true },
      { host: '192.0.2.65', wlanCredsMissing: true, unreachable: true },
      null,
    ];
    expect(wifiCopyCandidates(boxes, tried, now).map((b) => b.host)).toEqual(['192.0.2.51', '192.0.2.61']);
  });
});

describe('the quiet note', () => {
  it('shows only after an attempt could not keep a copy', () => {
    expect(wifiCopyNoteShows(missing)).toBe(false); // not tried yet
    noteWifiCopyResult(missing.host, { stored: false, reason: 'no-password', note: true });
    expect(wifiCopyNoteShows(missing)).toBe(true);
    noteWifiCopyResult(missing.host, { stored: false, reason: 'refused:not-needed', note: false });
    expect(wifiCopyNoteShows(missing)).toBe(false);
    noteWifiCopyResult(missing.host, { stored: true, reason: 'stored', note: false });
    expect(wifiCopyNoteShows(missing)).toBe(false);
    expect(wifiCopyNoteShows({ ...missing, wlanCredsMissing: false })).toBe(false);
    state.wifiCopy[missing.host] = { pending: true };
    expect(wifiCopyNoteShows(missing)).toBe(false);
  });

  it('is painted from the speaker list on the element the settings render', () => {
    state.boxes = [{ ...missing }];
    noteWifiCopyResult(missing.host, { stored: false, reason: 'macos', note: true });
    const el = { hidden: true, getAttribute: () => missing.host };
    paintWifiCopyNote({ getElementById: (id) => (id === 'wlanCopyNote' ? el : null) });
    expect(el.hidden).toBe(false);
    state.boxes[0].wlanCredsMissing = false;
    paintWifiCopyNote({ getElementById: () => el });
    expect(el.hidden).toBe(true);
    expect(() => paintWifiCopyNote({ getElementById: () => null })).not.toThrow();
  });
});

describe('keepWifiCopy', () => {
  it('clears the flag on a stored copy and records the outcome', async () => {
    const box = { ...missing };
    state.boxes = [box];
    const keep = vi.fn(async () => ({ stored: true, reason: 'stored', note: false }));
    const rec = await keepWifiCopy(box, 'install', keep);
    expect(keep).toHaveBeenCalledWith(missing.host, 17008, 'install');
    expect(rec.stored).toBe(true);
    expect(box.wlanCredsMissing).toBe(false);
    expect(wifiCopyNoteShows(box)).toBe(false);
  });

  it('treats a binding this build lacks as "cannot", never a throw', async () => {
    const box = { ...missing };
    const rec = await keepWifiCopy(box, 'seen', async () => { throw new Error('not available in this build'); });
    expect(rec).toMatchObject({ stored: false, reason: 'binding-missing', note: true });
    expect(box.wlanCredsMissing).toBe(true);
    expect(wifiCopyNoteShows(box)).toBe(true);
  });

  it('tries each candidate once per round, one at a time', async () => {
    const boxes = [{ ...missing }, { host: '192.0.2.53', wlanCredsMissing: true }, fine];
    state.boxes = boxes;
    let open = 0, maxOpen = 0;
    const keep = vi.fn(async () => {
      open++; maxOpen = Math.max(maxOpen, open);
      await Promise.resolve();
      open--;
      return { stored: false, reason: 'no-password', note: true };
    });
    const done = vi.fn();
    expect(await keepWifiCopies(boxes, keep, done)).toBe(2);
    expect(keep).toHaveBeenCalledTimes(2);
    expect(keep.mock.calls.every((c) => c[2] === 'seen')).toBe(true);
    expect(maxOpen).toBe(1);
    expect(done).toHaveBeenCalledTimes(1);
    // The next refresh within the retry window asks nobody.
    expect(await keepWifiCopies(boxes, keep, done)).toBe(0);
    expect(keep).toHaveBeenCalledTimes(2);
  });
});

describe('wiring', () => {
  const src = allSource();

  it('has no banner and no attention item for a speaker without a copy', () => {
    expect(src).not.toContain("t('speaker.noWifiBanner'");
    expect(src).not.toContain('boxIssueWifiBtn');
    expect(src).not.toContain("case 'saveWifi'");
    expect(srcOf('checkBoxIssueBanner')).toContain('keepWifiCopies(boxes, KeepSpeakerWiFiCopy, paintWifiCopyNote)');
  });

  it('keeps a copy at the end of an install without a Wi-Fi step', () => {
    expect(srcOf('finishInstall')).toContain("keepWifiCopy(agentBox, 'install', KeepSpeakerWiFiCopy)");
  });

  it('writes a typed copy store-only from the note, with the ordinary save only as the old-agent fallback', () => {
    const fn = srcOf('wireWlanSwitch');
    expect(fn).toContain("keepWifiCopy(box, 'button', KeepSpeakerWiFiCopy)");
    expect(fn).toContain('if (storeMode && await saveCopyOnly(ssid, pass)) return;');
    expect(fn).toContain("res.reason === 'agent-too-old'");
    expect(src).toContain('id="wlanCopyNote"');
  });

  it('has its words in every language', () => {
    const dir = join(here, 'i18n', 'bundles');
    const keys = ['settingsView.wlanCopyNote', 'settingsView.wlanCopySaveBtn', 'settingsView.wlanCopySaved',
      'settingsView.wlanCopyOtherNetwork', 'settingsView.wlanCopyBadPassword', 'settingsView.wlanCopyFailed',
      'settingsView.wlanStoreHint'];
    for (const f of readdirSync(dir).filter((n) => n.endsWith('.json'))) {
      const b = JSON.parse(readFileSync(join(dir, f), 'utf8'));
      for (const k of keys) expect(b[k], `${f} ${k}`).toBeTruthy();
      expect(b['speaker.noWifiBanner'], `${f} still carries the banner`).toBeUndefined();
      expect(b['speaker.wifiSaveBtn'], `${f} still carries the banner button`).toBeUndefined();
    }
  });
});
